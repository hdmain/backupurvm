package archive

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Extract unpacks archivePath into destDir (created if needed).
// Returns the archive Meta. Meta file itself is not written to destDir.
// Existing files in destDir are overwritten; caller applies Deleted separately.
func Extract(archivePath, destDir string) (Meta, error) {
	var zero Meta
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return zero, err
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return zero, err
	}
	defer f.Close()

	var r io.Reader = f
	switch {
	case hasSuffix(archivePath, ".tar.zst"), hasSuffix(archivePath, ".zst"):
		zr, err := zstd.NewReader(f)
		if err != nil {
			return zero, err
		}
		defer zr.Close()
		r = zr
	case hasSuffix(archivePath, ".tar.gz"), hasSuffix(archivePath, ".tgz"), hasSuffix(archivePath, ".gz"):
		gr, err := gzip.NewReader(f)
		if err != nil {
			return zero, err
		}
		defer gr.Close()
		r = gr
	default:
		return zero, fmt.Errorf("archive: unknown extension for %s", archivePath)
	}

	tr := tar.NewReader(r)
	var meta Meta
	metaFound := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return zero, err
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name == MetaFileName {
			if err := json.NewDecoder(tr).Decode(&meta); err != nil {
				return zero, err
			}
			metaFound = true
			continue
		}
		if name == "" || name == "." || strings.Contains(name, "..") {
			continue
		}
		target := filepath.Join(destDir, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return zero, err
			}
		case tar.TypeSymlink:
			_ = os.Remove(target)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return zero, err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return zero, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return zero, err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return zero, err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return zero, err
			}
			out.Close()
			if !hdr.ModTime.IsZero() {
				_ = os.Chtimes(target, hdr.ModTime, hdr.ModTime)
			}
		default:
			// skip other types
		}
	}
	if !metaFound {
		return zero, fmt.Errorf("archive: meta not found")
	}
	return meta, nil
}
