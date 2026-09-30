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
	return resolveChainFrom(recs[0], indexRecords(recs))
}

// ResolveBestMergeableChain finds the newest tip whose full→…→tip chain is intact.
// If the absolute latest is broken (pruned base), an older tip may still restore.
func ResolveBestMergeableChain(recs []BackupRecord) (chain []BackupRecord, tip BackupRecord, err error) {
	if len(recs) == 0 {
		return nil, BackupRecord{}, fmt.Errorf("no backups")
	}
	byID := indexRecords(recs)
	var lastErr error
	for _, tipRec := range recs {
		ch, e := resolveChainFrom(tipRec, byID)
		if e != nil {
			lastErr = e
			continue
		}
		return ch, tipRec, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no mergeable full→incremental chain")
	}
	return nil, BackupRecord{}, fmt.Errorf("%w\n"+
		"no complete full backup left on disk — use: ./host download <client> --salvage\n"+
		"(partial restore from incrementals only; files unchanged since the missing full are gone)", lastErr)
}

func indexRecords(recs []BackupRecord) map[string]BackupRecord {
	byID := make(map[string]BackupRecord, len(recs))
	for _, r := range recs {
		byID[r.ID] = r
	}
	return byID
}

func resolveChainFrom(tip BackupRecord, byID map[string]BackupRecord) ([]BackupRecord, error) {
	cur := tip
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
		return nil, fmt.Errorf("no full backup in chain ending at %s", tip.ID)
	}
	for _, r := range rev {
		if err := archivePresent(r); err != nil {
			return nil, err
		}
	}
	return reverseRecords(rev), nil
}

// ResolveSalvageChain walks from the newest tip backward as far as archives
// still exist, even if the full base is missing. Chronological order.
func ResolveSalvageChain(recs []BackupRecord) ([]BackupRecord, error) {
	if len(recs) == 0 {
		return nil, fmt.Errorf("no backups")
	}
	byID := indexRecords(recs)
	cur := recs[0]
	var rev []BackupRecord
	seen := map[string]bool{}
	for {
		if seen[cur.ID] {
			return nil, fmt.Errorf("backup chain loop at %s", cur.ID)
		}
		seen[cur.ID] = true
		if err := archivePresent(cur); err != nil {
			return nil, err
		}
		rev = append(rev, cur)
		if cur.Mode == protocol.ModeFull || strings.TrimSpace(cur.BaseBackupID) == "" {
			break
		}
		prev, ok := byID[cur.BaseBackupID]
		if !ok {
			break // stop at gap — salvage what we have
		}
		cur = prev
	}
	if len(rev) == 0 {
		return nil, fmt.Errorf("nothing to salvage")
	}
	return reverseRecords(rev), nil
}

func archivePresent(r BackupRecord) error {
	if r.ArchivePath == "" {
		return fmt.Errorf("backup %s has no archive path", r.ID)
	}
	if st, err := os.Stat(r.ArchivePath); err != nil || st.IsDir() {
		return fmt.Errorf("archive missing for %s (%s)", r.ID, r.ArchivePath)
	}
	return nil
}

func reverseRecords(rev []BackupRecord) []BackupRecord {
	out := make([]BackupRecord, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i])
	}
	return out
}

// MergeOptions controls where temporary extract trees are created.
type MergeOptions struct {
	WorkDir string // parent for MkdirTemp; empty = os.TempDir()
}

// BuildLatestFullArchive materializes full+incrementals into one full archive at outPath.
// Uses the newest intact chain (may be older than the absolute latest if prune broke it).
func BuildLatestFullArchive(recs []BackupRecord, outPath, compress string, opts ...MergeOptions) (BackupRecord, error) {
	chain, _, err := ResolveBestMergeableChain(recs)
	if err != nil {
		return BackupRecord{}, err
	}
	var o MergeOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	return buildMergedArchive(chain, outPath, compress, "latest-full", false, o)
}

// BuildSalvageArchive merges whatever incrementals remain (no full required).
// Result is incomplete: files that never changed after the missing full are absent.
func BuildSalvageArchive(recs []BackupRecord, outPath, compress string, opts ...MergeOptions) (BackupRecord, error) {
	chain, err := ResolveSalvageChain(recs)
	if err != nil {
		return BackupRecord{}, err
	}
	var o MergeOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	return buildMergedArchive(chain, outPath, compress, "salvage-partial", true, o)
}

func buildMergedArchive(chain []BackupRecord, outPath, compress, idPrefix string, partial bool, opts MergeOptions) (BackupRecord, error) {
	var zero BackupRecord
	if len(chain) == 0 {
		return zero, fmt.Errorf("empty chain")
	}
	if compress == "" {
		compress = protocol.CompressZstd
		if chain[len(chain)-1].Compress != "" {
			compress = chain[len(chain)-1].Compress
		}
	}

	parent := opts.WorkDir
	if parent == "" {
		parent = os.TempDir()
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return zero, fmt.Errorf("temp dir %s: %w", parent, err)
	}
	work, err := os.MkdirTemp(parent, "backupurvm-merge-*")
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

	id := idPrefix + "-" + time.Now().UTC().Format("20060102-150405")
	root := last.SourceRoot
	if partial && root != "" {
		root = root + " (PARTIAL — missing full base)"
	}
	meta := archive.Meta{
		BackupID:   id,
		Mode:       protocol.ModeFull,
		Compress:   compress,
		Hostname:   last.Hostname,
		SourceRoot: root,
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
		SourceRoot:  root,
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
