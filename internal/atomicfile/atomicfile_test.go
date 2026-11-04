package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesWithMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new" {
		t.Fatalf("content %q err %v", got, err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %04o", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("stray files: %v", entries)
	}
}

func TestWriteFailureLeavesOldFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A missing directory makes the write fail before anything is renamed.
	if err := Write(filepath.Join(dir, "missing", "x"), []byte("new"), 0o600); err == nil {
		t.Fatal("write into a missing directory succeeded")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old" {
		t.Fatalf("old file changed: %q", got)
	}
}

func TestLockDirIsExclusive(t *testing.T) {
	dir := t.TempDir()
	first, err := LockDir(dir, "server.lock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockDir(dir, "server.lock"); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := LockDir(dir, "server.lock")
	if err != nil {
		t.Fatal(err)
	}
	_ = again.Release()
}
