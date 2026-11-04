package controlcert

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/pki"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func setup(t *testing.T, names ...string) (*pki.CA, *clock, string) {
	t.Helper()
	c := &clock{t: time.Now()}
	ca, err := pki.NewCA(c.t)
	if err != nil {
		t.Fatal(err)
	}
	return ca, c, filepath.Join(t.TempDir(), "control-server.pem")
}

func TestIssuesOnceAndKeeps(t *testing.T) {
	ca, c, path := setup(t)
	m, err := New(ca, path, []string{"jp1.example"}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := m.GetCertificate(nil)
	if len(first.Certificate) != 2 {
		t.Fatalf("chain has %d certificates, want leaf and CA", len(first.Certificate))
	}
	// A restart loads the same certificate instead of issuing another.
	again, err := New(ca, path, []string{"jp1.example"}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := again.GetCertificate(nil)
	if !second.Leaf.Equal(first.Leaf) {
		t.Fatal("restart issued a new certificate")
	}
}

func TestReissuesNearExpiry(t *testing.T) {
	ca, c, path := setup(t)
	m, err := New(ca, path, []string{"jp1.example"}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := m.GetCertificate(nil)
	c.advance(pki.LeafLifetime - RenewBefore - time.Hour)
	if err := m.Check(); err != nil {
		t.Fatal(err)
	}
	if same, _ := m.GetCertificate(nil); !same.Leaf.Equal(first.Leaf) {
		t.Fatal("re-issued too early")
	}
	c.advance(2 * time.Hour)
	if err := m.Check(); err != nil {
		t.Fatal(err)
	}
	renewed, _ := m.GetCertificate(nil)
	if renewed.Leaf.Equal(first.Leaf) {
		t.Fatal("not re-issued within the renewal window")
	}
	if renewed.Leaf.PublicKey.(*ecdsa.PublicKey).Equal(first.Leaf.PublicKey) {
		t.Fatal("re-issued certificate reuses the old key")
	}
	onDisk, err := pki.ReadBundle(path)
	if err != nil || !onDisk.Leaf.Equal(renewed.Leaf) {
		t.Fatalf("file not updated: %v", err)
	}
}

func TestReissuesForNewNamesAndLegacyCertificates(t *testing.T) {
	ca, c, path := setup(t)
	if _, err := New(ca, path, []string{"jp1.example"}, c.now, nil); err != nil {
		t.Fatal(err)
	}
	m, err := New(ca, path, []string{"jp1.example", "192.0.2.10"}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := m.GetCertificate(nil)
	if !pki.ServerCovers(cert.Leaf, []string{"192.0.2.10"}) {
		t.Fatal("new name not covered")
	}
	// A certificate from another CA is replaced.
	other, _ := pki.NewCA(c.now())
	key, _ := pki.NewKey()
	foreign, _ := other.IssueServer(&key.PublicKey, []string{"jp1.example"}, c.now())
	if err := pki.WriteBundle(path, key, foreign); err != nil {
		t.Fatal(err)
	}
	m, err = New(ca, path, []string{"jp1.example"}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ = m.GetCertificate(nil)
	if cert.Leaf.CheckSignatureFrom(ca.Cert) != nil {
		t.Fatal("kept a certificate from another CA")
	}
	// A corrupt file is replaced, not fatal.
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(ca, path, []string{"jp1.example"}, c.now, nil); err != nil {
		t.Fatal(err)
	}
}

// TestServesRotatedCertificateWithAndWithoutSNI connects by name and by IP
// (no SNI) and sees the re-issued certificate without a restart.
func TestServesRotatedCertificateWithAndWithoutSNI(t *testing.T) {
	ca, c, path := setup(t)
	m, err := New(ca, path, []string{"localhost", "127.0.0.1"}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Not httptest: it adds its own certificate to Certificates, which Go
	// serves to clients without SNI instead of calling GetCertificate.
	listener, err := tls.Listen("tcp", "127.0.0.1:0", m.TLSConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() { _ = http.Serve(listener, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})) }()
	_, port, _ := net.SplitHostPort(listener.Addr().String())

	served := func(host string) *x509.Certificate {
		t.Helper()
		roots := x509.NewCertPool()
		roots.AddCert(ca.Cert)
		conn, err := tls.Dial("tcp", net.JoinHostPort(host, port), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13, Time: c.now})
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		defer conn.Close()
		if err := conn.Handshake(); err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		return conn.ConnectionState().PeerCertificates[0]
	}
	before, _ := m.GetCertificate(nil)
	for _, host := range []string{"localhost", "127.0.0.1"} {
		if !served(host).Equal(before.Leaf) {
			t.Fatalf("%s: unexpected certificate", host)
		}
	}
	c.advance(pki.LeafLifetime - RenewBefore + time.Hour)
	if err := m.Check(); err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetCertificate(nil)
	for _, host := range []string{"localhost", "127.0.0.1"} {
		if !served(host).Equal(after.Leaf) {
			t.Fatalf("%s: still served the old certificate", host)
		}
	}
}
