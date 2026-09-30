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

func TestScheduleDueIntervalOnly(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	if !scheduleDue("", 24*time.Hour, now, time.Time{}) {
		t.Fatal("first run should be due")
	}
	last := now.Add(-2 * time.Hour)
	if scheduleDue("", 24*time.Hour, now, last) {
		t.Fatal("should wait for interval")
	}
	if !scheduleDue("", 1*time.Hour, now, last) {
		t.Fatal("interval elapsed")
	}
}

func TestScheduleDueTimedWindow(t *testing.T) {
	loc := time.Local
	day := time.Date(2026, 9, 30, 0, 0, 0, 0, loc)
	at0300 := day.Add(3 * time.Hour)
	inWindow := at0300.Add(10 * time.Minute)
	afterWindow := at0300.Add(50 * time.Minute)
	before := at0300.Add(-time.Minute)

	if scheduleDue("03:00", 24*time.Hour, before, time.Time{}) {
		t.Fatal("before window")
	}
	if !scheduleDue("03:00", 24*time.Hour, inWindow, time.Time{}) {
		t.Fatal("in window first run")
	}
	if scheduleDue("03:00", 24*time.Hour, afterWindow, time.Time{}) {
		t.Fatal("after window")
	}
	// Already ran after today's 03:00
	if scheduleDue("03:00", 24*time.Hour, inWindow, at0300.Add(time.Minute)) {
		t.Fatal("already ran today")
	}
	// Ran yesterday — due again
	yesterday := at0300.Add(-24 * time.Hour)
	if !scheduleDue("03:00", 24*time.Hour, inWindow, yesterday) {
		t.Fatal("should run again next day")
	}
}

func TestScheduleTimeAllows(t *testing.T) {
	now := time.Date(2026, 7, 29, 3, 0, 30, 0, time.Local)
	if !scheduleTimeAllows("03:00", now) {
		t.Fatal("should allow 03:00 window")
	}
	if scheduleTimeAllows("04:00", now) {
		t.Fatal("should not allow 04:00")
	}
	if !scheduleTimeAllows("", now) {
		t.Fatal("empty should allow any time")
	}
}
