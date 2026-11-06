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
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/relay"
)

// fakeRelay records what the admin server asks of the agents.
type fakeRelay struct {
	mu        sync.Mutex
	calls     []string
	saves     []protocol.StackSave
	remaining map[string]time.Duration
	err       error
	operation protocol.StackOperation
	// block makes StackOperation wait for its context and report how it
	// ended on canceled.
	block    bool
	canceled chan error
}

func newFakeRelay() *fakeRelay {
	return &fakeRelay{remaining: map[string]time.Duration{}, canceled: make(chan error, 1)}
}

func (f *fakeRelay) record(ctx context.Context, call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if deadline, ok := ctx.Deadline(); ok {
		f.remaining[strings.Fields(call)[0]] = time.Until(deadline)
	}
}

func (f *fakeRelay) Hosts() []protocol.HostInfo { return nil }

func (f *fakeRelay) ListStacks(ctx context.Context, hostID string) ([]protocol.Stack, error) {
	f.record(ctx, "list "+hostID)
	if f.err != nil {
		return nil, f.err
	}
	return []protocol.Stack{{Name: "web", Project: "jp1-web", Status: "running", Version: 2}}, nil
}

func (f *fakeRelay) InspectStack(ctx context.Context, hostID, name string) (protocol.Stack, error) {
	f.record(ctx, "inspect "+hostID+"/"+name)
	return protocol.Stack{Name: name, ComposeYAML: "services: {}\n", Version: 2}, f.err
}

func (f *fakeRelay) SaveStack(ctx context.Context, hostID string, save protocol.StackSave) (protocol.Stack, error) {
	f.record(ctx, "save "+hostID+"/"+save.Name)
	f.mu.Lock()
	f.saves = append(f.saves, save)
	f.mu.Unlock()
	return protocol.Stack{Name: save.Name, Version: 1}, f.err
}

func (f *fakeRelay) StackOperation(ctx context.Context, hostID, name, operation string) (protocol.StackOperation, error) {
	f.record(ctx, operation+" "+hostID+"/"+name)
	if f.block {
		<-ctx.Done()
		f.canceled <- ctx.Err()
		return protocol.StackOperation{}, ctx.Err()
	}
	return f.operation, f.err
}

func (f *fakeRelay) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func startWithRelay(t *testing.T, r *fakeRelay) *fixture {
	return start(t, func(s *Server) { s.relay = r })
}

func TestStackActionsReachTheRelay(t *testing.T) {
	r := newFakeRelay()
	r.operation = protocol.StackOperation{Name: "web", Operation: "up", Success: true, Output: "started"}
	f := startWithRelay(t, r)
	ctx := context.Background()

	list, err := f.client.Stacks(ctx, "jp1")
	if err != nil || len(list) != 1 || list[0].Project != "jp1-web" {
		t.Fatalf("Stacks = %+v, %v", list, err)
	}
	stack, err := f.client.Stack(ctx, "jp1", "web")
	if err != nil || stack.ComposeYAML != "services: {}\n" {
		t.Fatalf("Stack = %+v, %v", stack, err)
	}
	version := 2
	save := protocol.StackSave{Name: "web", ComposeYAML: "services: {}\n", ExpectedVersion: &version}
	if _, err := f.client.SaveStack(ctx, "jp1", save); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"up", "down", "restart"} {
		result, err := f.client.StackOperation(ctx, "jp1", "web", op)
		if err != nil || !result.Success || result.Output != "started" {
			t.Fatalf("%s = %+v, %v", op, result, err)
		}
	}
	want := []string{"list jp1", "inspect jp1/web", "save jp1/web", "up jp1/web", "down jp1/web", "restart jp1/web"}
	if got := r.Calls(); !slices.Equal(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
	if got := r.saves[0]; got.ExpectedVersion == nil || *got.ExpectedVersion != 2 || got.ComposeYAML != save.ComposeYAML {
		t.Fatalf("save %+v", got)
	}
}

func TestEmptyStackListIsAnArray(t *testing.T) {
	f := startWithRelay(t, newFakeRelay())
	f.server.relay = emptyStacks{f.server.relay.(*fakeRelay)}
	var raw json.RawMessage
	if err := f.client.call(context.Background(), Request{Action: ActionStackList, HostID: "jp1"}, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw) != "[]" {
		t.Fatalf("answer %s", raw)
	}
}

type emptyStacks struct{ *fakeRelay }

func (emptyStacks) ListStacks(context.Context, string) ([]protocol.Stack, error) { return nil, nil }

