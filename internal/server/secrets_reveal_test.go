package server

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	rightPassphrase = `{"passphrase":"correct horse battery"}`
	wrongPassphrase = `{"passphrase":"not the passphrase"}`
	revealPath      = "/api/hosts/h1/stacks/web/secrets/db/reveal"
)

// captureLog collects the standard logger's output for the test's duration.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous); log.SetFlags(flags) })
	return &buf
}

func revealServer(t *testing.T) *Server {
	t.Helper()
	s, _ := secretServer(t)
	if err := s.secrets.Initialize("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if err := s.secrets.Put("h1", "web", "db", []byte("hunter2-value")); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRevealAnswersTheValueWithThePassphrase(t *testing.T) {
	s := revealServer(t)
	logged := captureLog(t)
	w := authenticatedStackRequest(t, s, http.MethodPost, revealPath, rightPassphrase)
	if w.Code != http.StatusOK {
		t.Fatalf("reveal %d %s", w.Code, w.Body.String())
	}
	if got := decodeBody[revealResponse](t, w); got.Value != "hunter2-value" {
		t.Fatalf("value %q", got.Value)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control %q, want no-store", cc)
	}
	if !strings.Contains(logged.String(), "revealed h1/web/db") {
		t.Fatalf("log does not name the secret: %q", logged.String())
	}
	if strings.Contains(logged.String(), "hunter2") || strings.Contains(logged.String(), "correct horse") {
		t.Fatalf("log holds the value or passphrase: %q", logged.String())
	}
}

func TestRevealRefusals(t *testing.T) {
	s := revealServer(t)
	logged := captureLog(t)
	w := authenticatedStackRequest(t, s, http.MethodPost, revealPath, wrongPassphrase)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "hunter2") {
		t.Fatalf("wrong passphrase %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(logged.String(), "reveal of h1/web/db refused") || strings.Contains(logged.String(), "not the passphrase") {
		t.Fatalf("refusal log %q", logged.String())
	}
	if w := authenticatedStackRequest(t, s, http.MethodPost, "/api/hosts/h1/stacks/web/secrets/absent/reveal", rightPassphrase); w.Code != http.StatusNotFound {
		t.Fatalf("missing secret %d", w.Code)
	}
	if w := authenticatedStackRequest(t, s, http.MethodPost, revealPath, `not json`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad body %d", w.Code)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, revealPath},
		{http.MethodPut, revealPath},
		{http.MethodPost, "/api/hosts/h1/stacks/web/secrets/db/other"},
	} {
		if w := authenticatedStackRequest(t, s, c.method, c.path, rightPassphrase); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "hunter2") {
			t.Fatalf("%s %s: %d", c.method, c.path, w.Code)
		}
	}
	// No session, no value, whatever the passphrase.
	anonymous := httptest.NewRecorder()
	s.Handler(nil).ServeHTTP(anonymous, httptest.NewRequest(http.MethodPost, revealPath, strings.NewReader(rightPassphrase)))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("without a session %d", anonymous.Code)
	}
	s.secrets.Lock()
	if w := authenticatedStackRequest(t, s, http.MethodPost, revealPath, rightPassphrase); w.Code != http.StatusLocked {
		t.Fatalf("locked %d %s", w.Code, w.Body.String())
	}
}

func TestWrongPassphrasesAreLimitedAcrossRevealAndUnlock(t *testing.T) {
	s := revealServer(t)
	captureLog(t)
	// Right passphrases cost nothing.
	for i := 0; i < 2*loginBurst; i++ {
		if w := authenticatedStackRequest(t, s, http.MethodPost, revealPath, rightPassphrase); w.Code != http.StatusOK {
			t.Fatalf("right reveal %d: %d", i, w.Code)
		}
	}
	for i := 0; i < loginBurst; i++ {
		path := revealPath
		if i%2 == 1 {
			path = "/api/secrets/unlock"
		}
		if w := authenticatedStackRequest(t, s, http.MethodPost, path, wrongPassphrase); w.Code != http.StatusForbidden {
			t.Fatalf("wrong attempt %d at %s: %d", i, path, w.Code)
		}
	}
	for _, path := range []string{revealPath, "/api/secrets/unlock"} {
		w := authenticatedStackRequest(t, s, http.MethodPost, path, rightPassphrase)
		if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || strings.Contains(w.Body.String(), "hunter2") {
			t.Fatalf("%s over the limit: %d %q", path, w.Code, w.Body.String())
		}
	}
}
