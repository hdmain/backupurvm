#!/usr/bin/env bash
# Build backupurvm-client_<version>_amd64.deb (and optionally arm64).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if [ -z "${VERSION:-}" ]; then
	# Prefer annotated release tags (v1.2.3). Otherwise use an always-increasing
	# 0.<commit-count>.0+g<hash> so apt upgrades beat older fixed versions like 0.1.0.
	if DESC="$(git -C "$ROOT" describe --tags --exact-match --match 'v*' 2>/dev/null)"; then
		VERSION="${DESC#v}"
	else
		COUNT="$(git -C "$ROOT" rev-list --count HEAD 2>/dev/null || echo 1)"
		HASH="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
		VERSION="0.${COUNT}.0+g${HASH}"
	fi
fi
VERSION="${VERSION#v}"
# Debian versions must start with a digit.
case "$VERSION" in
[0-9]*) ;;
*) VERSION="0.0.0+g${VERSION}" ;;
esac
# Sanitize remaining chars for Debian version rules.
VERSION="$(printf '%s' "$VERSION" | tr -c 'A-Za-z0-9.+~' '.')"
ARCH="${ARCH:-amd64}"
OUT_DIR="${OUT_DIR:-$ROOT/dist}"

case "$ARCH" in
amd64) GOARCH=amd64 ;;
arm64) GOARCH=arm64 ;;
*)
	echo "unsupported ARCH=$ARCH (use amd64 or arm64)" >&2
	exit 1
	;;
esac

PKG_NAME="backupurvm-client"
PKG_ROOT="$OUT_DIR/${PKG_NAME}_${VERSION}_${ARCH}"
DEB="$OUT_DIR/${PKG_NAME}_${VERSION}_${ARCH}.deb"

rm -rf "$PKG_ROOT"
mkdir -p \
	"$PKG_ROOT/DEBIAN" \
	"$PKG_ROOT/usr/bin" \
	"$PKG_ROOT/lib/systemd/system" \
	"$PKG_ROOT/usr/share/backupurvm-client" \
	"$PKG_ROOT/etc/backupurvm" \
	"$PKG_ROOT/usr/share/doc/$PKG_NAME"

echo "==> building binary (linux/$GOARCH)"
(
	cd "$ROOT"
	CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -trimpath -ldflags="-s -w" \
		-o "$PKG_ROOT/usr/bin/backupurvm-client" ./cmd/client
)

install -m 0644 "$ROOT/packaging/client/backupurvm-client.service" \
	"$PKG_ROOT/lib/systemd/system/backupurvm-client.service"
install -m 0644 "$ROOT/packaging/client/client.env.example" \
	"$PKG_ROOT/usr/share/backupurvm-client/client.env.example"
install -m 0644 "$ROOT/packaging/client/client.env.example" \
	"$PKG_ROOT/etc/backupurvm/client.env.example"

cat > "$PKG_ROOT/usr/share/doc/$PKG_NAME/README.Debian" <<EOF
backupurvm-client
=================

1. Edit /etc/backupurvm/client.env (BACKUPURVM_HOST, …)
2. Put the host shared_key into /etc/backupurvm/backup.key
3. sudo systemctl enable --now backupurvm-client
4. journalctl -u backupurvm-client -f
EOF

SIZE_KB="$(du -sk "$PKG_ROOT" | awk '{print $1}')"

cat > "$PKG_ROOT/DEBIAN/control" <<EOF
Package: $PKG_NAME
Version: $VERSION
Section: admin
Priority: optional
Architecture: $ARCH
Maintainer: hdmain <noreply@github.com>
Depends: systemd
Installed-Size: $SIZE_KB
Homepage: https://github.com/hdmain/backupurvm
Description: backupurvm VPS backup agent
 Long-running Linux agent that connects to a backupurvm host over
 tcpduplex, waits for backup commands, and uploads /root (or configured
 source) archives. Ships a systemd unit: backupurvm-client.service.
EOF

install -m 0755 "$ROOT/packaging/client/postinst" "$PKG_ROOT/DEBIAN/postinst"
install -m 0755 "$ROOT/packaging/client/prerm" "$PKG_ROOT/DEBIAN/prerm"
install -m 0755 "$ROOT/packaging/client/postrm" "$PKG_ROOT/DEBIAN/postrm"

# Config files — preserve local edits on upgrade.
cat > "$PKG_ROOT/DEBIAN/conffiles" <<EOF
/etc/backupurvm/client.env.example
EOF

echo "==> building $DEB"
mkdir -p "$OUT_DIR"
dpkg-deb --root-owner-group --build "$PKG_ROOT" "$DEB"
echo "built $DEB"
dpkg-deb -I "$DEB"
