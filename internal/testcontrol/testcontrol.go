// Package testcontrol starts a real control listener for tests: CA, server
// certificate, host registry, enrollment service, relay and control routes,
// on 127.0.0.1 with a controllable clock.
package testcontrol

import (
	"crypto/tls"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/controlcert"
	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/pki"
	"github.com/serialexp/sorry-portainer/internal/relay"
	"github.com/serialexp/sorry-portainer/internal/server"
)

// Clock is a settable time source shared by server and agents.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// Master is a running control listener.
type Master struct {
	Clock      *Clock
	CA         *pki.CA
	StateDir   string
	Enrollment *enrollment.Service
	Remote     *relay.Remote
	Control    *server.Control
	Cert       *controlcert.Manager
	// ServerURL is the relay URL agents dial, by IP address.
	ServerURL string
	// Addr is host:port of the listener.
	Addr string
}

// Start runs a master until the test ends.
func Start(t testing.TB) *Master {
	t.Helper()
	clock := &Clock{t: time.Now()}
	// A server's state directory is private, as provisioning makes it.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ca, err := pki.NewCA(clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := controlcert.New(ca, filepath.Join(dir, "control-server.pem"), []string{"127.0.0.1", "localhost"}, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := enrollment.OpenRegistry(filepath.Join(dir, "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := enrollment.NewService(ca, registry, clock.Now)
	remote := relay.NewRemote(relay.RemoteOptions{})
	svc.SetDisconnect(func(host string) { remote.Disconnect(host, errors.New("host removed or re-enrolled")) })
	control := &server.Control{Remote: remote, Enrollment: svc, Logf: t.Logf}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", cert.TLSConfig())
	if err != nil {
		t.Fatal(err)
	}
	srv := server.NewControlServer(control)
	srv.ErrorLog = log.New(testWriter{t}, "", 0)
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })
	addr := listener.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	return &Master{Clock: clock, CA: ca, StateDir: dir, Enrollment: svc, Remote: remote, Control: control, Cert: cert,
		ServerURL: "wss://127.0.0.1:" + port + "/control/agent", Addr: addr}
}

// Token creates an enrollment token for host.
func (m *Master) Token(t testing.TB, host string) string {
	t.Helper()
	token, _, err := m.Enrollment.CreateToken(host, 0)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

type testWriter struct{ t testing.TB }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
