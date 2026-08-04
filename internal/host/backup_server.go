package host

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	store   *ConfigStore
	storage *Storage
	tasks   *TaskHub
	peers   *PeerHub
	log     *log.Logger
}

func NewBackupServer(store *ConfigStore, storage *Storage, tasks *TaskHub, peers *PeerHub, logger *log.Logger) *BackupServer {
	if logger == nil {
		logger = log.Default()
	}
	if tasks == nil {
		tasks = NewTaskHub()
	}
	if peers == nil {
		peers = NewPeerHub()
	}
	return &BackupServer{store: store, storage: storage, tasks: tasks, peers: peers, log: logger}
}

func (s *BackupServer) Peers() *PeerHub { return s.peers }

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
			tdCfg := common.DuplexConfig([]byte(live.SharedKey))

			conn, err := tcpduplex.ServeConnContext(peerCtx, nc, tdCfg)
			if err != nil {
				s.log.Printf("handshake failed from %s: %v", nc.RemoteAddr(), err)
				return
			}
			defer conn.Close()
			if err := s.handlePeer(peerCtx, cancel, conn); err != nil {
				s.log.Printf("peer error: %v", err)
			}
		}(nc)
	}
}

func (s *BackupServer) handlePeer(ctx context.Context, cancel context.CancelFunc, conn *tcpduplex.Conn) error {
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
	wantKey := common.KeyID([]byte(cfg.SharedKey))
	if hello.KeyID != "" && hello.KeyID != wantKey {
		_ = s.send(conn, protocol.TypeHelloFail, protocol.HelloFail{Reason: "key_id mismatch"})
		return fmt.Errorf("key_id mismatch from %s", hello.Hostname)
	}

	if hello.ClientName == "" {
		hello.ClientName = hello.Hostname
	}
	if hello.Role == "" {
		hello.Role = protocol.RoleOneshot
	}
	clientID := ClientIdentity(hello.ClientName, hello.Hostname)
	_ = s.storage.TouchClient(clientID, hello.ClientName, hello.Hostname)

	if err := s.send(conn, protocol.TypeHelloOK, protocol.HelloOK{
		ServerTime: time.Now().UTC(),
		Message:    "ok",
		ClientID:   clientID,
	}); err != nil {
		return err
	}

	if hello.Role == protocol.RoleAgent {
		return s.handleAgent(ctx, cancel, conn, clientID, hello)
	}
	return s.handleOneshot(ctx, conn, clientID, hello)
}

func (s *BackupServer) handleOneshot(ctx context.Context, conn *tcpduplex.Conn, clientID string, hello protocol.Hello) error {
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
			return nil
		default:
			_ = s.send(conn, protocol.TypeError, protocol.ErrorMsg{Reason: "unexpected " + env.Type})
			return fmt.Errorf("unexpected message %s", env.Type)
		}
	}
}

func (s *BackupServer) handleAgent(ctx context.Context, cancel context.CancelFunc, conn *tcpduplex.Conn, clientID string, hello protocol.Hello) error {
	peer := &AgentPeer{
		ID:         clientID,
		Name:       hello.ClientName,
		Hostname:   hello.Hostname,
		SourceRoot: hello.SourceRoot,
		conn:       conn,
		cmdCh:      make(chan protocol.Command, 8),
		cancel:     cancel,
	}
	s.peers.Register(peer)
	s.log.Printf("agent online: %s (%s) id=%s", hello.ClientName, hello.Hostname, clientID)
	defer func() {
		s.peers.Unregister(clientID, peer)
		s.log.Printf("agent offline: %s (%s)", hello.ClientName, hello.Hostname)
	}()

	// Forward host commands without polling Receive on a short timeout
	// (that busy-woke every agent ~2×/sec and burned CPU when idle).
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case cmd := <-peer.cmdCh:
				s.log.Printf("command → %s: %s", hello.ClientName, cmd.Name)
				if err := s.send(conn, protocol.TypeCommand, cmd); err != nil {
					s.log.Printf("command send to %s failed: %v", hello.ClientName, err)
					cancel()
					return
				}
			}
		}
	}()

	for {
		msg, err := conn.ReceiveContext(ctx)
		if err != nil {
			return err
		}
		s.peers.Touch(clientID)
		env, err := protocol.Decode(msg)
		if err != nil {
			return err
		}
		if err := s.dispatchAgentMsg(ctx, conn, peer, clientID, hello, env); err != nil {
			return err
		}
	}
}

