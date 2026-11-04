package unixsock

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestListenReplacesOnlyAStaleSocket(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, "s.sock")
	first, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("directory %v %v", st.Mode(), err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v %v", st.Mode(), err)
	}
	// Go removes the socket file on Close; keep it to leave a stale one.
	first.SetUnlinkOnClose(false)
	_ = first.Close()
	second, err := Listen(path)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	defer second.Close()

	file := filepath.Join(dir, "file.sock")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(file); err == nil {
		t.Fatal("replaced a regular file")
	}
	if data, _ := os.ReadFile(file); string(data) != "keep" {
		t.Fatal("regular file changed")
	}
}

func TestListenRefusesAnOpenDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(filepath.Join(dir, "s.sock")); err == nil {
		t.Fatal("listened in a world-readable directory")
	}
}

func TestPeerUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p", "s.sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	for _, conn := range []*net.UnixConn{server, client.(*net.UnixConn)} {
		if uid, err := PeerUID(conn); err != nil || uid != os.Getuid() {
			t.Fatalf("peer uid %d %v", uid, err)
		}
	}
}
