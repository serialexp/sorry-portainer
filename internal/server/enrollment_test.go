package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/pki"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

type onlineRelay struct {
	stackRelay
	online []protocol.HostInfo
}

func (r *onlineRelay) Hosts() []protocol.HostInfo { return r.online }

func enrollmentServer(t *testing.T) (*Server, *enrollment.Service, *onlineRelay) {
	t.Helper()
	ca, err := pki.NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hosts.json")
	if err := enrollment.CreateRegistry(path); err != nil {
		t.Fatal(err)
	}
	registry, err := enrollment.OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := enrollment.NewService(ca, registry, time.Now)
	svc.SetDisconnect(func(string) {})
	relay := &onlineRelay{}
	s := New("a sufficiently long password", time.Hour, relay, nil)
	s.EnableEnrollment(svc, "wss://jp1.example:9443/control/agent")
	return s, svc, relay
}

func enrollHost(t *testing.T, svc *enrollment.Service, host string) {
	t.Helper()
	token, _, err := svc.CreateToken(host, 0)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := pki.NewKey()
	csr, _ := pki.NewCSR(key, host)
	if _, _, err := svc.Enroll(enrollment.EnrollRequest{Token: token, HostID: host, Prefix: host + "-", CSR: string(csr)}); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollmentTokenLifecycle(t *testing.T) {
	s, svc, _ := enrollmentServer(t)
	w := authenticatedStackRequest(t, s, http.MethodPost, "/api/enrollment/tokens", `{"host_id":"hel1","ttl":"30m"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token response may be cached")
	}
	var created TokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if _, err := enrollment.ParseToken(created.Token); err != nil {
		t.Fatalf("token %v", err)
	}
	if until := time.Until(created.ExpiresAt); until < 29*time.Minute || until > 30*time.Minute {
		t.Fatalf("expires in %s", until)
	}
	want := "sudo runuser -u sorry-portainer -- /usr/local/bin/sorry-portainer-agent enroll --server-url wss://jp1.example:9443/control/agent --host-id hel1 --dir /var/lib/sorry-portainer-agent --token-file -"
	if created.Command != want || created.Reenroll {
		t.Fatalf("command %q, reenroll %v", created.Command, created.Reenroll)
	}
	if strings.Contains(created.Command, created.Token) {
		t.Fatal("token on the command line")
	}

	w = authenticatedStackRequest(t, s, http.MethodGet, "/api/enrollment/tokens", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), created.Token) || !strings.Contains(w.Body.String(), `"host_id":"hel1"`) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if w = authenticatedStackRequest(t, s, http.MethodDelete, "/api/enrollment/tokens/hel1", ""); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if len(svc.Tokens()) != 0 {
		t.Fatal("token not revoked")
	}
	if w = authenticatedStackRequest(t, s, http.MethodDelete, "/api/enrollment/tokens/hel1", ""); w.Code != http.StatusNotFound {
		t.Fatalf("second revoke: %d", w.Code)
	}
}

// A token for an enrolled host comes with a command that replaces the
// agent's configuration.
func TestEnrollmentTokenForEnrolledHost(t *testing.T) {
	s, svc, _ := enrollmentServer(t)
	enrollHost(t, svc, "hel1")
	w := authenticatedStackRequest(t, s, http.MethodPost, "/api/enrollment/tokens", `{"host_id":"hel1"}`)
	var created TokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || w.Code != http.StatusOK {
		t.Fatalf("create: %d %s %v", w.Code, w.Body, err)
	}
	if !created.Reenroll || !strings.HasSuffix(created.Command, " --token-file - --replace") {
		t.Fatalf("command %q, reenroll %v", created.Command, created.Reenroll)
	}
}

func TestEnrollmentHosts(t *testing.T) {
	s, svc, relay := enrollmentServer(t)
	enrollHost(t, svc, "hel1")
	enrollHost(t, svc, "jp1")
	relay.online = []protocol.HostInfo{{HostID: "jp1"}}
	w := authenticatedStackRequest(t, s, http.MethodGet, "/api/enrollment/hosts", "")
	var hosts []enrollment.HostStatus
	if err := json.Unmarshal(w.Body.Bytes(), &hosts); err != nil || w.Code != http.StatusOK {
		t.Fatalf("hosts: %d %s %v", w.Code, w.Body, err)
	}
	if len(hosts) != 2 || hosts[0].HostID != "hel1" || hosts[0].Online || !hosts[1].Online || hosts[0].CurrentNotAfter.IsZero() {
		t.Fatalf("hosts %+v", hosts)
	}
	if w = authenticatedStackRequest(t, s, http.MethodDelete, "/api/enrollment/hosts/hel1", ""); w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	if _, ok := svc.Registry().Get("hel1"); ok {
		t.Fatal("host still enrolled")
	}
	if w = authenticatedStackRequest(t, s, http.MethodDelete, "/api/enrollment/hosts/hel1", ""); w.Code != http.StatusNotFound {
		t.Fatalf("second remove: %d", w.Code)
	}
}

func TestEnrollmentRequestErrors(t *testing.T) {
	s, _, _ := enrollmentServer(t)
	cases := []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPost, "/api/enrollment/tokens", `{"host_id":"../x"}`, http.StatusBadRequest},
		{http.MethodPost, "/api/enrollment/tokens", `{"host_id":"hel1","ttl":"soon"}`, http.StatusBadRequest},
		{http.MethodPost, "/api/enrollment/tokens", `{"host_id":"hel1","ttl":"72h"}`, http.StatusBadRequest},
		{http.MethodPost, "/api/enrollment/tokens", `{"host_id":"hel1","admin":true}`, http.StatusBadRequest},
		{http.MethodPost, "/api/enrollment/tokens", `{"host_id":"hel1"} {}`, http.StatusBadRequest},
		{http.MethodPost, "/api/enrollment/tokens", `{"host_id":"` + strings.Repeat("a", maxEnrollmentBody) + `"}`, http.StatusBadRequest},
		{http.MethodPut, "/api/enrollment/tokens", `{}`, http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/enrollment/hosts", `{}`, http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/enrollment/hosts/hel1", "", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/enrollment/other", "", http.StatusNotFound},
	}
	for _, c := range cases {
		if w := authenticatedStackRequest(t, s, c.method, c.path, c.body); w.Code != c.status {
			t.Errorf("%s %s %.40s: %d, want %d", c.method, c.path, c.body, w.Code, c.status)
		}
	}
}

func TestEnrollmentNeedsASessionAndConfiguration(t *testing.T) {
	s, _, _ := enrollmentServer(t)
	w := httptest.NewRecorder()
	s.Handler(nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/enrollment/hosts", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("without a session: %d", w.Code)
	}
	plain := New("a sufficiently long password", time.Hour, &onlineRelay{}, nil)
	if w := authenticatedStackRequest(t, plain, http.MethodGet, "/api/enrollment/hosts", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("without enrollment: %d", w.Code)
	}
}
