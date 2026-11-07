package podman

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectorySizeCountsRegularFilesOnly(t *testing.T) {
	root := t.TempDir()
	writeSizedFile(t, filepath.Join(root, "a"), 5)
	writeSizedFile(t, filepath.Join(root, "sub", "b"), 7)
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	got, err := directorySize(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	// The symbolic link is neither followed nor counted.
	if got.bytes != 12 || got.partial {
		t.Fatalf("size = %d bytes, partial %t; want 12 bytes, complete", got.bytes, got.partial)
	}
}

func TestDirectorySizeWalksNestedDirectories(t *testing.T) {
	root := t.TempDir()
	writeSizedFile(t, filepath.Join(root, "one", "two", "three", "deep"), 11)
	writeSizedFile(t, filepath.Join(root, "one", "side"), 3)
	writeSizedFile(t, filepath.Join(root, "top"), 2)
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := directorySize(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if got.bytes != 16 || got.partial {
		t.Fatalf("size = %d bytes, partial %t; want 16 bytes, complete", got.bytes, got.partial)
	}
}

func TestDirectorySizeCountsEveryEntryAcrossBufferReads(t *testing.T) {
	// Each read fills dirBufferSize bytes, so this directory takes several reads
	// and the walk must carry on from where each one ended.
	root := t.TempDir()
	const files = 3000
	for i := 0; i < files; i++ {
		writeSizedFile(t, filepath.Join(root, "a-fairly-long-file-name", fmt.Sprintf("entry-%06d-with-padding-to-fill-buffers", i)), 1)
	}
	got, err := directorySize(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if got.bytes != files || got.partial {
		t.Fatalf("size = %d bytes, partial %t; want %d bytes, complete", got.bytes, got.partial, files)
	}
}

func TestDirectorySizeDoesNotFollowSymlinkedDirectories(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeSizedFile(t, filepath.Join(outside, "big"), 1000)
	writeSizedFile(t, filepath.Join(root, "own"), 4)
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	got, err := directorySize(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if got.bytes != 4 || got.partial {
		t.Fatalf("size = %d bytes, partial %t; want 4 bytes, complete", got.bytes, got.partial)
	}
}

func TestDirectorySizeOfMissingRootIsAnError(t *testing.T) {
	if _, err := directorySize(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing root measured without an error")
	}
}

func TestDirectorySizeMarksUnreadableEntriesPartial(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory, so nothing is unreadable")
	}
	root := t.TempDir()
	writeSizedFile(t, filepath.Join(root, "readable"), 5)
	locked := filepath.Join(root, "locked")
	writeSizedFile(t, filepath.Join(locked, "hidden"), 100)
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	got, err := directorySize(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if got.bytes != 5 || !got.partial {
		t.Fatalf("size = %d bytes, partial %t; want 5 bytes, partial", got.bytes, got.partial)
	}
}

func TestDirectorySizeStopsWhenCancelled(t *testing.T) {
	root := t.TempDir()
	writeSizedFile(t, filepath.Join(root, "file"), 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := directorySize(ctx, root); err == nil {
		t.Fatal("a cancelled walk returned no error")
	}
}
