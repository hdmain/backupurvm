package host

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hdmain/backupurvm/internal/archive"
	"github.com/hdmain/backupurvm/internal/protocol"
)

// DownloadHub serves a single backup archive over HTTP until toggled off.
type DownloadHub struct {
	mu       sync.Mutex
	log      *log.Logger
	srv      *http.Server
	ln       net.Listener
	url      string
	token    string
	fileName string
	filePath string
	tempPath string // removed on Stop when set
	active   bool
	building bool
}

func NewDownloadHub(logger *log.Logger) *DownloadHub {
	if logger == nil {
		logger = log.Default()
	}
	return &DownloadHub{log: logger}
}

// Active reports whether a download server is running.
func (d *DownloadHub) Active() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active
}

// URL returns the public download URL, or empty if inactive.
func (d *DownloadHub) URL() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.url
}

// Building reports whether a merged latest archive is being prepared.
func (d *DownloadHub) Building() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.building
}

// Info returns active status, URL, and filename.
func (d *DownloadHub) Info() (active bool, url, fileName string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active, d.url, d.fileName
}

// Toggle starts a download server for rec, or stops the current one if already active.
func (d *DownloadHub) Toggle(rec BackupRecord) (url string, started bool, err error) {
	d.mu.Lock()
	if d.active || d.building {
		d.mu.Unlock()
		d.Stop()
		return "", false, nil
	}
	d.mu.Unlock()
	return d.Start(rec)
}

// ToggleLatest builds a merged full archive (full + incrementals) and serves it,
// or stops the server if already active.
func (d *DownloadHub) ToggleLatest(recs []BackupRecord, compress string) (url string, started bool, err error) {
	d.mu.Lock()
	if d.active || d.building {
		d.mu.Unlock()
		d.Stop()
		return "", false, nil
	}
	d.building = true
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		d.building = false
		d.mu.Unlock()
	}()

	tmpDir, err := os.MkdirTemp("", "backupurvm-dl-*")
	if err != nil {
		return "", false, err
	}
	outName := "latest-full" + archive.ExtFor(compress)
	if compress == "" {
		outName = "latest-full" + archive.ExtFor(protocol.CompressZstd)
	}
	outPath := filepath.Join(tmpDir, outName)
	rec, err := BuildLatestFullArchive(recs, outPath, compress)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", false, err
	}
	// Prefer a stable friendly name.
	friendly := sanitizeDownloadName(rec.ClientName)
	if friendly == "" {
		friendly = "backup"
	}
	finalName := friendly + "-latest-full" + archive.ExtFor(rec.Compress)
	finalPath := filepath.Join(tmpDir, finalName)
	if finalPath != outPath {
		if err := os.Rename(outPath, finalPath); err != nil {
			finalPath = outPath
			finalName = filepath.Base(outPath)
		} else {
			rec.ArchivePath = finalPath
		}
	}
	rec.ArchivePath = finalPath

	url, started, err = d.startFile(rec.ArchivePath, finalName, true)
	if err != nil || !started {
		_ = os.RemoveAll(tmpDir)
		return url, started, err
	}
	d.mu.Lock()
	d.tempPath = finalPath
	d.mu.Unlock()
	return url, started, nil
}

// Start exposes rec.ArchivePath on a random port until Stop.
func (d *DownloadHub) Start(rec BackupRecord) (url string, started bool, err error) {
	return d.startFile(rec.ArchivePath, filepath.Base(rec.ArchivePath), false)
}

func (d *DownloadHub) startFile(path, fileName string, isTemp bool) (url string, started bool, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.active {
		return d.url, true, nil
	}

	if path == "" {
		return "", false, fmt.Errorf("backup has no archive path")
	}
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a file")
		}
		return "", false, fmt.Errorf("archive missing: %w", err)
	}
	if fileName == "" {
		fileName = filepath.Base(path)
	}

	token, err := randomUUID()
	if err != nil {
		return "", false, err
	}

	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return "", false, fmt.Errorf("listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	pubIP, err := lookupPublicIP(8 * time.Second)
	if err != nil {
		_ = ln.Close()
		return "", false, fmt.Errorf("public IP (ifconfig.com): %w", err)
	}

	mux := http.NewServeMux()
	pattern := "/" + token + "/" + fileName
	servePath := path
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		f, err := os.Open(servePath)
		if err != nil {
			http.Error(w, "file unavailable", http.StatusNotFound)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			http.Error(w, "file unavailable", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", st.Size()))
		http.ServeContent(w, r, fileName, st.ModTime(), f)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	publicURL := fmt.Sprintf("http://%s:%d/%s/%s", pubIP, port, token, fileName)

	d.srv = srv
	d.ln = ln
	d.url = publicURL
	d.token = token
	d.fileName = fileName
	d.filePath = path
	if isTemp {
		d.tempPath = path
	}
	d.active = true

	go func() {
		d.log.Printf("download server listening on :%d → %s", port, publicURL)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			d.log.Printf("download server: %v", err)
		}
		d.mu.Lock()
		d.active = false
		d.url = ""
		d.mu.Unlock()
	}()

	return publicURL, true, nil
}

// Stop shuts down the download HTTP server if running.
func (d *DownloadHub) Stop() {
	d.mu.Lock()
	srv := d.srv
	ln := d.ln
	temp := d.tempPath
	d.srv = nil
	d.ln = nil
	d.active = false
	d.building = false
	d.url = ""
	d.token = ""
	d.fileName = ""
	d.filePath = ""
	d.tempPath = ""
	d.mu.Unlock()

	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}
	if ln != nil {
		_ = ln.Close()
	}
	if temp != "" {
		_ = os.Remove(temp)
		_ = os.RemoveAll(filepath.Dir(temp))
	}
	d.log.Printf("download server stopped")
}

func sanitizeDownloadName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ', r == '.':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func randomUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// lookupPublicIP is swapped in tests.
var lookupPublicIP = fetchPublicIP

func fetchPublicIP(timeout time.Duration) (string, error) {
	client := &http.Client{Timeout: timeout}
	// Prefer ifconfig.com (plain-text IP). Fall back if TLS/network fails.
	urls := []string{
		"https://ifconfig.com",
		"http://ifconfig.com",
		"https://ifconfig.me/ip",
		"https://api.ipify.org",
	}
	var lastErr error
	for _, u := range urls {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "curl/8.0")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
			continue
		}
		ip := strings.TrimSpace(string(body))
		// Strip accidental HTML / noise — take first token.
		if i := strings.IndexAny(ip, " \n\t\r<>"); i >= 0 {
			ip = ip[:i]
		}
		parsed := net.ParseIP(ip)
		if parsed == nil {
			lastErr = fmt.Errorf("%s: not an IP: %q", u, truncateStr(ip, 40))
			continue
		}
		v4 := parsed.To4()
		if v4 == nil {
			lastErr = fmt.Errorf("%s: IPv6 ignored (%s)", u, ip)
			continue
		}
		return v4.String(), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no public IP")
	}
	return "", lastErr
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
