package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/relay"
	"github.com/serialexp/sorry-portainer/internal/secretstore"
)

// secretRelay records secret pushes. Hosts in offline answer
// ErrHostUnavailable.
type secretRelay struct {
	stackRelay
	mu       sync.Mutex
	hosts    []protocol.HostInfo
	offline  map[string]bool
	problems []string
	synced   []protocol.SecretSync
	retained map[string][]string
}

func (r *secretRelay) Hosts() []protocol.HostInfo { return r.hosts }

func (r *secretRelay) SyncSecrets(_ context.Context, host string, sync protocol.SecretSync) (protocol.SecretSyncResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.offline[host] {
		return protocol.SecretSyncResult{}, relay.ErrHostUnavailable
	}
	copied := protocol.SecretSync{Stack: host + "/" + sync.Stack, Secrets: map[string][]byte{}}
	for name, value := range sync.Secrets {
		copied.Secrets[name] = append([]byte(nil), value...)
	}
	r.synced = append(r.synced, copied)
	return protocol.SecretSyncResult{Stack: sync.Stack, Problems: r.problems}, nil
}

func (r *secretRelay) RetainSecrets(_ context.Context, host string, stacks []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retained == nil {
		r.retained = map[string][]string{}
	}
	r.retained[host] = stacks
	return nil
}

func (r *secretRelay) syncs() []protocol.SecretSync {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]protocol.SecretSync(nil), r.synced...)
}

var cheapKDF = secretstore.KDF{Algorithm: "argon2id", Time: 1, MemoryKiB: 64, Threads: 1}