func (s *BackupServer) dispatchAgentMsg(ctx context.Context, conn *tcpduplex.Conn, peer *AgentPeer, clientID string, hello protocol.Hello, env protocol.Envelope) error {
	switch env.Type {
	case protocol.TypeHeartbeat:
		return nil
	case protocol.TypeCommandAck:
		ack, _ := protocol.DecodePayload[protocol.CommandAck](env)
		s.log.Printf("command ack from %s: %s ok=%v %s", hello.ClientName, ack.Name, ack.OK, ack.Message)
		return nil
	case protocol.TypeStatus:
		st, _ := protocol.DecodePayload[protocol.StatusReport](env)
		s.log.Printf("status %s: busy=%v %s", hello.ClientName, st.Busy, st.Message)
		s.peers.SetBusy(clientID, st.Busy)
		peer.Busy = st.Busy
		return nil
	case protocol.TypePlanReq:
		s.peers.SetBusy(clientID, true)
		peer.Busy = true
		req, err := protocol.DecodePayload[protocol.PlanReq](env)
		if err != nil {
			s.peers.SetBusy(clientID, false)
			peer.Busy = false
			return err
		}
		plan, err := s.buildPlan(clientID, req)
		if err != nil {
			s.peers.SetBusy(clientID, false)
			peer.Busy = false
			return err
		}
		return s.send(conn, protocol.TypePlan, plan)
	case protocol.TypeBackupOffer:
		offer, err := protocol.DecodePayload[protocol.BackupOffer](env)
		if err != nil {
			s.peers.SetBusy(clientID, false)
			peer.Busy = false
			return err
		}
		err = s.receiveBackup(ctx, conn, clientID, hello, offer)
		s.peers.SetBusy(clientID, false)
		peer.Busy = false
		if err != nil {
			_ = s.send(conn, protocol.TypeBackupFail, protocol.BackupFail{Reason: err.Error()})
			// stay connected in agent mode
			s.log.Printf("backup from %s failed: %v", hello.ClientName, err)
			return nil
		}
		return nil
	default:
		s.log.Printf("agent %s unexpected message %s", hello.ClientName, env.Type)
		return nil
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
		blob, err := protocol.EncodeManifestZstd(manifest)
		if err != nil {
			return protocol.Plan{}, fmt.Errorf("compress manifest: %w", err)
		}
		plan.ManifestZstd = blob
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
	_ = os.Remove(dest)

	taskID := s.tasks.Start(hello.ClientName, offer.Hostname, offer.Mode, offer.SourceRoot, offer.BackupID, offer.ArchiveSize)

	if err := s.send(conn, protocol.TypeBackupReady, protocol.BackupReady{
		DestName: filepath.Base(dest),
	}); err != nil {
		s.tasks.Fail(taskID, err.Error())
		return err
	}

	meta, err := transfer.ReceiveFile(ctx, conn, dest, &transfer.Options{
		OnProgress: func(n, total int64) {
			s.tasks.Progress(taskID, n, total)
			if total > 0 && (n == total || n%(32<<20) < (1<<20)) {
				s.log.Printf("recv %s %s/%s", offer.BackupID, common.FormatBytes(n), common.FormatBytes(total))
			}
		},
	})
	if err != nil {
		_ = os.Remove(dest)
		s.tasks.Fail(taskID, err.Error())
		return err
	}

	prev, _, err := s.storage.LatestManifest(clientID)
	if err != nil {
		s.tasks.Fail(taskID, err.Error())
		return err
	}
	fullManifest := prev
	if m, err := archive.ReadMeta(dest); err == nil {
		fullManifest = applyArchiveMeta(offer.Mode, prev, m)
	} else if offer.Mode == protocol.ModeFull {
		s.tasks.Fail(taskID, err.Error())
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
		s.tasks.Fail(taskID, err.Error())
		return err
	}

	cfg := s.store.Get()
	if _, err := s.storage.PruneBackups(clientID, cfg.MaxBackupsPerClient); err != nil {
		s.log.Printf("prune: %v", err)
	}

	s.tasks.Complete(taskID, rec.Bytes, fmt.Sprintf("%s %s", rec.Mode, common.FormatBytes(rec.Bytes)))
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

// ClientIdentity returns a stable per-server id (not the shared key fingerprint).
func ClientIdentity(name, hostname string) string {
	sum := sha256.Sum256([]byte(name + "\x00" + hostname))
	return hex.EncodeToString(sum[:16])
}

func KeyID(key []byte) string { return common.KeyID(key) }

func newBackupID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}
