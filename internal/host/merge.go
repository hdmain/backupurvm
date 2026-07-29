package host

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hdmain/backupurvm/internal/archive"
	"github.com/hdmain/backupurvm/internal/manifest"
	"github.com/hdmain/backupurvm/internal/protocol"
)

// ResolveBackupChain returns chronological archives from the last full
// through to the latest backup (inclusive), walking BaseBackupID.
func ResolveBackupChain(recs []BackupRecord) ([]BackupRecord, error) {
	if len(recs) == 0 {
		return nil, fmt.Errorf("no backups")
	}
	byID := make(map[string]BackupRecord, len(recs))
	for _, r := range recs {
		byID[r.ID] = r
	}
	// ListBackups is newest-first; start from latest.
	cur := recs[0]
	var rev []BackupRecord
	seen := map[string]bool{}
	for {
		if seen[cur.ID] {
			return nil, fmt.Errorf("backup chain loop at %s", cur.ID)
		}
		seen[cur.ID] = true
		rev = append(rev, cur)
		if cur.Mode == protocol.ModeFull || strings.TrimSpace(cur.BaseBackupID) == "" {
			break
		}
		prev, ok := byID[cur.BaseBackupID]
		if !ok {
			return nil, fmt.Errorf("broken chain: missing base backup %s (needed for %s)", cur.BaseBackupID, cur.ID)
		}
		cur = prev
	}
	if rev[len(rev)-1].Mode != protocol.ModeFull {
		return nil, fmt.Errorf("no full backup in chain ending at %s", recs[0].ID)
	}
	// chronological: full … latest
	out := make([]BackupRecord, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i])
	}
	return out, nil
}

// BuildLatestFullArchive materializes full+incrementals into one full archive at outPath.
func BuildLatestFullArchive(recs []BackupRecord, outPath, compress string) (BackupRecord, error) {
	var zero BackupRecord
	chain, err := ResolveBackupChain(recs)
	if err != nil {
		return zero, err
	}
	if compress == "" {
		compress = protocol.CompressZstd
		if chain[len(chain)-1].Compress != "" {
			compress = chain[len(chain)-1].Compress
		}
	}

	work, err := os.MkdirTemp("", "backupurvm-merge-*")
	if err != nil {
		return zero, err
	}
	defer os.RemoveAll(work)
	tree := filepath.Join(work, "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		return zero, err
	}

	var last MetaLike
	for _, rec := range chain {
		meta, err := archive.Extract(rec.ArchivePath, tree)
		if err != nil {
			return zero, fmt.Errorf("extract %s: %w", rec.ID, err)
		}
		for _, del := range meta.Deleted {
			rel := filepath.FromSlash(del)
			if rel == "" || strings.Contains(rel, "..") {
				continue
			}
			_ = os.RemoveAll(filepath.Join(tree, rel))
		}
		last = MetaLike{
			Hostname:   meta.Hostname,
			SourceRoot: meta.SourceRoot,
			ClientName: rec.ClientName,
			ClientID:   rec.ClientID,
		}
	}

	entries, err := manifest.Scan(tree, false)
	if err != nil {
		return zero, fmt.Errorf("scan merged tree: %w", err)
	}
	// Paths from Scan are relative to tree — good for Pack.

	id := "latest-full-" + time.Now().UTC().Format("20060102-150405")
	meta := archive.Meta{
		BackupID:   id,
		Mode:       protocol.ModeFull,
		Compress:   compress,
		Hostname:   last.Hostname,
		SourceRoot: last.SourceRoot,
		Files:      entries,
		CreatedAt:  time.Now().UTC(),
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return zero, err
	}
	size, err := archive.Pack(archive.PackOptions{
		Root:     tree,
		OutPath:  outPath,
		Compress: compress,
		Entries:  entries,
		Meta:     meta,
	})
	if err != nil {
		return zero, err
	}

	name := last.ClientName
	if name == "" {
		name = last.Hostname
	}
	return BackupRecord{
		ID:          id,
		ClientID:    last.ClientID,
		ClientName:  name,
		Hostname:    last.Hostname,
		Mode:        protocol.ModeFull,
		Compress:    compress,
		ArchivePath: outPath,
		Bytes:       size,
		FileCount:   len(entries),
		SourceRoot:  last.SourceRoot,
		CreatedAt:   time.Now().UTC(),
	}, nil
}

type MetaLike struct {
	Hostname   string
	SourceRoot string
	ClientName string
	ClientID   string
}

// SortBackupsNewestFirst ensures ListBackups order.
func SortBackupsNewestFirst(recs []BackupRecord) {
	sort.Slice(recs, func(i, j int) bool {
		return recs[i].CreatedAt.After(recs[j].CreatedAt)
	})
}
