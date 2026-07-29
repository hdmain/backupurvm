package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hdmain/backupurvm/internal/archive"
	"github.com/hdmain/backupurvm/internal/common"
	"github.com/hdmain/backupurvm/internal/manifest"
	"github.com/hdmain/backupurvm/internal/protocol"
	"github.com/hdmain/tcpduplex"
	"github.com/hdmain/tcpduplex/transfer"
)

// Options for agent / oneshot client.
type Options struct {
	Addr       string
	KeyPath    string
	SourceRoot string
	WantMode   string // used by --once
	Compress   string
	ClientName string
	TempDir    string
	Once       bool // single backup then exit
	Logger     *log.Logger
}

// RunAgent connects and stays online, reconnecting until ctx is canceled.
func RunAgent(ctx context.Context, opts Options) error {
	if err := normalizeOpts(&opts); err != nil {
		return err
	}
	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := runSession(ctx, opts, protocol.RoleAgent)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		opts.Logger.Printf("disconnected: %v — reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// RunOnce connects, performs one backup, disconnects.
func RunOnce(ctx context.Context, opts Options) error {
	if err := normalizeOpts(&opts); err != nil {
		return err
	}
	if opts.WantMode == "" {
		opts.WantMode = protocol.ModeAuto
	}
	return runSession(ctx, opts, protocol.RoleOneshot)
}

// Run is kept for compatibility: agent by default.
func Run(ctx context.Context, opts Options) error {
	if opts.Once {
		return RunOnce(ctx, opts)
	}
	return RunAgent(ctx, opts)
}

func normalizeOpts(opts *Options) error {
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	if opts.SourceRoot == "" {
		opts.SourceRoot = "/root"
	}
	if opts.TempDir == "" {
		opts.TempDir = os.TempDir()
	}
	return nil
}

func runSession(ctx context.Context, opts Options, role string) error {
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
		Version:    "1.1.0",
		KeyID:      common.KeyID(key),
		Role:       role,
		SourceRoot: opts.SourceRoot,
	}); err != nil {
		return err
	}

	env, err := recv(conn)
	if err != nil {
		return err
	}
	switch env.Type {
	case protocol.TypeHelloOK:
		ok, _ := protocol.DecodePayload[protocol.HelloOK](env)
		opts.Logger.Printf("connected to host as %s (id=%s role=%s)", name, ok.ClientID, role)
	case protocol.TypeHelloFail:
		fail, _ := protocol.DecodePayload[protocol.HelloFail](env)
		return fmt.Errorf("auth failed: %s", fail.Reason)
	default:
		return fmt.Errorf("unexpected hello response: %s", env.Type)
	}

	if role == protocol.RoleOneshot {
		return doBackup(ctx, conn, opts, opts.WantMode)
	}
	return agentLoop(ctx, conn, opts, name, hostname)
}

func agentLoop(ctx context.Context, conn *tcpduplex.Conn, opts Options, name, hostname string) error {
	started := time.Now()
	var (
		mu         sync.Mutex
		busy       bool
		lastBackup time.Time
	)

	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				mu.Lock()
				if !busy {
					_ = send(conn, protocol.TypeHeartbeat, protocol.Heartbeat{At: time.Now().UTC()})
				}
				mu.Unlock()
			}
		}
	}()

	for {
		env, err := recvCtx(ctx, conn)
		if err != nil {
			return err
		}
		switch env.Type {
		case protocol.TypeCommand:
			cmd, err := protocol.DecodePayload[protocol.Command](env)
			if err != nil {
				return err
			}
			opts.Logger.Printf("host command: %s", cmd.Name)

			mu.Lock()
			if busy {
				mu.Unlock()
				_ = send(conn, protocol.TypeCommandAck, protocol.CommandAck{
					ID: cmd.ID, Name: cmd.Name, OK: false, Message: "busy",
				})
				continue
			}
			busy = true
			mu.Unlock()

			_ = send(conn, protocol.TypeCommandAck, protocol.CommandAck{
				ID: cmd.ID, Name: cmd.Name, OK: true, Message: "accepted",
			})

			switch cmd.Name {
			case protocol.CmdPing:
				_ = send(conn, protocol.TypeStatus, protocol.StatusReport{
					ClientName: name,
					Hostname:   hostname,
					SourceRoot: opts.SourceRoot,
					Busy:       false,
					UptimeSec:  int64(time.Since(started).Seconds()),
					LastBackup: lastBackup,
					Message:    "pong",
				})
			case protocol.CmdStatus:
				_ = send(conn, protocol.TypeStatus, protocol.StatusReport{
					ClientName: name,
					Hostname:   hostname,
					SourceRoot: opts.SourceRoot,
					Busy:       false,
					UptimeSec:  int64(time.Since(started).Seconds()),
					LastBackup: lastBackup,
					Message:    "ok",
				})
			case protocol.CmdBackupAuto, protocol.CmdBackupFull, protocol.CmdBackupIncr:
				mode := protocol.ModeAuto
				switch cmd.Name {
				case protocol.CmdBackupFull:
					mode = protocol.ModeFull
				case protocol.CmdBackupIncr:
					mode = protocol.ModeIncremental
				}
				err := doBackup(ctx, conn, opts, mode)
				if err != nil {
					opts.Logger.Printf("backup failed: %v", err)
				} else {
					lastBackup = time.Now().UTC()
					opts.Logger.Printf("backup finished")
				}
			default:
				opts.Logger.Printf("unknown command %q", cmd.Name)
			}

			mu.Lock()
			busy = false
			mu.Unlock()

		case protocol.TypeHeartbeat:
			// ignore
		default:
			opts.Logger.Printf("unexpected message while idle: %s", env.Type)
		}
	}
}

func doBackup(ctx context.Context, conn *tcpduplex.Conn, opts Options, wantMode string) error {
	if wantMode == "" {
		wantMode = protocol.ModeAuto
	}
	hostname, _ := os.Hostname()

	if err := send(conn, protocol.TypePlanReq, protocol.PlanReq{
		WantMode:   wantMode,
		SourceRoot: opts.SourceRoot,
	}); err != nil {
		return err
	}
	env, err := recvCtx(ctx, conn)
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
			// Still notify host? Skip transfer — send a tiny empty? Better inform via status.
			_ = send(conn, protocol.TypeStatus, protocol.StatusReport{
				ClientName: opts.ClientName,
				Hostname:   hostname,
				SourceRoot: opts.SourceRoot,
				Busy:       false,
				Message:    "nothing to backup",
			})
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
	env, err = recvCtx(ctx, conn)
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

	env, err = recvCtx(ctx, conn)
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

func recvCtx(ctx context.Context, conn *tcpduplex.Conn) (protocol.Envelope, error) {
	msg, err := conn.ReceiveContext(ctx)
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