func secretServer(t *testing.T) (*Server, *secretRelay) {
	t.Helper()
	store, err := secretstore.OpenWithKDF(filepath.Join(t.TempDir(), "secrets"), cheapKDF)
	if err != nil {
		t.Fatal(err)
	}
	r := &secretRelay{hosts: []protocol.HostInfo{{HostID: "h1"}, {HostID: "h2"}}, offline: map[string]bool{}}
	return New("a sufficiently long password", time.Hour, r, store), r
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(w.Body).Decode(&v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

func TestSecretStoreLifecycleOverHTTP(t *testing.T) {
	s, r := secretServer(t)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		return authenticatedStackRequest(t, s, method, path, body)
	}
	if w := call(http.MethodGet, "/api/secrets/status", ""); !strings.Contains(w.Body.String(), `"uninitialized"`) {
		t.Fatalf("status %s", w.Body.String())
	}
	if w := call(http.MethodPut, "/api/hosts/h1/stacks/web/secrets/db", `{"value":"pw"}`); w.Code != http.StatusConflict {
		t.Fatalf("put before init %d", w.Code)
	}
	if w := call(http.MethodPost, "/api/secrets/initialize", `{"passphrase":"short"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("weak %d", w.Code)
	}
	if w := call(http.MethodPost, "/api/secrets/initialize", `{"passphrase":"correct horse battery"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"unlocked"`) {
		t.Fatalf("init %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, "/api/secrets/initialize", `{"passphrase":"correct horse battery"}`); w.Code != http.StatusConflict {
		t.Fatalf("second init %d", w.Code)
	}

	w := call(http.MethodPut, "/api/hosts/h1/stacks/web/secrets/db", `{"value":"hunter2"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("put %d %s", w.Code, w.Body.String())
	}
	if change := decodeBody[secretChangeResponse](t, w); change.Delivery.State != "delivered" {
		t.Fatalf("delivery %+v", change.Delivery)
	}
	syncs := r.syncs()
	if len(syncs) != 1 || syncs[0].Stack != "h1/web" || string(syncs[0].Secrets["db"]) != "hunter2" {
		t.Fatalf("syncs %+v", syncs)
	}

	w = call(http.MethodGet, "/api/hosts/h1/stacks/web/secrets", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "hunter2") {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
	list := decodeBody[stackSecretsResponse](t, w)
	if len(list.Secrets) != 1 || list.Secrets[0].Name != "db" || list.Secrets[0].Size != 7 || list.Delivery == nil || list.Delivery.State != "delivered" {
		t.Fatalf("list %+v", list)
	}

	if w := call(http.MethodPost, "/api/secrets/lock", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"locked"`) {
		t.Fatalf("lock %d", w.Code)
	}
	if w := call(http.MethodPut, "/api/hosts/h1/stacks/web/secrets/db", `{"value":"x"}`); w.Code != http.StatusLocked {
		t.Fatalf("put locked %d", w.Code)
	}
	if w := call(http.MethodDelete, "/api/hosts/h1/stacks/web/secrets/db", ""); w.Code != http.StatusLocked {
		t.Fatalf("delete locked %d", w.Code)
	}
	if w := call(http.MethodGet, "/api/hosts/h1/stacks/web/secrets", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"db"`) {
		t.Fatalf("list locked %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, "/api/secrets/unlock", `{"passphrase":"wrong passphrase!!"}`); w.Code != http.StatusForbidden {
		t.Fatalf("wrong unlock %d", w.Code)
	}
	if w := call(http.MethodPost, "/api/secrets/unlock", `{"passphrase":"correct horse battery"}`); w.Code != http.StatusOK {
		t.Fatalf("unlock %d", w.Code)
	}
	// Unlock pushes every connected host in the background.
	waitUntil(t, "unlock push", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return reflect.DeepEqual(r.retained["h1"], []string{"web"}) && r.retained["h2"] != nil
	})

	if w := call(http.MethodDelete, "/api/hosts/h1/stacks/web/secrets/db", ""); w.Code != http.StatusOK {
		t.Fatalf("delete %d", w.Code)
	}
	syncs = r.syncs()
	if last := syncs[len(syncs)-1]; last.Stack != "h1/web" || len(last.Secrets) != 0 {
		t.Fatalf("delete pushed %+v", last)
	}
	if w := call(http.MethodDelete, "/api/hosts/h1/stacks/web/secrets/db", ""); w.Code != http.StatusNotFound {
		t.Fatalf("delete missing %d", w.Code)
	}
	for _, path := range []string{"/api/hosts/h1/stacks/..%2Fx/secrets/db", "/api/hosts/h1/stacks/web/secrets/bad%2Fname", "/api/hosts/h1/stacks/Web/secrets/db"} {
		if w := call(http.MethodPut, path, `{"value":"x"}`); w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if w := call(http.MethodPut, "/api/hosts/h1/stacks/web/secrets/db", `{"value":""}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty value %d", w.Code)
	}
}

func TestSecretRoutesRequireLogin(t *testing.T) {
	s, _ := secretServer(t)
	for _, path := range []string{"/api/secrets/status", "/api/hosts/h1/stacks/web/secrets"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
}

func TestSecretDeliveryStates(t *testing.T) {
	s, r := secretServer(t)
	if err := s.secrets.Initialize("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	r.offline["h2"] = true
	w := authenticatedStackRequest(t, s, http.MethodPut, "/api/hosts/h2/stacks/web/secrets/db", `{"value":"pw"}`)
	if change := decodeBody[secretChangeResponse](t, w); change.Delivery.State != "pending" || !strings.Contains(change.Delivery.Error, "offline") {
		t.Fatalf("offline %+v", change.Delivery)
	}
	r.problems = []string{"restart failed"}
	w = authenticatedStackRequest(t, s, http.MethodPut, "/api/hosts/h1/stacks/web/secrets/db", `{"value":"pw"}`)
	if change := decodeBody[secretChangeResponse](t, w); change.Delivery.State != "failed" || !strings.Contains(change.Delivery.Error, "restart failed") {
		t.Fatalf("problems %+v", change.Delivery)
	}
}

func TestHostConnectedPushesAllStacksThenRetains(t *testing.T) {
	s, r := secretServer(t)
	if err := s.secrets.Initialize("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	for _, stack := range []string{"web", "api"} {
		if err := s.secrets.Put("h1", stack, "k", []byte(stack)); err != nil {
			t.Fatal(err)
		}
	}
	s.HostConnected(protocol.HostInfo{HostID: "h1"})
	var stacks []string
	for _, sync := range r.syncs() {
		stacks = append(stacks, sync.Stack)
	}
	if !reflect.DeepEqual(stacks, []string{"h1/api", "h1/web"}) || !reflect.DeepEqual(r.retained["h1"], []string{"api", "web"}) {
		t.Fatalf("pushed %v retained %v", stacks, r.retained)
	}
	// Locked: nothing is sent, and the agent keeps what it has.
	s.secrets.Lock()
	r.retained = nil
	s.HostConnected(protocol.HostInfo{HostID: "h1"})
	if len(r.syncs()) != 2 || r.retained != nil {
		t.Fatal("pushed while locked")
	}
	// A failed stack push must not make the agent forget other stacks.
	if err := s.secrets.Unlock("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	r.problems = []string{"boom"}
	s.HostConnected(protocol.HostInfo{HostID: "h1"})
	if r.retained != nil {
		t.Fatal("retained after a failed push")
	}
}

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
