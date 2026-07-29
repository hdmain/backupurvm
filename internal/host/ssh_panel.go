package host

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// SSHPanel serves an interactive admin TUI over SSH.
type SSHPanel struct {
	store   *ConfigStore
	storage *Storage
	tasks   *TaskHub
	peers   *PeerHub
	log     *log.Logger
}

func NewSSHPanel(store *ConfigStore, storage *Storage, tasks *TaskHub, peers *PeerHub, logger *log.Logger) *SSHPanel {
	if logger == nil {
		logger = log.Default()
	}
	if tasks == nil {
		tasks = NewTaskHub()
	}
	if peers == nil {
		peers = NewPeerHub()
	}
	return &SSHPanel{store: store, storage: storage, tasks: tasks, peers: peers, log: logger}
}

func (p *SSHPanel) ListenAndServe() error {
	cfg := p.store.Get()
	if err := ensureHostKey(cfg.SSHHostKeyPath); err != nil {
		return err
	}
	signer, err := loadHostKey(cfg.SSHHostKeyPath)
	if err != nil {
		return err
	}

	server := &ssh.Server{
		Addr:             cfg.ListenSSH,
		Handler:          p.session,
		HostSigners:      []ssh.Signer{signer},
		Version:          "backupurvm",
		PublicKeyHandler: p.publicKeyHandler,
		PasswordHandler:  p.passwordHandler,
	}
	p.log.Printf("SSH admin panel on %s", cfg.ListenSSH)
	return server.ListenAndServe()
}

func (p *SSHPanel) publicKeyHandler(ctx ssh.Context, key ssh.PublicKey) bool {
	cfg := p.store.Get()
	if len(cfg.SSHAuthorizedKeys) == 0 {
		return false
	}
	for _, line := range cfg.SSHAuthorizedKeys {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			continue
		}
		if ssh.KeysEqual(pub, key) {
			return true
		}
	}
	return false
}

func (p *SSHPanel) passwordHandler(ctx ssh.Context, password string) bool {
	cfg := p.store.Get()
	if cfg.SSHPassword == "" {
		return false
	}
	return password == cfg.SSHPassword
}

func (p *SSHPanel) session(s ssh.Session) {
	defer s.Close()
	user := s.User()
	if user == "" {
		user = "(unknown)"
	}
	remote := "unknown"
	if addr := s.RemoteAddr(); addr != nil {
		remote = addr.String()
	}
	p.log.Printf("SSH login: user %q connected from %s", user, remote)
	defer p.log.Printf("SSH logout: user %q disconnected (%s)", user, remote)
	p.runTUI(s)
}

func maskSecret(s string) string {
	if s == "" {
		return "(empty)"
	}
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + strings.Repeat("*", len(s)-4) + s[len(s)-2:]
}

func ensureHostKey(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		return fmt.Errorf("marshal host key: %w", err)
	}
	return os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
}

func loadHostKey(path string) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return gossh.ParsePrivateKey(b)
}
