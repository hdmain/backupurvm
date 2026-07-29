package archive

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// ReadMeta extracts .backupurvm-meta.json from a packed archive.
func ReadMeta(path string) (Meta, error) {
	var zero Meta
	f, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	defer f.Close()

	var r io.Reader = f
	switch {
	case hasSuffix(path, ".tar.zst"), hasSuffix(path, ".zst"):
		zr, err := zstd.NewReader(f)
		if err != nil {
			return zero, err
		}
		defer zr.Close()
		r = zr
	case hasSuffix(path, ".tar.gz"), hasSuffix(path, ".tgz"), hasSuffix(path, ".gz"):
		gr, err := gzip.NewReader(f)
		if err != nil {
			return zero, err
		}
		defer gr.Close()
		r = gr
	default:
		return zero, fmt.Errorf("archive: unknown extension for %s", path)
	}

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return zero, err
		}
		if hdr.Name == MetaFileName || hdr.Name == "./"+MetaFileName {
			var m Meta
			if err := json.NewDecoder(tr).Decode(&m); err != nil {
				return zero, err
			}
			return m, nil
		}
	}
	return zero, fmt.Errorf("archive: meta not found")
}

func hasSuffix(s, suf string) bool {
	return len(s) >= len(suf) && s[len(s)-len(suf):] == suf
}
