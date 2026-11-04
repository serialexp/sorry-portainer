// Package atomicfile replaces files durably and guards state directories.
package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrDirectorySync reports that a file was renamed into place but the
// directory entry may not be durable yet. The new content is what the file
// holds; callers decide whether to keep going.
var ErrDirectorySync = errors.New("renamed, but syncing the directory failed")

// Write replaces path with data. The data goes to a temporary file in the same
// directory, which is synced and renamed over path; then the directory is
// synced. A reader sees either the old file or the new one, never a mixture,
// and a crash leaves at most a stray temporary file. If only the directory
// sync fails, the returned error wraps ErrDirectorySync.
func Write(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	committed = true
	if err := SyncDir(dir); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrDirectorySync, path, err)
	}
	return nil
}

// SyncDir makes the directory's entries durable.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Lock is an exclusive advisory lock on a state directory, held until Release
// or process exit.
type Lock struct{ file *os.File }

// ErrLocked reports that another process holds the lock.
var ErrLocked = errors.New("state directory is in use by another process")

// LockDir takes the exclusive lock file name inside dir without waiting.
func LockDir(dir, name string) (*Lock, error) {
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, dir)
		}
		return nil, err
	}
	return &Lock{file: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() error { return l.file.Close() }
