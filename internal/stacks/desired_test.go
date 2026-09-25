package stacks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/secrets"
)

const plainCompose = "services:\n  app:\n    image: x\n"

// failingExecutor fails every compose run.
type failingExecutor struct {
	mu    sync.Mutex
	calls int
}

func (e *failingExecutor) Run(context.Context, []string, string, map[string]string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return "Error: image not known", errors.New("exit status 1")
}

// operations lists the compose subcommand of each call, with its project.
func operations(e *recordingExecutor) []string {
	var out []string
	for _, call := range e.callsSnapshot() {
		out = append(out, call.args[1]+" "+call.args[4])
	}
	return out
}

func stackByName(t *testing.T, m *Manager, n string) protocol.Stack {
	t.Helper()
	list, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.Name == n {
			return s
		}
	}
	t.Fatalf("stack %s not listed", n)
	return protocol.Stack{}
}

func TestUpAndDownRecordDesiredState(t *testing.T) {
	ctx := context.Background()
	m := New(t.TempDir(), "agent-", &recordingExecutor{})
	m.SetBootID("boot-1")
	if _, err := m.Save(ctx, "web", plainCompose, nil, nil); err != nil {
		t.Fatal(err)
	}
	if s := stackByName(t, m, "web"); s.Desired != "" || s.Waiting != "" {
		t.Fatalf("new stack %+v", s)
	}
	if _, err := m.Up(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	state, err := m.readState("web")
	if err != nil || state != (stackState{Desired: desiredUp, UpBoot: "boot-1"}) {
		t.Fatalf("after up %+v %v", state, err)
	}
	if s, _ := m.Inspect(ctx, "web"); s.Desired != "up" || s.Waiting != "" {
		t.Fatalf("inspect %+v", s)
	}
	// Restart leaves the desired state alone.
	if _, err := m.Restart(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Down(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if s := stackByName(t, m, "web"); s.Desired != "down" || s.Waiting != "" {
		t.Fatalf("after down %+v", s)
	}
}

func TestFailedUpStillWantsUpAndSaysWhy(t *testing.T) {
	ctx := context.Background()
	executor := &failingExecutor{}
	m := New(t.TempDir(), "agent-", executor)
	m.SetBootID("boot-1")
	if _, err := m.Save(ctx, "web", plainCompose, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx, "web"); err == nil {
		t.Fatal("up succeeded")
	}
	s := stackByName(t, m, "web")
	if s.Desired != "up" || s.Waiting != "start failed: exit status 1" {
		t.Fatalf("after failed up %+v", s)
	}
	if _, err := m.Down(ctx, "web"); err == nil {
		t.Fatal("down succeeded")
	}
	if s := stackByName(t, m, "web"); s.Desired != "down" || s.Waiting != "" {
		t.Fatalf("after down %+v", s)
	}
}

func TestResumeBringsBackOnlyStacksNotUpSinceBoot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	before := New(root, "agent-", &recordingExecutor{})
	before.SetBootID("boot-1")
	for _, n := range []string{"up1", "up2", "downed", "never"} {
		if _, err := before.Save(ctx, n, plainCompose, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"up1", "up2", "downed"} {
		if _, err := before.Up(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := before.Down(ctx, "downed"); err != nil {
		t.Fatal(err)
	}

	// The agent restarts without a reboot: everything is where it was.
	restarted := &recordingExecutor{}
	sameBoot := New(root, "agent-", restarted)
	sameBoot.SetBootID("boot-1")
	sameBoot.Resume(ctx)
	if calls := operations(restarted); len(calls) != 0 {
		t.Fatalf("agent restart ran %v", calls)
	}

	// After a reboot, before Resume gets to them, the up stacks say so.
	rebooted := &recordingExecutor{}
	afterBoot := New(root, "agent-", rebooted)
	afterBoot.SetBootID("boot-2")
	if s := stackByName(t, afterBoot, "up1"); s.Waiting != "waiting to start after the agent started" {
		t.Fatalf("before resume %+v", s)
	}
	if s := stackByName(t, afterBoot, "downed"); s.Waiting != "" || s.Desired != "down" {
		t.Fatalf("downed %+v", s)
	}
	afterBoot.Resume(ctx)
	if calls := operations(rebooted); !reflect.DeepEqual(calls, []string{"agent-up1 up", "agent-up2 up"}) {
		t.Fatalf("reboot ran %v", calls)
	}
	for _, n := range []string{"up1", "up2"} {
		if s := stackByName(t, afterBoot, n); s.Desired != "up" || s.Waiting != "" {
			t.Fatalf("%s after resume %+v", n, s)
		}
	}
	// A second Resume in the same boot has nothing left to do.
	afterBoot.Resume(ctx)
	if calls := operations(rebooted); len(calls) != 2 {
		t.Fatalf("second resume ran %v", calls)
	}
}

func TestResumeWithoutBootIDDoesNothing(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	before := New(root, "agent-", &recordingExecutor{})
	before.SetBootID("boot-1")
	if _, err := before.Save(ctx, "web", plainCompose, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := before.Up(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	executor := &recordingExecutor{}
	unknown := New(root, "agent-", executor)
	unknown.Resume(ctx)
	if len(executor.callsSnapshot()) != 0 {
		t.Fatal("resumed without a boot ID")
	}
	if s := stackByName(t, unknown, "web"); s.Waiting != "" {
		t.Fatalf("waiting without a boot ID: %+v", s)
	}
}

func TestBrokenStateFileStillListsTheStack(t *testing.T) {
	ctx := context.Background()
	m := New(t.TempDir(), "agent-", &recordingExecutor{})
	m.SetBootID("boot-1")
	if _, err := m.Save(ctx, "web", plainCompose, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.dir("web"), stateFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := stackByName(t, m, "web"); !strings.Contains(s.Waiting, stateFile) {
		t.Fatalf("broken state %+v", s)
	}
	// Up refuses rather than overwrite state it cannot read.
	if _, err := m.Up(ctx, "web"); err == nil {
		t.Fatal("up over a broken state file")
	}
}

// TestRebootWaitsForSecretsThenStarts: after a reboot the agent's vault is
// empty, so a secret stack waits until the master pushes its values.
func TestRebootWaitsForSecretsThenStarts(t *testing.T) {
	ctx := context.Background()
	before, runtime, vault, _ := secretManager(t, nil)
	before.SetBootID("boot-1")
	values := map[string][]byte{"db": []byte("pw"), "token": []byte("tk")}
	if _, _, err := vault.Replace("web", values); err != nil {
		t.Fatal(err)
	}
	if _, err := before.Up(ctx, "web"); err != nil {
		t.Fatal(err)
	}

	after, _, executor := secretManagerAt(t, before.root, runtime, nil)
	after.SetBootID("boot-2")
	after.Resume(ctx)
	if calls := operations(executor); len(calls) != 0 {
		t.Fatalf("started without secrets: %v", calls)
	}
	s := stackByName(t, after, "web")
	if s.Waiting != "waiting for secrets db, token from the master (is it unlocked?)" {
		t.Fatalf("waiting %+v", s)
	}
	// A partial push is not enough.
	result, err := after.SyncSecrets(ctx, protocol.SecretSync{Stack: "web", Secrets: map[string][]byte{"db": []byte("pw")}})
	if err != nil || result.Started || len(operations(executor)) != 0 {
		t.Fatalf("partial push %+v %v", result, err)
	}
	result, err = after.SyncSecrets(ctx, protocol.SecretSync{Stack: "web", Secrets: map[string][]byte{"db": []byte("pw"), "token": []byte("tk")}})
	if err != nil || !result.Started || len(result.Problems) != 0 {
		t.Fatalf("full push %+v %v", result, err)
	}
	if calls := operations(executor); !reflect.DeepEqual(calls, []string{"agent-web up"}) {
		t.Fatalf("calls %v", calls)
	}
	if s := stackByName(t, after, "web"); s.Waiting != "" || s.Desired != "up" {
		t.Fatalf("after start %+v", s)
	}
	// Later pushes do not start it again.
	if result, _ := after.SyncSecrets(ctx, protocol.SecretSync{Stack: "web", Secrets: map[string][]byte{"db": []byte("pw"), "token": []byte("tk")}}); result.Started {
		t.Fatal("started twice")
	}
}

// TestAgentRestartKeepsContainersWithCurrentValues: the reconnect push after
// an agent restart looks like a change to the empty vault, but containers
// whose digest matches keep running.
func TestAgentRestartKeepsContainersWithCurrentValues(t *testing.T) {
	ctx := context.Background()
	before, runtime, vault, _ := secretManager(t, nil)
	before.SetBootID("boot-1")
	values := map[string][]byte{"db": []byte("pw"), "token": []byte("tk")}
	if _, _, err := vault.Replace("web", values); err != nil {
		t.Fatal(err)
	}
	if _, err := before.Up(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	// What the hook left in each container: app is current, worker's token
	// was rotated on the master while the agent was down.
	runtime.digests = map[int]string{
		101: mustDigest(t, []secrets.Mount{{Source: "db", Target: "db", Mode: secrets.DefaultMode}}, values),
		102: mustDigest(t, []secrets.Mount{{Source: "token", Target: "t", Mode: secrets.DefaultMode}}, values),
	}

	after, _, executor := secretManagerAt(t, before.root, runtime, nil)
	after.SetBootID("boot-1")
	after.Resume(ctx)
	result, err := after.SyncSecrets(ctx, protocol.SecretSync{Stack: "web", Secrets: map[string][]byte{"db": []byte("pw"), "token": []byte("rotated")}})
	if err != nil || len(result.Problems) != 0 || result.Started {
		t.Fatalf("push %+v %v", result, err)
	}
	if !reflect.DeepEqual(result.Changed, []string{"db", "token"}) {
		t.Fatalf("changed %v", result.Changed)
	}
	if !reflect.DeepEqual(result.Restarted, []string{"agent-web_worker_1"}) || !reflect.DeepEqual(runtime.restarted, []string{"id-worker"}) {
		t.Fatalf("restarted %v / %v", result.Restarted, runtime.restarted)
	}
	if calls := operations(executor); len(calls) != 0 {
		t.Fatalf("compose ran %v", calls)
	}
	// A container without a digest (started before digests existed) is
	// restarted, since the agent cannot tell what it holds.
	runtime.restarted = nil
	runtime.digests = nil
	if _, _, err := after.secrets.Vault.Replace("web", nil); err != nil {
		t.Fatal(err)
	}
	result, err = after.SyncSecrets(ctx, protocol.SecretSync{Stack: "web", Secrets: map[string][]byte{"db": []byte("pw"), "token": []byte("rotated")}})
	if err != nil || len(result.Restarted) != 2 {
		t.Fatalf("no digests %+v %v", result, err)
	}
}

// startedStacks saves and starts count stacks in boot "boot-1".
func startedStacks(b *testing.B, count int) *Manager {
	b.Helper()
	m := New(b.TempDir(), "agent", &recordingExecutor{})
	m.SetBootID("boot-1")
	for i := range count {
		name := fmt.Sprintf("stack-%04d", i)
		if _, err := m.Save(context.Background(), name, plainCompose, nil, nil); err != nil {
			b.Fatal(err)
		}
		if _, err := m.Up(context.Background(), name); err != nil {
			b.Fatal(err)
		}
	}
	return m
}

// BenchmarkListStartedStacks1000 is List with every stack's state file.
func BenchmarkListStartedStacks1000(b *testing.B) {
	m := startedStacks(b, 1000)
	for b.Loop() {
		stacks, err := m.List(context.Background())
		if err != nil || len(stacks) != 1000 || stacks[0].Desired != desiredUp {
			b.Fatalf("list: %d stacks, %v", len(stacks), err)
		}
	}
}

// BenchmarkResumeAfterAgentRestart1000 is the agent-restart case: every
// stack is already up this boot, so Resume only reads state files.
func BenchmarkResumeAfterAgentRestart1000(b *testing.B) {
	m := startedStacks(b, 1000)
	executor := &recordingExecutor{}
	restarted := New(m.root, "agent", executor)
	restarted.SetBootID("boot-1")
	for b.Loop() {
		restarted.Resume(context.Background())
	}
	if calls := len(executor.callsSnapshot()); calls != 0 {
		b.Fatalf("resume ran compose %d times", calls)
	}
}

func mustDigest(t *testing.T, mounts []secrets.Mount, values map[string][]byte) string {
	t.Helper()
	d, err := secrets.Digest(mounts, values)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
