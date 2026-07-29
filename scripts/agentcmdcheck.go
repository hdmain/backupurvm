//go:build ignore

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/hdmain/backupurvm/internal/client"
	"github.com/hdmain/backupurvm/internal/host"
	"github.com/hdmain/backupurvm/internal/protocol"
)

func main() {
	dir := "/tmp/bu-cmdtest"
	_ = os.RemoveAll(dir)
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "src", "f.txt"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "key"), []byte("secret"), 0o600)
	cfgPath := filepath.Join(dir, "config.yml")
	_ = os.WriteFile(cfgPath, []byte(fmt.Sprintf(`
listen_backup: "127.0.0.1:19290"
listen_ssh: "127.0.0.1:12422"
data_dir: %q
shared_key: "secret"
ssh_password: "x"
ssh_host_key_path: %q
compress_prefer: "zstd"
max_backups_per_client: 5
`, filepath.Join(dir, "data"), filepath.Join(dir, "data", "ssh_host"))), 0o600)

	_ = host.EnsureConfig(cfgPath)
	store, err := host.NewConfigStore(cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	storage, err := host.NewStorage(store.Get().DataDir)
	if err != nil {
		log.Fatal(err)
	}
	tasks := host.NewTaskHub()
	peers := host.NewPeerHub()
	srv := host.NewBackupServer(store, storage, tasks, peers, log.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.ListenAndServe(ctx) }()
	time.Sleep(300 * time.Millisecond)

	agentCtx, agentCancel := context.WithCancel(context.Background())
	defer agentCancel()
	go func() {
		_ = client.RunAgent(agentCtx, client.Options{
			Addr: "127.0.0.1:19290", KeyPath: filepath.Join(dir, "key"),
			SourceRoot: filepath.Join(dir, "src"), ClientName: "cmdvps", Logger: log.Default(),
		})
	}()

	deadline := time.Now().Add(5 * time.Second)
	var id string
	for time.Now().Before(deadline) {
		online := peers.OnlineIDs()
		if len(online) > 0 {
			for k := range online {
				id = k
				break
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if id == "" {
		log.Fatal("agent did not connect")
	}
	fmt.Println("online", id)

	if err := peers.SendCommand(id, protocol.CmdBackupFull); err != nil {
		log.Fatal(err)
	}
	fmt.Println("command sent")

	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		recs, _ := storage.ListBackups(id)
		if len(recs) > 0 {
			fmt.Println("SUCCESS backup", recs[0].ID, recs[0].Bytes)
			cancel()
			agentCancel()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	log.Fatal("timeout waiting for backup")
}
