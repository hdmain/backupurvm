package host

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is persisted host settings (editable via SSH panel).
type Config struct {
	ListenBackup string `yaml:"listen_backup"` // tcpduplex, e.g. ":9090"
	ListenSSH    string `yaml:"listen_ssh"`    // SSH TUI, e.g. ":2222"
	DataDir      string `yaml:"data_dir"`
	// SharedKey is the PSK for tcpduplex clients (also hashed for client id).
	SharedKey string `yaml:"shared_key"`
	// SSHPassword enables password login for the admin panel (empty = disabled).
	SSHPassword string `yaml:"ssh_password"`
	// SSHHostKeyPath stores the SSH host private key.
	SSHHostKeyPath string `yaml:"ssh_host_key_path"`
	// SSHAuthorizedKeys lists public keys allowed to open the admin panel.
	SSHAuthorizedKeys []string `yaml:"ssh_authorized_keys"`
	// CompressPrefer is zstd or gzip.
	CompressPrefer string `yaml:"compress_prefer"`
	// MaxBackupsPerClient keeps the newest N backups (0 = unlimited).
	MaxBackupsPerClient int `yaml:"max_backups_per_client"`
	// AutoBackup enables scheduled backups for all online agents.
	AutoBackup bool `yaml:"auto_backup"`
	// AutoBackupEvery is a Go duration string (e.g. 1h, 6h, 24h). Also accepts Nd (days).
	AutoBackupEvery string `yaml:"auto_backup_every"`
	// AutoBackupAt is local HH:MM when backups should run (empty = any time / interval only).
	AutoBackupAt string `yaml:"auto_backup_at"`
	// AutoBackupMode is auto, full, or incremental.
	AutoBackupMode string `yaml:"auto_backup_mode"`
	// ArchiveOfflineAfter moves servers offline longer than this to the Archived list
	// (e.g. 3d, 72h). Empty or 0 disables archiving.
	ArchiveOfflineAfter string `yaml:"archive_offline_after"`
}

func DefaultConfig() Config {
	return Config{
		ListenBackup:        ":9090",
		ListenSSH:           ":2222",
		DataDir:             "./data",
		SharedKey:           "change-me-shared-key",
		SSHPassword:         "change-me-ssh-password",
		SSHHostKeyPath:      "./data/ssh_host_ed25519",
		SSHAuthorizedKeys:   nil,
		CompressPrefer:      "zstd",
		MaxBackupsPerClient: 30,
		AutoBackup:            false,
		AutoBackupEvery:       "24h",
		AutoBackupAt:          "03:00",
		AutoBackupMode:        "auto",
		ArchiveOfflineAfter:   "3d",
	}
}

type ConfigStore struct {
	path string
	mu   sync.RWMutex
	cfg  Config
}

func NewConfigStore(path string) (*ConfigStore, error) {
	s := &ConfigStore{path: path, cfg: DefaultConfig()}
	if err := s.Load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(s.cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *ConfigStore) Path() string { return s.path }

func (s *ConfigStore) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *ConfigStore) Update(fn func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.cfg
	if err := fn(&cp); err != nil {
		return err
	}
	if err := saveConfig(s.path, cp); err != nil {
		return err
	}
	s.cfg = cp
	return nil
}

func (s *ConfigStore) Load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return err
	}
	def := DefaultConfig()
	if cfg.ListenBackup == "" {
		cfg.ListenBackup = def.ListenBackup
	}
	if cfg.ListenSSH == "" {
		cfg.ListenSSH = def.ListenSSH
	}
	if cfg.DataDir == "" {
		cfg.DataDir = def.DataDir
	}
	if cfg.CompressPrefer == "" {
		cfg.CompressPrefer = def.CompressPrefer
	}
	if cfg.AutoBackupEvery == "" {
		cfg.AutoBackupEvery = def.AutoBackupEvery
	}
	if cfg.AutoBackupMode == "" {
		cfg.AutoBackupMode = def.AutoBackupMode
	}
	if cfg.ArchiveOfflineAfter == "" {
		cfg.ArchiveOfflineAfter = def.ArchiveOfflineAfter
	}
	if cfg.SSHHostKeyPath == "" {
		cfg.SSHHostKeyPath = filepath.Join(cfg.DataDir, "ssh_host_ed25519")
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}

func saveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// EnsureExists writes default config if missing.
func EnsureConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	cfg := DefaultConfig()
	return saveConfig(path, cfg)
}

func (c Config) StringSummary() string {
	return fmt.Sprintf("backup=%s ssh=%s data=%s compress=%s max_backups=%d keys=%d updated=%s",
		c.ListenBackup, c.ListenSSH, c.DataDir, c.CompressPrefer, c.MaxBackupsPerClient,
		len(c.SSHAuthorizedKeys), time.Now().Format(time.RFC3339))
}
