package host

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hdmain/backupurvm/internal/protocol"
)

func TestBackupsToKeepExpandsChain(t *testing.T) {
	// Newest 3 seeds would drop full0, but incr1 needs it.
	recs := []BackupRecord{
		{ID: "incr3", Mode: protocol.ModeIncremental, BaseBackupID: "incr2", CreatedAt: time.Unix(30, 0)},
		{ID: "incr2", Mode: protocol.ModeIncremental, BaseBackupID: "incr1", CreatedAt: time.Unix(20, 0)},
		{ID: "incr1", Mode: protocol.ModeIncremental, BaseBackupID: "full0", CreatedAt: time.Unix(10, 0)},
		{ID: "full0", Mode: protocol.ModeFull, CreatedAt: time.Unix(0, 0)},
	}
	keep := backupsToKeep(recs, 3)
	for _, id := range []string{"incr3", "incr2", "incr1", "full0"} {
		if !keep[id] {
			t.Fatalf("expected to keep %s: %#v", id, keep)
		}
	}
}

func TestPruneBackupsPreservesFull(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	cid := "c1"
	mk := func(id, mode, base string, ts int64) {
		path := filepath.Join(dir, "clients", cid, "backups", id+".tar.zst")
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(id), 0o644); err != nil {
			t.Fatal(err)
		}
		rec := BackupRecord{
			ID: id, ClientID: cid, Mode: mode, BaseBackupID: base,
			ArchivePath: path, CreatedAt: time.Unix(ts, 0).UTC(),
		}
		if err := s.CommitBackup(rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	mk("full0", protocol.ModeFull, "", 1)
	mk("incr1", protocol.ModeIncremental, "full0", 2)
	mk("incr2", protocol.ModeIncremental, "incr1", 3)
	mk("incr3", protocol.ModeIncremental, "incr2", 4)

	n, err := s.PruneBackups(cid, 3)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("removed %d, want 0 (chain must keep full0)", n)
	}
	recs, err := s.ListBackups(cid)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 {
		t.Fatalf("got %d backups, want 4", len(recs))
	}
	if _, err := ResolveBackupChain(recs); err != nil {
		t.Fatalf("chain after prune: %v", err)
	}
}

func TestPruneBackupsRemovesOldCompleteChain(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	cid := "c1"
	mk := func(id, mode, base string, ts int64) {
		path := filepath.Join(dir, "clients", cid, "backups", id+".tar.zst")
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte(id), 0o644)
		rec := BackupRecord{
			ID: id, ClientID: cid, Mode: mode, BaseBackupID: base,
			ArchivePath: path, CreatedAt: time.Unix(ts, 0).UTC(),
		}
		if err := s.CommitBackup(rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	mk("fullA", protocol.ModeFull, "", 1)
	mk("incrA", protocol.ModeIncremental, "fullA", 2)
	mk("fullB", protocol.ModeFull, "", 3)
	mk("incrB", protocol.ModeIncremental, "fullB", 4)

	n, err := s.PruneBackups(cid, 2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}
	recs, _ := s.ListBackups(cid)
	if len(recs) != 2 {
		t.Fatalf("left %d", len(recs))
	}
	if _, err := ResolveBackupChain(recs); err != nil {
		t.Fatal(err)
	}
	if recs[0].ID != "incrB" && recs[1].ID != "incrB" {
		t.Fatalf("expected live chain B kept: %+v", recs)
	}
}
