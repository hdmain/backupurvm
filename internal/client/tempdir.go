package client

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/hdmain/backupurvm/internal/common"
	"github.com/hdmain/backupurvm/internal/protocol"
)

const defaultPackTempPath = "/var/tmp/backupurvm"

// resolveTempDir picks where to write packed archives before transfer.
// Explicit flag/env wins; otherwise use /var/tmp/backupurvm on disk.
// os.TempDir() (/tmp) is often a small tmpfs — too small for large ISOs.
func resolveTempDir(explicit string) (string, error) {
	if explicit != "" {
		return ensureTempDir(explicit)
	}
	if v := os.Getenv("BACKUPURVM_TEMP"); v != "" {
		return ensureTempDir(v)
	}
	return defaultPackTempDir()
}

func defaultPackTempDir() (string, error) {
	if dir, err := ensureTempDir(defaultPackTempPath); err == nil {
		return dir, nil
	}
	return ensureTempDir(filepath.Join(os.TempDir(), "backupurvm"))
}

func ensureTempDir(dir string) (string, error) {
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("temp dir %s: %w", dir, err)
	}
	return dir, nil
}

func availableBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// checkPackSpace verifies the temp filesystem has room for the archive.
// Needed bytes ≈ sum(entry sizes) + 10% overhead (compressed size is usually lower).
func checkPackSpace(tempDir string, entries []protocol.FileEntry) error {
	var need int64
	for _, e := range entries {
		if e.Size > 0 {
			need += e.Size
		}
	}
	need += need / 10
	if need < 64<<20 {
		need = 64 << 20
	}

	avail, err := availableBytes(tempDir)
	if err != nil {
		return nil
	}
	if int64(avail) < need {
		return fmt.Errorf(
			"not enough space in %s: need ~%s, have %s (set BACKUPURVM_TEMP or --temp to a directory on a larger disk)",
			tempDir, common.FormatBytes(need), common.FormatBytes(int64(avail)),
		)
	}
	return nil
}
