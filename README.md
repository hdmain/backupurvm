# backupurvm

Linux-only VPS backup system using [tcpduplex](https://github.com/hdmain/tcpduplex) for encrypted control messages and resumable file transfer.

- **host** — receives backups, commands agents, SSH TUI admin panel
- **client** — long-running agent (or `--once` backup) packing `/root` as `.tar.zst` / `.tar.gz`

## Requirements

- Linux (amd64/arm64)
- Go 1.24+

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

SSH login opens the host panel:

```bash
ssh -p 2222 -o PreferredAuthentications=password -o PubkeyAuthentication=no admin@HOST
```

| View | Purpose |
|------|---------|
| OVERVIEW | Last completed task, running transfers, and servers |
| CLIENTS | Each VPS client (Tab) |
| SETTINGS | All configuration — press **S** |

| Key | Action |
|-----|--------|
| ↑/↓ | Select |
| Enter | Open client / edit setting |
| `b` / `B` / `i` | Backup auto / full / incremental |
| `p` | Ping agent |
| Tab | OVERVIEW ↔ CLIENTS |
| S | Open Settings |
| Esc | Leave Settings |
| F | Find client |
| U | Refresh |
| Q | Quit |

Set `ssh_password: ""` to disable password login (keys only).

### Auto backup (Settings → Auto backup)

| Setting | Meaning |
|---------|---------|
| Enabled | `on` / `off` — schedule backups for all online agents |
| Interval | Go duration, e.g. `1h`, `6h`, `24h` (minimum `1m`) |
| Mode | `auto`, `full`, or `incremental` |

Or in `config.yml`:

```yaml
auto_backup: true
auto_backup_every: "6h"
auto_backup_mode: "auto"
```

Keep `config.yml` and `data/` out of git — both are ignored. Use `config.yml.example` as the template.

## Client

Create a key file with the **same** secret as host `shared_key`:

```bash
echo -n 'change-me-shared-key' > /root/backup.key
chmod 600 /root/backup.key
```

### Agent mode (default) — stays connected

```bash
./bin/client --connect HOST:9090 --key /root/backup.key
# optional: --name myvps --source /root
```

The agent reconnects automatically and waits for host commands (`backup_auto`, `backup_full`, `backup_incremental`, `ping`).

From the host SSH panel, select a server and press:

| Key | Command |
|-----|---------|
| `b` | Backup (auto full/incremental) |
| `B` | Full backup |
| `i` | Incremental backup |
| `p` | Ping |

### One-shot backup

```bash
./bin/client --connect HOST:9090 --key /root/backup.key --once
./bin/client --connect HOST:9090 --key /root/backup.key --once --full
```

### Flags

| Flag | Description |
|------|-------------|
| `--connect` | Host `ip:port` (required) |
| `--key` | Path to shared key file (required) |
| `--once` | Single backup then exit |
| `--full` / `--incremental` | With `--once` only |
| `--source` | Directory to backup (default `/root`) |
| `--compress` | `zstd` or `gzip` |
| `--name` | Client display name |
| `--temp` | Temp dir for packing |

## Backup behavior

1. Client dials with tcpduplex PSK = key file contents.
2. Host returns a **plan**: `full` or `incremental` (+ last file manifest).
3. Client scans the source tree, packs changed files (or everything on full) into tar+zstd/gzip.
4. Archive is sent with `tcpduplex/transfer` (encrypted, resumable).
5. Host stores under `data/clients/<key_id>/backups/` and updates the merged manifest.

Incremental packs only files whose size/mtime/mode changed since the last backup, and records deletions in archive metadata.
