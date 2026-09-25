package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// fakeHost is a Handler whose operations tests can replace.
type fakeHost struct {
	hostname   string
	containers func(context.Context) ([]protocol.Container, error)
	volumes    func(context.Context) ([]protocol.Volume, error)
	images     func(context.Context) ([]protocol.Image, error)
}

func (h *fakeHost) Info(context.Context) (protocol.HostInfo, error) {
	return protocol.HostInfo{Hostname: h.hostname, EngineVersion: "test"}, nil
}

func (h *fakeHost) Containers(ctx context.Context) ([]protocol.Container, error) {
	if h.containers != nil {
		return h.containers(ctx)
	}
	return []protocol.Container{{ID: "local-b-worker-1", Name: "local-b-worker-1", State: "created"}}, nil
}

func (h *fakeHost) Volumes(ctx context.Context) ([]protocol.Volume, error) {
	if h.volumes != nil {
		return h.volumes(ctx)
	}
	return []protocol.Volume{{Name: "local-b-data"}}, nil
}

func (h *fakeHost) Images(ctx context.Context) ([]protocol.Image, error) {
	if h.images != nil {
		return h.images(ctx)
	}
	return []protocol.Image{{ID: "sha256:test"}}, nil
}

func (h *fakeHost) Start(context.Context, string) error { return nil }
func (h *fakeHost) Stop(context.Context, string) error  { return nil }

// harness serves a Remote over a real WebSocket. The ?host= query parameter
// stands in for the host identity the mTLS certificate would carry.
type harness struct {
	remote *Remote
	url    string
	served chan error
}

func newHarness(t testing.TB, heartbeat time.Duration) *harness {
	t.Helper()
	h := &harness{remote: NewRemote(RemoteOptions{Heartbeat: heartbeat}), served: make(chan error, 16)}
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		h.served <- h.remote.Serve(conn, r.URL.Query().Get("host"))
	}))
	t.Cleanup(server.Close)
	h.url = "ws" + strings.TrimPrefix(server.URL, "http")
	return h
}

