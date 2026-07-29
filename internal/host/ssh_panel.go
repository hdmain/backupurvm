package host

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gliderlabs/ssh"
	"github.com/hdmain/backupurvm/internal/common"
	gossh "golang.org/x/crypto/ssh"
)

// SSHPanel serves an interactive admin TUI over SSH.
type SSHPanel struct {
	store   *ConfigStore
	storage *Storage
	log     *log.Logger
}

func NewSSHPanel(store *ConfigStore, storage *Storage, logger *log.Logger) *SSHPanel {
	if logger == nil {
		logger = log.Default()
	}
	return &SSHPanel{store: store, storage: storage, log: logger}
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
	_, _ = io.WriteString(s, "\r\n=== backupurvm host panel ===\r\n")
	cfg := p.store.Get()
	if cfg.SSHPassword == "" && len(cfg.SSHAuthorizedKeys) == 0 {
		_, _ = io.WriteString(s, "WARNING: no ssh_password and no authorized keys — set one in config.yml\r\n")
	} else if len(cfg.SSHAuthorizedKeys) == 0 {
		_, _ = io.WriteString(s, "TIP: password login enabled (ssh_password). Optionally add an SSH public key (menu 8).\r\n")
	}
	in := bufio.NewReader(s)
	for {
		cfg = p.store.Get()
		_, _ = io.WriteString(s, "\r\n")
		_, _ = fmt.Fprintf(s, "backup listen : %s\r\n", cfg.ListenBackup)
		_, _ = fmt.Fprintf(s, "ssh listen    : %s\r\n", cfg.ListenSSH)
		_, _ = fmt.Fprintf(s, "data dir      : %s\r\n", cfg.DataDir)
		_, _ = fmt.Fprintf(s, "compress      : %s\r\n", cfg.CompressPrefer)
		_, _ = fmt.Fprintf(s, "max backups   : %d\r\n", cfg.MaxBackupsPerClient)
		_, _ = fmt.Fprintf(s, "shared key    : %s\r\n", maskSecret(cfg.SharedKey))
		_, _ = fmt.Fprintf(s, "ssh password  : %s\r\n", maskSecret(cfg.SSHPassword))
		clients, backups, bytes, _ := p.storage.DiskUsage()
		_, _ = fmt.Fprintf(s, "storage       : %d clients, %d backups, %s\r\n", clients, backups, FormatBytes(bytes))

		_, _ = io.WriteString(s, "\r\nMenu:\r\n")
		_, _ = io.WriteString(s, "  1) List clients\r\n")
		_, _ = io.WriteString(s, "  2) List backups for client\r\n")
		_, _ = io.WriteString(s, "  3) Set shared key (client --key)\r\n")
		_, _ = io.WriteString(s, "  4) Set SSH password\r\n")
		_, _ = io.WriteString(s, "  5) Set compress prefer (zstd|gzip)\r\n")
		_, _ = io.WriteString(s, "  6) Set max backups per client\r\n")
		_, _ = io.WriteString(s, "  7) Set backup listen address\r\n")
		_, _ = io.WriteString(s, "  8) Add SSH authorized public key\r\n")
		_, _ = io.WriteString(s, "  9) Show config path / key id\r\n")
		_, _ = io.WriteString(s, "  q) Quit\r\n")
		_, _ = io.WriteString(s, "> ")

		line, err := readLine(in)
		if err != nil {
			return
		}
		switch strings.TrimSpace(line) {
		case "1":
			p.menuListClients(s)
		case "2":
			p.menuListBackups(s, in)
		case "3":
			p.menuSetSharedKey(s, in)
		case "4":
			p.menuSetSSHPassword(s, in)
		case "5":
			p.menuSetCompress(s, in)
		case "6":
			p.menuSetMaxBackups(s, in)
		case "7":
			p.menuSetListenBackup(s, in)
		case "8":
			p.menuAddSSHKey(s, in)
		case "9":
			p.menuShowInfo(s)
		case "q", "Q", "quit", "exit":
			_, _ = io.WriteString(s, "bye\r\n")
			return
		default:
			_, _ = io.WriteString(s, "unknown option\r\n")
		}
	}
}

func (p *SSHPanel) menuListClients(s ssh.Session) {
	list, err := p.storage.ListClients()
	if err != nil {
		_, _ = fmt.Fprintf(s, "error: %v\r\n", err)
		return
	}
	if len(list) == 0 {
		_, _ = io.WriteString(s, "(no clients yet)\r\n")
		return
	}
	for _, c := range list {
		_, _ = fmt.Fprintf(s, "- %s  name=%s host=%s last=%s backup=%s files=%d\r\n",
			c.ID, c.Name, c.Hostname, c.LastSeen.Format(time.RFC3339), c.LastBackupID, len(c.Manifest))
	}
}

