package host

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadHubServeAndStop(t *testing.T) {
	prev := lookupPublicIP
	lookupPublicIP = func(time.Duration) (string, error) { return "203.0.113.10", nil }
	t.Cleanup(func() { lookupPublicIP = prev })

	dir := t.TempDir()
	path := filepath.Join(dir, "20260101-120000-abcdef.tar.zst")
	payload := []byte("backup-payload-data")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	d := NewDownloadHub(nil)
	rec := BackupRecord{ID: "20260101-120000-abcdef", ArchivePath: path, Bytes: int64(len(payload))}
	url, started, err := d.Start(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("expected started")
	}
	if want := "http://203.0.113.10:"; url[:len(want)] != want {
		t.Fatalf("url %q", url)
	}
	defer d.Stop()

	d.mu.Lock()
	addr := d.ln.Addr().String()
	token := d.token
	name := d.fileName
	d.mu.Unlock()

	local := "http://" + addr + "/" + token + "/" + name
	resp, err := http.Get(local)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != string(payload) {
		t.Fatalf("body %q", got)
	}

	// Wrong token → 404
	bad := "http://" + addr + "/wrong-token/" + name
	resp2, err := http.Get(bad)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp2.StatusCode)
	}

	d.Stop()
	time.Sleep(50 * time.Millisecond)
	if d.Active() {
		t.Fatal("still active after stop")
	}
}
