package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func newCA(t testing.TB) *CA {
	t.Helper()
	ca, err := NewCA(now)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestIssueAgentCertificate(t *testing.T) {
	ca := newCA(t)
	key := newKey(t)
	cert, err := ca.IssueAgent(&key.PublicKey, "hel1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckIssuedAgent(cert, ca.Cert, "hel1", &key.PublicKey, now); err != nil {
		t.Fatal(err)
	}
	if got, err := AgentHostID(cert); err != nil || got != "hel1" {
		t.Fatalf("host ID %q err %v", got, err)
	}
	if len(cert.DNSNames) != 0 || len(cert.IPAddresses) != 0 {
		t.Fatalf("agent certificate has names: %v %v", cert.DNSNames, cert.IPAddresses)
	}
	if !cert.NotBefore.Equal(now.Add(-Backdate)) || !cert.NotAfter.Equal(now.Add(LeafLifetime)) {
		t.Fatalf("validity %s – %s", cert.NotBefore, cert.NotAfter)
	}
	if _, err := ca.IssueAgent(&key.PublicKey, "../escape", now); err == nil {
		t.Fatal("issued a certificate for an invalid host ID")
	}
}

func TestLeafNeverOutlivesCA(t *testing.T) {
	ca := newCA(t)
	key := newKey(t)
	late := ca.Cert.NotAfter.Add(-24 * time.Hour)
	cert, err := ca.IssueAgent(&key.PublicKey, "hel1", late)
	if err != nil {
		t.Fatal(err)
	}
	if !cert.NotAfter.Equal(ca.Cert.NotAfter) {
		t.Fatalf("leaf expires %s, CA %s", cert.NotAfter, ca.Cert.NotAfter)
	}
}

func TestIssueServerCertificateNames(t *testing.T) {
	ca := newCA(t)
	key := newKey(t)
	names := []string{"control.example.com", "203.0.113.10", "::1"}
	cert, err := ca.IssueServer(&key.PublicKey, names, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.DNSNames) != 1 || len(cert.IPAddresses) != 2 || !cert.IPAddresses[0].Equal(net.ParseIP("203.0.113.10")) {
		t.Fatalf("names %v %v", cert.DNSNames, cert.IPAddresses)
	}
	if !ServerCovers(cert, names) {
		t.Fatal("certificate does not cover its own names")
	}
	if ServerCovers(cert, append(names, "other.example")) {
		t.Fatal("certificate covers a name it lacks")
	}
	if _, err := AgentHostID(cert); err == nil {
		t.Fatal("a server certificate was taken for an agent")
	}
}

func TestAgentHostIDRefusesLegacyAndLookalikes(t *testing.T) {
	ca := newCA(t)
	key := newKey(t)
	issue := func(usages []x509.ExtKeyUsage, uris ...string) *x509.Certificate {
		t.Helper()
		tmpl := ca.leaf(pkix.Name{CommonName: "x"}, now)
		tmpl.ExtKeyUsage = usages
		tmpl.DNSNames = []string{"host-local-a"}
		for _, raw := range uris {
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			tmpl.URIs = append(tmpl.URIs, u)
		}
		cert, err := sign(tmpl, &key.PublicKey, ca.Cert, ca.Key)
		if err != nil {
			t.Fatal(err)
		}
		return cert
	}
	client := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	both := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}
	for name, cert := range map[string]*x509.Certificate{
		"legacy usages":     issue(both, "sorry-host://local-a"),
		"DNS fallback only": issue(client),
		"lookalike scheme":  issue(client, "sorry-hostile://local-a"),
		"invalid ID":        issue(client, "sorry-host://-local-a"),
		"two identities":    issue(client, "sorry-host://local-a", "sorry-host://local-b"),
	} {
		if got, err := AgentHostID(cert); err == nil {
			t.Errorf("%s: accepted %q", name, got)
		}
	}
	if got, err := AgentHostID(issue(client, "sorry-host://local-a")); err != nil || got != "local-a" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestCheckIssuedAgentRefusesMismatches(t *testing.T) {
	ca := newCA(t)
	other := newCA(t)
	key, otherKey := newKey(t), newKey(t)
	cert, err := ca.IssueAgent(&key.PublicKey, "hel1", now)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]error{
		"other CA":   CheckIssuedAgent(cert, other.Cert, "hel1", &key.PublicKey, now),
		"other host": CheckIssuedAgent(cert, ca.Cert, "jp1", &key.PublicKey, now),
		"other key":  CheckIssuedAgent(cert, ca.Cert, "hel1", &otherKey.PublicKey, now),
		"clock":      CheckIssuedAgent(cert, ca.Cert, "hel1", &key.PublicKey, now.Add(-time.Hour)),
	}
	for name, err := range cases {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := cases["clock"]; err != nil && !strings.Contains(err.Error(), "clock") {
		t.Errorf("clock error does not mention the clock: %v", err)
	}
}

func TestParseCSR(t *testing.T) {
	key := newKey(t)
	csr, err := NewCSR(key, "hel1")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ParseCSR(csr)
	if err != nil || !pub.Equal(&key.PublicKey) {
		t.Fatalf("pub %v err %v", pub, err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, p384)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCSR(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})); err != ErrUnsupportedKey {
		t.Fatalf("P-384 request: %v", err)
	}
	block, _ := pem.Decode(csr)
	tampered := append([]byte(nil), block.Bytes...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := ParseCSR(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: tampered})); err == nil {
		t.Fatal("accepted a request with a broken signature")
	}
	if _, err := ParseCSR(append(append([]byte(nil), csr...), csr...)); err == nil {
		t.Fatal("accepted two requests")
	}
	if _, err := ParseCSR([]byte("not pem")); err == nil {
		t.Fatal("accepted garbage")
	}
}

