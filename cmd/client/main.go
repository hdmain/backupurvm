//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hdmain/backupurvm/internal/client"
	"github.com/hdmain/backupurvm/internal/protocol"
)

func main() {
	connect := flag.String("connect", "", "host address ip:port")
	keyPath := flag.String("key", "", "path to shared private key file")
	full := flag.Bool("full", false, "force full backup")
	incremental := flag.Bool("incremental", false, "force incremental backup")
	source := flag.String("source", "/root", "directory to backup")
	compress := flag.String("compress", "", "zstd or gzip (default: host preference)")
	name := flag.String("name", "", "client name (default: hostname)")
	tempDir := flag.String("temp", "", "temp directory for packing")
	flag.Parse()

	if *connect == "" || *keyPath == "" {
		fmt.Fprintf(os.Stderr, "usage: %s --connect host:port --key /path/to/key [--full|--incremental]\n", os.Args[0])
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

	err := client.Run(ctx, client.Options{
		Addr:       *connect,
		KeyPath:    *keyPath,
		SourceRoot: *source,
		WantMode:   mode,
		Compress:   *compress,
		ClientName: *name,
		TempDir:    *tempDir,
		Logger:     log.Default(),
	})
	if err != nil {
		log.Fatalf("backup failed: %v", err)
	}
}
