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
	var lastLog time.Time

	// 1m is enough for schedule windows and keeps idle host quiet on small VPS.
	ticker := time.NewTicker(time.Minute)
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
			if _, _, _, err := ParseClockHHMM(cfg.AutoBackupAt); err != nil {
				logger.Printf("auto backup: invalid schedule time %q (use HH:MM)", cfg.AutoBackupAt)
				continue
			}
			now := time.Now()
			if !scheduleDue(cfg.AutoBackupAt, every, now, lastRun) {
				continue
			}
			cmd := autoBackupCommand(cfg.AutoBackupMode)
			sent, skipped := peers.BroadcastCommand(cmd)
			if sent > 0 {
				logger.Printf("auto backup: sent %s to %d agent(s) (skipped %d)", cmd, sent, skipped)
				lastRun = now
				continue
			}
			// Nobody online: in timed window keep retrying without burning the day;
			// in interval-only mode advance so we don't stampede when they reconnect.
			if strings.TrimSpace(cfg.AutoBackupAt) == "" {
				lastRun = now
				if now.Sub(lastLog) > 30*time.Minute {
					logger.Printf("auto backup: no online agents (next in %s)", every)
					lastLog = now
				}
				continue
			}
			if now.Sub(lastLog) > 10*time.Minute {
				logger.Printf("auto backup: window open, no online agents yet (will retry)")
				lastLog = now
			}
		}
	}
}

// scheduleDue reports whether an auto-backup should fire now.
//
//   - auto_backup_at empty: true when `every` has elapsed since lastRun
//   - auto_backup_at set: true in a 45-minute window starting at HH:MM local,
//     if lastRun was before today's scheduled instant (and `every` elapsed)
func scheduleDue(at string, every time.Duration, now, lastRun time.Time) bool {
	if every < time.Minute {
		return false
	}
	if !lastRun.IsZero() && now.Sub(lastRun) < every {
		return false
	}
	hour, min, set, err := ParseClockHHMM(at)
	if err != nil {
		return false
	}
	if !set {
		return true
	}
	local := now.Local()
	scheduled := time.Date(local.Year(), local.Month(), local.Day(), hour, min, 0, 0, local.Location())
	windowEnd := scheduled.Add(45 * time.Minute)
	if local.Before(scheduled) || !local.Before(windowEnd) {
		return false
	}
	// Already succeeded (or interval-advanced) after today's slot opened.
	if !lastRun.IsZero() && !lastRun.Before(scheduled) {
		return false
	}
	return true
}

// scheduleTimeAllows is kept for tests/compat: exact-minute or empty.
func scheduleTimeAllows(at string, now time.Time) bool {
	return scheduleDue(at, time.Minute, now, time.Time{})
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