func TestBundleRoundTrip(t *testing.T) {
	ca := newCA(t)
	key := newKey(t)
	cert, err := ca.IssueAgent(&key.PublicKey, "hel1", now)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.pem")
	if err := WriteBundle(path, key, cert, ca.Cert); err != nil {
		t.Fatal(err)
	}
	pair, err := ReadBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	if !pair.Leaf.Equal(cert) || len(pair.Certificate) != 2 {
		t.Fatalf("leaf or chain differs: %d certificates", len(pair.Certificate))
	}
	if err := WriteBundle(path, newKey(t), cert); err == nil {
		t.Fatal("wrote a bundle whose key does not match")
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBundle(path); err == nil {
		t.Fatal("read a group-readable bundle")
	}
}

func TestCAFilesRoundTrip(t *testing.T) {
	ca := newCA(t)
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if err := ca.WriteFiles(certFile, keyFile); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCA(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fingerprint() != ca.Fingerprint() {
		t.Fatal("fingerprint changed")
	}
	if err := WriteKey(keyFile, newKey(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCA(certFile, keyFile); err == nil {
		t.Fatal("loaded a CA whose key does not match")
	}
}

func TestFingerprintMatches(t *testing.T) {
	ca, err := NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fp := ca.Fingerprint()
	if len(fp.String()) != fingerprintTextLen || !fp.Matches(fp.String()) {
		t.Fatalf("%q does not match itself", fp.String())
	}
	other, err := NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{other.Fingerprint().String(), "", fp.String()[:42], fp.String() + "A"} {
		if fp.Matches(s) {
			t.Fatalf("matched %q", s)
		}
	}
	s := other.Fingerprint().String()
	if n := testing.AllocsPerRun(100, func() { fp.Matches(s) }); n != 0 {
		t.Fatalf("Matches allocates %v times", n)
	}
}

func TestFingerprintString(t *testing.T) {
	ca := newCA(t)
	s := ca.Fingerprint().String()
	if strings.ContainsAny(s, "=+/") || len(s) != 43 {
		t.Fatalf("not unpadded base64url: %q", s)
	}
	back, err := ParseFingerprint(s)
	if err != nil || back != ca.Fingerprint() {
		t.Fatalf("round trip: %v", err)
	}
	if _, err := ParseFingerprint(s[:42]); err == nil {
		t.Fatal("accepted a short fingerprint")
	}
}

func TestVerifyPinned(t *testing.T) {
	ca, other := newCA(t), newCA(t)
	key := newKey(t)
	server, err := ca.IssueServer(&key.PublicKey, []string{"jp1.example", "192.0.2.10"}, now)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := other.IssueServer(&key.PublicKey, []string{"jp1.example"}, now)
	if err != nil {
		t.Fatal(err)
	}
	agentKey := newKey(t)
	agentCert, err := ca.IssueAgent(&agentKey.PublicKey, "jp1.example", now)
	if err != nil {
		t.Fatal(err)
	}
	pin := ca.Fingerprint()
	if _, err := VerifyPinned([]*x509.Certificate{server, ca.Cert}, pin, "jp1.example", now); err != nil {
		t.Fatalf("DNS name: %v", err)
	}
	if _, err := VerifyPinned([]*x509.Certificate{server, ca.Cert}, pin, "192.0.2.10", now); err != nil {
		t.Fatalf("IP address: %v", err)
	}
	refused := map[string][]*x509.Certificate{
		"no CA in chain":           {server},
		"pinned CA appended":       {foreign, ca.Cert},
		"pinned CA as leaf":        {ca.Cert},
		"agent certificate as TLS": {agentCert, ca.Cert},
	}
	for name, chain := range refused {
		if _, err := VerifyPinned(chain, pin, "jp1.example", now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := VerifyPinned([]*x509.Certificate{server, ca.Cert}, pin, "other.example", now); err == nil {
		t.Error("other name: accepted")
	}
	if _, err := VerifyPinned([]*x509.Certificate{server, ca.Cert}, other.Fingerprint(), "jp1.example", now); err == nil {
		t.Error("other pin: accepted")
	}
}
