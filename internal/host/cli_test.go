package host

import (
	"strings"
	"testing"
	"time"
)

func TestFindClient(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TouchClient("abc123def456", "vps-alpha", "alpha.example"); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchClient("zzz999yyy888", "vps-beta", "beta.example"); err != nil {
		t.Fatal(err)
	}

	c, err := s.FindClient("vps-alpha")
	if err != nil || c.ID != "abc123def456" {
		t.Fatalf("by name: %v %+v", err, c)
	}
	c, err = s.FindClient("beta.example")
	if err != nil || c.Name != "vps-beta" {
		t.Fatalf("by hostname: %v %+v", err, c)
	}
	c, err = s.FindClient("abc123")
	if err != nil || c.Name != "vps-alpha" {
		t.Fatalf("by id prefix: %v %+v", err, c)
	}
	_, err = s.FindClient("vps")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous, got %v", err)
	}
	_, err = s.FindClient("missing")
	if err == nil {
		t.Fatal("expected not found")
	}
}

func TestFindBackup(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	cid := "client1"
	if err := s.TouchClient(cid, "n", "h"); err != nil {
		t.Fatal(err)
	}
	older := BackupRecord{
		ID: "20260101-120000-aaaaaa", ClientID: cid, Mode: "full",
		CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	newer := BackupRecord{
		ID: "20260102-120000-bbbbbb", ClientID: cid, Mode: "incremental",
		CreatedAt: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC),
	}
	if err := s.CommitBackup(older, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitBackup(newer, nil); err != nil {
		t.Fatal(err)
	}

	rec, err := s.FindBackup(cid, "")
	if err != nil || rec.ID != newer.ID {
		t.Fatalf("newest: %v %+v", err, rec)
	}
	rec, err = s.FindBackup(cid, "20260101")
	if err != nil || rec.ID != older.ID {
		t.Fatalf("prefix: %v %+v", err, rec)
	}
}
