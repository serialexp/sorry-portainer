package imagecheck

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

func TestFlushDropsCachedAnswers(t *testing.T) {
	fake := newFake()
	ref := "docker.io/a/b:latest"
	fake.digests[ref] = "sha256:one"
	checker := New(fake, Options{})
	checker.Latest(context.Background(), []string{ref}, false)
	checker.Latest(context.Background(), []string{ref}, false)
	if fake.headCount(ref) != 1 {
		t.Fatalf("cache not used: %d requests", fake.headCount(ref))
	}
	checker.Flush()
	checker.Latest(context.Background(), []string{ref}, false)
	if fake.headCount(ref) != 2 {
		t.Fatalf("Flush kept the answer: %d requests", fake.headCount(ref))
	}
}

// loginRegistry answers each Head by the logins in force when the request
// started, and holds request n until gates[n] is closed.
type loginRegistry struct {
	mu       sync.Mutex
	loggedIn bool
	started  int
	gates    []chan struct{}
	running  atomic.Int32
}

func (r *loginRegistry) Head(ctx context.Context, ref string) (string, error) {
	r.mu.Lock()
	loggedIn, gate := r.loggedIn, r.gates[r.started]
	r.started++
	r.mu.Unlock()
	r.running.Add(1)
	<-gate
	if !loggedIn {
		return "", &transport.Error{StatusCode: http.StatusUnauthorized}
	}
	return "sha256:new", nil
}

func (r *loginRegistry) Created(context.Context, string, string) (time.Time, error) {
	return time.Time{}, errors.New("unused")
}

// TestARequestFromBeforeAFlushCachesNothing covers logins changing while a
// check is in flight: the old request's denial, arriving last, must not be
// cached, and a check asked for after the change must not join it.
func TestARequestFromBeforeAFlushCachesNothing(t *testing.T) {
	ref := "ghcr.io/myorg/app:1"
	registry := &loginRegistry{gates: []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}}
	checker := New(registry, Options{})
	wait := func(n int32) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for registry.running.Load() < n {
			if time.Now().After(deadline) {
				t.Fatalf("only %d requests running, want %d", registry.running.Load(), n)
			}
			time.Sleep(time.Millisecond)
		}
	}

	before := make(chan Remote, 1)
	go func() { before <- checker.Latest(context.Background(), []string{ref}, false)[ref] }()
	wait(1)
	registry.mu.Lock()
	registry.loggedIn = true
	registry.mu.Unlock()
	checker.Flush()
	after := make(chan Remote, 1)
	go func() { after <- checker.Latest(context.Background(), []string{ref}, false)[ref] }()
	wait(2) // the second check made its own request

	close(registry.gates[1])
	if got := <-after; got.Digest != "sha256:new" {
		t.Fatalf("new request answered %+v", got)
	}
	close(registry.gates[0])
	if got := <-before; got.Failure != Denied {
		t.Fatalf("old request answered %+v", got)
	}
	got := checker.Latest(context.Background(), []string{ref}, false)[ref]
	if got.Failure != "" || got.Digest != "sha256:new" {
		t.Fatalf("cached %+v", got)
	}
	close(registry.gates[2])
}

func TestAFailureKeepsNoRegistryText(t *testing.T) {
	fake := newFake()
	ref := "ghcr.io/myorg/app:1"
	fake.errs[ref] = &transport.Error{StatusCode: http.StatusForbidden, Errors: []transport.Diagnostic{{Code: transport.DeniedErrorCode, Message: "token tok-secret has no pull scope"}}}
	got := New(fake, Options{}).Latest(context.Background(), []string{ref}, false)[ref]
	if strings.Contains(got.Err, "tok-secret") || got.Err != "the registry answered 403 Forbidden" || got.Failure != Denied {
		t.Fatalf("got %+v", got)
	}
}

type countingTransport struct{ calls int }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls++
	return nil, errors.New("sent")
}

func TestHTTPSOnlyRefusesPlainHTTP(t *testing.T) {
	next := &countingTransport{}
	guarded := HTTPSOnly(next)
	plain := &http.Request{URL: &url.URL{Scheme: "http", Host: "10.0.0.2:5000", Path: "/v2/"}, Header: http.Header{}}
	if _, err := guarded.RoundTrip(plain); err == nil || next.calls != 0 {
		t.Fatalf("plain HTTP: err %v, sent %d", err, next.calls)
	}
	secure := &http.Request{URL: &url.URL{Scheme: "https", Host: "ghcr.io", Path: "/v2/"}, Header: http.Header{}}
	if _, err := guarded.RoundTrip(secure); err == nil || err.Error() != "sent" || next.calls != 1 {
		t.Fatalf("HTTPS: err %v, sent %d", err, next.calls)
	}
}
