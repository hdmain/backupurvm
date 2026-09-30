//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/hdmain/backupurvm/internal/common"
	"github.com/hdmain/backupurvm/internal/host"
	"github.com/muesli/termenv"
)

func main() {
	log.SetFlags(0)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "clients", "list":
			runClients(os.Args[2:])
			return
		case "backups":
			runBackups(os.Args[2:])
			return
		case "download", "dl":
			runDownload(os.Args[2:])
			return
		case "recover":
			runRecover(os.Args[2:])
			return
		case "help", "-h", "--help":
			printUsage()
			return
		case "serve", "run":
			runDaemon(os.Args[2:])
			return
		}
		// Unknown first arg that looks like a flag → daemon mode (compat).
		if os.Args[1] == "-config" || os.Args[1] == "--config" ||
			len(os.Args[1]) > 1 && os.Args[1][0] == '-' {
			runDaemon(os.Args[1:])
			return
		}
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
	runDaemon(nil)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `backupurvm host

Usage:
  backupurvm-host [-config PATH]                 start host daemon (backup + SSH)
  backupurvm-host clients [-config PATH]         list servers
  backupurvm-host backups <client> [-config PATH]
  backupurvm-host download <client> [flags]      expose HTTP download link (no SSH)
  backupurvm-host recover <client>               re-index orphan archives + diagnose chain

Download flags:
  --backup ID     specific archive (id or prefix; default: newest single archive)
  --latest        merge best available full + incrementals into one archive
  --salvage       PARTIAL merge from incrementals only (when full base is gone)
  --temp DIR      merge work dir (default: <data_dir>/tmp — use a big disk)
  --timeout DUR   stop after duration (e.g. 1h); default: until Ctrl+C
  --config PATH   host config (default: config.yml)

Examples:
  backupurvm-host clients
  backupurvm-host backups myvps
  backupurvm-host recover myvps
  backupurvm-host download myvps --latest
  backupurvm-host download myvps --salvage
  backupurvm-host download myvps --salvage --temp /storage/tmp
  backupurvm-host download myvps --backup 20260829-0200
`)
}

func configFlag(fs *flag.FlagSet) *string {
	return fs.String("config", "config.yml", "path to host config")
}

// parseArgs accepts flags before or after positionals (Go's flag stops at the first
// non-flag, so `download takevps --latest` would otherwise ignore --latest).
// valueFlags lists flag names that take an argument (without leading dashes).
func parseArgs(fs *flag.FlagSet, args []string, valueFlags ...string) error {
	needsValue := make(map[string]bool, len(valueFlags))
	for _, n := range valueFlags {
		needsValue[n] = true
	}
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name = name[:eq]
			} else if needsValue[name] && i+1 < len(args) && (len(args[i+1]) == 0 || args[i+1][0] != '-') {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	return fs.Parse(append(flags, pos...))
}

func openStorage(configPath string) (*host.ConfigStore, *host.Storage, error) {
	if err := host.EnsureConfig(configPath); err != nil {
		return nil, nil, err
	}
	store, err := host.NewConfigStore(configPath)
	if err != nil {
		return nil, nil, err
	}
	cfg := store.Get()
	storage, err := host.NewStorage(cfg.DataDir)
	if err != nil {
		return nil, nil, err
	}
	return store, storage, nil
}

