package stacks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func writePodman(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "podman")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestComposeExecutorUsesPodmanComposeAndPinnedProvider(t *testing.T) {
	bin := writePodman(t, `printf 'args:'; printf '<%s>' "$@"; printf '\nprovider:%s\nstack:%s\n' "$PODMAN_COMPOSE_PROVIDER" "$STACK_VALUE"`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	out, err := NewComposeExecutor("/opt/pinned/podman-compose").Run(context.Background(), []string{"--file", "compose.yaml", "up", "-d"}, t.TempDir(), map[string]string{
		"STACK_VALUE":             "from-stack",
		"PODMAN_COMPOSE_PROVIDER": "attempted-override",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "args:<compose><--file><compose.yaml><up><-d>\nprovider:/opt/pinned/podman-compose\nstack:from-stack\n"
	if out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
}

func TestComposeExecutorUsesDefaultProvider(t *testing.T) {
	bin := writePodman(t, `printf %s "$PODMAN_COMPOSE_PROVIDER"`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	out, err := (ComposeExecutor{}).Run(context.Background(), nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != defaultComposeProvider {
		t.Errorf("provider = %q, want %q", out, defaultComposeProvider)
	}
}

func TestComposeExecutorCapsOutputDuringExecution(t *testing.T) {
	bin := writePodman(t, `head -c 2097152 /dev/zero | tr '\000' x`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	out, err := NewComposeExecutor("").Run(context.Background(), nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != maxComposeOutput {
		t.Errorf("output length = %d, want %d", len(out), maxComposeOutput)
	}
}

func TestComposeExecutorHonorsCancellation(t *testing.T) {
	bin := writePodman(t, `sleep 10`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := NewComposeExecutor("").Run(ctx, nil, t.TempDir(), nil)
	if err == nil {
		t.Fatal("Run returned nil error after context cancellation")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("context error = %v, want deadline exceeded", ctx.Err())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("Run took %s after cancellation", elapsed)
	}
}

func TestManagerSaveAndOperations(t *testing.T) {
	executor := &recordingExecutor{}
	m := New(t.TempDir(), "agent_prefix", executor)
	const composeYAML = "services: {}\n"
	if _, err := m.Save(context.Background(), "web_1", composeYAML, map[string]string{"ANSWER": "42"}, nil); err != nil {
		t.Fatal(err)
	}
	stack, err := m.Inspect(context.Background(), "web_1")
	if err != nil {
		t.Fatal(err)
	}
	if stack.ComposeYAML != composeYAML || stack.Project != "agent-prefix-web_1" || stack.Status != "saved" {
		t.Errorf("Inspect = %#v", stack)
	}

	for _, op := range []struct {
		name string
		run  func(context.Context, string) (any, error)
	}{
		{"up", func(ctx context.Context, name string) (any, error) { return m.Up(ctx, name) }},
		{"down", func(ctx context.Context, name string) (any, error) { return m.Down(ctx, name) }},
		{"restart", func(ctx context.Context, name string) (any, error) { return m.Restart(ctx, name) }},
	} {
		if _, err := op.run(context.Background(), "web_1"); err != nil {
			t.Fatalf("%s: %v", op.name, err)
		}
	}

	calls := executor.callsSnapshot()
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	base := []string{"-p", "agent-prefix-web_1", "-f", "compose.yaml"}
	wants := [][]string{append(append([]string{}, base...), "up", "-d"), append(append([]string{}, base...), "down"), append(append([]string{}, base...), "restart")}
	for i, call := range calls {
		if !reflect.DeepEqual(call.args, wants[i]) {
			t.Errorf("call %d args = %#v, want %#v", i, call.args, wants[i])
		}
		if call.dir != filepath.Join(m.root, "web_1") || call.env["ANSWER"] != "42" {
			t.Errorf("call %d = %#v", i, call)
		}
	}
}

func TestManagerOperationSetsTimeout(t *testing.T) {
	executor := &deadlineExecutor{}
	m := New(t.TempDir(), "agent", executor)
	if _, err := m.Save(context.Background(), "web", "services: {}", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	if executor.deadline.IsZero() {
		t.Fatal("executor context has no deadline")
	}
	remaining := time.Until(executor.deadline)
	if remaining < 4*time.Minute || remaining > 5*time.Minute {
		t.Errorf("operation timeout remaining = %s, want approximately 5m", remaining)
	}
}

func TestManagerRejectsInvalidNames(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	for _, name := range []string{"", "../escape", "UPPER", "white space", strings.Repeat("a", 64)} {
		if ValidName(name) {
			t.Errorf("ValidName(%q) = true", name)
		}
		if _, err := m.Save(context.Background(), name, "services: {}", nil, nil); err == nil {
			t.Errorf("Save(%q) accepted an invalid name", name)
		}
		if _, err := m.Up(context.Background(), name); err == nil {
			t.Errorf("Up(%q) accepted an invalid name", name)
		}
	}
}

func TestManagerSerializesOperationsForOneStack(t *testing.T) {
	executor := &blockingExecutor{started: make(chan struct{}, 2), release: make(chan struct{})}
	m := New(t.TempDir(), "agent", executor)
	if _, err := m.Save(context.Background(), "web", "services: {}", nil, nil); err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { _, err := m.Up(context.Background(), "web"); firstDone <- err }()
	<-executor.started
	go func() { _, err := m.Down(context.Background(), "web"); secondDone <- err }()
	select {
	case <-executor.started:
		t.Fatal("second operation entered executor before the first completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(executor.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	<-executor.started
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

type executorCall struct {
	args []string
	dir  string
	env  map[string]string
}
type recordingExecutor struct {
	mu    sync.Mutex
	calls []executorCall
}

func (e *recordingExecutor) Run(_ context.Context, args []string, dir string, env map[string]string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	copyEnv := make(map[string]string, len(env))
	for k, v := range env {
		copyEnv[k] = v
	}
	e.calls = append(e.calls, executorCall{append([]string(nil), args...), dir, copyEnv})
	return "ok", nil
}
func (e *recordingExecutor) callsSnapshot() []executorCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]executorCall(nil), e.calls...)
}

type blockingExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (e *blockingExecutor) Run(context.Context, []string, string, map[string]string) (string, error) {
	e.started <- struct{}{}
	<-e.release
	return "", nil
}

type deadlineExecutor struct{ deadline time.Time }

func (e *deadlineExecutor) Run(ctx context.Context, _ []string, _ string, _ map[string]string) (string, error) {
	e.deadline, _ = ctx.Deadline()
	return "", nil
}
