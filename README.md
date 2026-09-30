# backupurvm

Linux-only VPS backup system using [tcpduplex](https://github.com/hdmain/tcpduplex) for encrypted control messages and resumable file transfer.

- **host** — receives backups, commands agents, SSH TUI admin panel
- **client** — long-running agent (or `--once` backup) packing `/root` as `.tar.zst` / `.tar.gz`

## Install

### Debian / Ubuntu (apt)

```bash
echo "deb [trusted=yes] https://hdmain.github.io/backupurvm ./" \
  | sudo tee /etc/apt/sources.list.d/backupurvm.list
sudo apt update
sudo apt install backupurvm-client
```

Then run the interactive setup:

```bash
sudo backupurvm-client init
```

It asks for the host address and shared key, writes the config, and starts the systemd service.

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
# same as: ./bin/host serve -config config.yml
```

| Port (default) | Purpose |
|----------------|---------|
| `:9090` | tcpduplex backup listener |
| `:2222` | SSH admin TUI |

### Host CLI (no SSH)

List servers and expose an HTTP download link from the machine running the host (same `config.yml` / `data/`):

```bash
./bin/host clients
./bin/host backups myvps
./bin/host download myvps                    # newest single archive → prints URL
./bin/host download myvps --backup 20260829  # specific archive (id prefix OK)
./bin/host download myvps --latest           # merge last full + incrementals
./bin/host download myvps --latest --timeout 1h
```

The download command prints the public URL on stdout and keeps serving until Ctrl+C (or `--timeout`). Same tokenized HTTP link as the SSH panel `D` / `L` keys.

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
| `D` | Download selected archive (HTTP link; press again to stop) |
| `L` | Download latest full (merges last full + incrementals) |
| `?` | Help |
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
| Schedule time | Local `HH:MM` — run in a ~45m window after that time (empty = anytime / interval only) |
| Interval | Go duration, e.g. `1h`, `6h`, `24h`, `3d` (minimum `1m`) |
| Mode | `auto`, `full`, or `incremental` |

Or in `config.yml`:

```yaml
auto_backup: true
auto_backup_every: "24h"
auto_backup_at: ""          # or "03:00" for a nightly window
auto_backup_mode: "auto"
archive_offline_after: "3d"
```

Servers offline longer than **Archive offline after** (Settings → Servers, default `3d`) move to an **Archived** section in OVERVIEW / CLIENTS. They return to the main list when they reconnect.

Keep `config.yml` and `data/` out of git — both are ignored. Use `config.yml.example` as the template.

## Client

| Path | Purpose |
|------|---------|
| `/usr/bin/backupurvm-client` | Agent binary |
| `/lib/systemd/system/backupurvm-client.service` | systemd unit |
| `/etc/backupurvm/client.env` | Host address, source path, display name, optional temp dir |
| `/etc/backupurvm/backup.key` | Shared key (must match host `shared_key`) |

One-liner from a GitHub Release `.deb` (no apt repo):

```bash
curl -sSL https://raw.githubusercontent.com/hdmain/backupurvm/main/install_client.sh | sudo bash
```

Build a local `.deb`:

```bash
./scripts/build-deb-client.sh   # → dist/backupurvm-client_<ver>_<arch>.deb
sudo apt install ./dist/backupurvm-client_*.deb
```

### Manual / from source

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
| `--temp` | Temp dir for packing (default `/var/tmp/backupurvm`; env `BACKUPURVM_TEMP`) |

## Backup behavior

1. Client dials with tcpduplex PSK = key file contents.
2. Host returns a **plan**: `full` or `incremental` (+ last file manifest).
3. Client scans the source tree, packs changed files (or everything on full) into tar+zstd/gzip.
4. Archive is sent with `tcpduplex/transfer` (encrypted, resumable).
5. Host stores under `data/clients/<key_id>/backups/` and updates the merged manifest.

Incremental packs only files whose size/mtime/mode changed since the last backup, and records deletions in archive metadata.
