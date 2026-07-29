package host

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseFlexibleDuration accepts Go durations plus day units (e.g. 3d, 1.5d).
func ParseFlexibleDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "0" || s == "off" || s == "never" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		num := strings.TrimSuffix(s, "d")
		days, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return d, nil
}

// ParseClockHHMM parses "HH:MM" (24h). Empty string is allowed (returns ok=false).
func ParseClockHHMM(s string) (hour, min int, ok bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false, nil
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, false, fmt.Errorf("use HH:MM (e.g. 03:00)")
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false, fmt.Errorf("use HH:MM (e.g. 03:00)")
	}
	return h, m, true, nil
}

// FormatClockHHMM formats hour/min as HH:MM.
func FormatClockHHMM(hour, min int) string {
	return fmt.Sprintf("%02d:%02d", hour, min)
}
