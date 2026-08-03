package protocol

import (
	"fmt"
	"testing"
	"time"
)

func TestManifestZstdRoundTrip(t *testing.T) {
	in := make([]FileEntry, 5000)
	for i := range in {
		in[i] = FileEntry{
			Path:    fmt.Sprintf("root/dir%d/file%d.txt", i%50, i),
			Size:    int64(1000 + i),
			Mode:    0o644,
			ModTime: time.Unix(1_700_000_000+int64(i), 0).UTC(),
		}
	}
	blob, err := EncodeManifestZstd(in)
	if err != nil {
		t.Fatal(err)
	}
	rawJSONApprox := len(in) * 120
	if len(blob) >= rawJSONApprox {
		t.Fatalf("expected compression, blob=%d approxJSON=%d", len(blob), rawJSONApprox)
	}
	out, err := DecodeManifestZstd(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("len %d != %d", len(out), len(in))
	}
	if out[0].Path != in[0].Path || out[len(out)-1].Size != in[len(in)-1].Size {
		t.Fatal("mismatch")
	}
	plan := Plan{ManifestZstd: blob}
	got, err := PlanManifest(plan)
	if err != nil || len(got) != len(in) {
		t.Fatalf("PlanManifest: %v len=%d", err, len(got))
	}
}
