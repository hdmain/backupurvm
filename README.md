# backupurvm

Linux-only VPS backup system using [tcpduplex](https://github.com/hdmain/tcpduplex) for encrypted control messages and resumable file transfer.

- **host** — receives backups + SSH TUI admin panel
- **client** — packs `/root` (or `--source`) as `.tar.zst` / `.tar.gz` and uploads full or incremental archives

## Build

```bash
go build -o bin/host ./cmd/host
go build -o bin/client ./cmd/client
```

## Host

```bash
cp config.yml.example config.yml
# edit shared_key and ssh_password
./bin/host -config config.yml
```

| Port (default) | Purpose |
|----------------|---------|
| `:9090` | tcpduplex backup listener |
| `:2222` | SSH admin TUI |

SSH login (password or public key):

```bash
# password auth (ssh_password in config.yml)
ssh -p 2222 -o PreferredAuthentications=password -o PubkeyAuthentication=no admin@HOST

# or public key from ssh_authorized_keys
ssh -p 2222 -i ~/.ssh/id_ed25519 admin@HOST
```

Set `ssh_password: ""` to disable password login (keys only). Panel menu **4** changes the SSH password live.

## Client

Create a key file with the **same** secret as host `shared_key`:

```bash
echo -n 'change-me-shared-key' > /root/backup.key
chmod 600 /root/backup.key
```

```bash
./bin/client --connect HOST:9090 --key /root/backup.key
./bin/client --connect HOST:9090 --key /root/backup.key --full
./bin/client --connect HOST:9090 --key /root/backup.key --incremental
./bin/client --connect HOST:9090 --key /root/backup.key --source /root --compress zstd
```

### Flags

| Flag | Description |
|------|-------------|
| `--connect` | Host `ip:port` (required) |
| `--key` | Path to shared private key file (required) |
| `--full` | Force full backup |
| `--incremental` | Force incremental (falls back to full if none exists) |
| `--source` | Directory to backup (default `/root`) |
| `--compress` | `zstd` or `gzip` (default: host preference) |
| `--name` | Client display name |
| `--temp` | Temp dir for packing |

## Backup behavior

1. Client dials with tcpduplex PSK = key file contents.
2. Host returns a **plan**: `full` or `incremental` (+ last file manifest).
3. Client scans the source tree, packs changed files (or everything on full) into tar+zstd/gzip.
4. Archive is sent with `tcpduplex/transfer` (encrypted, resumable).
5. Host stores under `data/clients/<key_id>/backups/` and updates the merged manifest.

Incremental packs only files whose size/mtime/mode changed since the last backup, and records deletions in archive metadata.