func runClients(args []string) {
	fs := flag.NewFlagSet("clients", flag.ExitOnError)
	cfgPath := configFlag(fs)
	_ = parseArgs(fs, args, "config")

	_, storage, err := openStorage(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	sums, err := storage.SummarizeClients()
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	if len(sums) == 0 {
		fmt.Println("(no clients)")
		return
	}
	for _, s := range sums {
		fmt.Println(host.FormatClientLine(s.Client, s.BackupCount, s.StoredBytes))
	}
}

func runBackups(args []string) {
	fs := flag.NewFlagSet("backups", flag.ExitOnError)
	cfgPath := configFlag(fs)
	_ = parseArgs(fs, args, "config")
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: backupurvm-host backups <client>")
		os.Exit(2)
	}

	_, storage, err := openStorage(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	client, err := storage.FindClient(fs.Arg(0))
	if err != nil {
		log.Fatalf("%v", err)
	}
	recs, err := storage.ListBackups(client.ID)
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	name := client.Name
	if name == "" {
		name = client.Hostname
	}
	fmt.Fprintf(os.Stderr, "client %s (%s) — %d backups\n", name, client.ID, len(recs))
	if len(recs) == 0 {
		fmt.Println("(no backups)")
		return
	}
	for _, r := range recs {
		fmt.Println(host.FormatBackupLine(r))
	}
}

func runRecover(args []string) {
	fs := flag.NewFlagSet("recover", flag.ExitOnError)
	cfgPath := configFlag(fs)
	_ = parseArgs(fs, args, "config")
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: backupurvm-host recover <client>")
		os.Exit(2)
	}
	_, storage, err := openStorage(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	client, err := storage.FindClient(fs.Arg(0))
	if err != nil {
		log.Fatalf("%v", err)
	}
	name := client.Name
	if name == "" {
		name = client.Hostname
	}
	fmt.Fprintf(os.Stderr, "recovering %s (%s)…\n", name, client.ID)

	res, err := storage.RecoverClient(client.ID)
	if err != nil {
		log.Fatalf("recover: %v", err)
	}
	if len(res.Reindexed) > 0 {
		fmt.Fprintf(os.Stderr, "re-indexed %d orphan archive(s):\n", len(res.Reindexed))
		for _, r := range res.Reindexed {
			fmt.Fprintf(os.Stderr, "  + %s\n", host.FormatBackupLine(r))
		}
	} else {
		fmt.Fprintln(os.Stderr, "no orphan archives to re-index")
	}
	for _, s := range res.Skipped {
		fmt.Fprintf(os.Stderr, "skip: %s\n", s)
	}
	fmt.Fprintf(os.Stderr, "inventory: %d full, %d incremental\n", res.FullCount, res.IncrCount)
	if res.ChainOK {
		fmt.Fprintf(os.Stderr, "mergeable tip: %s (%s)\n", res.MergeTip, res.MergeAge.Local().Format("2006-01-02 15:04"))
	} else if res.ChainError != "" {
		fmt.Fprintf(os.Stderr, "chain: %s\n", res.ChainError)
	}
	for _, a := range res.Advice {
		fmt.Fprintf(os.Stderr, "→ %s\n", a)
	}
	if res.ChainOK {
		os.Exit(0)
	}
	os.Exit(1)
}

func runDownload(args []string) {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	cfgPath := configFlag(fs)
	backupID := fs.String("backup", "", "backup id or prefix (default: newest archive)")
	latest := fs.Bool("latest", false, "merge last full + incrementals")
	salvage := fs.Bool("salvage", false, "partial merge from incrementals only (no full required)")
	tempDir := fs.String("temp", "", "temp dir for merge (default: <data_dir>/tmp)")
	timeoutStr := fs.String("timeout", "", "stop after duration (e.g. 30m, 2h)")
	_ = parseArgs(fs, args, "config", "backup", "timeout", "temp")
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: backupurvm-host download <client> [--backup ID|--latest|--salvage]")
		os.Exit(2)
	}
	nModes := 0
	if *latest {
		nModes++
	}
	if *salvage {
		nModes++
	}
	if *backupID != "" {
		nModes++
	}
	if nModes > 1 {
		log.Fatal("use only one of --backup, --latest, or --salvage")
	}
	var timeout time.Duration
	if *timeoutStr != "" {
		var err error
		timeout, err = time.ParseDuration(*timeoutStr)
		if err != nil {
			log.Fatalf("timeout: %v", err)
		}
	}

	store, storage, err := openStorage(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	client, err := storage.FindClient(fs.Arg(0))
	if err != nil {
		log.Fatalf("%v", err)
	}

	hub := host.NewDownloadHub(log.Default())
	work := strings.TrimSpace(*tempDir)
	if work == "" {
		work = filepath.Join(store.Get().DataDir, "tmp")
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		log.Fatalf("temp dir: %v", err)
	}
	hub.TempDir = work
	fmt.Fprintf(os.Stderr, "merge temp: %s\n", work)
	var url string
	var started bool

	if *latest || *salvage {
		recs, err := storage.ListBackups(client.ID)
		if err != nil {
			log.Fatalf("list backups: %v", err)
		}
		if len(recs) == 0 {
			log.Fatal("no backups for this client")
		}
		if *salvage {
			chain, err := host.ResolveSalvageChain(recs)
			if err != nil {
				log.Fatalf("salvage: %v", err)
			}
			fmt.Fprintf(os.Stderr, "WARNING: PARTIAL restore — no full base on disk.\n")
			fmt.Fprintf(os.Stderr, "merging %d incremental archive(s) from %s → %s\n",
				len(chain), chain[0].ID, chain[len(chain)-1].ID)
			fmt.Fprintln(os.Stderr, "files that never changed after the missing full will NOT be included")
			url, started, err = hub.ToggleSalvage(recs, store.Get().CompressPrefer)
			if err != nil {
				log.Fatalf("download: %v", err)
			}
		} else {
			_, tip, err := host.ResolveBestMergeableChain(recs)
			if err != nil {
				log.Fatalf("download: %v\n\ntry: ./host download %s --salvage", err, fs.Arg(0))
			}
			if tip.ID != recs[0].ID {
				fmt.Fprintf(os.Stderr, "warning: newest %s is broken; merging through %s (%s)\n",
					recs[0].ID, tip.ID, tip.CreatedAt.Local().Format("2006-01-02 15:04"))
			}
			fmt.Fprintf(os.Stderr, "building merged full archive through %s…\n", tip.ID)
			url, started, err = hub.ToggleLatest(recs, store.Get().CompressPrefer)
			if err != nil {
				log.Fatalf("download: %v", err)
			}
		}
	} else {
		rec, err := storage.FindBackup(client.ID, *backupID)
		if err != nil {
			log.Fatalf("%v", err)
		}
		fmt.Fprintf(os.Stderr, "serving %s (%s, %s)…\n",
			rec.ID, rec.Mode, common.FormatBytes(rec.Bytes))
		url, started, err = hub.Start(rec)
		if err != nil {
			log.Fatalf("download: %v", err)
		}
	}
	if !started || url == "" {
		log.Fatal("download server did not start")
	}

	// URL on stdout for scripts; status on stderr.
	fmt.Println(url)
	fmt.Fprintln(os.Stderr, "download server running — Ctrl+C to stop")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	host.WaitOrTimeout(ctx.Done(), timeout)
	hub.Stop()
}

func runDaemon(args []string) {
	// Host often runs under systemd/screen with no TTY; force colors so the
	// SSH panel lipgloss styles (created at package init) aren't stuck on Ascii.
	lipgloss.SetColorProfile(termenv.ANSI256)

	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := configFlag(fs)
	_ = fs.Parse(args)

	if err := host.EnsureConfig(*configPath); err != nil {
		log.Fatalf("config: %v", err)
	}
	store, err := host.NewConfigStore(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	cfg := store.Get()
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	if cfg.SSHHostKeyPath == "" {
		_ = store.Update(func(c *host.Config) error {
			c.SSHHostKeyPath = filepath.Join(c.DataDir, "ssh_host_ed25519")
			return nil
		})
		cfg = store.Get()
	}

	storage, err := host.NewStorage(cfg.DataDir)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	tasks := host.NewTaskHub()
	peers := host.NewPeerHub()
	backupSrv := host.NewBackupServer(store, storage, tasks, peers, log.Default())
	panel := host.NewSSHPanel(store, storage, tasks, peers, log.Default())

	go host.RunAutoBackupScheduler(ctx, store, peers, log.Default())

	errCh := make(chan error, 2)
	go func() {
		errCh <- backupSrv.ListenAndServe(ctx)
	}()
	go func() {
		errCh <- panel.ListenAndServe()
	}()

	fmt.Printf("backupurvm host started\n  config: %s\n  backup: %s\n  ssh:    %s\n  key_id: %s\n",
		*configPath, cfg.ListenBackup, cfg.ListenSSH, common.KeyID([]byte(cfg.SharedKey)))

	select {
	case <-ctx.Done():
		log.Printf("shutting down")
	case err := <-errCh:
		if err != nil && ctx.Err() == nil {
			log.Fatalf("server: %v", err)
		}
	}
}
