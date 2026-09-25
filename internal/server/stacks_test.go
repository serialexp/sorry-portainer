package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

type stackRelay struct {
	saved protocol.StackSave
	stack protocol.Stack
	err   error
}

func (r *stackRelay) Hosts() []protocol.HostInfo                          { return nil }
func (r *stackRelay) Containers(string) ([]protocol.Container, error)     { return nil, nil }
func (r *stackRelay) Volumes(string) ([]protocol.Volume, error)           { return nil, nil }
func (r *stackRelay) Images(string) ([]protocol.Image, error)             { return nil, nil }
func (r *stackRelay) Start(string, string) error                          { return nil }
func (r *stackRelay) Stop(string, string) error                           { return nil }
func (r *stackRelay) Info(string) (protocol.HostInfo, error)              { return protocol.HostInfo{}, nil }
func (r *stackRelay) ListStacks(string) ([]protocol.Stack, error)         { return nil, nil }
func (r *stackRelay) InspectStack(string, string) (protocol.Stack, error) { return r.stack, r.err }
func (r *stackRelay) StackVersions(string, string) ([]protocol.StackVersion, error) {
	return []protocol.StackVersion{{Version: 1}, {Version: 2}}, r.err
}
func (r *stackRelay) SaveStack(_ string, save protocol.StackSave) (protocol.Stack, error) {
	r.saved = save
	return r.stack, r.err
}
func (r *stackRelay) StackOperation(string, string, string) (protocol.StackOperation, error) {
	return protocol.StackOperation{}, nil
}

func authenticatedStackRequest(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	login := httptest.NewRecorder()
	s.sessions.Login(login, httptest.NewRequest(http.MethodPost, "/api/session/login", strings.NewReader(`{"password":"a sufficiently long password"}`)))
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(login.Result().Cookies()[0])
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func TestStackHTTPVersionContracts(t *testing.T) {
	two := 2
	relay := &stackRelay{stack: protocol.Stack{Name: "web", ComposeYAML: "services: {}", Version: 3}}
	s := New("a sufficiently long password", time.Hour, relay)
	w := authenticatedStackRequest(t, s, http.MethodPost, "/api/hosts/host/stacks", `{"name":"web","compose_yaml":"services: {}","expected_version":2}`)
	if w.Code != http.StatusOK {
		t.Fatalf("POST status = %d: %s", w.Code, w.Body.String())
	}
	if relay.saved.ExpectedVersion == nil || *relay.saved.ExpectedVersion != two {
		t.Fatalf("expected version = %#v", relay.saved.ExpectedVersion)
	}
	var saved protocol.Stack
	if err := json.NewDecoder(w.Body).Decode(&saved); err != nil || saved.Version != 3 {
		t.Fatalf("save response = %#v, %v", saved, err)
	}
	w = authenticatedStackRequest(t, s, http.MethodGet, "/api/hosts/host/stacks/web", "")
	if w.Code != http.StatusOK {
		t.Fatalf("detail status = %d", w.Code)
	}
	w = authenticatedStackRequest(t, s, http.MethodGet, "/api/hosts/host/stacks/web/versions", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"version":2`) {
		t.Fatalf("versions = %d: %s", w.Code, w.Body.String())
	}
}
func TestStackHTTPConflict(t *testing.T) {
	relay := &stackRelay{err: errors.New("stack version mismatch")}
	s := New("a sufficiently long password", time.Hour, relay)
	w := authenticatedStackRequest(t, s, http.MethodPost, "/api/hosts/host/stacks", `{"name":"web","compose_yaml":"services: {}","expected_version":1}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("POST status = %d", w.Code)
	}
}
