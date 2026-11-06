package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/agentcred"
	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/testcontrol"
)

type fixture struct {
	master *testcontrol.Master
	server *Server
	client *Client
	mu     sync.Mutex
	logged []string
}

func (f *fixture) lines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.logged)
}

// start serves the admin socket of a test master. configure, if given,
// adjusts the server before it starts.
func start(t *testing.T, configure ...func(*Server)) *fixture {
	t.Helper()
	m := testcontrol.Start(t)
	f := &fixture{master: m}
	f.server = NewServer(m.Enrollment, m.Remote, func(format string, args ...any) {
		f.mu.Lock()
		f.logged = append(f.logged, fmt.Sprintf(format, args...))
		f.mu.Unlock()
	})
	for _, c := range configure {
		c(f.server)
	}
	listener, err := Listen(m.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Serve = %v", err)
		}
	})
	if f.client, err = NewClient(m.StateDir); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestTokenListRemove(t *testing.T) {
	var connected atomic.Pointer[[]protocol.HostInfo]
	connected.Store(&[]protocol.HostInfo{})
	f := start(t, func(s *Server) { s.connected = func() []protocol.HostInfo { return *connected.Load() } })
	ctx := context.Background()
	issued, err := f.client.Token(ctx, "hel1", 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enrollment.ParseToken(issued.Token); err != nil {
		t.Fatalf("token %v", err)
	}
	if want := f.master.Clock.Now().Add(30 * time.Minute); !issued.ExpiresAt.Equal(want) {
		t.Fatalf("expires %s, want %s", issued.ExpiresAt, want)
	}
	for _, line := range f.lines() {
		if strings.Contains(line, issued.Token) {
			t.Fatal("token logged")
		}
	}
	list, err := f.client.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Hosts) != 0 || len(list.Tokens) != 1 || list.Tokens[0].HostID != "hel1" {
		t.Fatalf("list before enrolling %+v", list)
	}

	dir := filepath.Join(t.TempDir(), "hel1")
	if _, err := agentcred.Enroll(ctx, agentcred.EnrollOptions{ServerURL: f.master.ServerURL, HostID: "hel1", Token: issued.Token, Dir: dir, Now: f.master.Clock.Now}); err != nil {
		t.Fatal(err)
	}
	if list, err = f.client.List(ctx); err != nil {
		t.Fatal(err)
	}
	if len(list.Hosts) != 1 {
		t.Fatalf("hosts %+v", list.Hosts)
	}
	h := list.Hosts[0]
	if h.HostID != "hel1" || h.Prefix != "hel1-" || h.Online || h.Pending || h.CurrentNotAfter.IsZero() || h.EnrolledAt.IsZero() {
		t.Fatalf("host %+v", h)
	}
	if len(list.Tokens) != 0 {
		t.Fatalf("spent token still listed: %+v", list.Tokens)
	}
	connected.Store(&[]protocol.HostInfo{{HostID: "hel1"}, {HostID: "stranger"}})
	if list, err = f.client.List(ctx); err != nil {
		t.Fatal(err)
	}
	if len(list.Hosts) != 1 || !list.Hosts[0].Online {
		t.Fatalf("online host %+v", list.Hosts)
	}

	if err := f.client.Remove(ctx, "hel1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.master.Enrollment.Registry().Get("hel1"); ok {
		t.Fatal("host still enrolled")
	}
	err = f.client.Remove(ctx, "hel1")
	var e *Error
	if !errors.As(err, &e) || e.Code != enrollment.CodeNotFound {
		t.Fatalf("second remove = %v", err)
	}
}

func TestEmptyListEncodesArrays(t *testing.T) {
	m := testcontrol.Start(t)
	srv := NewServer(m.Enrollment, m.Remote, t.Logf)
	encoded, err := json.Marshal(srv.list())
	if err != nil {
		t.Fatal(err)
	}
	// Empty lists are arrays, not null, so scripts can iterate them.
	if string(encoded) != `{"hosts":[],"tokens":[]}` {
		t.Fatalf("encoded %s", encoded)
	}
}

func TestRequestErrors(t *testing.T) {
	f := start(t)
	cases := []struct {
		request Request
		code    string
	}{
		{Request{Action: "explode"}, enrollment.CodeInvalidRequest},
		{Request{Action: ActionToken, HostID: "../etc"}, enrollment.CodeInvalidRequest},
		{Request{Action: ActionToken, HostID: "hel1", TTL: "soon"}, enrollment.CodeInvalidRequest},
		{Request{Action: ActionToken, HostID: "hel1", TTL: "-1h"}, enrollment.CodeInvalidRequest},
		{Request{Action: ActionToken, HostID: "hel1", TTL: "48h"}, enrollment.CodeInvalidRequest},
		{Request{Action: ActionList, HostID: "hel1"}, enrollment.CodeInvalidRequest},
		{Request{Action: ActionRemove, HostID: "hel1", TTL: "1h"}, enrollment.CodeInvalidRequest},
		{Request{Action: ActionRemove, HostID: "ghost"}, enrollment.CodeNotFound},
	}
	for _, c := range cases {
		err := f.client.call(context.Background(), c.request, &struct{}{})
		var e *Error
		if !errors.As(err, &e) || e.Code != c.code {
			t.Errorf("%+v = %v, want code %s", c.request, err, c.code)
		}
	}
}

