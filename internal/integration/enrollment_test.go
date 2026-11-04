package integration_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/agentcred"
	"github.com/serialexp/sorry-portainer/internal/config"
	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/pki"
	"github.com/serialexp/sorry-portainer/internal/relay"
	"github.com/serialexp/sorry-portainer/internal/testcontrol"
)

// enrolledAgent is an agent enrolled into dir and connected through RunAgent.
type enrolledAgent struct {
	cfg    config.Agent
	holder *agentcred.Holder
	ca     *x509.Certificate
	agent  *relay.Agent
	cancel context.CancelFunc
	done   chan error
}

func enroll(t *testing.T, m *testcontrol.Master, host, dir string, replace bool) string {
	t.Helper()
	path, err := agentcred.Enroll(context.Background(), agentcred.EnrollOptions{
		ServerURL: m.ServerURL, HostID: host, Token: m.Token(t, host), Dir: dir, Replace: replace, Now: m.Clock.Now,
	})
	if err != nil {
		t.Fatalf("enroll %s: %v", host, err)
	}
	return path
}

func runAgent(t *testing.T, m *testcontrol.Master, configPath string) *enrolledAgent {
	t.Helper()
	cfg, err := config.LoadAgentFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := agentcred.LoadHolder(cfg.CredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := agentcred.ReadCA(cfg.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := agentcred.ClientTLS(ca, cfg.ServerName, holder, m.Clock.Now)
	a := &enrolledAgent{cfg: cfg, holder: holder, ca: ca, done: make(chan error, 1),
		agent: &relay.Agent{HostID: cfg.HostID, Prefix: cfg.Prefix, Handler: &fakeAgent{id: cfg.HostID}}}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	go func() {
		a.done <- relay.RunAgent(ctx, a.agent, relay.ReconnectOptions{
			MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
			Dial: func(ctx context.Context) (*websocket.Conn, error) {
				return relay.DialAgent(ctx, cfg.ServerURL, tlsConfig)
			},
		})
	}()
	t.Cleanup(func() {
		cancel()
		<-a.done
	})
	return a
}

func (a *enrolledAgent) renewer(m *testcontrol.Master) *agentcred.Renewer {
	return &agentcred.Renewer{HostID: a.cfg.HostID, ServerURL: a.cfg.ServerURL, ServerName: a.cfg.ServerName, CA: a.ca,
		Dir: a.cfg.StateDir, CredentialFile: a.cfg.CredentialFile, Holder: a.holder, OnRenewed: a.agent.RequestReconnect, Now: m.Clock.Now}
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func online(m *testcontrol.Master, host string) bool {
	for _, h := range m.Remote.Hosts() {
		if h.HostID == host {
			return true
		}
	}
	return false
}

// dialWith opens a relay connection with an explicit client certificate
// and returns the HTTP status of a refused upgrade.
func dialWith(t testing.TB, m *testcontrol.Master, cert *tls.Certificate) (int, error) {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(m.CA.Cert)
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "127.0.0.1", Time: m.Clock.Now}
	if cert != nil {
		tlsConfig.Certificates = []tls.Certificate{*cert}
	}
	dialer := websocket.Dialer{TLSClientConfig: tlsConfig, HandshakeTimeout: 5 * time.Second}
	conn, resp, err := dialer.Dial(m.ServerURL, nil)
	if conn != nil {
		_ = conn.Close()
	}
	if resp != nil {
		return resp.StatusCode, err
	}
	return 0, err
}

func TestEnrollConnectRenewAndRemove(t *testing.T) {
	m := testcontrol.Start(t)
	agents := map[string]*enrolledAgent{}
	for _, host := range []string{"local-a", "local-b", "local-c"} {
		agents[host] = runAgent(t, m, enroll(t, m, host, filepath.Join(t.TempDir(), host), false))
	}
	eventually(t, "three agents online", func() bool { return len(m.Remote.Hosts()) == 3 })

	// Renewal: at the renewal time the agent gets a new certificate, drains,
	// reconnects with it, and the master promotes it.
	b := agents["local-b"]
	old := b.holder.Current()
	m.Clock.Advance(agentcred.RenewAt(old.Leaf).Sub(m.Clock.Now()) + time.Minute)
	if err := b.renewer(m).RenewOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	renewed := b.holder.Current()
	if renewed.Leaf.Equal(old.Leaf) {
		t.Fatal("holder still has the old certificate")
	}
	eventually(t, "renewed certificate promoted", func() bool {
		host, _ := m.Enrollment.Registry().Get("local-b")
		return host.Current.SHA256 == pki.FingerprintOf(renewed.Leaf).String() && host.Pending == (enrollment.CertRef{}) && online(m, "local-b")
	})
	onDisk, err := pki.ReadBundle(b.cfg.CredentialFile)
	if err != nil || !onDisk.Leaf.Equal(renewed.Leaf) {
		t.Fatalf("renewed credential not on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.cfg.StateDir, agentcred.StagedKeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged key left behind: %v", err)
	}
	// The old certificate no longer gets in.
	if status, _ := dialWith(t, m, old); status != http.StatusForbidden {
		t.Fatalf("old certificate: status %d", status)
	}

	// Removal disconnects the host and keeps it out.
	if err := m.Enrollment.Remove("local-c"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "removed host offline", func() bool { return !online(m, "local-c") })
	time.Sleep(100 * time.Millisecond) // several reconnect attempts
	if online(m, "local-c") {
		t.Fatal("removed host reconnected")
	}
	if !online(m, "local-a") || !online(m, "local-b") {
		t.Fatal("other hosts were disconnected")
	}
}

func TestControlRefusesUnenrolledAndForeignCertificates(t *testing.T) {
	m := testcontrol.Start(t)
	key, _ := pki.NewKey()
	stray, err := m.CA.IssueAgent(&key.PublicKey, "stray", m.Clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := dialWith(t, m, pki.TLSCertificate(key, stray)); status != http.StatusForbidden {
		t.Fatalf("CA-signed but unenrolled: status %d", status)
	}
	other, _ := pki.NewCA(m.Clock.Now())
	foreign, _ := other.IssueAgent(&key.PublicKey, "stray", m.Clock.Now())
	if _, err := dialWith(t, m, pki.TLSCertificate(key, foreign)); err == nil {
		t.Fatal("certificate from another CA got through")
	}
	if status, _ := dialWith(t, m, nil); status != http.StatusUnauthorized {
		t.Fatalf("no certificate: status %d", status)
	}
}

func TestEnrollPinsTheMaster(t *testing.T) {
	m := testcontrol.Start(t)
	dir := filepath.Join(t.TempDir(), "hel1")
	token := m.Token(t, "hel1")
	parsed, err := enrollment.ParseToken(token)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := pki.NewCA(m.Clock.Now())
	parsed.CA = other.Fingerprint()
	_, err = agentcred.Enroll(context.Background(), agentcred.EnrollOptions{ServerURL: m.ServerURL, HostID: "hel1", Token: parsed.String(), Dir: dir, Now: m.Clock.Now})
	if err == nil || !strings.Contains(err.Error(), "control CA named in the token") {
		t.Fatalf("enrolled against an unpinned master: %v", err)
	}
	for _, name := range []string{agentcred.ConfigFile, agentcred.CredentialFile, agentcred.CAFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s written by a failed enrollment", name)
		}
	}
	// The wrong pin never reached the master, so the real token still works
	// and the staged key is reused.
	staged, err := pki.ReadKey(filepath.Join(dir, agentcred.StagedKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentcred.Enroll(context.Background(), agentcred.EnrollOptions{ServerURL: m.ServerURL, HostID: "hel1", Token: token, Dir: dir, Now: m.Clock.Now}); err != nil {
		t.Fatal(err)
	}
	pair, err := pki.ReadBundle(filepath.Join(dir, agentcred.CredentialFile))
	if err != nil {
		t.Fatal(err)
	}
	if !staged.PublicKey.Equal(pair.Leaf.PublicKey) {
		t.Fatal("enrollment did not use the staged key")
	}
}

func TestEnrollByNameNeedsMatchingServerName(t *testing.T) {
	m := testcontrol.Start(t)
	byName := strings.Replace(m.ServerURL, "127.0.0.1", "localhost", 1)
	dir := filepath.Join(t.TempDir(), "hel1")
	if _, err := agentcred.Enroll(context.Background(), agentcred.EnrollOptions{ServerURL: byName, HostID: "hel1", Token: m.Token(t, "hel1"), Dir: dir, Now: m.Clock.Now}); err != nil {
		t.Fatalf("by name: %v", err)
	}
	dir2 := filepath.Join(t.TempDir(), "jp1")
	_, err := agentcred.Enroll(context.Background(), agentcred.EnrollOptions{ServerURL: m.ServerURL, HostID: "jp1", Token: m.Token(t, "jp1"), Dir: dir2, ServerName: "other.example", Now: m.Clock.Now})
	if err == nil {
		t.Fatal("enrolled with a server name the certificate does not cover")
	}
}

func TestEnrollReplaceKeepsSettings(t *testing.T) {
	m := testcontrol.Start(t)
	dir := filepath.Join(t.TempDir(), "hel1")
	path := enroll(t, m, "hel1", dir, false)
	if _, err := agentcred.Enroll(context.Background(), agentcred.EnrollOptions{ServerURL: m.ServerURL, HostID: "hel1", Token: m.Token(t, "hel1"), Dir: dir, Now: m.Clock.Now}); err == nil {
		t.Fatal("enrolled over an existing config without --replace")
	}
	// Settings an operator added survive re-enrollment, as does stack state.
	data, _ := os.ReadFile(path)
	edited := strings.Replace(string(data), `"compose_provider"`, `"secret_socket": "/run/custom.sock",
  "compose_provider"`, 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "stacks", "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, _ := pki.ReadBundle(filepath.Join(dir, agentcred.CredentialFile))
	enroll(t, m, "hel1", dir, true)
	cfg, err := config.LoadAgentFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SecretSocket != "/run/custom.sock" || cfg.Prefix != "hel1-" {
		t.Fatalf("settings lost: %+v", cfg)
	}
	if _, err := os.Stat(filepath.Join(dir, "stacks", "web")); err != nil {
		t.Fatal("stack state lost")
	}
	second, _ := pki.ReadBundle(filepath.Join(dir, agentcred.CredentialFile))
	if second.Leaf.Equal(first.Leaf) {
		t.Fatal("re-enrollment kept the old certificate")
	}
	if _, err := m.Enrollment.Check("hel1", first.Leaf); err == nil {
		t.Fatal("old certificate still accepted after re-enrollment")
	}
}

func TestUnauthenticatedRequestsAreBounded(t *testing.T) {
	m := testcontrol.Start(t)
	roots := x509.NewCertPool()
	roots.AddCert(m.CA.Cert)
	open := func() *tls.Conn {
		t.Helper()
		conn, err := tls.Dial("tcp", m.Addr, &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "127.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	// Eight requests that never finish sending their body hold every slot.
	for range 8 {
		conn := open()
		fmt.Fprintf(conn, "POST /control/enroll HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{")
	}
	time.Sleep(50 * time.Millisecond)
	conn := open()
	fmt.Fprintf(conn, "POST /control/enroll HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}")
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ninth unauthenticated request: status %d", resp.StatusCode)
	}
	// An enrolled agent is not held up by them.
	a := runAgent(t, m, enrollDirect(t, m, "hel1"))
	eventually(t, "agent online despite the flood", func() bool { return online(m, a.cfg.HostID) })
}

// enrollDirect enrolls through the service rather than the busy endpoint and
// writes the files Enroll would.
func enrollDirect(t *testing.T, m *testcontrol.Master, host string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), host)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key, _ := pki.NewKey()
	csr, _ := pki.NewCSR(key, host)
	resp, _, err := m.Enrollment.Enroll(enrollment.EnrollRequest{Token: m.Token(t, host), HostID: host, Prefix: host + "-", CSR: string(csr)})
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := pki.ParseCertificatePEM([]byte(resp.Certificate))
	if err := pki.WriteBundle(filepath.Join(dir, agentcred.CredentialFile), key, cert); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, agentcred.CAFile), []byte(resp.CA), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, agentcred.ConfigFile)
	body := fmt.Sprintf(`{"server_url":%q,"host_id":%q,"host_prefix":%q,"agent_credential":%q,"agent_ca":%q,"state_dir":%q}`,
		m.ServerURL, host, host+"-", filepath.Join(dir, agentcred.CredentialFile), filepath.Join(dir, agentcred.CAFile), dir)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExpiredCertificateCannotConnect(t *testing.T) {
	m := testcontrol.Start(t)
	a := runAgent(t, m, enroll(t, m, "hel1", filepath.Join(t.TempDir(), "hel1"), false))
	eventually(t, "online", func() bool { return online(m, "hel1") })
	cert := a.holder.Current()
	m.Clock.Advance(cert.Leaf.NotAfter.Sub(m.Clock.Now()) + time.Minute)
	if _, err := dialWith(t, m, cert); err == nil {
		t.Fatal("expired certificate connected")
	}
}
