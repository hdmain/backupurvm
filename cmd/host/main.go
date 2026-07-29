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
	"syscall"

	"github.com/hdmain/backupurvm/internal/common"
	"github.com/hdmain/backupurvm/internal/host"
)

func main() {
	configPath := flag.String("config", "config.yml", "path to host config")
	flag.Parse()

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

	backupSrv := host.NewBackupServer(store, storage, log.Default())
	panel := host.NewSSHPanel(store, storage, log.Default())

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