func TestStackRequestErrors(t *testing.T) {
	r := newFakeRelay()
	f := startWithRelay(t, r)
	save := &protocol.StackSave{Name: "web"}
	cases := []Request{
		{Action: ActionStackList, HostID: "../x"},
		{Action: ActionStackList, HostID: "jp1", Stack: "web"},
		{Action: ActionStackList, HostID: "jp1", Save: save},
		{Action: ActionStackList, HostID: "jp1", TTL: "1h"},
		{Action: ActionStackInspect, HostID: "jp1"},
		{Action: ActionStackInspect, HostID: "jp1", Stack: "../web"},
		{Action: ActionStackInspect, HostID: "jp1", Stack: "web", Save: save},
		{Action: ActionStackUp, HostID: "jp1", Stack: "Web Site"},
		{Action: ActionStackSave, HostID: "jp1"},
		{Action: ActionStackSave, HostID: "jp1", Stack: "web", Save: save},
		{Action: ActionStackSave, HostID: "jp1", Save: &protocol.StackSave{Name: "../web"}},
		{Action: "stacks.versions", HostID: "jp1", Stack: "web"},
		{Action: ActionToken, HostID: "jp1", Stack: "web"},
		{Action: ActionRemove, HostID: "jp1", Save: save},
	}
	for _, request := range cases {
		err := f.client.call(context.Background(), request, &json.RawMessage{})
		var e *Error
		if !errors.As(err, &e) || e.Code != enrollment.CodeInvalidRequest {
			t.Errorf("%+v = %v, want invalid_request", request, err)
		}
	}
	if calls := r.Calls(); len(calls) != 0 {
		t.Fatalf("invalid requests reached the relay: %v", calls)
	}
}

func TestRelayErrorsMapToCodes(t *testing.T) {
	cases := map[error]string{
		relay.ErrHostUnavailable: enrollment.CodeUnavailable,
		relay.ErrHostBusy:        enrollment.CodeUnavailable,
		relay.ErrDisconnected:    enrollment.CodeUnavailable,
		relay.ErrTimeout:         CodeTimeout,
		&relay.RemoteError{Code: protocol.CodeOperationFailed, Message: "stack already exists"}: CodeFailed,
	}
	for relayErr, code := range cases {
		r := newFakeRelay()
		r.err = relayErr
		f := startWithRelay(t, r)
		_, err := f.client.Stacks(context.Background(), "jp1")
		var e *Error
		if !errors.As(err, &e) || e.Code != code {
			t.Errorf("relay %v: %v, want code %s", relayErr, err, code)
		}
		if remote := (*relay.RemoteError)(nil); errors.As(relayErr, &remote) && e != nil && e.Message != "stack already exists" {
			t.Errorf("agent message lost: %q", e.Message)
		}
	}
}

// TestFailedOperationIsAnAnswer: Compose failing is a result to show, not an
// error.
func TestFailedOperationIsAnAnswer(t *testing.T) {
	r := newFakeRelay()
	r.operation = protocol.StackOperation{Name: "web", Operation: "up", Output: "Error: no such image"}
	f := startWithRelay(t, r)
	result, err := f.client.StackOperation(context.Background(), "jp1", "web", "up")
	if err != nil || result.Success || result.Output != "Error: no such image" {
		t.Fatalf("up = %+v, %v", result, err)
	}
}

// TestStackDeadlines: the relay gets the action's deadline, not the five
// seconds the enrollment actions have.
func TestStackDeadlines(t *testing.T) {
	r := newFakeRelay()
	r.operation.Success = true
	f := startWithRelay(t, r)
	ctx := context.Background()
	if _, err := f.client.Stacks(ctx, "jp1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.StackOperation(ctx, "jp1", "web", "up"); err != nil {
		t.Fatal(err)
	}
	for call, want := range map[string]time.Duration{"list": StackTimeout, "up": StackOperationTimeout} {
		got := r.remaining[call]
		// The test's own scheduling may eat into it, not a quarter.
		if got > want || got < want-want/4 {
			t.Errorf("%s had %s left, want about %s", call, got, want)
		}
	}
	if StackTimeout <= relay.DefaultRequestTimeout || StackOperationTimeout <= relay.StackOperationTimeout {
		t.Fatal("the socket gives up before the relay does")
	}
}

