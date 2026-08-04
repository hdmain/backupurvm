package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hdmain/backupurvm/internal/common"
	"github.com/hdmain/backupurvm/internal/protocol"
)

// BackupRecord is persisted metadata for one received backup.
type BackupRecord struct {
	ID           string    `json:"id"`
	ClientID     string    `json:"client_id"`
	ClientName   string    `json:"client_name"`
	Hostname     string    `json:"hostname"`
	Mode         string    `json:"mode"`
	BaseBackupID string    `json:"base_backup_id,omitempty"`
	Compress     string    `json:"compress"`
	ArchivePath  string    `json:"archive_path"`
	Bytes        int64     `json:"bytes"`
	FileCount    int       `json:"file_count"`
	DeletedCount int       `json:"deleted_count"`
	SourceRoot   string    `json:"source_root"`
	CreatedAt    time.Time `json:"created_at"`
}

// ClientInfo tracks the last known client and its latest manifest.
type ClientInfo struct {
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	Hostname     string               `json:"hostname"`
	LastSeen     time.Time            `json:"last_seen"`
	LastBackupID string               `json:"last_backup_id,omitempty"`
	FileCount    int                  `json:"file_count,omitempty"`
	Manifest     []protocol.FileEntry `json:"manifest,omitempty"`
}

// clientInfoLite is used for TUI listings so we do not unmarshal huge manifests.
type clientInfoLite struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Hostname     string    `json:"hostname"`
	LastSeen     time.Time `json:"last_seen"`
	LastBackupID string    `json:"last_backup_id,omitempty"`
	FileCount    int       `json:"file_count,omitempty"`
}

// Storage manages on-disk backups under DataDir/clients/<id>/.
type Storage struct {
	root string
	mu   sync.Mutex
}

func NewStorage(dataDir string) (*Storage, error) {
	root := filepath.Join(dataDir, "clients")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Storage{root: root}, nil
}

func (s *Storage) clientDir(clientID string) string {
	safe := sanitizeID(clientID)
	return filepath.Join(s.root, safe)
}

func sanitizeID(id string) string {
	id = strings.TrimSpace(id)
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	return out
}

func (s *Storage) LoadClient(clientID string) (ClientInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadClientLocked(clientID)
}

func (s *Storage) loadClientLocked(clientID string) (ClientInfo, error) {
	path := filepath.Join(s.clientDir(clientID), "client.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ClientInfo{ID: clientID}, nil
		}
		return ClientInfo{}, err
	}
	var info ClientInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return ClientInfo{}, err
	}
	if info.FileCount == 0 && len(info.Manifest) > 0 {
		info.FileCount = len(info.Manifest)
	}
	return info, nil
}

func (s *Storage) saveClientLocked(info ClientInfo) error {
	dir := s.clientDir(info.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "client.json"), b, 0o644)
}

func (s *Storage) TouchClient(clientID, name, hostname string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := s.loadClientLocked(clientID)
	if err != nil {
		return err
	}
	info.ID = clientID
	if name != "" {
		info.Name = name
	}
	if hostname != "" {
		info.Hostname = hostname
	}
	info.LastSeen = time.Now().UTC()
	return s.saveClientLocked(info)
}

func (s *Storage) LatestManifest(clientID string) ([]protocol.FileEntry, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := s.loadClientLocked(clientID)
	if err != nil {
		return nil, "", err
	}
	return info.Manifest, info.LastBackupID, nil
}

