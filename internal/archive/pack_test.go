package archive

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hdmain/backupurvm/internal/protocol"
)

func TestPackGrowingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grow.log")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate a live log: after Lstat inside Pack, append more bytes via a
	// concurrent writer that grows the file while packing many copies of a
	// small delay isn't needed if we monkey-patch — instead open, write header
	// size 6, then append before copy by packing with a custom sequence.
	//
	// Practical approach: write a file, then during Pack the LimitReader path
	// is exercised by appending after we set up a larger-looking scenario via
	// direct addEntry after growing.
	out := filepath.Join(dir, "out.tar.zst")
	entries := []protocol.FileEntry{{
		Path:    "grow.log",
		Size:    6,
		Mode:    0o644,
		ModTime: time.Now().UTC(),
	}}

	// Grow the file before Pack's Lstat so header sees the larger size, then
	// grow again mid-pack by packing twice in a helper — simpler: grow then
	// pack while another goroutine appends continuously.
	done := make(chan struct{})
	go func() {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer f.Close()
		for {
			select {
			case <-done:
				return
			default:
				_, _ = f.WriteString("xxxxxxxx\n")
				time.Sleep(time.Millisecond)
			}
		}
	}()
	defer close(done)

	time.Sleep(5 * time.Millisecond)
	_, err := Pack(PackOptions{
		Root:     dir,
		OutPath:  out,
		Compress: protocol.CompressZstd,
		Entries:  entries,
		Meta: Meta{
			BackupID: "t1",
			Mode:     protocol.ModeFull,
			Files:    entries,
		},
	})
	if err != nil {
		t.Fatalf("pack growing file: %v", err)
	}
	if _, err := ReadMeta(out); err != nil {
		t.Fatalf("read meta: %v", err)
	}
}
