#!/usr/bin/env bash
# Quick install of backupurvm-client (.deb) + systemd unit.
# Usage:
#   curl -sSL https://raw.githubusercontent.com/hdmain/backupurvm/main/install_client.sh | sudo bash
# Or from apt repo (preferred once packages are published):
#   see README.md
set -euo pipefail

REPO="${BACKUPURVM_REPO:-hdmain/backupurvm}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ "$(id -u)" -ne 0 ]; then
	echo "run as root (sudo)" >&2
	exit 1
fi

if ! command -v systemctl >/dev/null 2>&1; then
	echo "systemd is required" >&2
	exit 1
fi

ARCH="$(dpkg --print-architecture 2>/dev/null || uname -m)"
case "$ARCH" in
amd64 | x86_64) ARCH=amd64 ;;
arm64 | aarch64) ARCH=arm64 ;;
*)
	echo "unsupported architecture: $ARCH" >&2
	exit 1
	;;
esac

echo "==> fetching latest backupurvm-client .deb ($ARCH)"
API="https://api.github.com/repos/${REPO}/releases/latest"
URL="$(curl -fsSL "$API" | python3 -c "
import json,sys
rel=json.load(sys.stdin)
arch='${ARCH}'
for a in rel.get('assets',[]):
    n=a['name']
    if n.startswith('backupurvm-client_') and n.endswith('_'+arch+'.deb'):
        print(a['browser_download_url']); break
else:
    sys.exit('no matching .deb asset in latest release')
")"

curl -fsSL -o "$TMP/client.deb" "$URL"
apt-get install -y "$TMP/client.deb" 2>/dev/null || dpkg -i "$TMP/client.deb"
apt-get install -f -y >/dev/null 2>&1 || true

echo
echo "Installed. Configure then start:"
echo "  sudo nano /etc/backupurvm/client.env"
echo "  sudo nano /etc/backupurvm/backup.key   # same as host shared_key"
echo "  sudo systemctl enable --now backupurvm-client"
echo "  sudo journalctl -u backupurvm-client -f"