func (p *SSHPanel) menuListBackups(s ssh.Session, in *bufio.Reader) {
	_, _ = io.WriteString(s, "client id: ")
	id, err := readLine(in)
	if err != nil {
		return
	}
	id = strings.TrimSpace(id)
	recs, err := p.storage.ListBackups(id)
	if err != nil {
		_, _ = fmt.Fprintf(s, "error: %v\r\n", err)
		return
	}
	if len(recs) == 0 {
		_, _ = io.WriteString(s, "(no backups)\r\n")
		return
	}
	for _, r := range recs {
		_, _ = fmt.Fprintf(s, "- %s  %s  %s  files=%d deleted=%d  %s\r\n",
			r.ID, r.Mode, FormatBytes(r.Bytes), r.FileCount, r.DeletedCount, r.CreatedAt.Format(time.RFC3339))
	}
}

func (p *SSHPanel) menuSetSharedKey(s ssh.Session, in *bufio.Reader) {
	_, _ = io.WriteString(s, "new shared key (clients use --key file with same value): ")
	key, err := readLine(in)
	if err != nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		_, _ = io.WriteString(s, "cancelled\r\n")
		return
	}
	if err := p.store.Update(func(c *Config) error {
		c.SharedKey = key
		return nil
	}); err != nil {
		_, _ = fmt.Fprintf(s, "error: %v\r\n", err)
		return
	}
	_, _ = fmt.Fprintf(s, "updated. key_id=%s (applies to new client connections)\r\n", common.KeyID([]byte(key)))
}

func (p *SSHPanel) menuSetSSHPassword(s ssh.Session, in *bufio.Reader) {
	_, _ = io.WriteString(s, "new SSH password (empty disables password login): ")
	pw, err := readLine(in)
	if err != nil {
		return
	}
	pw = strings.TrimRight(pw, "\r\n")
	if err := p.store.Update(func(c *Config) error {
		c.SSHPassword = pw
		return nil
	}); err != nil {
		_, _ = fmt.Fprintf(s, "error: %v\r\n", err)
		return
	}
	if pw == "" {
		_, _ = io.WriteString(s, "password login disabled — use authorized keys only\r\n")
		return
	}
	_, _ = io.WriteString(s, "SSH password updated (applies to new logins)\r\n")
}

func (p *SSHPanel) menuSetCompress(s ssh.Session, in *bufio.Reader) {
	_, _ = io.WriteString(s, "compress (zstd|gzip): ")
	v, err := readLine(in)
	if err != nil {
		return
	}
	v = strings.TrimSpace(strings.ToLower(v))
	if v != "zstd" && v != "gzip" {
		_, _ = io.WriteString(s, "invalid\r\n")
		return
	}
	_ = p.store.Update(func(c *Config) error {
		c.CompressPrefer = v
		return nil
	})
	_, _ = io.WriteString(s, "ok\r\n")
}

func (p *SSHPanel) menuSetMaxBackups(s ssh.Session, in *bufio.Reader) {
	_, _ = io.WriteString(s, "max backups per client (0=unlimited): ")
	v, err := readLine(in)
	if err != nil {
		return
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err != nil || n < 0 {
		_, _ = io.WriteString(s, "invalid\r\n")
		return
	}
	_ = p.store.Update(func(c *Config) error {
		c.MaxBackupsPerClient = n
		return nil
	})
	_, _ = io.WriteString(s, "ok\r\n")
}

func (p *SSHPanel) menuSetListenBackup(s ssh.Session, in *bufio.Reader) {
	_, _ = io.WriteString(s, "backup listen address (e.g. :9090) [requires restart]: ")
	v, err := readLine(in)
	if err != nil {
		return
	}
	v = strings.TrimSpace(v)
	if v == "" {
		_, _ = io.WriteString(s, "cancelled\r\n")
		return
	}
	_ = p.store.Update(func(c *Config) error {
		c.ListenBackup = v
		return nil
	})
	_, _ = io.WriteString(s, "saved — restart host to apply\r\n")
}

func (p *SSHPanel) menuAddSSHKey(s ssh.Session, in *bufio.Reader) {
	_, _ = io.WriteString(s, "paste one authorized_keys line: ")
	line, err := readLine(in)
	if err != nil {
		return
	}
	line = strings.TrimSpace(line)
	if line == "" {
		_, _ = io.WriteString(s, "cancelled\r\n")
		return
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err != nil {
		_, _ = fmt.Fprintf(s, "invalid key: %v\r\n", err)
		return
	}
	_ = p.store.Update(func(c *Config) error {
		c.SSHAuthorizedKeys = append(c.SSHAuthorizedKeys, line)
		return nil
	})
	_, _ = io.WriteString(s, "added (takes effect for new SSH sessions)\r\n")
}

func (p *SSHPanel) menuShowInfo(s ssh.Session) {
	cfg := p.store.Get()
	_, _ = fmt.Fprintf(s, "config file : %s\r\n", p.store.Path())
	_, _ = fmt.Fprintf(s, "key_id      : %s\r\n", common.KeyID([]byte(cfg.SharedKey)))
	_, _ = fmt.Fprintf(s, "ssh host key: %s\r\n", cfg.SSHHostKeyPath)
	_, _ = fmt.Fprintf(s, "authorized  : %d keys\r\n", len(cfg.SSHAuthorizedKeys))
}

func readLine(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
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
