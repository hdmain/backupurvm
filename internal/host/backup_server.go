package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hdmain/backupurvm/internal/archive"
	"github.com/hdmain/backupurvm/internal/common"
	"github.com/hdmain/backupurvm/internal/protocol"
	"github.com/hdmain/tcpduplex"
	"github.com/hdmain/tcpduplex/transfer"
)

// BackupServer accepts tcpduplex clients and stores archives.
type BackupServer struct {
	store  *ConfigStore
	storage *Storage
	log    *log.Logger
}

func NewBackupServer(store *ConfigStore, storage *Storage, logger *log.Logger) *BackupServer {
	if logger == nil {
		logger = log.Default()
	}
	return &BackupServer{store: store, storage: storage, log: logger}
}

func (s *BackupServer) ListenAndServe(ctx context.Context) error {
	cfg := s.store.Get()
	ln, err := net.Listen("tcp", cfg.ListenBackup)
	if err != nil {
		return err
	}
	defer ln.Close()
	s.log.Printf("backup listener on %s", ln.Addr())

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var wg sync.WaitGroup
	for {
		nc, err := ln.Accept()
		if err != nil {
			wg.Wait()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		wg.Add(1)
		go func(nc net.Conn) {
			defer wg.Done()
			peerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			live := s.store.Get()
			tdCfg := tcpduplex.DefaultConfig()
			tdCfg.Handshake.PreSharedKey = []byte(live.SharedKey)
			tdCfg.MaxMessageBytes = 1 << 20

			conn, err := tcpduplex.ServeConnContext(peerCtx, nc, tdCfg)
			if err != nil {
				s.log.Printf("handshake failed: %v", err)
				return
			}
			defer conn.Close()
			if err := s.handlePeer(peerCtx, conn); err != nil {
				s.log.Printf("peer error: %v", err)
			}
		}(nc)
	}
}

func (s *BackupServer) handlePeer(ctx context.Context, conn *tcpduplex.Conn) error {
	msg, err := conn.ReceiveContext(ctx)
	if err != nil {
		return err
	}
	env, err := protocol.Decode(msg)
	if err != nil {
		return err
	}
	if env.Type != protocol.TypeHello {
		return s.send(conn, protocol.TypeError, protocol.ErrorMsg{Reason: "expected hello"})
	}
	hello, err := protocol.DecodePayload[protocol.Hello](env)
	if err != nil {
		return err
	}

	cfg := s.store.Get()
	wantID := common.KeyID([]byte(cfg.SharedKey))
	if hello.KeyID != "" && hello.KeyID != wantID {
		_ = s.send(conn, protocol.TypeHelloFail, protocol.HelloFail{Reason: "key_id mismatch"})
		return fmt.Errorf("key_id mismatch from %s", hello.Hostname)
	}

	clientID := wantID
	if hello.ClientName == "" {
		hello.ClientName = hello.Hostname
	}
	_ = s.storage.TouchClient(clientID, hello.ClientName, hello.Hostname)
	if err := s.send(conn, protocol.TypeHelloOK, protocol.HelloOK{
		ServerTime: time.Now().UTC(),
		Message:    "ok",
	}); err != nil {
		return err
	}

	for {
		msg, err := conn.ReceiveContext(ctx)
		if err != nil {
			return err
		}
		env, err := protocol.Decode(msg)
		if err != nil {
			return err
		}
		switch env.Type {
		case protocol.TypePlanReq:
			req, err := protocol.DecodePayload[protocol.PlanReq](env)
			if err != nil {
				return err
			}
			plan, err := s.buildPlan(clientID, req)
			if err != nil {
				return err
			}
			if err := s.send(conn, protocol.TypePlan, plan); err != nil {
				return err
			}
		case protocol.TypeBackupOffer:
			offer, err := protocol.DecodePayload[protocol.BackupOffer](env)
			if err != nil {
				return err
			}
			if err := s.receiveBackup(ctx, conn, clientID, hello, offer); err != nil {
				_ = s.send(conn, protocol.TypeBackupFail, protocol.BackupFail{Reason: err.Error()})
				return err
			}
			return nil // one backup per connection
		default:
			_ = s.send(conn, protocol.TypeError, protocol.ErrorMsg{Reason: "unexpected " + env.Type})
			return fmt.Errorf("unexpected message %s", env.Type)
		}
	}
}

func (s *BackupServer) buildPlan(clientID string, req protocol.PlanReq) (protocol.Plan, error) {
	cfg := s.store.Get()
	manifest, lastID, err := s.storage.LatestManifest(clientID)
	if err != nil {
		return protocol.Plan{}, err
	}

	mode := req.WantMode
	if mode == "" || mode == protocol.ModeAuto {
		if len(manifest) == 0 || lastID == "" {
			mode = protocol.ModeFull
		} else {
			mode = protocol.ModeIncremental
		}
	}
	if mode == protocol.ModeIncremental && (len(manifest) == 0 || lastID == "") {
		mode = protocol.ModeFull
	}

	plan := protocol.Plan{
		Mode:           mode,
		CompressPrefer: cfg.CompressPrefer,
	}
	if mode == protocol.ModeIncremental {
		plan.BaseBackupID = lastID
		plan.LastManifest = manifest
	}
	return plan, nil
}

func (s *BackupServer) receiveBackup(ctx context.Context, conn *tcpduplex.Conn, clientID string, hello protocol.Hello, offer protocol.BackupOffer) error {
	if offer.BackupID == "" {
		offer.BackupID = newBackupID()
	}
	dest, err := s.storage.ArchiveDest(clientID, offer.BackupID, offer.Compress)
	if err != nil {
		return err
	}
	_ = os.Remove(dest) // fresh receive; transfer.ReceiveFile resumes partials

	if err := s.send(conn, protocol.TypeBackupReady, protocol.BackupReady{
		DestName: filepath.Base(dest),
	}); err != nil {
		return err
	}

	meta, err := transfer.ReceiveFile(ctx, conn, dest, &transfer.Options{
		OnProgress: func(n, total int64) {
			if total > 0 && (n == total || n%(32<<20) < (1<<20)) {
				s.log.Printf("recv %s %s/%s", offer.BackupID, common.FormatBytes(n), common.FormatBytes(total))
			}
		},
	})
	if err != nil {
		_ = os.Remove(dest)
		return err
	}

	prev, _, err := s.storage.LatestManifest(clientID)
	if err != nil {
		return err
	}
	fullManifest := prev
	if m, err := archive.ReadMeta(dest); err == nil {
		fullManifest = applyArchiveMeta(offer.Mode, prev, m)
	} else if offer.Mode == protocol.ModeFull {
		return fmt.Errorf("read archive meta: %w", err)
	}

	st, _ := os.Stat(dest)
	var size int64
	if st != nil {
		size = st.Size()
	}
	if size == 0 {
		size = meta.Size
	}

	rec := BackupRecord{
		ID:           offer.BackupID,
		ClientID:     clientID,
		ClientName:   hello.ClientName,
		Hostname:     offer.Hostname,
		Mode:         offer.Mode,
		BaseBackupID: offer.BaseBackupID,
		Compress:     offer.Compress,
		ArchivePath:  dest,
		Bytes:        size,
		FileCount:    offer.FileCount,
		DeletedCount: len(offer.Deleted),
		SourceRoot:   offer.SourceRoot,
		CreatedAt:    offer.CreatedAt,
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	if err := s.storage.CommitBackup(rec, fullManifest); err != nil {
		return err
	}

	cfg := s.store.Get()
	if _, err := s.storage.PruneBackups(clientID, cfg.MaxBackupsPerClient); err != nil {
		s.log.Printf("prune: %v", err)
	}

	s.log.Printf("stored backup %s from %s (%s, %s)", rec.ID, hello.ClientName, rec.Mode, common.FormatBytes(rec.Bytes))
	return s.send(conn, protocol.TypeBackupDone, protocol.BackupDone{
		BackupID: rec.ID,
		StoredAs: dest,
		Bytes:    rec.Bytes,
	})
}

func applyArchiveMeta(mode string, prev []protocol.FileEntry, m archive.Meta) []protocol.FileEntry {
	if mode == protocol.ModeFull {
		return m.Files
	}
	idx := make(map[string]protocol.FileEntry, len(prev)+len(m.Files))
	for _, e := range prev {
		idx[e.Path] = e
	}
	for _, e := range m.Files {
		idx[e.Path] = e
	}
	for _, d := range m.Deleted {
		delete(idx, d)
	}
	out := make([]protocol.FileEntry, 0, len(idx))
	for _, e := range idx {
		out = append(out, e)
	}
	return out
}

func (s *BackupServer) send(conn *tcpduplex.Conn, typ string, payload any) error {
	b, err := protocol.Encode(typ, payload)
	if err != nil {
		return err
	}
	return conn.Send(b)
}

// KeyID is an alias for common.KeyID.
func KeyID(key []byte) string { return common.KeyID(key) }

func newBackupID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}