func (s *Storage) ArchiveDest(clientID, backupID, compress string) (string, error) {
	dir := filepath.Join(s.clientDir(clientID), "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ext := ".tar.zst"
	if compress == protocol.CompressGzip {
		ext = ".tar.gz"
	}
	return filepath.Join(dir, backupID+ext), nil
}

func (s *Storage) CommitBackup(rec BackupRecord, fullManifest []protocol.FileEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.clientDir(rec.ClientID)
	if err := os.MkdirAll(filepath.Join(dir, "backups"), 0o755); err != nil {
		return err
	}
	metaPath := filepath.Join(dir, "backups", rec.ID+".json")
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(metaPath, b, 0o644); err != nil {
		return err
	}

	info, err := s.loadClientLocked(rec.ClientID)
	if err != nil {
		return err
	}
	info.ID = rec.ClientID
	info.Name = rec.ClientName
	info.Hostname = rec.Hostname
	info.LastSeen = time.Now().UTC()
	info.LastBackupID = rec.ID
	info.Manifest = fullManifest
	info.FileCount = len(fullManifest)
	if err := s.saveClientLocked(info); err != nil {
		return err
	}
	return nil
}

func (s *Storage) ListClients() ([]ClientInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []ClientInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := s.loadClientLocked(e.Name())
		if err != nil {
			continue
		}
		if info.ID == "" {
			info.ID = e.Name()
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	return out, nil
}

// ListClientsLite lists clients without unmarshaling file manifests (TUI hot path).
func (s *Storage) ListClientsLite() ([]ClientInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []ClientInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(s.clientDir(e.Name()), "client.json")
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var lite clientInfoLite
		if err := json.Unmarshal(b, &lite); err != nil {
			continue
		}
		info := ClientInfo{
			ID:           lite.ID,
			Name:         lite.Name,
			Hostname:     lite.Hostname,
			LastSeen:     lite.LastSeen,
			LastBackupID: lite.LastBackupID,
			FileCount:    lite.FileCount,
		}
		if info.ID == "" {
			info.ID = e.Name()
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	return out, nil
}

func (s *Storage) ListBackups(clientID string) ([]BackupRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.clientDir(clientID), "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []BackupRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var rec BackupRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Storage) PruneBackups(clientID string, keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	recs, err := s.ListBackups(clientID)
	if err != nil {
		return 0, err
	}
	if len(recs) <= keep {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for _, rec := range recs[keep:] {
		_ = os.Remove(rec.ArchivePath)
		meta := filepath.Join(s.clientDir(clientID), "backups", rec.ID+".json")
		_ = os.Remove(meta)
		removed++
	}
	return removed, nil
}

func (s *Storage) DiskUsage() (clients int, backups int, bytes int64, err error) {
	clientsList, err := s.ListClients()
	if err != nil {
		return 0, 0, 0, err
	}
	clients = len(clientsList)
	for _, c := range clientsList {
		recs, err := s.ListBackups(c.ID)
		if err != nil {
			continue
		}
		backups += len(recs)
		for _, r := range recs {
			bytes += r.Bytes
		}
	}
	return clients, backups, bytes, nil
}

// ClientSummary is a client row with aggregate backup stats for the panel.
type ClientSummary struct {
	Client      ClientInfo
	BackupCount int
	StoredBytes int64
	LastBackup  BackupRecord
	LastMode    string
}

func (s *Storage) SummarizeClients() ([]ClientSummary, error) {
	clients, err := s.ListClientsLite()
	if err != nil {
		return nil, err
	}
	out := make([]ClientSummary, 0, len(clients))
	for _, c := range clients {
		recs, err := s.ListBackups(c.ID)
		if err != nil {
			continue
		}
		sum := ClientSummary{Client: c, BackupCount: len(recs)}
		for _, r := range recs {
			sum.StoredBytes += r.Bytes
		}
		if len(recs) > 0 {
			sum.LastBackup = recs[0]
			sum.LastMode = recs[0].Mode
		}
		out = append(out, sum)
	}
	return out, nil
}

// RecentBackups returns the newest backups across all clients (newest first).
func (s *Storage) RecentBackups(limit int) ([]BackupRecord, error) {
	clients, err := s.ListClients()
	if err != nil {
		return nil, err
	}
	var all []BackupRecord
	for _, c := range clients {
		recs, err := s.ListBackups(c.ID)
		if err != nil {
			continue
		}
		all = append(all, recs...)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func FormatBytes(n int64) string {
	return common.FormatBytes(n)
}
