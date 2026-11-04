package relay

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

func TestAdmitRefusalKeepsHostOut(t *testing.T) {
	h := newHarness(t, time.Minute)
	refuse := func() error { return errors.New("not enrolled") }
	h.admit.Store(&refuse)
	conn := h.dial(t, "local-b")
	agentDone := make(chan error, 1)
	go func() { agentDone <- testAgent(&fakeHost{}).Serve(context.Background(), conn) }()
	if err := receive(t, "server refusal", h.served); err == nil {
		t.Fatal("refused agent was served")
	}
	receive(t, "agent end", agentDone)
	if len(h.remote.Hosts()) != 0 {
		t.Fatal("refused agent was registered")
	}
}

func TestDisconnectClosesSessionAndFailsRequests(t *testing.T) {
	h := newHarness(t, time.Minute)
	started := make(chan struct{}, 1)
	host := &fakeHost{volumes: func(ctx context.Context) ([]protocol.Volume, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	h.connect(t, testAgent(host), "local-b")
	result := make(chan error, 1)
	go func() {
		_, err := h.remote.Volumes(context.Background(), "local-b")
		result <- err
	}()
	receive(t, "operation start", started)
	h.remote.Disconnect("local-b", errors.New("host removed"))
	if err := receive(t, "pending request", result); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("pending request = %v", err)
	}
	if len(h.remote.Hosts()) != 0 {
		t.Fatal("disconnected host still listed")
	}
	h.remote.Disconnect("nobody", errors.New("no session")) // no-op
}

// TestRevocationCannotBeOvertaken revokes admission while a connection is
// between its early check and registration: the session must not survive.
func TestRevocationCannotBeOvertaken(t *testing.T) {
	h := newHarness(t, time.Minute)
	var revoked atomic.Bool
	admit := func() error {
		if revoked.Load() {
			return errors.New("revoked")
		}
		return nil
	}
	h.admit.Store(&admit)
	for range 50 {
		revoked.Store(false)
		conn := h.dial(t, "local-b")
		ctx, cancel := context.WithCancel(context.Background())
		agentDone := make(chan error, 1)
		go func() { agentDone <- testAgent(&fakeHost{}).Serve(ctx, conn) }()
		// Revoke at an arbitrary point of the setup, the way the
		// enrollment service does: state first, then Disconnect.
		revoked.Store(true)
		h.remote.Disconnect("local-b", errors.New("revoked"))
		receive(t, "server end", h.served)
		if len(h.remote.Hosts()) != 0 {
			t.Fatal("a session registered after its admission was revoked")
		}
		cancel()
		receive(t, "agent end", agentDone)
	}
}

func TestRequestReconnectDrainsThenEnds(t *testing.T) {
	h := newHarness(t, time.Minute)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	host := &fakeHost{volumes: func(ctx context.Context) ([]protocol.Volume, error) {
		started <- struct{}{}
		<-release
		return []protocol.Volume{{Name: "kept"}}, nil
	}}
	agent := testAgent(host)
	running := h.connect(t, agent, "local-b")
	slow := make(chan error, 1)
	go func() {
		_, err := h.remote.Volumes(context.Background(), "local-b")
		slow <- err
	}()
	receive(t, "operation start", started)
	agent.RequestReconnect()
	// New requests are refused while draining; the running one finishes.
	waitFor(t, "draining", func() bool {
		_, err := h.remote.Containers(context.Background(), "local-b")
		var remote *RemoteError
		return errors.As(err, &remote) && remote.Code == protocol.CodeBusy
	})
	close(release)
	if err := receive(t, "running request", slow); err != nil {
		t.Fatalf("in-flight request during drain: %v", err)
	}
	select {
	case <-running.done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end after draining")
	}
	if !errors.Is(running.err, ErrReconnectRequested) {
		t.Fatalf("Serve = %v", running.err)
	}
}

func TestDrainTimeoutEndsStuckSession(t *testing.T) {
	h := newHarness(t, time.Minute)
	started := make(chan struct{}, 1)
	host := &fakeHost{volumes: func(ctx context.Context) ([]protocol.Volume, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	agent := testAgent(host)
	agent.DrainTimeout = 20 * time.Millisecond
	running := h.connect(t, agent, "local-b")
	go func() { _, _ = h.remote.Volumes(context.Background(), "local-b") }()
	receive(t, "operation start", started)
	agent.RequestReconnect()
	select {
	case <-running.done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not time out")
	}
	if !errors.Is(running.err, ErrReconnectRequested) {
		t.Fatalf("Serve = %v", running.err)
	}
}

func TestRunAgentRedialsAtOnceAfterReconnectRequest(t *testing.T) {
	h := newHarness(t, time.Minute)
	agent := testAgent(&fakeHost{})
	waits := make(chan time.Duration, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunAgent(ctx, agent, ReconnectOptions{
			MinBackoff: time.Hour,
			Dial: func(ctx context.Context) (*websocket.Conn, error) {
				conn, _, err := websocket.DefaultDialer.DialContext(ctx, h.url+"?host=local-b", nil)
				return conn, err
			},
			OnDisconnect: func(_ error, wait time.Duration) { waits <- wait },
		})
	}()
	waitFor(t, "first session", func() bool { return h.sessionFor("local-b") != nil })
	first := h.sessionFor("local-b")
	agent.RequestReconnect()
	// With an hour of backoff, only an immediate redial gets here in time.
	waitFor(t, "second session", func() bool {
		s := h.sessionFor("local-b")
		return s != nil && s != first
	})
	if wait := receive(t, "reconnect wait", waits); wait != 0 {
		t.Fatalf("waited %s before redialling", wait)
	}
	cancel()
	receive(t, "RunAgent exit", done)
}
