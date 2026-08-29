package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTempDirExplicit(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveTempDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("got %q want %q", got, dir)
	}
}

func TestResolveTempDirFromEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BACKUPURVM_TEMP", dir)
	got, err := resolveTempDir("")
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("got %q want %q", got, dir)
	}
}

func TestEnsureTempDirCreates(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "pack")
	got, err := ensureTempDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("got %q want %q", got, dir)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsDir() {
		t.Fatal("expected directory")
	}
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("mode %o want 0700", st.Mode().Perm())
	}
}
