package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hdmain/backupurvm/internal/archive"
	"github.com/hdmain/backupurvm/internal/common"
	"github.com/hdmain/backupurvm/internal/protocol"
)

// RecoverResult summarizes orphan re-index + chain diagnosis.
type RecoverResult struct {
	Reindexed   []BackupRecord
	Skipped     []string
	HasFull     bool
	LatestTip   string
	MergeTip    string
	MergeAge    time.Time
	ChainOK     bool
	ChainError  string
	FullCount   int
	IncrCount   int
	Advice      []string
}

// RecoverClient re-indexes orphan .tar.zst/.tar.gz archives (missing .json)
// from embedded meta, then diagnoses whether --latest can merge.
func (s *Storage) RecoverClient(clientID string) (RecoverResult, error) {
	var out RecoverResult
	dir := filepath.Join(s.clientDir(clientID), "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			out.Advice = append(out.Advice, "no backups directory — nothing to recover")
			return out, nil
		}
		return out, err
	}

	known, err := s.ListBackups(clientID)
	if err != nil {
		return out, err
	}
	have := map[string]bool{}
	for _, r := range known {
		have[r.ID] = true
	}

	info, _ := s.LoadClient(clientID)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".tar.zst") && !strings.HasSuffix(lower, ".tar.gz") {
			continue
		}
		path := filepath.Join(dir, name)
		meta, err := archive.ReadMeta(path)
		if err != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		id := meta.BackupID
		if id == "" {
			id = strings.TrimSuffix(strings.TrimSuffix(name, ".tar.zst"), ".tar.gz")
		}
		if have[id] {
			continue
		}
		st, err := os.Stat(path)
		if err != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		compress := meta.Compress
		if compress == "" {
			if strings.HasSuffix(lower, ".tar.gz") {
				compress = protocol.CompressGzip
			} else {
				compress = protocol.CompressZstd
			}
		}
		created := meta.CreatedAt
		if created.IsZero() {
			created = st.ModTime().UTC()
		}
		nameHint := info.Name
		if nameHint == "" {
			nameHint = meta.Hostname
		}
		rec := BackupRecord{
			ID:           id,
			ClientID:     clientID,
			ClientName:   nameHint,
			Hostname:     meta.Hostname,
			Mode:         meta.Mode,
			BaseBackupID: meta.BaseBackupID,
			Compress:     compress,
			ArchivePath:  path,
			Bytes:        st.Size(),
			FileCount:    len(meta.Files),
			DeletedCount: len(meta.Deleted),
			SourceRoot:   meta.SourceRoot,
			CreatedAt:    created,
		}
		if rec.Mode == "" {
			if rec.BaseBackupID == "" {
				rec.Mode = protocol.ModeFull
			} else {
				rec.Mode = protocol.ModeIncremental
			}
		}
		// Persist metadata only — do not rewrite client manifest (unknown).
		if err := s.writeBackupMeta(rec); err != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf("%s: write meta: %v", name, err))
			continue
		}
		have[id] = true
		out.Reindexed = append(out.Reindexed, rec)
	}

	recs, err := s.ListBackups(clientID)
	if err != nil {
		return out, err
	}
	for _, r := range recs {
		switch r.Mode {
		case protocol.ModeFull:
			out.FullCount++
			out.HasFull = true
		default:
			out.IncrCount++
		}
	}
	if len(recs) > 0 {
		out.LatestTip = recs[0].ID
	}

	if len(recs) == 0 {
		out.Advice = append(out.Advice, "no archives found for this client")
		return out, nil
	}

	chain, tip, err := ResolveBestMergeableChain(recs)
	if err != nil {
		out.ChainOK = false
		out.ChainError = err.Error()
		out.Advice = append(out.Advice,
			"cannot build a complete full tree from what is on disk",
			"check if the missing full .tar.zst still exists elsewhere (old disk, trash, snapshots)",
			"PARTIAL option: ./host download <client> --salvage  (merges remaining incrementals; incomplete)",
			fmt.Sprintf("or single archives: ./host download %s --backup <id>", shortID(clientID)),
		)
		return out, nil
	}
	out.ChainOK = true
	out.MergeTip = tip.ID
	out.MergeAge = tip.CreatedAt
	_ = chain
	if tip.ID != recs[0].ID {
		out.Advice = append(out.Advice,
			fmt.Sprintf("newest backup %s is NOT mergeable (broken chain)", recs[0].ID),
			fmt.Sprintf("best recoverable point is %s (%s)", tip.ID, tip.CreatedAt.Local().Format("2006-01-02 15:04")),
			"run: ./host download <client> --latest",
		)
	} else {
		out.Advice = append(out.Advice,
			fmt.Sprintf("chain OK through %s (%s, %d archives)", tip.ID, common.FormatBytes(sumBytes(chain)), len(chain)),
			"run: ./host download <client> --latest",
		)
	}
	return out, nil
}

func sumBytes(recs []BackupRecord) int64 {
	var n int64
	for _, r := range recs {
		n += r.Bytes
	}
	return n
}

func (s *Storage) writeBackupMeta(rec BackupRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.clientDir(rec.ClientID), "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, rec.ID+".json"), b, 0o644)
}
