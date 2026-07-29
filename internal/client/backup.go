package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/hdmain/backupurvm/internal/archive"
	"github.com/hdmain/backupurvm/internal/common"
	"github.com/hdmain/backupurvm/internal/manifest"
	"github.com/hdmain/backupurvm/internal/protocol"
	"github.com/hdmain/tcpduplex"
	"github.com/hdmain/tcpduplex/transfer"
)

// Options for a backup run.
type Options struct {
	Addr       string
	KeyPath    string
	SourceRoot string
	WantMode   string // auto|full|incremental
	Compress   string // empty = host preference
	ClientName string
	TempDir    string
	Logger     *log.Logger
}

// Run connects to the host and performs one backup.
func Run(ctx context.Context, opts Options) error {
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	if opts.SourceRoot == "" {
		opts.SourceRoot = "/root"
	}
	if opts.WantMode == "" {
		opts.WantMode = protocol.ModeAuto
	}
	if opts.TempDir == "" {
		opts.TempDir = os.TempDir()
	}

	key, err := os.ReadFile(opts.KeyPath)
	if err != nil {
		return fmt.Errorf("read key: %w", err)
	}
	key = trimKey(key)
	if len(key) == 0 {
		return fmt.Errorf("empty key file")
	}

	cfg := tcpduplex.DefaultConfig()
	cfg.Handshake.PreSharedKey = key
	cfg.MaxMessageBytes = 1 << 20

	conn, err := tcpduplex.DialContext(ctx, opts.Addr, cfg)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	hostname, _ := os.Hostname()
	name := opts.ClientName
	if name == "" {
		name = hostname
	}

	if err := send(conn, protocol.TypeHello, protocol.Hello{
		ClientName: name,
		Hostname:   hostname,
		Version:    "1.0.0",
		KeyID:      common.KeyID(key),
	}); err != nil {
		return err
	}

	env, err := recv(conn)
	if err != nil {
		return err
	}
	switch env.Type {
	case protocol.TypeHelloOK:
		// ok
	case protocol.TypeHelloFail:
		fail, _ := protocol.DecodePayload[protocol.HelloFail](env)
		return fmt.Errorf("auth failed: %s", fail.Reason)
	default:
		return fmt.Errorf("unexpected hello response: %s", env.Type)
	}

	if err := send(conn, protocol.TypePlanReq, protocol.PlanReq{
		WantMode:   opts.WantMode,
		SourceRoot: opts.SourceRoot,
	}); err != nil {
		return err
	}
	env, err = recv(conn)
	if err != nil {
		return err
	}
	if env.Type != protocol.TypePlan {
		return fmt.Errorf("expected plan, got %s", env.Type)
	}
	plan, err := protocol.DecodePayload[protocol.Plan](env)
	if err != nil {
		return err
	}

	compress := opts.Compress
	if compress == "" {
		compress = plan.CompressPrefer
	}
	if compress == "" {
		compress = protocol.CompressZstd
	}

	opts.Logger.Printf("plan: mode=%s base=%s compress=%s", plan.Mode, plan.BaseBackupID, compress)
	opts.Logger.Printf("scanning %s ...", opts.SourceRoot)

	current, err := manifest.Scan(opts.SourceRoot, false)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}

	var (
		toPack  []protocol.FileEntry
		deleted []string
		mode    = plan.Mode
	)
	switch mode {
	case protocol.ModeFull:
		toPack = current
	case protocol.ModeIncremental:
		toPack, deleted = manifest.Diff(plan.LastManifest, current)
		opts.Logger.Printf("incremental: %d changed, %d deleted (of %d total)", len(toPack), len(deleted), len(current))
		if len(toPack) == 0 && len(deleted) == 0 {
			opts.Logger.Printf("nothing to backup")
			return nil
		}
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}

	backupID := newBackupID()
	meta := archive.Meta{
		BackupID:     backupID,
		Mode:         mode,
		BaseBackupID: plan.BaseBackupID,
		Compress:     compress,
		Hostname:     hostname,
		SourceRoot:   opts.SourceRoot,
		CreatedAt:    time.Now().UTC(),
		Files:        toPack,
		Deleted:      deleted,
	}
	if mode == protocol.ModeFull {
		meta.Files = current
	}

	ext := archive.ExtFor(compress)
	outPath := filepath.Join(opts.TempDir, "backupurvm-"+backupID+ext)
	defer os.Remove(outPath)

	opts.Logger.Printf("packing %d entries -> %s", len(toPack), outPath)
	size, err := archive.Pack(archive.PackOptions{
		Root:     opts.SourceRoot,
		OutPath:  outPath,
		Compress: compress,
		Entries:  toPack,
		Meta:     meta,
	})
	if err != nil {
		return fmt.Errorf("pack: %w", err)
	}
	opts.Logger.Printf("archive size %s", common.FormatBytes(size))

	offer := protocol.BackupOffer{
		BackupID:     backupID,
		Mode:         mode,
		BaseBackupID: plan.BaseBackupID,
		Compress:     compress,
		ArchiveName:  filepath.Base(outPath),
		ArchiveSize:  size,
		FileCount:    len(toPack),
		Deleted:      deleted,
		Hostname:     hostname,
		SourceRoot:   opts.SourceRoot,
		CreatedAt:    meta.CreatedAt,
	}
	if err := send(conn, protocol.TypeBackupOffer, offer); err != nil {
		return err
	}
	env, err = recv(conn)
	if err != nil {
		return err
	}
	if env.Type != protocol.TypeBackupReady {
		if env.Type == protocol.TypeBackupFail {
			fail, _ := protocol.DecodePayload[protocol.BackupFail](env)
			return fmt.Errorf("host rejected: %s", fail.Reason)
		}
		return fmt.Errorf("expected backup_ready, got %s", env.Type)
	}

	opts.Logger.Printf("transferring ...")
	if err := transfer.SendFile(ctx, conn, outPath, &transfer.Options{
		OnProgress: func(n, total int64) {
			if total > 0 && (n == total || n%(16<<20) == 0) {
				opts.Logger.Printf("progress %s/%s", common.FormatBytes(n), common.FormatBytes(total))
			}
		},
	}); err != nil {
		return fmt.Errorf("transfer: %w", err)
	}

	env, err = recv(conn)
	if err != nil {
		return err
	}
	switch env.Type {
	case protocol.TypeBackupDone:
		done, _ := protocol.DecodePayload[protocol.BackupDone](env)
		opts.Logger.Printf("done: backup_id=%s stored=%s bytes=%s", done.BackupID, done.StoredAs, common.FormatBytes(done.Bytes))
		return nil
	case protocol.TypeBackupFail:
		fail, _ := protocol.DecodePayload[protocol.BackupFail](env)
		return fmt.Errorf("host failed: %s", fail.Reason)
	default:
		return fmt.Errorf("unexpected response: %s", env.Type)
	}
}

func send(conn *tcpduplex.Conn, typ string, payload any) error {
	b, err := protocol.Encode(typ, payload)
	if err != nil {
		return err
	}
	return conn.Send(b)
}

func recv(conn *tcpduplex.Conn) (protocol.Envelope, error) {
	msg, err := conn.Receive()
	if err != nil {
		return protocol.Envelope{}, err
	}
	return protocol.Decode(msg)
}

func trimKey(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}

func newBackupID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}
