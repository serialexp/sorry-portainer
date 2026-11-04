package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

const testPassword = "a sufficiently long password"

var testSessions = SessionOptions{Password: testPassword, TTL: time.Hour}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newTestSessions(o SessionOptions) (*Sessions, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	if o.Password == "" {
		o.Password = testPassword
	}
	if o.TTL == 0 {
		o.TTL = time.Hour
	}
	o.Now = clock.now
	return NewSessions(o), clock
}

// login posts password from remote (host:port), with X-Forwarded-For
// entries if given.
func login(s *Sessions, password, remote string, forwarded ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/session/login", strings.NewReader(fmt.Sprintf(`{"password":%q}`, password)))
	r.RemoteAddr = remote
	for _, f := range forwarded {
		r.Header.Add("X-Forwarded-For", f)
	}
	w := httptest.NewRecorder()
	s.Login(w, r)
	return w
}

func authenticated(s *Sessions, c *http.Cookie) bool {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(w, r)
	return w.Code == http.StatusNoContent
}

func sessionCookieOf(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("no session cookie (status %d)", w.Code)
	return nil
}

func TestLoginAndRequire(t *testing.T) {
	s, _ := newTestSessions(SessionOptions{})
	w := login(s, testPassword, "192.0.2.1:1234")
	if w.Code != http.StatusNoContent {
		t.Fatalf("login status %d", w.Code)
	}
	c := sessionCookieOf(t, w)
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != 3600 {
		t.Fatalf("cookie attributes %+v", c)
	}
	if !authenticated(s, c) {
		t.Fatal("session not accepted")
	}
}

func TestInsecureCookieForPlainHTTPDevelopment(t *testing.T) {
	s, _ := newTestSessions(SessionOptions{InsecureCookie: true})
	if c := sessionCookieOf(t, login(s, testPassword, "192.0.2.1:1234")); c.Secure {
		t.Fatal("cookie is Secure although insecure cookies were configured")
	}
}

func TestRequireRejectsMissing(t *testing.T) {
	s, _ := newTestSessions(SessionOptions{})
	w := httptest.NewRecorder()
	s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
}

func TestSessionExpires(t *testing.T) {
	s, clock := newTestSessions(SessionOptions{})
	c := sessionCookieOf(t, login(s, testPassword, "192.0.2.1:1234"))
	clock.t = clock.t.Add(time.Hour + time.Second)
	if authenticated(s, c) {
		t.Fatal("expired session accepted")
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	s, _ := newTestSessions(SessionOptions{})
	c := sessionCookieOf(t, login(s, testPassword, "192.0.2.1:1234"))
	other := sessionCookieOf(t, login(s, testPassword, "192.0.2.1:1234"))

	r := httptest.NewRequest(http.MethodPost, "/api/session/logout", nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	s.Logout(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout status %d", w.Code)
	}
	cleared := sessionCookieOf(t, w)
	if cleared.MaxAge >= 0 || cleared.Value != "" || !cleared.Secure || !cleared.HttpOnly {
		t.Fatalf("logout cookie %+v", cleared)
	}
	if authenticated(s, c) {
		t.Fatal("session still valid after logout")
	}
	if !authenticated(s, other) {
		t.Fatal("logout ended another session")
	}

	w = httptest.NewRecorder()
	s.Logout(w, httptest.NewRequest(http.MethodPost, "/api/session/logout", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout without a session: %d", w.Code)
	}
}

func TestSessionRoutesNeedPost(t *testing.T) {
	s, _ := newTestSessions(SessionOptions{})
	for name, handler := range map[string]http.HandlerFunc{"login": s.Login, "logout": s.Logout} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodPost {
			t.Fatalf("%s GET: %d allow=%q", name, w.Code, w.Header().Get("Allow"))
		}
	}
}

func TestFailedLoginsAreLimitedPerClient(t *testing.T) {
	s, clock := newTestSessions(SessionOptions{})
	for i := range loginBurst {
		if w := login(s, "wrong password", "192.0.2.1:1000"); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, w.Code)
		}
	}
	w := login(s, testPassword, "192.0.2.1:1001")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "3" || !strings.Contains(w.Body.String(), "try again in 3 seconds") {
		t.Fatalf("over the limit: %d retry-after=%q %q", w.Code, w.Header().Get("Retry-After"), w.Body)
	}
	clock.t = clock.t.Add(loginInterval - time.Second)
	if w := login(s, testPassword, "192.0.2.1:1001"); !strings.Contains(w.Body.String(), "try again in 1 second\n") {
		t.Fatalf("one second left: %q", w.Body)
	}
	clock.t = clock.t.Add(-(loginInterval - time.Second))
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("a refused attempt got a session")
	}
	// Another client is not affected.
	if w := login(s, testPassword, "192.0.2.2:1000"); w.Code != http.StatusNoContent {
		t.Fatalf("other client: %d", w.Code)
	}
	// One attempt comes back per interval.
	clock.t = clock.t.Add(loginInterval)
	if w := login(s, "wrong password", "192.0.2.1:1000"); w.Code != http.StatusUnauthorized {
		t.Fatalf("after waiting: %d", w.Code)
	}
	if w := login(s, testPassword, "192.0.2.1:1000"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second attempt after one interval: %d", w.Code)
	}
}

func TestSuccessfulLoginsDoNotCount(t *testing.T) {
	s, _ := newTestSessions(SessionOptions{})
	for i := range 3 * loginBurst {
		if w := login(s, testPassword, "192.0.2.1:1000"); w.Code != http.StatusNoContent {
			t.Fatalf("login %d: %d", i+1, w.Code)
		}
	}
	for i := range loginBurst {
		if w := login(s, "wrong password", "192.0.2.1:1000"); w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d after successes: %d", i+1, w.Code)
		}
	}
}