// TestClientGoingAwayCancelsTheOperation: a client that closes its
// connection stops the operation it asked for.
func TestClientGoingAwayCancelsTheOperation(t *testing.T) {
	r := newFakeRelay()
	r.block = true
	f := startWithRelay(t, r)
	conn, err := net.Dial("unix", socketPath(f.master.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(Request{Action: ActionStackUp, HostID: "jp1", Stack: "web"}); err != nil {
		t.Fatal(err)
	}
	waitForCall(t, r, "up jp1/web")
	_ = conn.Close()
	select {
	case err := <-r.canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("operation ended with %v, want cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("operation still running after the client left")
	}
}

func waitForCall(t *testing.T, r *fakeRelay, call string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !slices.Contains(r.Calls(), call) {
		if time.Now().After(deadline) {
			t.Fatalf("no %q call; calls %v", call, r.Calls())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSaveLogsNoEnvironment: the log names the stack, never a value.
func TestSaveLogsNoEnvironment(t *testing.T) {
	r := newFakeRelay()
	f := startWithRelay(t, r)
	const secret = "hunter2-very-secret"
	save := protocol.StackSave{Name: "homes", ComposeYAML: "services: {}\n", Environment: map[string]string{"MYSQL_ROOT_PASSWORD": secret}}
	if _, err := f.client.SaveStack(context.Background(), "jp1", save); err != nil {
		t.Fatal(err)
	}
	if r.saves[0].Environment["MYSQL_ROOT_PASSWORD"] != secret {
		t.Fatal("environment did not reach the relay")
	}
	logged := strings.Join(f.lines(), "\n")
	if !strings.Contains(logged, "jp1/homes") || !strings.Contains(logged, "1 environment entries") {
		t.Fatalf("save not logged: %q", logged)
	}
	if strings.Contains(logged, secret) || strings.Contains(logged, "MYSQL_ROOT_PASSWORD") {
		t.Fatalf("environment logged: %q", logged)
	}
}

// TestLargeSaves: the agent's largest Compose file, all quotes so JSON
// escaping doubles it, fits; a request over MaxRequest (the relay's message
// limit) does not.
func TestLargeSaves(t *testing.T) {
	r := newFakeRelay()
	f := startWithRelay(t, r)
	big := strings.Repeat(`"`, 1<<20)
	if _, err := f.client.SaveStack(context.Background(), "jp1", protocol.StackSave{Name: "big", ComposeYAML: big}); err != nil {
		t.Fatalf("1 MiB save: %v", err)
	}
	if len(r.saves) != 1 || r.saves[0].ComposeYAML != big {
		t.Fatal("1 MiB save did not arrive whole")
	}
	_, err := f.client.SaveStack(context.Background(), "jp1", protocol.StackSave{Name: "huge", ComposeYAML: strings.Repeat("a", MaxRequest)})
	var e *Error
	if !errors.As(err, &e) || e.Code != enrollment.CodeInvalidRequest {
		t.Fatalf("oversized save = %v", err)
	}
}

func TestDialStdio(t *testing.T) {
	r := newFakeRelay()
	f := startWithRelay(t, r)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- DialStdio(context.Background(), f.master.StateDir, inR, outW)
		_ = outW.Close()
	}()
	go func() { _ = json.NewEncoder(inW).Encode(Request{Action: ActionStackList, HostID: "jp1"}) }()
	var answer []protocol.Stack
	if err := json.NewDecoder(outR).Decode(&answer); err != nil {
		t.Fatal(err)
	}
	if len(answer) != 1 || answer[0].Name != "web" {
		t.Fatalf("answer %+v", answer)
	}
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatalf("DialStdio = %v", err)
	}
}

// TestDialStdioEndOfInputCancels: the SSH client going away (stdin ends)
// cancels what it asked for.
func TestDialStdioEndOfInputCancels(t *testing.T) {
	r := newFakeRelay()
	r.block = true
	f := startWithRelay(t, r)
	inR, inW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- DialStdio(context.Background(), f.master.StateDir, inR, io.Discard) }()
	if err := json.NewEncoder(inW).Encode(Request{Action: ActionStackUp, HostID: "jp1", Stack: "web"}); err != nil {
		t.Fatal(err)
	}
	waitForCall(t, r, "up jp1/web")
	_ = inW.Close()
	select {
	case <-r.canceled:
	case <-time.After(10 * time.Second):
		t.Fatal("operation still running after stdin ended")
	}
	if err := <-done; err != nil {
		t.Fatalf("DialStdio = %v", err)
	}
}

func TestParseSSHURL(t *testing.T) {
	good := map[string]SSHTarget{
		"ssh://jp1.example.com":            {Host: "jp1.example.com"},
		"ssh://root@jp1.example.com":       {User: "root", Host: "jp1.example.com"},
		"ssh://root@jp1.example.com:2222/": {User: "root", Host: "jp1.example.com", Port: 2222},
		"ssh://root@[2001:db8::1]:22":      {User: "root", Host: "2001:db8::1", Port: 22},
	}
	for raw, want := range good {
		got, err := ParseSSHURL(raw)
		if err != nil || got != want {
			t.Errorf("%s = %+v, %v; want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"jp1.example.com", "http://jp1.example.com", "ssh://", "ssh://root:pw@jp1", "ssh://jp1/var/lib",
		"ssh://jp1?x=1", "ssh://jp1:0", "ssh://jp1:70000", "ssh://jp1:ssh", "ssh://-oProxyCommand=x", "ssh://-x@jp1",
	} {
		if got, err := ParseSSHURL(raw); err == nil {
			t.Errorf("%s accepted as %+v", raw, got)
		}
	}
}

func TestSSHClientRefusesUnsafeStateDirs(t *testing.T) {
	for _, dir := range []string{"var/lib/sp", "/var/lib/../sp", "/var/lib/sp dir", "/var/lib/$(id)", "/var/lib/sp;rm"} {
		if _, err := NewSSHClient(SSHTarget{Host: "jp1"}, dir, nil); err == nil {
			t.Errorf("state dir %q accepted", dir)
		}
	}
}

func TestSSHArgs(t *testing.T) {
	got := sshArgs(SSHTarget{User: "root", Host: "jp1", Port: 2222}, "/var/lib/sorry-portainer", []string{"-F", "cfg"})
	want := []string{"-F", "cfg", "-T", "-o", "BatchMode=yes", "-p", "2222", "root@jp1", "--",
		"sorry-portainer-server", "admin", "dial-stdio", "--state-dir", "/var/lib/sorry-portainer"}
	if !slices.Equal(got, want) {
		t.Fatalf("args %q\nwant %q", got, want)
	}
	if got := sshArgs(SSHTarget{Host: "jp1"}, "/s", nil); slices.Contains(got, "-p") || got[2] != "BatchMode=yes" || got[3] != "jp1" {
		t.Fatalf("args %q", got)
	}
}

// helperSSH makes this test binary stand in for ssh: it checks the command
// line it was given and runs dial-stdio, or fails the way ssh does.
func helperSSH(t *testing.T, mode string) []string {
	t.Helper()
	t.Setenv("SORRY_PORTAINER_TEST_SSH", mode)
	t.Setenv("SORRY_PORTAINER_TEST_SSH_ARGS", filepath.Join(t.TempDir(), "args.json"))
	return []string{os.Args[0], "-test.run=^TestHelperSSH$", "--"}
}

func TestHelperSSH(t *testing.T) {
	mode := os.Getenv("SORRY_PORTAINER_TEST_SSH")
	if mode == "" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	data, _ := json.Marshal(args)
	_ = os.WriteFile(os.Getenv("SORRY_PORTAINER_TEST_SSH_ARGS"), data, 0o600)
	if mode == "fail" {
		fmt.Fprintln(os.Stderr, "root@jp1: Permission denied (publickey).")
		os.Exit(255)
	}
	remote := args[slices.Index(args, "--")+1:]
	if len(remote) != 5 || remote[0] != RemoteServerCommand || remote[1] != "admin" || remote[2] != "dial-stdio" || remote[3] != "--state-dir" {
		fmt.Fprintf(os.Stderr, "unexpected remote command %q\n", remote)
		os.Exit(2)
	}
	if err := DialStdio(context.Background(), remote[4], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// TestSSHClientEndToEnd runs the whole chain: client, an ssh stand-in,
// dial-stdio, the socket, the server and the relay.
func TestSSHClientEndToEnd(t *testing.T) {
	r := newFakeRelay()
	r.operation = protocol.StackOperation{Name: "web", Operation: "up", Success: true, Output: "started"}
	f := startWithRelay(t, r)
	client, err := NewSSHClient(SSHTarget{User: "root", Host: "jp1.example.com", Port: 2222}, f.master.StateDir, helperSSH(t, "ok"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	list, err := client.List(ctx)
	if err != nil || len(list.Hosts) != 0 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	save := protocol.StackSave{Name: "web", ComposeYAML: "services: {}\n", Environment: map[string]string{"A": "1"}}
	if _, err := client.SaveStack(ctx, "jp1", save); err != nil {
		t.Fatal(err)
	}
	result, err := client.StackOperation(ctx, "jp1", "web", "up")
	if err != nil || result.Output != "started" {
		t.Fatalf("up = %+v, %v", result, err)
	}
	if got := r.saves[0].Environment["A"]; got != "1" {
		t.Fatalf("environment %q", got)
	}
	var args []string
	data, _ := os.ReadFile(os.Getenv("SORRY_PORTAINER_TEST_SSH_ARGS"))
	_ = json.Unmarshal(data, &args)
	if !slices.Contains(args, "root@jp1.example.com") || !slices.Contains(args, "2222") || !slices.Contains(args, "BatchMode=yes") {
		t.Fatalf("ssh args %q", args)
	}
}

func TestSSHClientReportsSSHFailures(t *testing.T) {
	client, err := NewSSHClient(SSHTarget{Host: "jp1"}, "/var/lib/sorry-portainer", helperSSH(t, "fail"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Permission denied (publickey)") {
		t.Fatalf("List = %v", err)
	}
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	b := &tailBuffer{limit: 8}
	_, _ = b.Write([]byte("0123456789"))
	_, _ = b.Write([]byte("ab"))
	if got := b.String(); got != "456789ab" {
		t.Fatalf("tail %q", got)
	}
}
