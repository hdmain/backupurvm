package host

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hdmain/backupurvm/internal/common"
)

// FindClient resolves a client by ID, name, or hostname (case-insensitive).
// Exact ID match wins; otherwise unique name/hostname/partial match.
func (s *Storage) FindClient(query string) (ClientInfo, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return ClientInfo{}, fmt.Errorf("client name or id required")
	}
	clients, err := s.ListClientsLite()
	if err != nil {
		return ClientInfo{}, err
	}
	q := strings.ToLower(query)

	for _, c := range clients {
		if strings.EqualFold(c.ID, query) {
			return c, nil
		}
	}

	var nameHits, hostHits, partial []ClientInfo
	for _, c := range clients {
		if strings.EqualFold(c.Name, query) {
			nameHits = append(nameHits, c)
		}
		if strings.EqualFold(c.Hostname, query) {
			hostHits = append(hostHits, c)
		}
		if strings.Contains(strings.ToLower(c.Name), q) ||
			strings.Contains(strings.ToLower(c.Hostname), q) ||
			strings.Contains(strings.ToLower(c.ID), q) {
			partial = append(partial, c)
		}
	}
	if len(nameHits) == 1 {
		return nameHits[0], nil
	}
	if len(nameHits) > 1 {
		return ClientInfo{}, fmt.Errorf("ambiguous client name %q (%d matches)", query, len(nameHits))
	}
	if len(hostHits) == 1 {
		return hostHits[0], nil
	}
	if len(hostHits) > 1 {
		return ClientInfo{}, fmt.Errorf("ambiguous hostname %q (%d matches)", query, len(hostHits))
	}
	if len(partial) == 1 {
		return partial[0], nil
	}
	if len(partial) > 1 {
		return ClientInfo{}, fmt.Errorf("ambiguous client %q (%d matches); use full id or name", query, len(partial))
	}
	return ClientInfo{}, fmt.Errorf("client not found: %q", query)
}

// FindBackup returns a backup by ID (exact or prefix) for a client.
// Empty backupID returns the newest backup.
func (s *Storage) FindBackup(clientID, backupID string) (BackupRecord, error) {
	backupID = strings.TrimSpace(backupID)
	recs, err := s.ListBackups(clientID)
	if err != nil {
		return BackupRecord{}, err
	}
	if backupID == "" {
		if len(recs) == 0 {
			return BackupRecord{}, fmt.Errorf("no backups for this client")
		}
		return recs[0], nil
	}
	var hits []BackupRecord
	for _, r := range recs {
		if r.ID == backupID || strings.HasPrefix(r.ID, backupID) {
			hits = append(hits, r)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) > 1 {
		return BackupRecord{}, fmt.Errorf("ambiguous backup id %q (%d matches)", backupID, len(hits))
	}
	return BackupRecord{}, fmt.Errorf("backup not found: %q", backupID)
}

// FormatClientLine is a one-line client summary for CLI listing.
func FormatClientLine(c ClientInfo, backups int, bytes int64) string {
	name := c.Name
	if name == "" {
		name = c.Hostname
	}
	if name == "" {
		name = c.ID
	}
	seen := "-"
	if !c.LastSeen.IsZero() {
		seen = c.LastSeen.Local().Format("2006-01-02 15:04")
	}
	return fmt.Sprintf("%-24s  id=%s  backups=%d  %s  last_seen=%s",
		name, shortID(c.ID), backups, common.FormatBytes(bytes), seen)
}

// FormatBackupLine is a one-line backup summary for CLI listing.
func FormatBackupLine(r BackupRecord) string {
	return fmt.Sprintf("%s  %-12s  %10s  files=%d  %s",
		r.ID, r.Mode, common.FormatBytes(r.Bytes), r.FileCount,
		r.CreatedAt.Local().Format("2006-01-02 15:04:05"))
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// WaitOrTimeout closes when done fires or after timeout (if > 0).
func WaitOrTimeout(done <-chan struct{}, timeout time.Duration) {
	if timeout <= 0 {
		<-done
		return
	}
	select {
	case <-done:
	case <-time.After(timeout):
		fmt.Fprintln(os.Stderr, "download timeout — stopping")
	}
}
