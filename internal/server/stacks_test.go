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

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/relay"
)

type stackRelay struct {
	saved     protocol.StackSave
	stack     protocol.Stack
	operation *protocol.StackOperation
	err       error
	// revisionRequested is the revision number the last read asked for.
	revisionRequested int
	// volumeRequested is the volume the last detail read asked for.
	volumeRequested string
	// imageRequested is the image the last detail read asked for.
	imageRequested string
	// removed names the volume or image the last removal asked for.
	removed string
	// imagesForced is the force flag of the last image check; updated is
	// "stack/service" of the last update.
	imagesForced bool
	updated      string
	// steps are reported, in order, by each update.
	steps []string
}

func (r *stackRelay) Hosts() []protocol.HostInfo { return nil }
func (r *stackRelay) Containers(context.Context, string) ([]protocol.Container, error) {
	return nil, nil
}
func (r *stackRelay) Volumes(context.Context, string) ([]protocol.Volume, error) { return nil, nil }
func (r *stackRelay) VolumeDetail(_ context.Context, _ string, name string) (protocol.VolumeDetail, error) {
	r.volumeRequested = name
	return protocol.VolumeDetail{Volume: protocol.Volume{Name: name}}, nil
}
func (r *stackRelay) RemoveVolume(_ context.Context, _ string, name string) error {
	r.removed = "volume:" + name
	return r.err
}

func (r *stackRelay) RemoveImage(_ context.Context, _ string, id string) error {
	r.removed = "image:" + id
	return r.err
}

func (r *stackRelay) ImageDetail(_ context.Context, _ string, id string) (protocol.ImageDetail, error) {
	r.imageRequested = id
	return protocol.ImageDetail{Image: protocol.Image{ID: id}}, nil
}
func (r *stackRelay) Images(context.Context, string) ([]protocol.Image, error) { return nil, nil }
func (r *stackRelay) Start(context.Context, string, string) error              { return nil }
func (r *stackRelay) Stop(context.Context, string, string) error               { return nil }
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
func (r *stackRelay) StackRevision(_ context.Context, _ string, _ string, version int) (protocol.Stack, error) {
	r.revisionRequested = version
	return r.stack, r.err
}
func (r *stackRelay) SaveStack(_ context.Context, _ string, save protocol.StackSave) (protocol.StackSaved, error) {
	r.saved = save
	return protocol.StackSaved{Stack: r.stack, Operation: r.operation}, r.err
}
func (r *stackRelay) StackOperation(context.Context, string, string, string) (protocol.StackOperation, error) {
	return protocol.StackOperation{}, r.err
}
func (r *stackRelay) StackImages(_ context.Context, _ string, name string, force bool) (protocol.StackImages, error) {
	r.imagesForced = force
	return protocol.StackImages{Name: name, Services: []protocol.ServiceImage{{Service: "app", Status: protocol.ImageBehind}}}, r.err
}
func (r *stackRelay) UpdateStackService(_ context.Context, _ string, name, service string, onStep func(string)) (protocol.StackOperation, error) {
	r.updated = name + "/" + service
	for _, step := range r.steps {
		onStep(step)
	}
	return protocol.StackOperation{Name: name, Operation: "update", Success: true}, r.err
}
func (r *stackRelay) SyncSecrets(context.Context, string, protocol.SecretSync) (protocol.SecretSyncResult, error) {
	return protocol.SecretSyncResult{}, r.err
}
func (r *stackRelay) RetainSecrets(context.Context, string, []string) error { return r.err }
func (r *stackRelay) SyncRegistries(_ context.Context, _ string, set protocol.EncodedRegistrySync) (protocol.RegistrySynced, error) {
	return protocol.RegistrySynced{SetID: set.SetID}, r.err
}
func (r *stackRelay) TestRegistry(context.Context, string, protocol.RegistryLogin) (protocol.RegistryTestResult, error) {
	return protocol.RegistryTestResult{OK: true}, r.err
}