func (h *harness) dial(t testing.TB, identity string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(h.url+"?host="+identity, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// runningAgent is an Agent.Serve running in the background.
type runningAgent struct {
	cancel context.CancelFunc
	done   chan struct{} // closed once err holds Serve's result
	err    error
}

// stop cancels the agent and returns Serve's result once it has returned.
func (a *runningAgent) stop(t testing.TB) error {
	t.Helper()
	a.cancel()
	select {
	case <-a.done:
		return a.err
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not stop")
		return nil
	}
}

// connect runs agent against the harness under the given certificate identity
// and waits until the server has registered it.
func (h *harness) connect(t testing.TB, agent *Agent, identity string) *runningAgent {
	t.Helper()
	before := h.sessionFor(identity)
	conn := h.dial(t, identity)
	ctx, cancel := context.WithCancel(context.Background())
	running := &runningAgent{cancel: cancel, done: make(chan struct{})}
	go func() {
		running.err = agent.Serve(ctx, conn)
		close(running.done)
	}()
	t.Cleanup(func() {
		cancel()
		<-running.done
	})
	waitFor(t, "agent registration", func() bool {
		s := h.sessionFor(identity)
		return s != nil && s != before
	})
	return running
}

func (h *harness) sessionFor(hostID string) *session {
	h.remote.mu.RLock()
	defer h.remote.mu.RUnlock()
	return h.remote.sessions[hostID]
}

func (h *harness) pending(hostID string) int {
	s := h.sessionFor(hostID)
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

func waitFor(t testing.TB, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func receive[T any](t testing.TB, what string, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func testAgent(host *fakeHost) *Agent {
	return &Agent{HostID: "local-b", Prefix: "local-b-", Handler: host}
}

func TestRoundTrip(t *testing.T) {
	h := newHarness(t, time.Minute)
	deadlines := make(chan time.Duration, 1)
	host := &fakeHost{hostname: "fake", containers: func(ctx context.Context) ([]protocol.Container, error) {
		deadline, _ := ctx.Deadline()
		deadlines <- time.Until(deadline)
		return []protocol.Container{{ID: "local-b-worker-1"}}, nil
	}}
	h.connect(t, testAgent(host), "local-b")
	ctx := context.Background()

	hosts := h.remote.Hosts()
	if len(hosts) != 1 || hosts[0].HostID != "local-b" || hosts[0].Prefix != "local-b-" || hosts[0].Hostname != "fake" {
		t.Fatalf("hosts = %#v", hosts)
	}
	containers, err := h.remote.Containers(ctx, "local-b")
	if err != nil || len(containers) != 1 || containers[0].ID != "local-b-worker-1" {
		t.Fatalf("containers = %#v, %v", containers, err)
	}
	// The agent's operation deadline comes from the server's time budget.
	if remaining := <-deadlines; remaining <= DefaultRequestTimeout-2*time.Second || remaining > DefaultRequestTimeout {
		t.Fatalf("agent deadline %s, want just under %s", remaining, DefaultRequestTimeout)
	}
	if volumes, err := h.remote.Volumes(ctx, "local-b"); err != nil || len(volumes) != 1 {
		t.Fatalf("volumes = %#v, %v", volumes, err)
	}
	if images, err := h.remote.Images(ctx, "local-b"); err != nil || len(images) != 1 {
		t.Fatalf("images = %#v, %v", images, err)
	}
	if err := h.remote.Start(ctx, "local-b", "local-b-worker-1"); err != nil {
		t.Fatal(err)
	}
	if err := h.remote.Stop(ctx, "local-b", "local-b-worker-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.remote.Containers(ctx, "nobody"); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("unknown host error = %v", err)
	}
}

func TestSlowRequestDoesNotBlockOthers(t *testing.T) {
	h := newHarness(t, time.Minute)
	release := make(chan struct{})
	host := &fakeHost{volumes: func(ctx context.Context) ([]protocol.Volume, error) {
		<-release
		return []protocol.Volume{{Name: "slow"}}, nil
	}}
	h.connect(t, testAgent(host), "local-b")

	slow := make(chan error, 1)
	go func() {
		_, err := h.remote.Volumes(context.Background(), "local-b")
		slow <- err
	}()
	waitFor(t, "slow request to be pending", func() bool { return h.pending("local-b") == 1 })
	if _, err := h.remote.Containers(context.Background(), "local-b"); err != nil {
		t.Fatalf("fast request behind a slow one: %v", err)
	}
	select {
	case err := <-slow:
		t.Fatalf("slow request finished early: %v", err)
	default:
	}
	close(release)
	if err := receive(t, "slow request", slow); err != nil {
		t.Fatal(err)
	}
}

func TestCancelReachesAgent(t *testing.T) {
	h := newHarness(t, time.Minute)
	started := make(chan struct{}, 1)
	stopped := make(chan error, 1)
	host := &fakeHost{volumes: func(ctx context.Context) ([]protocol.Volume, error) {
		started <- struct{}{}
		<-ctx.Done()
		stopped <- ctx.Err()
		return nil, ctx.Err()
	}}
	h.connect(t, testAgent(host), "local-b")

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := h.remote.Volumes(ctx, "local-b")
		result <- err
	}()
	receive(t, "operation start", started)
	cancel()
	if err := receive(t, "caller result", result); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller error = %v", err)
	}
	// The agent's own deadline is ten seconds away, so only the cancel message
	// can end the operation this quickly.
	if err := receive(t, "agent cancellation", stopped); !errors.Is(err, context.Canceled) {
		t.Fatalf("agent operation ended with %v", err)
	}
	if h.pending("local-b") != 0 {
		t.Fatal("cancelled request is still pending")
	}
}

func TestLateResponseIsNotDeliveredToTheNextRequest(t *testing.T) {
	h := newHarness(t, time.Minute)
	finish := make(chan struct{})
	finished := make(chan struct{})
	host := &fakeHost{images: func(context.Context) ([]protocol.Image, error) {
		// Ignores cancellation, so its answer arrives after the caller gave up.
		<-finish
		defer close(finished)
		return []protocol.Image{{ID: "sha256:late"}}, nil
	}}
	h.connect(t, testAgent(host), "local-b")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := h.remote.Images(ctx, "local-b"); !errors.Is(err, ErrTimeout) {
		t.Fatalf("timed out request error = %v", err)
	}
	close(finish)
	<-finished
	for range 3 {
		containers, err := h.remote.Containers(context.Background(), "local-b")
		if err != nil || len(containers) != 1 || containers[0].ID != "local-b-worker-1" {
			t.Fatalf("request after a late response = %#v, %v", containers, err)
		}
	}
}

func TestDisconnectFailsPendingRequestsAndRemovesHost(t *testing.T) {
	h := newHarness(t, time.Minute)
	host := &fakeHost{volumes: func(ctx context.Context) ([]protocol.Volume, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	agent := h.connect(t, testAgent(host), "local-b")

	result := make(chan error, 1)
	go func() {
		_, err := h.remote.Volumes(context.Background(), "local-b")
		result <- err
	}()
	waitFor(t, "request to be pending", func() bool { return h.pending("local-b") == 1 })
	if err := agent.stop(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("agent Serve = %v", err)
	}
	if err := receive(t, "pending request", result); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("pending request error = %v", err)
	}
	receive(t, "server session end", h.served)
	if hosts := h.remote.Hosts(); len(hosts) != 0 {
		t.Fatalf("hosts after disconnect = %#v", hosts)
	}
	if _, err := h.remote.Containers(context.Background(), "local-b"); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("request after disconnect = %v", err)
	}
}

// helloFrame is a valid agent hello for local-b.
func helloFrame(t *testing.T) []byte {
	t.Helper()
	data, err := protocol.Encode(protocol.Message{ID: "hello", Type: protocol.TypeHello, HostID: "local-b",
		Payload: []byte(`{"host_id":"local-b","prefix":"local-b-"}`)})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestServerDropsSilentAgent(t *testing.T) {
	const heartbeat = 30 * time.Millisecond
	h := newHarness(t, heartbeat)
	// A raw client that says hello and then never reads, so it never answers pings.
	conn := h.dial(t, "local-b")
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, helloFrame(t)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "registration", func() bool { return h.sessionFor("local-b") != nil })
	started := time.Now()
	if err := receive(t, "server session end", h.served); err == nil {
		t.Fatal("silent agent session ended without an error")
	}
	if elapsed := time.Since(started); elapsed > heartbeatMisses*heartbeat+time.Second {
		t.Fatalf("silent agent dropped after %s", elapsed)
	}
	if len(h.remote.Hosts()) != 0 {
		t.Fatal("silent agent is still registered")
	}
}

func TestAgentDropsSilentServer(t *testing.T) {
	upgrader := websocket.Upgrader{}
	hold := make(chan struct{})
	defer close(hold)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		<-hold // accepts the agent, then neither reads, writes, nor pings
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	agent := testAgent(&fakeHost{})
	agent.Heartbeat = 30 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- agent.Serve(context.Background(), conn) }()
	err = receive(t, "agent to give up", done)
	var netErr interface{ Timeout() bool }
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("agent Serve = %v, want a read timeout", err)
	}
}

func TestHeartbeatKeepsIdleSessionAlive(t *testing.T) {
	const heartbeat = 20 * time.Millisecond
	h := newHarness(t, heartbeat)
	agent := testAgent(&fakeHost{})
	agent.Heartbeat = heartbeat
	h.connect(t, agent, "local-b")
	time.Sleep(10 * heartbeat)
	if _, err := h.remote.Containers(context.Background(), "local-b"); err != nil {
		t.Fatalf("idle session with heartbeats: %v", err)
	}
}

func TestServerRefusesBeyondMaxInFlight(t *testing.T) {
	h := newHarness(t, time.Minute)
	release := make(chan struct{})
	host := &fakeHost{volumes: func(context.Context) ([]protocol.Volume, error) {
		<-release
		return nil, nil
	}}
	h.connect(t, testAgent(host), "local-b")

	results := make(chan error, protocol.MaxInFlight)
	for range protocol.MaxInFlight {
		go func() {
			_, err := h.remote.Volumes(context.Background(), "local-b")
			results <- err
		}()
	}
	waitFor(t, "full request window", func() bool { return h.pending("local-b") == protocol.MaxInFlight })
	if _, err := h.remote.Containers(context.Background(), "local-b"); !errors.Is(err, ErrHostBusy) {
		t.Fatalf("request beyond the limit = %v", err)
	}
	close(release)
	for range protocol.MaxInFlight {
		if err := receive(t, "queued request", results); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.remote.Containers(context.Background(), "local-b"); err != nil {
		t.Fatalf("request after the window drained: %v", err)
	}
}

// rawServer accepts one agent and hands the test the server end of the socket.
func rawServer(t *testing.T) (*websocket.Conn, *Agent, chan error) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	conns := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conns <- conn
	}))
	t.Cleanup(server.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	serverSide := receive(t, "server connection", conns)
	t.Cleanup(func() { _ = serverSide.Close() })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	agent := testAgent(&fakeHost{volumes: func(ctx context.Context) ([]protocol.Volume, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agent.Serve(ctx, client) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	readMessage(t, serverSide) // hello
	return serverSide, agent, done
}

func readMessage(t *testing.T, conn *websocket.Conn) protocol.Message {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	message, err := protocol.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func sendRequest(t *testing.T, conn *websocket.Conn, id, operation string) {
	t.Helper()
	data, err := protocol.Encode(protocol.Message{ID: id, Type: protocol.TypeRequest, Operation: operation, TimeoutMS: 60_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatal(err)
	}
}

func TestAgentRefusesBeyondMaxInFlight(t *testing.T) {
	conn, _, _ := rawServer(t)
	for i := range protocol.MaxInFlight {
		sendRequest(t, conn, fmt.Sprintf("slow-%d", i), "volumes.list")
	}
	sendRequest(t, conn, "one-too-many", "containers.list")
	response := readMessage(t, conn)
	if response.ID != "one-too-many" || response.Error == nil || response.Error.Code != protocol.CodeBusy || !response.Error.Retryable {
		t.Fatalf("response beyond the agent limit = %#v", response)
	}
}

func TestAgentRefusesDuplicateInFlightID(t *testing.T) {
	conn, _, _ := rawServer(t)
	sendRequest(t, conn, "same", "volumes.list")
	sendRequest(t, conn, "same", "containers.list")
	response := readMessage(t, conn)
	if response.ID != "same" || response.Error == nil || response.Error.Code != protocol.CodeBusy {
		t.Fatalf("duplicate ID response = %#v", response)
	}
}

func TestAgentShutdownCancelsAndAwaitsOperations(t *testing.T) {
	h := newHarness(t, time.Minute)
	var finished atomic.Bool
	started := make(chan struct{}, 1)
	host := &fakeHost{volumes: func(ctx context.Context) ([]protocol.Volume, error) {
		started <- struct{}{}
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond) // cleanup, like reaping a Podman child
		finished.Store(true)
		return nil, ctx.Err()
	}}
	agent := h.connect(t, testAgent(host), "local-b")
	go func() { _, _ = h.remote.Volumes(context.Background(), "local-b") }()
	receive(t, "operation start", started)
	_ = agent.stop(t)
	if !finished.Load() {
		t.Fatal("agent Serve returned before its operation finished")
	}
}

func TestIdentityMismatchIsRejected(t *testing.T) {
	h := newHarness(t, time.Minute)
	for _, identity := range []string{"local-a", ""} {
		conn := h.dial(t, identity)
		serveErr := make(chan error, 1)
		go func() { serveErr <- testAgent(&fakeHost{}).Serve(context.Background(), conn) }()
		if err := receive(t, "server refusal", h.served); err == nil {
			t.Fatalf("identity %q: server accepted an agent claiming local-b", identity)
		}
		receive(t, "agent end", serveErr)
		if len(h.remote.Hosts()) != 0 {
			t.Fatalf("identity %q: mismatched agent was registered", identity)
		}
	}
}

func TestNewSessionReplacesOldOne(t *testing.T) {
	h := newHarness(t, time.Minute)
	oldHost := &fakeHost{hostname: "old", volumes: func(ctx context.Context) ([]protocol.Volume, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	h.connect(t, testAgent(oldHost), "local-b")
	pending := make(chan error, 1)
	go func() {
		_, err := h.remote.Volumes(context.Background(), "local-b")
		pending <- err
	}()
	waitFor(t, "request on the old session", func() bool { return h.pending("local-b") == 1 })

	h.connect(t, testAgent(&fakeHost{hostname: "new"}), "local-b")
	if err := receive(t, "old session request", pending); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("request on the replaced session = %v", err)
	}
	receive(t, "old session end", h.served)
	hosts := h.remote.Hosts()
	if len(hosts) != 1 || hosts[0].Hostname != "new" {
		t.Fatalf("hosts after replacement = %#v", hosts)
	}
	if info, err := h.remote.Info(context.Background(), "local-b"); err != nil || info.Hostname != "new" {
		t.Fatalf("info after replacement = %#v, %v", info, err)
	}
}

func TestOversizedResponseIsReportedAndSessionSurvives(t *testing.T) {
	h := newHarness(t, time.Minute)
	huge := strings.Repeat("x", protocol.MaxMessageSize)
	host := &fakeHost{images: func(context.Context) ([]protocol.Image, error) {
		return []protocol.Image{{ID: huge}}, nil
	}}
	h.connect(t, testAgent(host), "local-b")
	_, err := h.remote.Images(context.Background(), "local-b")
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Code != protocol.CodeResponseTooLarge {
		t.Fatalf("oversized response error = %v", err)
	}
	if _, err := h.remote.Containers(context.Background(), "local-b"); err != nil {
		t.Fatalf("session after an oversized response: %v", err)
	}
}

func TestOversizedRequestFailsAlone(t *testing.T) {
	h := newHarness(t, time.Minute)
	h.connect(t, testAgent(&fakeHost{}), "local-b")
	save := protocol.StackSave{Name: "web", ComposeYAML: strings.Repeat("x", protocol.MaxMessageSize)}
	if _, err := h.remote.SaveStack(context.Background(), "local-b", save); !errors.Is(err, protocol.ErrMessageTooLarge) {
		t.Fatalf("oversized request error = %v", err)
	}
	if _, err := h.remote.Containers(context.Background(), "local-b"); err != nil {
		t.Fatalf("session after an oversized request: %v", err)
	}
}

func TestAgentOperationErrors(t *testing.T) {
	h := newHarness(t, time.Minute)
	h.connect(t, testAgent(&fakeHost{}), "local-b")
	ctx := context.Background()

	var remote *RemoteError
	err := h.remote.call(ctx, "local-b", "containers.explode", nil, nil, time.Second)
	if !errors.As(err, &remote) || remote.Code != protocol.CodeUnknownOperation || remote.Retryable {
		t.Fatalf("unknown operation error = %v", err)
	}
	// An agent without a stack manager must refuse, not report empty success.
	checks := map[string]func() error{
		"list":     func() error { _, err := h.remote.ListStacks(ctx, "local-b"); return err },
		"inspect":  func() error { _, err := h.remote.InspectStack(ctx, "local-b", "web"); return err },
		"versions": func() error { _, err := h.remote.StackVersions(ctx, "local-b", "web"); return err },
		"save": func() error {
			_, err := h.remote.SaveStack(ctx, "local-b", protocol.StackSave{Name: "web"})
			return err
		},
		"up": func() error { _, err := h.remote.StackOperation(ctx, "local-b", "web", "up"); return err },
	}
	for name, check := range checks {
		err := check()
		if !errors.As(err, &remote) || remote.Message != errStacksUnavailable.Error() {
			t.Fatalf("%s without stacks = %v", name, err)
		}
	}
}

func TestRunAgentReconnects(t *testing.T) {
	h := newHarness(t, time.Minute)
	agent := testAgent(&fakeHost{})
	var dials atomic.Int32
	waits := make(chan time.Duration, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunAgent(ctx, agent, ReconnectOptions{
			MinBackoff: 10 * time.Millisecond,
			MaxBackoff: 40 * time.Millisecond,
			Dial: func(ctx context.Context) (*websocket.Conn, error) {
				if dials.Add(1) <= 2 {
					return nil, errors.New("server not up yet")
				}
				conn, _, err := websocket.DefaultDialer.DialContext(ctx, h.url+"?host=local-b", nil)
				return conn, err
			},
			OnDisconnect: func(_ error, wait time.Duration) { waits <- wait },
		})
	}()
	waitFor(t, "first session", func() bool { return h.sessionFor("local-b") != nil })
	first := h.sessionFor("local-b")
	if first, second := receive(t, "first wait", waits), receive(t, "second wait", waits); first < 5*time.Millisecond || first > 10*time.Millisecond || second < 10*time.Millisecond || second > 20*time.Millisecond {
		t.Fatalf("backoff waits = %s, %s", first, second)
	}

	first.close(errors.New("server kicked the agent"))
	waitFor(t, "second session", func() bool {
		s := h.sessionFor("local-b")
		return s != nil && s != first
	})
	if _, err := h.remote.Containers(context.Background(), "local-b"); err != nil {
		t.Fatalf("request after reconnect: %v", err)
	}
	cancel()
	if err := receive(t, "RunAgent exit", done); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunAgent = %v", err)
	}
}

func TestRunAgentBackoffIsCapped(t *testing.T) {
	var dials atomic.Int32
	var mu sync.Mutex
	var waits []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunAgent(ctx, testAgent(&fakeHost{}), ReconnectOptions{
			MinBackoff: time.Millisecond,
			MaxBackoff: 4 * time.Millisecond,
			Dial: func(context.Context) (*websocket.Conn, error) {
				dials.Add(1)
				return nil, errors.New("refused")
			},
			OnDisconnect: func(_ error, wait time.Duration) {
				mu.Lock()
				waits = append(waits, wait)
				mu.Unlock()
			},
		})
	}()
	waitFor(t, "several attempts", func() bool { return dials.Load() >= 8 })
	cancel()
	receive(t, "RunAgent exit", done)
	mu.Lock()
	defer mu.Unlock()
	for _, wait := range waits {
		if wait > 4*time.Millisecond {
			t.Fatalf("wait %s exceeds the cap", wait)
		}
	}
}

func BenchmarkContainersInventory1000(b *testing.B) {
	containers := make([]protocol.Container, 1000)
	for i := range containers {
		name := fmt.Sprintf("local-b-service-%04d", i)
		containers[i] = protocol.Container{ID: fmt.Sprintf("%064x", i), Name: name, Image: "registry.example.com/team/" + name + ":latest", State: "running", Status: "Up 3 days"}
	}
	h := newHarness(b, time.Minute)
	h.connect(b, testAgent(&fakeHost{containers: func(context.Context) ([]protocol.Container, error) { return containers, nil }}), "local-b")
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		out, err := h.remote.Containers(ctx, "local-b")
		if err != nil || len(out) != len(containers) {
			b.Fatalf("containers = %d, %v", len(out), err)
		}
	}
}

func BenchmarkConcurrentSmallRequests(b *testing.B) {
	h := newHarness(b, time.Minute)
	h.connect(b, testAgent(&fakeHost{}), "local-b")
	ctx := context.Background()
	// A fixed worker count keeps the window at half of MaxInFlight whatever the
	// core count; RunParallel's goroutine count scales with GOMAXPROCS.
	const workers = protocol.MaxInFlight / 2
	var next atomic.Int64
	var wg sync.WaitGroup
	b.ReportAllocs()
	b.ResetTimer()
	for range workers {
		wg.Go(func() {
			for next.Add(1) <= int64(b.N) {
				if _, err := h.remote.Containers(ctx, "local-b"); err != nil {
					b.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
}
