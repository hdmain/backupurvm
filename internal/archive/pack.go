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
	"time"

	"github.com/hdmain/backupurvm/internal/protocol"
	"github.com/klauspost/compress/zstd"
)

// MetaFileName is stored at the root of every backup archive.
const MetaFileName = ".backupurvm-meta.json"

// Meta describes the archive contents for the host.
type Meta struct {
	BackupID     string               `json:"backup_id"`
	Mode         string               `json:"mode"`
	BaseBackupID string               `json:"base_backup_id,omitempty"`
	Compress     string               `json:"compress"`
	Hostname     string               `json:"hostname"`
	SourceRoot   string               `json:"source_root"`
	CreatedAt    time.Time            `json:"created_at"`
	Files        []protocol.FileEntry `json:"files"`
	Deleted      []string             `json:"deleted,omitempty"`
}

// PackOptions controls archive creation.
type PackOptions struct {
	Root       string
	OutPath    string
	Compress   string // zstd|gzip
	Entries    []protocol.FileEntry // files/dirs to include (relative paths)
	Meta       Meta
}

// Pack creates a compressed tar at OutPath containing Meta plus listed entries.
func Pack(opts PackOptions) (int64, error) {
	if opts.Root == "" || opts.OutPath == "" {
		return 0, fmt.Errorf("archive: root and out path required")
	}
	compress := opts.Compress
	if compress == "" {
		compress = protocol.CompressZstd
	}
	opts.Meta.Compress = compress
	opts.Meta.CreatedAt = time.Now().UTC()

	f, err := os.Create(opts.OutPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var (
		wc io.WriteCloser
		tw *tar.Writer
	)

	switch compress {
	case protocol.CompressGzip:
		gw := gzip.NewWriter(f)
		wc = gw
		tw = tar.NewWriter(gw)
	case protocol.CompressZstd:
		zw, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err != nil {
			return 0, err
		}
		wc = zw
		tw = tar.NewWriter(zw)
	default:
		return 0, fmt.Errorf("archive: unknown compress %q", compress)
	}

	metaBytes, err := json.MarshalIndent(opts.Meta, "", "  ")
	if err != nil {
		_ = tw.Close()
		_ = wc.Close()
		return 0, err
	}
	if err := writeBytes(tw, MetaFileName, metaBytes, 0o644); err != nil {
		_ = tw.Close()
		_ = wc.Close()
		return 0, err
	}

	root := filepath.Clean(opts.Root)
	for _, ent := range opts.Entries {
		if err := addEntry(tw, root, ent); err != nil {
			_ = tw.Close()
			_ = wc.Close()
			return 0, fmt.Errorf("archive: %s: %w", ent.Path, err)
		}
	}

	if err := tw.Close(); err != nil {
		_ = wc.Close()
		return 0, err
	}
	if err := wc.Close(); err != nil {
		return 0, err
	}
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func writeBytes(tw *tar.Writer, name string, data []byte, mode int64) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    int64(len(data)),
		ModTime: time.Now().UTC(),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func addEntry(tw *tar.Writer, root string, ent protocol.FileEntry) error {
	rel := filepath.FromSlash(ent.Path)
	if rel == "" || strings.Contains(rel, "..") {
		return fmt.Errorf("invalid path")
	}
	full := filepath.Join(root, rel)

	info, err := os.Lstat(full)
	if err != nil {
		return err
	}

	link := ""
	if info.Mode()&os.ModeSymlink != 0 {
		link, err = os.Readlink(full)
		if err != nil {
			return err
		}
	}

	hdr, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return err
	}
	hdr.Name = filepath.ToSlash(ent.Path)
	hdr.ModTime = ent.ModTime
	if info.IsDir() && !strings.HasSuffix(hdr.Name, "/") {
		hdr.Name += "/"
	}

	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(full)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

// ExtFor returns the filename extension for a compression type.
func ExtFor(compress string) string {
	switch compress {
	case protocol.CompressGzip:
		return ".tar.gz"
	default:
		return ".tar.zst"
	}
}
