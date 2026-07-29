package host

import (
	"testing"
	"time"
)

func TestParseFlexibleDuration(t *testing.T) {
	d, err := ParseFlexibleDuration("3d")
	if err != nil || d != 72*time.Hour {
		t.Fatalf("3d: got %v %v", d, err)
	}
	d, err = ParseFlexibleDuration("24h")
	if err != nil || d != 24*time.Hour {
		t.Fatalf("24h: got %v %v", d, err)
	}
	d, err = ParseFlexibleDuration("")
	if err != nil || d != 0 {
		t.Fatalf("empty: got %v %v", d, err)
	}
}

func TestParseClockHHMM(t *testing.T) {
	h, m, ok, err := ParseClockHHMM("03:00")
	if err != nil || !ok || h != 3 || m != 0 {
		t.Fatalf("03:00: %d %d %v %v", h, m, ok, err)
	}
	_, _, ok, err = ParseClockHHMM("")
	if err != nil || ok {
		t.Fatalf("empty should be ok=false: %v %v", ok, err)
	}
	if _, _, _, err := ParseClockHHMM("25:00"); err == nil {
		t.Fatal("expected error")
	}
}

func TestScheduleTimeAllows(t *testing.T) {
	now := time.Date(2026, 7, 29, 3, 0, 30, 0, time.Local)
	if !scheduleTimeAllows("03:00", now) {
		t.Fatal("should allow 03:00")
	}
	if scheduleTimeAllows("04:00", now) {
		t.Fatal("should not allow 04:00")
	}
	if !scheduleTimeAllows("", now) {
		t.Fatal("empty should allow any time")
	}
}
