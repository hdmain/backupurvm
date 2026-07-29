package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hdmain/backupurvm/internal/protocol"
)

// Scan walks root and builds a sorted file inventory. Symlinks are recorded as
// zero-size entries; regular files are optionally hashed.
func Scan(root string, hashFiles bool) ([]protocol.FileEntry, error) {
	root = filepath.Clean(root)
	var entries []protocol.FileEntry

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsPermission(err) || os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || strings.HasPrefix(rel, "../") {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		ent := protocol.FileEntry{
			Path:    rel,
			Size:    info.Size(),
			Mode:    uint32(info.Mode()),
			ModTime: info.ModTime().UTC().Truncate(time.Second),
		}

		if d.Type()&os.ModeSymlink != 0 {
			ent.Size = 0
			entries = append(entries, ent)
			return nil
		}
		if !d.Type().IsRegular() {
			if d.IsDir() {
				ent.Size = 0
				entries = append(entries, ent)
			}
			return nil
		}

		if hashFiles {
			sum, err := hashFile(path)
			if err != nil {
				return nil
			}
			ent.SHA256 = sum
		}
		entries = append(entries, ent)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	return entries, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Diff returns files that must be packed for an incremental backup and paths
// present in old but missing in current (deleted).
func Diff(old, current []protocol.FileEntry) (changed []protocol.FileEntry, deleted []string) {
	oldMap := index(old)
	curMap := index(current)

	for _, e := range current {
		prev, ok := oldMap[e.Path]
		if !ok || changedSince(prev, e) {
			changed = append(changed, e)
		}
	}
	for path := range oldMap {
		if _, ok := curMap[path]; !ok {
			deleted = append(deleted, path)
		}
	}
	sort.Strings(deleted)
	return changed, deleted
}

func index(entries []protocol.FileEntry) map[string]protocol.FileEntry {
	m := make(map[string]protocol.FileEntry, len(entries))
	for _, e := range entries {
		m[e.Path] = e
	}
	return m
}

func changedSince(old, cur protocol.FileEntry) bool {
	if old.Mode != cur.Mode {
		return true
	}
	if old.Size != cur.Size {
		return true
	}
	if !old.ModTime.Equal(cur.ModTime) {
		return true
	}
	if old.SHA256 != "" && cur.SHA256 != "" && old.SHA256 != cur.SHA256 {
		return true
	}
	return false
}

func WriteJSON(path string, entries []protocol.FileEntry) error {
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func ReadJSON(path string) ([]protocol.FileEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []protocol.FileEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return entries, nil
}