// exchange sends raw bytes and returns the decoded answer.
func exchange(t *testing.T, stateDir, payload string) errorBody {
	t.Helper()
	conn, err := net.Dial("unix", socketPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	var out errorBody
	if err := json.NewDecoder(conn).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMalformedAndOversizedRequests(t *testing.T) {
	f := start(t)
	dir := f.master.StateDir
	for name, payload := range map[string]string{
		"not json":      "hello\n",
		"unknown field": `{"action":"list","admin":true}` + "\n",
		"oversized":     `{"action":"list","host_id":"` + strings.Repeat("a", MaxRequest) + `"}` + "\n",
	} {
		if got := exchange(t, dir, payload); got.Code != enrollment.CodeInvalidRequest {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// answerWith serves one admin connection in a fresh state directory with
// answer, whatever the request, and returns a client for it.
func answerWith(t *testing.T, answer []byte) *Client {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write(answer)
	}()
	client, err := NewClient(dir)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestClientReadsLargeLists: a list far beyond the request limit, here
// thirty thousand hosts, reaches the client.
func TestClientReadsLargeLists(t *testing.T) {
	const hosts = 30_000
	list := ListResponse{Hosts: make([]enrollment.HostStatus, hosts), Tokens: []enrollment.TokenInfo{}}
	for i := range list.Hosts {
		id := fmt.Sprintf("host-%05d", i)
		list.Hosts[i] = enrollment.HostStatus{HostID: id, Prefix: id + "-", EnrolledAt: time.Now().UTC(), CurrentNotAfter: time.Now().UTC()}
	}
	answer, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if len(answer) <= MaxRequest || len(answer) > MaxResponse {
		t.Fatalf("answer of %d bytes does not exercise the response limit", len(answer))
	}
	got, err := answerWith(t, answer).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hosts) != hosts || got.Hosts[hosts-1].HostID != "host-29999" {
		t.Fatalf("got %d hosts", len(got.Hosts))
	}
}

func TestClientRefusesOversizedAnswers(t *testing.T) {
	answer := []byte(`{"hosts":[],"tokens":[],"pad":"` + strings.Repeat("a", MaxResponse) + `"}`)
	if _, err := answerWith(t, answer).List(context.Background()); err == nil || !strings.Contains(err.Error(), "read the server's answer") {
		t.Fatalf("List = %v", err)
	}
}

func TestRefusesOtherPeers(t *testing.T) {
	f := start(t, func(s *Server) { s.allowPeer = func(int) bool { return false } })
	_, err := f.client.List(context.Background())
	var e *Error
	if !errors.As(err, &e) || e.Code != "forbidden" {
		t.Fatalf("List = %v", err)
	}
	if lines := f.lines(); len(lines) == 0 || !strings.Contains(lines[len(lines)-1], "refused peer") {
		t.Fatalf("logged %v", lines)
	}
}

func TestPeerPolicies(t *testing.T) {
	const self, owner, other = 1000, 998, 1001
	for uid, want := range map[int]bool{self: true, 0: true, owner: false, other: false} {
		if got := peerAllowed(uid, self); got != want {
			t.Errorf("server answering uid %d: %v, want %v", uid, got, want)
		}
	}
	for uid, want := range map[int]bool{self: true, 0: true, owner: true, other: false} {
		if got := serverAllowed(uid, self, owner); got != want {
			t.Errorf("client trusting server uid %d: %v, want %v", uid, got, want)
		}
	}
}

func TestClientRefusesAForeignServer(t *testing.T) {
	f := start(t)
	f.client.allowServer = func(int) bool { return false }
	_, err := f.client.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not the server's account") {
		t.Fatalf("List = %v", err)
	}
	if len(f.master.Enrollment.Tokens()) != 0 {
		t.Fatal("request went through")
	}
}

func TestConnectionLimit(t *testing.T) {
	f := start(t)
	// Hold every slot with a connection that has not sent its request.
	var held []net.Conn
	for range MaxConnections {
		conn, err := net.Dial("unix", socketPath(f.master.StateDir))
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	// The server takes the held connections in order; poll until the next
	// one is refused rather than queued.
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := f.client.List(context.Background())
		var e *Error
		if errors.As(err, &e) && e.Code == CodeBusy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never busy: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Freeing a slot lets requests through again.
	_ = held[0].Close()
	deadline = time.Now().Add(10 * time.Second)
	for {
		if _, err := f.client.List(context.Background()); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("still refused: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIdleConnectionsAreClosed(t *testing.T) {
	f := start(t, func(s *Server) { s.timeout = 100 * time.Millisecond })
	conn, err := net.Dial("unix", socketPath(f.master.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	// Nothing is sent; the server gives up and answers with an error.
	var out errorBody
	if err := json.NewDecoder(conn).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Code != enrollment.CodeInvalidRequest {
		t.Fatalf("answer %+v", out)
	}
}

func TestListenRefusesAnOpenStateDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(dir); err == nil {
		t.Fatal("listened in a world-readable state directory")
	}
}
