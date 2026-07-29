//go:build linux

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/hdmain/backupurvm/internal/client"
	"github.com/hdmain/backupurvm/internal/protocol"
)

const (
	envFile = "/etc/backupurvm/client.env"
	keyFile = "/etc/backupurvm/backup.key"
)

func runInit() {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "run as root: sudo backupurvm-client init")
		os.Exit(1)
	}

	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Host address (ip:port): ")
	host, _ := reader.ReadString('\n')
	host = strings.TrimSpace(host)
	if host == "" {
		fmt.Fprintln(os.Stderr, "host address is required")
		os.Exit(1)
	}

	fmt.Print("Shared key: ")
	key, _ := reader.ReadString('\n')
	key = strings.TrimSpace(key)
	if key == "" {
		fmt.Fprintln(os.Stderr, "shared key is required")
		os.Exit(1)
	}

	source := "/root"
	fmt.Printf("Source directory [%s]: ", source)
	src, _ := reader.ReadString('\n')
	src = strings.TrimSpace(src)
	if src != "" {
		source = src
	}

	clientName := ""
	fmt.Print("Client name (empty = hostname): ")
	cn, _ := reader.ReadString('\n')
	clientName = strings.TrimSpace(cn)

	os.MkdirAll("/etc/backupurvm", 0700)

	envContent := fmt.Sprintf(
		"BACKUPURVM_HOST=%s\nBACKUPURVM_KEY=%s\nBACKUPURVM_SOURCE=%s\nBACKUPURVM_NAME=%s\n",
		host, keyFile, source, clientName,
	)
	if err := os.WriteFile(envFile, []byte(envContent), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write %s: %v\n", envFile, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s\n", envFile)

	if err := os.WriteFile(keyFile, []byte(key), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write %s: %v\n", keyFile, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s\n", keyFile)

	run := func(args ...string) error {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	run("systemctl", "daemon-reload")
	run("systemctl", "enable", "backupurvm-client.service")
	if err := run("systemctl", "start", "backupurvm-client.service"); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start service: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("backupurvm-client is running.")
	fmt.Println("  sudo journalctl -u backupurvm-client -f")
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "init" {
		runInit()
		return
	}

	connect := flag.String("connect", "", "host address ip:port")
	keyPath := flag.String("key", "", "path to shared private key file")
	once := flag.Bool("once", false, "run a single backup and exit (default: stay connected as agent)")
	full := flag.Bool("full", false, "with --once: force full backup")
	incremental := flag.Bool("incremental", false, "with --once: force incremental backup")
	source := flag.String("source", "/root", "directory to backup")
	compress := flag.String("compress", "", "zstd or gzip (default: host preference)")
	name := flag.String("name", "", "client name (default: hostname)")
	tempDir := flag.String("temp", "", "temp directory for packing")
	flag.Parse()

	if *connect == "" || *keyPath == "" {
		fmt.Fprintf(os.Stderr, "usage: %s --connect host:port --key /path/to/key [--once]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "       %s init    — interactive setup + start systemd service\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(2)
	}

	mode := protocol.ModeAuto
	if *full && *incremental {
		log.Fatal("use only one of --full or --incremental")
	}
	if *full {
		mode = protocol.ModeFull
	}
	if *incremental {
		mode = protocol.ModeIncremental
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	opts := client.Options{
		Addr:       *connect,
		KeyPath:    *keyPath,
		SourceRoot: *source,
		WantMode:   mode,
		Compress:   *compress,
		ClientName: *name,
		TempDir:    *tempDir,
		Once:       *once,
		Logger:     log.Default(),
	}

	if *once {
		log.Printf("oneshot backup mode")
		if err := client.RunOnce(ctx, opts); err != nil {
			log.Fatalf("backup failed: %v", err)
		}
		return
	}

	log.Printf("agent mode — connected to %s, waiting for host commands (Ctrl+C to stop)", *connect)
	if err := client.RunAgent(ctx, opts); err != nil && ctx.Err() == nil {
		log.Fatalf("agent stopped: %v", err)
	}
	log.Printf("agent stopped")
}
