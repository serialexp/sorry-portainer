// Package unixsock holds the Unix socket handling shared by the agent's
// secret socket and the server's admin socket: a private listener and the
// peer's uid.
package unixsock

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Listen creates the socket at path with mode 0600, replacing a stale socket
// but never any other kind of file. The directory is created with mode 0700
// and must not be group or world accessible.
func Listen(path string) (*net.UnixListener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("socket directory %s must not be group/world accessible (mode %04o)", filepath.Dir(path), info.Mode().Perm())
	}
	if existing, err := os.Lstat(path); err == nil {
		if existing.Mode().Type() != fs.ModeSocket {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

// PeerUID returns the uid of the process at the other end, as the kernel
// recorded it when the connection was made (SO_PEERCRED).
func PeerUID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return int(cred.Uid), nil
}
