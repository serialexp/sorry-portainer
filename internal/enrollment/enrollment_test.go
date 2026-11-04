package enrollment

import (
	"crypto/ecdsa"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/atomicfile"
	"github.com/serialexp/sorry-portainer/internal/pki"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

type fixture struct {
	t            testing.TB
	ca           *pki.CA
	clock        *clock
	path         string
	svc          *Service
	disconnected []string
}

func newFixture(t testing.TB) *fixture {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	ca, err := pki.NewCA(c.t)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hosts.json")
	if err := CreateRegistry(path); err != nil {
		t.Fatal(err)
	}
	registry, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ca: ca, clock: c, path: path}
	f.svc = NewService(ca, registry, c.now)
	f.svc.SetDisconnect(func(host string) { f.disconnected = append(f.disconnected, host) })
	return f
}

func (f *fixture) token(host string) string {
	f.t.Helper()
	token, _, err := f.svc.CreateToken(host, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return token
}

func (f *fixture) csr(key *ecdsa.PrivateKey, host string) string {
	f.t.Helper()
	csr, err := pki.NewCSR(key, host)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(csr)
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	key, err := pki.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// enroll enrolls host with a fresh key and returns the key and certificate.
func (f *fixture) enroll(host string) (*ecdsa.PrivateKey, *x509.Certificate) {
	f.t.Helper()
	key := newKey(f.t)
	resp, _, err := f.svc.Enroll(EnrollRequest{Token: f.token(host), HostID: host, Prefix: host + "-", CSR: f.csr(key, host)})
	if err != nil {
		f.t.Fatal(err)
	}
	cert, err := pki.ParseCertificatePEM([]byte(resp.Certificate))
	if err != nil {
		f.t.Fatal(err)
	}
	return key, cert
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err == nil {
		return ""
	}
	return "uncoded: " + err.Error()
}

func TestTokenFormat(t *testing.T) {
	f := newFixture(t)
	text := f.token("hel1")
	parsed, err := ParseToken(text)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.String() != text || parsed.CA != f.ca.Fingerprint() {
		t.Fatal("token does not round trip or lacks the CA fingerprint")
	}
	if strings.ContainsAny(text, "=+/") {
		t.Fatalf("token is not unpadded base64url: %s", text)
	}
	for _, bad := range []string{"", "spt1", "spt2." + text[5:], text + ".x", text[:len(text)-1], strings.Replace(text, ".", "..", 1)} {
		if _, err := ParseToken(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestEnrollIssuesAndRecords(t *testing.T) {
	f := newFixture(t)
	key, cert := f.enroll("hel1")
	if err := pki.CheckIssuedAgent(cert, f.ca.Cert, "hel1", &key.PublicKey, f.clock.t); err != nil {
		t.Fatal(err)
	}
	host, ok := f.svc.Registry().Get("hel1")
	if !ok || host.Current.SHA256 != pki.FingerprintOf(cert).String() || host.Prefix != "hel1-" {
		t.Fatalf("registry entry %+v", host)
	}
	// Durable: a fresh registry from the file sees it.
	reopened, err := OpenRegistry(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Get("hel1"); !ok {
		t.Fatal("enrollment was not written")
	}
	if standing, err := f.svc.Check("hel1", cert); err != nil || standing != StandingCurrent {
		t.Fatalf("standing %v err %v", standing, err)
	}
	if len(f.disconnected) != 1 || f.disconnected[0] != "hel1" {
		t.Fatalf("disconnected %v", f.disconnected)
	}
}

func TestEnrollRefusals(t *testing.T) {
	f := newFixture(t)
	token := f.token("hel1")
	key := newKey(t)
	csr := f.csr(key, "hel1")
	parsed, _ := ParseToken(token)
	wrongSecret := parsed
	wrongSecret.Secret[0] ^= 1
	unknown := parsed
	unknown.ID = strings.Repeat("A", tokenIDLength)

	cases := []struct {
		name string
		req  EnrollRequest
		want string
	}{
		{"malformed token", EnrollRequest{Token: "nope", HostID: "hel1", Prefix: "hel1-", CSR: csr}, CodeInvalidRequest},
		{"invalid host", EnrollRequest{Token: token, HostID: "../x", Prefix: "hel1-", CSR: csr}, CodeInvalidRequest},
		{"invalid prefix", EnrollRequest{Token: token, HostID: "hel1", Prefix: "", CSR: csr}, CodeInvalidRequest},
		{"unknown token", EnrollRequest{Token: unknown.String(), HostID: "hel1", Prefix: "hel1-", CSR: csr}, CodeInvalidToken},
		{"wrong secret", EnrollRequest{Token: wrongSecret.String(), HostID: "hel1", Prefix: "hel1-", CSR: csr}, CodeInvalidToken},
		{"wrong host", EnrollRequest{Token: token, HostID: "jp1", Prefix: "jp1-", CSR: csr}, CodeWrongHost},
		{"bad CSR", EnrollRequest{Token: token, HostID: "hel1", Prefix: "hel1-", CSR: "garbage"}, CodeInvalidRequest},
	}
	for _, c := range cases {
		if _, _, err := f.svc.Enroll(c.req); code(err) != c.want {
			t.Errorf("%s: got %q, want %q", c.name, code(err), c.want)
		}
	}
	// None of the refusals above spent the token.
	if _, _, err := f.svc.Enroll(EnrollRequest{Token: token, HostID: "hel1", Prefix: "hel1-", CSR: csr}); err != nil {
		t.Fatalf("token was spent by a refusal: %v", err)
	}
}

func TestEnrollIsSingleUseButRetryable(t *testing.T) {
	f := newFixture(t)
	token := f.token("hel1")
	key := newKey(t)
	req := EnrollRequest{Token: token, HostID: "hel1", Prefix: "hel1-", CSR: f.csr(key, "hel1")}
	first, _, err := f.svc.Enroll(req)
	if err != nil {
		t.Fatal(err)
	}
	again, done, err := f.svc.Enroll(req)
	if err != nil || again.Certificate != first.Certificate || !done.Retry {
		t.Fatalf("retry with the same key: err %v, same certificate %v", err, again.Certificate == first.Certificate)
	}
	other := EnrollRequest{Token: token, HostID: "hel1", Prefix: "hel1-", CSR: f.csr(newKey(t), "hel1")}
	if _, _, err := f.svc.Enroll(other); code(err) != CodeInvalidToken {
		t.Fatalf("used token with another key: %v", err)
	}
}

func TestTokenExpiry(t *testing.T) {
	f := newFixture(t)
	token, expires, err := f.svc.CreateToken("hel1", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !expires.Equal(f.clock.t.Add(10 * time.Minute)) {
		t.Fatalf("expires %s", expires)
	}
	f.clock.t = expires
	key := newKey(t)
	if _, _, err := f.svc.Enroll(EnrollRequest{Token: token, HostID: "hel1", Prefix: "hel1-", CSR: f.csr(key, "hel1")}); code(err) != CodeInvalidToken {
		t.Fatalf("expired token: %v", err)
	}
	for _, ttl := range []time.Duration{time.Second, 25 * time.Hour} {
		if _, _, err := f.svc.CreateToken("hel1", ttl); code(err) != CodeInvalidRequest {
			t.Errorf("ttl %s: %v", ttl, err)
		}
	}
}

func TestNewTokenReplacesOld(t *testing.T) {
	f := newFixture(t)
	old := f.token("hel1")
	_ = f.token("hel1")
	key := newKey(t)
	if _, _, err := f.svc.Enroll(EnrollRequest{Token: old, HostID: "hel1", Prefix: "hel1-", CSR: f.csr(key, "hel1")}); code(err) != CodeInvalidToken {
		t.Fatalf("replaced token: %v", err)
	}
	if got := f.svc.Tokens(); len(got) != 1 || got[0].HostID != "hel1" {
		t.Fatalf("tokens %v", got)
	}
}

func TestTokenBound(t *testing.T) {
	f := newFixture(t)
	for i := range MaxTokens {
		if _, _, err := f.svc.CreateToken(hostName(i), 10*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := f.svc.CreateToken("one-too-many", 0); code(err) != CodeTooManyTokens {
		t.Fatalf("65th token: %v", err)
	}
	// Expired tokens are purged first.
	f.clock.t = f.clock.t.Add(11 * time.Minute)
	if _, _, err := f.svc.CreateToken("one-too-many", 0); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	if got := len(f.svc.Tokens()); got != 1 {
		t.Fatalf("%d tokens listed", got)
	}
}

func TestUsedTokensGiveWay(t *testing.T) {
	f := newFixture(t)
	f.enroll("used")
	for i := range MaxTokens - 1 {
		if _, _, err := f.svc.CreateToken(hostName(i), 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := f.svc.CreateToken("last", 0); err != nil {
		t.Fatalf("a used token blocked a new one: %v", err)
	}
}

func hostName(i int) string { return "host-" + string(rune('a'+i/26)) + string(rune('a'+i%26)) }

func TestRemoveRevokesTokenAndHost(t *testing.T) {
	f := newFixture(t)
	_, cert := f.enroll("hel1")
	token := f.token("hel1")
	f.disconnected = nil
	if err := f.svc.Remove("hel1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Check("hel1", cert); code(err) != CodeNotEnrolled {
		t.Fatalf("removed host: %v", err)
	}
	key := newKey(t)
	if _, _, err := f.svc.Enroll(EnrollRequest{Token: token, HostID: "hel1", Prefix: "hel1-", CSR: f.csr(key, "hel1")}); code(err) != CodeInvalidToken {
		t.Fatalf("token survived removal: %v", err)
	}
	if len(f.disconnected) != 1 {
		t.Fatalf("disconnected %v", f.disconnected)
	}
	if err := f.svc.Remove("hel1"); code(err) != CodeNotFound {
		t.Fatalf("second removal: %v", err)
	}
}

func TestReenrollReplacesCertificate(t *testing.T) {
	f := newFixture(t)
	_, first := f.enroll("hel1")
	_, second := f.enroll("hel1")
	if _, err := f.svc.Check("hel1", first); code(err) != CodeNotEnrolled {
		t.Fatalf("superseded certificate: %v", err)
	}
	if _, err := f.svc.Check("hel1", second); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRefusesExpiredCertificate(t *testing.T) {
	f := newFixture(t)
	_, cert := f.enroll("hel1")
	f.clock.t = cert.NotAfter.Add(time.Second)
	if _, err := f.svc.Check("hel1", cert); code(err) != CodeNotEnrolled {
		t.Fatalf("expired: %v", err)
	}
}

func TestRenewPromote(t *testing.T) {
	f := newFixture(t)
	_, current := f.enroll("hel1")
	next := newKey(t)
	csr := []byte(f.csr(next, "hel1"))
	renewed, err := f.svc.Renew("hel1", current, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err := pki.CheckIssuedAgent(renewed, f.ca.Cert, "hel1", &next.PublicKey, f.clock.t); err != nil {
		t.Fatal(err)
	}
	// Repeating the request for the same key returns the same certificate.
	again, err := f.svc.Renew("hel1", current, csr)
	if err != nil || !again.Equal(renewed) {
		t.Fatalf("repeated renewal: err %v same %v", err, again != nil && again.Equal(renewed))
	}
	// Both are accepted until the new one is used; the pending one only
	// after promotion passes the final check.
	if s, err := f.svc.Check("hel1", current); err != nil || s != StandingCurrent {
		t.Fatalf("current: %v %v", s, err)
	}
	if s, err := f.svc.Check("hel1", renewed); err != nil || s != StandingPending {
		t.Fatalf("pending: %v %v", s, err)
	}
	if err := f.svc.Authorize("hel1", renewed); err == nil {
		t.Fatal("pending certificate authorized before promotion")
	}
	// Renewing with the pending certificate is refused.
	if _, err := f.svc.Renew("hel1", renewed, []byte(f.csr(newKey(t), "hel1"))); code(err) != CodeNotEnrolled {
		t.Fatalf("renew with pending: %v", err)
	}
	// The pending entry survives a restart.
	reopened, err := OpenRegistry(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if host, _ := reopened.Get("hel1"); host.Pending.SHA256 != pki.FingerprintOf(renewed).String() {
		t.Fatal("pending certificate was not written")
	}
	if err := f.svc.Promote("hel1", renewed); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Authorize("hel1", renewed); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Check("hel1", current); code(err) != CodeNotEnrolled {
		t.Fatalf("old certificate after promotion: %v", err)
	}
	reopened, _ = OpenRegistry(f.path)
	if host, _ := reopened.Get("hel1"); host.Current.SHA256 != pki.FingerprintOf(renewed).String() || host.Pending != (CertRef{}) {
		t.Fatalf("promotion was not written: %+v", host)
	}
}

func TestSecondRenewalReplacesPending(t *testing.T) {
	f := newFixture(t)
	_, current := f.enroll("hel1")
	first, err := f.svc.Renew("hel1", current, []byte(f.csr(newKey(t), "hel1")))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.Renew("hel1", current, []byte(f.csr(newKey(t), "hel1")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Check("hel1", first); code(err) != CodeNotEnrolled {
		t.Fatalf("first pending: %v", err)
	}
	if s, _ := f.svc.Check("hel1", second); s != StandingPending {
		t.Fatal("second renewal is not pending")
	}
}

func TestRenewalRacingRemovalDoesNotResurrect(t *testing.T) {
	f := newFixture(t)
	_, current := f.enroll("hel1")
	f.svc.renewChecked = func() {
		if err := f.svc.Remove("hel1"); err != nil {
			t.Error(err)
		}
	}
	if _, err := f.svc.Renew("hel1", current, []byte(f.csr(newKey(t), "hel1"))); code(err) != CodeNotEnrolled {
		t.Fatalf("renewal after removal: %v", err)
	}
	if _, ok := f.svc.Registry().Get("hel1"); ok {
		t.Fatal("renewal brought the host back")
	}
}

func TestPromoteAfterRemovalIsNoOp(t *testing.T) {
	f := newFixture(t)
	_, current := f.enroll("hel1")
	renewed, err := f.svc.Renew("hel1", current, []byte(f.csr(newKey(t), "hel1")))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Remove("hel1"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Promote("hel1", renewed); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Authorize("hel1", renewed); code(err) != CodeNotEnrolled {
		t.Fatalf("authorized after removal: %v", err)
	}
}

func TestRegistryWriteFailures(t *testing.T) {
	f := newFixture(t)
	f.svc.registry.write = func(string, []byte) error { return errors.New("disk full") }
	key := newKey(t)
	token := f.token("hel1")
	req := EnrollRequest{Token: token, HostID: "hel1", Prefix: "hel1-", CSR: f.csr(key, "hel1")}
	if _, _, err := f.svc.Enroll(req); code(err) != CodeUnavailable {
		t.Fatalf("failed write: %v", err)
	}
	if _, ok := f.svc.Registry().Get("hel1"); ok {
		t.Fatal("failed write was published")
	}
	// The token was not spent by the failed write.
	f.svc.registry.write = func(path string, data []byte) error {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
		return atomicfile.ErrDirectorySync
	}
	if _, _, err := f.svc.Enroll(req); code(err) != CodeUnavailable {
		t.Fatalf("directory sync failure: %v", err)
	}
	if _, ok := f.svc.Registry().Get("hel1"); !ok {
		t.Fatal("renamed content was not published")
	}
	f.svc.registry.write = writeRegistryFile
	if _, _, err := f.svc.CreateToken("jp1", 0); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Remove("hel1"); code(err) != CodeUnavailable || !errors.Is(f.svc.registry.degraded, ErrDegraded) {
		t.Fatalf("degraded registry accepted a change: %v", err)
	}
}

func TestOpenRegistryFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if r, err := OpenRegistry(filepath.Join(dir, "missing.json")); err != nil || len(r.List()) != 0 {
		t.Fatalf("missing file: %v", err)
	}
	write := func(name, content string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for name, path := range map[string]string{
		"corrupt":         write("corrupt.json", "{", 0o600),
		"unknown field":   write("field.json", `{"version":1,"hosts":{},"extra":1}`, 0o600),
		"version":         write("version.json", `{"version":2,"hosts":{}}`, 0o600),
		"world readable":  write("mode.json", `{"version":1,"hosts":{}}`, 0o644),
		"bad host":        write("host.json", `{"version":1,"hosts":{"../x":{"current":{"sha256":"x"}}}}`, 0o600),
		"bad fingerprint": write("fp.json", `{"version":1,"hosts":{"hel1":{"current":{"sha256":"x"}}}}`, 0o600),
	} {
		if _, err := OpenRegistry(path); err == nil {
			t.Errorf("%s: opened", name)
		}
	}
	if err := CreateRegistry(filepath.Join(dir, "corrupt.json")); err == nil {
		t.Error("CreateRegistry replaced an existing file")
	}
}