func authenticatedStackRequest(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	login := httptest.NewRecorder()
	s.sessions.Login(login, httptest.NewRequest(http.MethodPost, "/api/session/login", strings.NewReader(`{"password":"a sufficiently long password"}`)))
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(login.Result().Cookies()[0])
	w := httptest.NewRecorder()
	s.Handler(nil).ServeHTTP(w, r)
	return w
}
func TestStackHTTPVersionContracts(t *testing.T) {
	two := 2
	relay := &stackRelay{stack: protocol.Stack{Name: "web", ComposeYAML: "services: {}", Version: 3}}
	s := New(testSessions, relay, nil)
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

// TestStackHTTPSaveCarriesEnvironmentAndOperation: the web API passes "keep"
// (absent) and "clear" ({}) apart, and answers with the up a save ran.
func TestStackHTTPSaveCarriesEnvironmentAndOperation(t *testing.T) {
	relay := &stackRelay{stack: protocol.Stack{Name: "web", Version: 4, Environment: map[string]string{"MODE": "b"}}}
	s := New(testSessions, relay, nil)
	w := authenticatedStackRequest(t, s, http.MethodPost, "/api/hosts/host/stacks", `{"name":"web","compose_yaml":"services: {}","expected_version":3}`)
	if w.Code != http.StatusOK || relay.saved.Environment != nil {
		t.Fatalf("absent: %d, %#v", w.Code, relay.saved.Environment)
	}
	w = authenticatedStackRequest(t, s, http.MethodPost, "/api/hosts/host/stacks", `{"name":"web","compose_yaml":"services: {}","expected_version":3,"environment":{}}`)
	if w.Code != http.StatusOK || relay.saved.Environment == nil || len(relay.saved.Environment) != 0 {
		t.Fatalf("empty: %d, %#v", w.Code, relay.saved.Environment)
	}
	relay.operation = &protocol.StackOperation{Name: "web", Operation: "up", Success: true, Output: "recreated"}
	w = authenticatedStackRequest(t, s, http.MethodPost, "/api/hosts/host/stacks", `{"name":"web","compose_yaml":"services: {}","expected_version":3,"environment":{"MODE":"b"}}`)
	var saved protocol.StackSaved
	if err := json.NewDecoder(w.Body).Decode(&saved); err != nil || saved.Operation == nil || saved.Operation.Output != "recreated" || saved.Environment["MODE"] != "b" {
		t.Fatalf("answer = %+v, %v", saved, err)
	}
}

// TestStackHTTPRevision: a revision is read by number; anything but a
// positive whole number is refused before the relay is asked.
func TestStackHTTPRevision(t *testing.T) {
	relay := &stackRelay{stack: protocol.Stack{Name: "web", Version: 1, ComposeYAML: "services: {}"}}
	s := New(testSessions, relay, nil)
	w := authenticatedStackRequest(t, s, http.MethodGet, "/api/hosts/host/stacks/web/versions/1", "")
	if w.Code != http.StatusOK || relay.revisionRequested != 1 || !strings.Contains(w.Body.String(), `"compose_yaml":"services: {}"`) {
		t.Fatalf("revision = %d (requested %d): %s", w.Code, relay.revisionRequested, w.Body.String())
	}
	relay.revisionRequested = 0
	for _, bad := range []string{"0", "-1", "one", "1.5"} {
		w = authenticatedStackRequest(t, s, http.MethodGet, "/api/hosts/host/stacks/web/versions/"+bad, "")
		if w.Code != http.StatusBadRequest || relay.revisionRequested != 0 {
			t.Fatalf("version %q: status %d, relay asked for %d", bad, w.Code, relay.revisionRequested)
		}
	}
}

// TestStackImagesHTTP: Check now forces a registry check, a page open does
// not, and an update needs a service.
func TestStackImagesHTTP(t *testing.T) {
	relay := &stackRelay{}
	s := New(testSessions, relay, nil)
	w := authenticatedStackRequest(t, s, http.MethodGet, "/api/hosts/host/stacks/web/images", "")
	if w.Code != http.StatusOK || relay.imagesForced || !strings.Contains(w.Body.String(), `"status":"behind"`) {
		t.Fatalf("images = %d (forced %v): %s", w.Code, relay.imagesForced, w.Body.String())
	}
	w = authenticatedStackRequest(t, s, http.MethodGet, "/api/hosts/host/stacks/web/images?force=1", "")
	if w.Code != http.StatusOK || !relay.imagesForced {
		t.Fatalf("forced images = %d (forced %v)", w.Code, relay.imagesForced)
	}
	w = authenticatedStackRequest(t, s, http.MethodPost, "/api/hosts/host/stacks/web/update", `{"service":"app"}`)
	if w.Code != http.StatusOK || relay.updated != "web/app" || !strings.Contains(w.Body.String(), `"operation":"update"`) {
		t.Fatalf("update = %d (updated %q): %s", w.Code, relay.updated, w.Body.String())
	}
	relay.updated = ""
	for _, body := range []string{`{}`, `not json`} {
		w = authenticatedStackRequest(t, s, http.MethodPost, "/api/hosts/host/stacks/web/update", body)
		if w.Code != http.StatusBadRequest || relay.updated != "" {
			t.Fatalf("update with %s = %d (updated %q)", body, w.Code, relay.updated)
		}
	}
	w = authenticatedStackRequest(t, s, http.MethodPost, "/api/hosts/host/stacks/web/images", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("POST images = %d, want 404", w.Code)
	}
}

// An update that reports steps answers with server-sent events: each step,
// then the result, or the failure with the status it would have had.
func TestStackUpdateStreamsItsSteps(t *testing.T) {
	relay := &stackRelay{steps: []string{protocol.UpdatePulling, protocol.UpdateRecreating}}
	s := New(testSessions, relay, nil)
	w := authenticatedStackRequest(t, s, http.MethodPost, "/api/hosts/host/stacks/web/update", `{"service":"app"}`)
	want := "event: step\ndata: {\"step\":\"pulling\"}\n\n" +
		"event: step\ndata: {\"step\":\"recreating\"}\n\n" +
		"event: result\ndata: {\"name\":\"web\",\"operation\":\"update\",\"success\":true"
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/event-stream" || !strings.HasPrefix(w.Body.String(), want) {
		t.Fatalf("update = %d %q:\n%s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	if !w.Flushed {
		t.Fatal("events were not flushed")
	}

	relay.err = fmt.Errorf("%w: lost", relayDisconnected())
	w = authenticatedStackRequest(t, s, http.MethodPost, "/api/hosts/host/stacks/web/update", `{"service":"app"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "event: error\ndata: {\"message\":") ||
		!strings.Contains(w.Body.String(), `"status":503}`) || strings.Contains(w.Body.String(), "event: result") {
		t.Fatalf("failed update = %d:\n%s", w.Code, w.Body.String())
	}

	// Before any step the failure is an ordinary HTTP error.
	relay.steps = nil
	w = authenticatedStackRequest(t, s, http.MethodPost, "/api/hosts/host/stacks/web/update", `{"service":"app"}`)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Content-Type") == "text/event-stream" {
		t.Fatalf("update failing at once = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
}

func TestVolumeDetailHTTP(t *testing.T) {
	relay := &stackRelay{}
	s := New(testSessions, relay, nil)
	w := authenticatedStackRequest(t, s, http.MethodGet, "/api/hosts/host/volumes/app-data", "")
	if w.Code != http.StatusOK || relay.volumeRequested != "app-data" || !strings.Contains(w.Body.String(), `"name":"app-data"`) {
		t.Fatalf("volume = %d (requested %q): %s", w.Code, relay.volumeRequested, w.Body.String())
	}
	w = authenticatedStackRequest(t, s, http.MethodGet, "/api/hosts/host/volumes/app-data/extra", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("nested path status = %d", w.Code)
	}
}

func TestRemoveHTTP(t *testing.T) {
	relay := &stackRelay{}
	s := New(testSessions, relay, nil)
	w := authenticatedStackRequest(t, s, http.MethodDelete, "/api/hosts/host/volumes/app-data", "")
	if w.Code != http.StatusOK || relay.removed != "volume:app-data" || !strings.Contains(w.Body.String(), `"id":"app-data"`) {
		t.Fatalf("volume removal = %d (removed %q): %s", w.Code, relay.removed, w.Body.String())
	}
	w = authenticatedStackRequest(t, s, http.MethodDelete, "/api/hosts/host/images/abcdef012345", "")
	if w.Code != http.StatusOK || relay.removed != "image:abcdef012345" {
		t.Fatalf("image removal = %d (removed %q)", w.Code, relay.removed)
	}
}

// A refused removal, such as a volume a container uses, is a conflict.
func TestRemoveRefusedHTTPConflict(t *testing.T) {
	relay := &stackRelay{err: &relay.RemoteError{Code: protocol.CodeOperationFailed, Message: "volume is being used"}}
	s := New(testSessions, relay, nil)
	for _, path := range []string{"/api/hosts/host/volumes/app-data", "/api/hosts/host/images/abcdef012345"} {
		w := authenticatedStackRequest(t, s, http.MethodDelete, path, "")
		if w.Code != http.StatusConflict {
			t.Fatalf("DELETE %s status = %d, want %d", path, w.Code, http.StatusConflict)
		}
	}
}

func TestStackHTTPConflict(t *testing.T) {
	stacks := &stackRelay{err: &relay.RemoteError{Code: protocol.CodeOperationFailed, Message: "stack version mismatch"}}
	s := New(testSessions, stacks, nil)
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
			s := New(testSessions, &stackRelay{err: c.err}, nil)
			w := authenticatedStackRequest(t, s, c.method, c.path, c.body)
			if w.Code != c.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, c.want, w.Body.String())
			}
		})
	}
}

// relayDisconnected is relay.ErrDisconnected, for tests whose fake shadows
// the package name.
func relayDisconnected() error { return relay.ErrDisconnected }
