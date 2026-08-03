package common

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/hdmain/tcpduplex"
)

// MaxDuplexMessageBytes caps control-plane payloads (plans with large manifests).
// 64 MiB is enough for zstd-compressed inventories of very large trees.
const MaxDuplexMessageBytes = 64 << 20

// KeyID returns a short hex identity for a shared key.
func KeyID(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:16])
}

// FormatBytes renders a human-readable byte count.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// DuplexConfig returns a tcpduplex config with the shared message size cap and PSK.
func DuplexConfig(psk []byte) *tcpduplex.Config {
	cfg := tcpduplex.DefaultConfig()
	cfg.Handshake.PreSharedKey = psk
	cfg.MaxMessageBytes = MaxDuplexMessageBytes
	return cfg
}
