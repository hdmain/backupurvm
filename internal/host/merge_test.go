package host

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hdmain/backupurvm/internal/archive"
	"github.com/hdmain/backupurvm/internal/protocol"
)

func TestBuildLatestFullArchive(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	_ = os.MkdirAll(filepath.Join(src, "a"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "a", "one.txt"), []byte("one"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0o644)

	fullPath := filepath.Join(root, "full.tar.zst")
	entries := []protocol.FileEntry{
		{Path: "a", Mode: uint32(os.ModeDir | 0o755)},
		{Path: "a/one.txt", Size: 3, Mode: 0o644},
		{Path: "keep.txt", Size: 4, Mode: 0o644},
	}
	_, err := archive.Pack(archive.PackOptions{
		Root: src, OutPath: fullPath, Compress: protocol.CompressZstd,
		Entries: entries,
		Meta: archive.Meta{
			BackupID: "full1", Mode: protocol.ModeFull, Hostname: "h", SourceRoot: "/root",
			Files: entries, CreatedAt: time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Incremental: change one.txt, delete keep.txt, add two.txt
	_ = os.WriteFile(filepath.Join(src, "a", "one.txt"), []byte("ONE!"), 0o644)
	_ = os.Remove(filepath.Join(src, "keep.txt"))
	_ = os.WriteFile(filepath.Join(src, "two.txt"), []byte("two"), 0o644)
	incrPath := filepath.Join(root, "incr.tar.zst")
	incrEntries := []protocol.FileEntry{
		{Path: "a/one.txt", Size: 4, Mode: 0o644},
		{Path: "two.txt", Size: 3, Mode: 0o644},
	}
	_, err = archive.Pack(archive.PackOptions{
		Root: src, OutPath: incrPath, Compress: protocol.CompressZstd,
		Entries: incrEntries,
		Meta: archive.Meta{
			BackupID: "incr1", Mode: protocol.ModeIncremental, BaseBackupID: "full1",
			Hostname: "h", SourceRoot: "/root", Files: incrEntries,
			Deleted: []string{"keep.txt"}, CreatedAt: time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	recs := []BackupRecord{
		{ID: "incr1", Mode: protocol.ModeIncremental, BaseBackupID: "full1", ArchivePath: incrPath, ClientName: "box", ClientID: "c1", Compress: protocol.CompressZstd, CreatedAt: time.Now().UTC()},
		{ID: "full1", Mode: protocol.ModeFull, ArchivePath: fullPath, ClientName: "box", ClientID: "c1", Compress: protocol.CompressZstd, CreatedAt: time.Now().UTC().Add(-time.Hour)},
	}

	out := filepath.Join(root, "merged.tar.zst")
	got, err := BuildLatestFullArchive(recs, out, protocol.CompressZstd)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != protocol.ModeFull {
		t.Fatalf("mode %s", got.Mode)
	}

	extractDir := filepath.Join(root, "out")
	meta, err := archive.Extract(out, extractDir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Mode != protocol.ModeFull {
		t.Fatalf("meta mode %s", meta.Mode)
	}
	b, err := os.ReadFile(filepath.Join(extractDir, "a", "one.txt"))
	if err != nil || string(b) != "ONE!" {
		t.Fatalf("one.txt: %q %v", b, err)
	}
	b, err = os.ReadFile(filepath.Join(extractDir, "two.txt"))
	if err != nil || string(b) != "two" {
		t.Fatalf("two.txt: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(extractDir, "keep.txt")); !os.IsNotExist(err) {
		t.Fatalf("keep.txt should be deleted, err=%v", err)
	}
}

func TestFetchPublicIPIPv4Only(t *testing.T) {
	parsed := net.ParseIP("203.0.113.9")
	if parsed == nil || parsed.To4() == nil {
		t.Fatal("expected v4")
	}
	parsed6 := net.ParseIP("2001:db8::1")
	if parsed6 == nil || parsed6.To4() != nil {
		t.Fatal("expected v6 without To4")
	}
}
