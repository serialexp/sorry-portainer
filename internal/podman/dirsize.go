package podman

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// treeSize is the total size of the regular files under a directory.
type treeSize struct {
	bytes int64
	// partial is set when some entries could not be read and are left out.
	partial bool
}

// dirBufferSize is the buffer a directory level reads its entries into. A read
// returns as many entries as fit, so a directory of many entries takes several.
const dirBufferSize = 32 << 10

// The fixed fields of a linux_dirent64 record, which come before its name.
const (
	direntRecLenOffset = 16
	direntTypeOffset   = 18
	direntNameOffset   = 19
)

// directorySize totals the regular files under root. Entries it cannot read
// are left out and make the total partial: a volume's files often belong to
// container users that the agent cannot read. Symbolic links are neither
// followed nor counted, so a link cannot pull another tree into the total.
//
// The walk reads each directory with getdents and sizes each file with fstatat
// relative to its directory's descriptor. It builds no path strings and no
// file-info objects, so a scan of many files allocates only for the names of
// the directories it opens. Linux only, like the agent's other host interfaces.
func directorySize(ctx context.Context, root string) (treeSize, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return treeSize{}, fmt.Errorf("open directory: %w", err)
	}
	defer unix.Close(fd)
	walker := treeWalker{ctx: ctx}
	err = walker.walk(fd, true)
	return walker.out, err
}

// treeWalker carries one walk's state. Each directory level holds one
// descriptor and one buffer, so the walk's memory is bounded by the depth of
// the tree, not by its size.
type treeWalker struct {
	ctx context.Context
	out treeSize
	// stat receives the metadata of the file being sized.
	stat unix.Stat_t
	// free holds the buffers of directory levels that have finished, for reuse.
	free [][]byte
}

// walk reads the entries of the directory fd. root is set for the walk's first
// directory: a failure to read it fails the walk, while a failure deeper in the
// tree only makes the total partial.
func (w *treeWalker) walk(fd int, root bool) error {
	buf := w.buffer()
	defer w.release(buf)
	for {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		n, err := unix.ReadDirent(fd, buf)
		if err != nil {
			if root {
				return fmt.Errorf("read directory: %w", err)
			}
			w.out.partial = true
			return nil
		}
		if n == 0 {
			return nil
		}
		if err := w.entries(fd, buf[:n]); err != nil {
			return err
		}
	}
}

// entries handles the records of one read of the directory fd. Subdirectories
// are walked as they are found, which is why each level has its own buffer.
func (w *treeWalker) entries(fd int, data []byte) error {
	for len(data) > 0 {
		if len(data) < direntNameOffset {
			return errors.New("truncated directory entry")
		}
		reclen := int(binary.NativeEndian.Uint16(data[direntRecLenOffset:]))
		if reclen <= direntNameOffset || reclen > len(data) {
			return fmt.Errorf("directory entry of %d bytes", reclen)
		}
		kind := data[direntTypeOffset]
		record := data[direntNameOffset:reclen]
		data = data[reclen:]
		nameLen := bytes.IndexByte(record, 0)
		if nameLen < 0 {
			return errors.New("directory entry without a name")
		}
		name := record[:nameLen]
		if string(name) == "." || string(name) == ".." {
			continue
		}
		if kind == unix.DT_REG || kind == unix.DT_UNKNOWN {
			// The kernel NUL-terminates every name in the record, so record[0]
			// is a C string for fstatat.
			if err := w.statAt(fd, &record[0]); err != nil {
				w.out.partial = true
				continue
			}
			if kind == unix.DT_UNKNOWN {
				kind = kindOf(w.stat.Mode)
			}
		}
		switch kind {
		case unix.DT_REG:
			w.out.bytes += w.stat.Size
		case unix.DT_DIR:
			if err := w.subdir(fd, string(name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// subdir walks the directory name inside the directory parent. One that cannot
// be opened makes the total partial, as one that cannot be read does.
func (w *treeWalker) subdir(parent int, name string) error {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		w.out.partial = true
		return nil
	}
	defer unix.Close(fd)
	return w.walk(fd, false)
}

// statAt fills w.stat with the metadata of the entry that name points to in the
// directory fd, without following a symbolic link. name must be NUL-terminated.
func (w *treeWalker) statAt(fd int, name *byte) error {
	_, _, errno := unix.Syscall6(
		unix.SYS_NEWFSTATAT,
		uintptr(fd),
		uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&w.stat)),
		unix.AT_SYMLINK_NOFOLLOW,
		0,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

// kindOf maps a file mode to the d_type value of its kind.
func kindOf(mode uint32) uint8 {
	switch mode & unix.S_IFMT {
	case unix.S_IFREG:
		return unix.DT_REG
	case unix.S_IFDIR:
		return unix.DT_DIR
	}
	return unix.DT_UNKNOWN
}

// buffer returns a free read buffer for a directory level.
func (w *treeWalker) buffer() []byte {
	if n := len(w.free); n > 0 {
		buf := w.free[n-1]
		w.free = w.free[:n-1]
		return buf
	}
	return make([]byte, dirBufferSize)
}

// release returns a directory level's buffer for reuse.
func (w *treeWalker) release(buf []byte) {
	w.free = append(w.free, buf)
}
