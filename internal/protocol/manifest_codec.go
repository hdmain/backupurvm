package protocol

import (
	"encoding/json"
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// EncodeManifestZstd compresses a file inventory for plan messages.
func EncodeManifestZstd(entries []FileEntry) ([]byte, error) {
	raw, err := json.Marshal(entries)
	if err != nil {
		return nil, err
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	return enc.EncodeAll(raw, make([]byte, 0, len(raw)/2)), nil
}

// DecodeManifestZstd expands a compressed file inventory.
func DecodeManifestZstd(blob []byte) ([]FileEntry, error) {
	if len(blob) == 0 {
		return nil, nil
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(blob, nil)
	if err != nil {
		return nil, fmt.Errorf("manifest zstd: %w", err)
	}
	var entries []FileEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("manifest json: %w", err)
	}
	return entries, nil
}

// PlanManifest returns the previous inventory from a plan (zstd preferred).
func PlanManifest(p Plan) ([]FileEntry, error) {
	if len(p.ManifestZstd) > 0 {
		return DecodeManifestZstd(p.ManifestZstd)
	}
	return p.LastManifest, nil
}
