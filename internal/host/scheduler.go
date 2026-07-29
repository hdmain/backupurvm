package host

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/hdmain/backupurvm/internal/protocol"
)

// RunAutoBackupScheduler periodically commands all online agents to back up
// when Config.AutoBackup is enabled. Interval/mode/time are read live from ConfigStore.
func RunAutoBackupScheduler(ctx context.Context, store *ConfigStore, peers *PeerHub, logger *log.Logger) {
	if logger == nil {
		logger = log.Default()
	}
	var lastRun time.Time

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg := store.Get()
			if !cfg.AutoBackup {
				continue
			}
			every, err := ParseFlexibleDuration(cfg.AutoBackupEvery)
			if err != nil || every < time.Minute {
				logger.Printf("auto backup: invalid interval %q (min 1m)", cfg.AutoBackupEvery)
				continue
			}
			if !lastRun.IsZero() && time.Since(lastRun) < every {
				continue
			}
			if _, _, _, err := ParseClockHHMM(cfg.AutoBackupAt); err != nil {
				logger.Printf("auto backup: invalid schedule time %q (use HH:MM)", cfg.AutoBackupAt)
				continue
			}
			if !scheduleTimeAllows(cfg.AutoBackupAt, time.Now()) {
				continue
			}
			cmd := autoBackupCommand(cfg.AutoBackupMode)
			sent, skipped := peers.BroadcastCommand(cmd)
			if sent > 0 || skipped > 0 {
				logger.Printf("auto backup: sent %s to %d agent(s) (skipped %d)", cmd, sent, skipped)
			}
			// Advance schedule even if nobody was online, so we don't stampede later.
			lastRun = time.Now()
		}
	}
}

// scheduleTimeAllows is true when AutoBackupAt is empty, or local clock is in that minute.
func scheduleTimeAllows(at string, now time.Time) bool {
	hour, min, ok, err := ParseClockHHMM(at)
	if err != nil {
		return false
	}
	if !ok {
		return true // empty = any time
	}
	local := now.Local()
	return local.Hour() == hour && local.Minute() == min
}

func autoBackupCommand(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case protocol.ModeFull:
		return protocol.CmdBackupFull
	case protocol.ModeIncremental:
		return protocol.CmdBackupIncr
	default:
		return protocol.CmdBackupAuto
	}
}
