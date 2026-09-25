package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/relay"
)

type stackRelay struct {
	saved protocol.StackSave
	stack protocol.Stack
	err   error
}

func (r *stackRelay) Hosts() []protocol.HostInfo { return nil }
func (r *stackRelay) Containers(context.Context, string) ([]protocol.Container, error) {
	return nil, nil
}
func (r *stackRelay) Volumes(context.Context, string) ([]protocol.Volume, error) { return nil, nil }
func (r *stackRelay) Images(context.Context, string) ([]protocol.Image, error)   { return nil, nil }
func (r *stackRelay) Start(context.Context, string, string) error                { return nil }
func (r *stackRelay) Stop(context.Context, string, string) error                 { return nil }
func (r *stackRelay) Info(context.Context, string) (protocol.HostInfo, error) {
	return protocol.HostInfo{}, nil
}
func (r *stackRelay) ListStacks(context.Context, string) ([]protocol.Stack, error) { return nil, nil }
func (r *stackRelay) InspectStack(context.Context, string, string) (protocol.Stack, error) {
	return r.stack, r.err
}
func (r *stackRelay) StackVersions(context.Context, string, string) ([]protocol.StackVersion, error) {
	return []protocol.StackVersion{{Version: 1}, {Version: 2}}, r.err
}
func (r *stackRelay) SaveStack(_ context.Context, _ string, save protocol.StackSave) (protocol.Stack, error) {
	r.saved = save
	return r.stack, r.err
}
func (r *stackRelay) StackOperation(context.Context, string, string, string) (protocol.StackOperation, error) {
	return protocol.StackOperation{}, r.err
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
	stacks := &stackRelay{err: &relay.RemoteError{Code: protocol.CodeOperationFailed, Message: "stack version mismatch"}}
	s := New("a sufficiently long password", time.Hour, stacks)
	w := authenticatedStackRequest(t, s, http.MethodPost, "/api/hosts/host/stacks", `{"name":"web","compose_yaml":"services: {}","expected_version":1}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("POST status = %d", w.Code)
	}
}

// Transport failures keep their own status whatever the route, so a save that
// never reached the host is not reported as a version conflict.
func TestRelayFailureStatuses(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		method string
		path   string
		body   string
		want   int
	}{
		{"remote save failure", &relay.RemoteError{Message: "mismatch"}, http.MethodPost, "/api/hosts/host/stacks", `{"name":"web"}`, http.StatusConflict},
		{"remote inspect failure", &relay.RemoteError{Message: "missing"}, http.MethodGet, "/api/hosts/host/stacks/web", "", http.StatusNotFound},
		{"remote operation failure", &relay.RemoteError{Message: "compose failed"}, http.MethodPost, "/api/hosts/host/stacks/web/up", "", http.StatusBadGateway},
		{"host unavailable", relay.ErrHostUnavailable, http.MethodPost, "/api/hosts/host/stacks", `{"name":"web"}`, http.StatusServiceUnavailable},
		{"host busy", relay.ErrHostBusy, http.MethodGet, "/api/hosts/host/stacks/web", "", http.StatusServiceUnavailable},
		{"agent busy", &relay.RemoteError{Code: protocol.CodeBusy, Message: "busy"}, http.MethodPost, "/api/hosts/host/stacks", `{"name":"web"}`, http.StatusServiceUnavailable},
		{"disconnected", fmt.Errorf("%w: reset", relay.ErrDisconnected), http.MethodPost, "/api/hosts/host/stacks", `{"name":"web"}`, http.StatusServiceUnavailable},
		{"timeout", fmt.Errorf("%w: stacks.up", relay.ErrTimeout), http.MethodPost, "/api/hosts/host/stacks/web/up", "", http.StatusGatewayTimeout},
		{"client gone", context.Canceled, http.MethodGet, "/api/hosts/host/stacks/web/versions", "", http.StatusServiceUnavailable},
		{"unclassified", errors.New("boom"), http.MethodGet, "/api/hosts/host/stacks/web", "", http.StatusBadGateway},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New("a sufficiently long password", time.Hour, &stackRelay{err: c.err})
			w := authenticatedStackRequest(t, s, c.method, c.path, c.body)
			if w.Code != c.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, c.want, w.Body.String())
			}
		})
	}
}