func TestIPv6ClientsAreLimitedPerSlash64(t *testing.T) {
	s, _ := newTestSessions(SessionOptions{})
	for i := range loginBurst {
		login(s, "wrong password", fmt.Sprintf("[2001:db8:1:2::%x]:1000", i+1))
	}
	if w := login(s, testPassword, "[2001:db8:1:2::ffff]:1000"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("same /64: %d", w.Code)
	}
	if w := login(s, testPassword, "[2001:db8:1:3::1]:1000"); w.Code != http.StatusNoContent {
		t.Fatalf("other /64: %d", w.Code)
	}
}

func TestTrustedProxyForwardsTheClient(t *testing.T) {
	proxy := netip.MustParsePrefix("172.18.0.0/16")
	s, _ := newTestSessions(SessionOptions{TrustedProxies: []netip.Prefix{proxy}})
	for range loginBurst {
		login(s, "wrong password", "172.18.0.5:4000", "198.51.100.7")
	}
	if w := login(s, testPassword, "172.18.0.5:4000", "198.51.100.7"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("limited client through the proxy: %d", w.Code)
	}
	// Another client through the same proxy is separate.
	if w := login(s, testPassword, "172.18.0.5:4000", "198.51.100.8"); w.Code != http.StatusNoContent {
		t.Fatalf("other client through the proxy: %d", w.Code)
	}
	// A client cannot escape its limit by sending its own X-Forwarded-For
	// through the proxy: the proxy appends the address it saw.
	if w := login(s, testPassword, "172.18.0.5:4000", "203.0.113.9, 198.51.100.7"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("spoofed entry left of the real client: %d", w.Code)
	}
}

func TestUntrustedPeersCannotChooseTheirClientKey(t *testing.T) {
	s, _ := newTestSessions(SessionOptions{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16")}})
	for i := range loginBurst {
		login(s, "wrong password", "192.0.2.1:1000", fmt.Sprintf("198.51.100.%d", i+1))
	}
	if w := login(s, testPassword, "192.0.2.1:1000", "198.51.100.200"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("X-Forwarded-For from an untrusted peer was honoured: %d", w.Code)
	}
}

func TestClientKey(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	cases := []struct {
		name, remote string
		forwarded    []string
		want         string
	}{
		{"direct IPv4", "192.0.2.1:1", nil, "192.0.2.1/32"},
		{"direct IPv6", "[2001:db8::1]:1", nil, "2001:db8::/64"},
		{"IPv4-mapped peer", "[::ffff:192.0.2.1]:1", nil, "192.0.2.1/32"},
		{"proxy without header", "10.0.0.2:1", nil, "10.0.0.2/32"},
		{"one proxy", "10.0.0.2:1", []string{"192.0.2.9"}, "192.0.2.9/32"},
		{"chain of trusted proxies", "10.0.0.2:1", []string{"192.0.2.9, 10.0.0.3"}, "192.0.2.9/32"},
		{"header lines", "10.0.0.2:1", []string{"203.0.113.1", "192.0.2.9"}, "192.0.2.9/32"},
		{"garbled entry", "10.0.0.2:1", []string{"192.0.2.9, nonsense"}, "10.0.0.2/32"},
		{"garbled entry left of the client", "10.0.0.2:1", []string{"nonsense, 192.0.2.9"}, "192.0.2.9/32"},
		{"all trusted", "10.0.0.2:1", []string{"10.0.0.4, 10.0.0.3"}, "10.0.0.4/32"},
		{"IPv6 loopback proxy", "[::1]:1", []string{"2001:db8:5:6::7"}, "2001:db8:5:6::/64"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.RemoteAddr = c.remote
		for _, f := range c.forwarded {
			r.Header.Add("X-Forwarded-For", f)
		}
		if got := clientKey(r, trusted).String(); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestLimiterTableIsBounded(t *testing.T) {
	l := newLoginLimiter(2)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	a, b, c := netip.MustParsePrefix("192.0.2.1/32"), netip.MustParsePrefix("192.0.2.2/32"), netip.MustParsePrefix("192.0.2.3/32")
	for _, client := range []netip.Prefix{a, b} {
		if _, ok := l.take(client, now); !ok {
			t.Fatal("first attempt refused")
		}
	}
	if wait, ok := l.take(c, now); ok || wait != loginInterval {
		t.Fatalf("third client admitted into a full table: ok=%v wait=%s", ok, wait)
	}
	// Known clients keep working while the table is full.
	if _, ok := l.take(a, now); !ok {
		t.Fatal("known client refused")
	}
	// Once the others' allowances are full again, their entries go.
	now = now.Add(loginBurst * loginInterval)
	if _, ok := l.take(c, now); !ok {
		t.Fatal("third client refused after the table could be pruned")
	}
	if len(l.full) > 2 {
		t.Fatalf("table holds %d clients", len(l.full))
	}
}

func TestSessionTableIsBounded(t *testing.T) {
	s, clock := newTestSessions(SessionOptions{})
	s.max = 3
	var cookies []*http.Cookie
	for range 5 {
		cookies = append(cookies, sessionCookieOf(t, login(s, testPassword, "192.0.2.1:1")))
		clock.t = clock.t.Add(time.Minute)
	}
	if len(s.values) != 3 {
		t.Fatalf("%d sessions, want 3", len(s.values))
	}
	for i, c := range cookies {
		if want := i >= 2; authenticated(s, c) != want {
			t.Fatalf("session %d valid=%v, want %v", i, !want, want)
		}
	}
}
