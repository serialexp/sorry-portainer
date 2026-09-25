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

	"github.com/serialexp/sorry-portainer/internal/podman"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/secrets"
)

type fakeRuntime struct {
	mu         sync.Mutex
	containers []podman.ProjectContainer
	stopped    []string
	restarted  []string
	project    string
}

func (f *fakeRuntime) ProjectContainers(_ context.Context, project string) ([]podman.ProjectContainer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.project = project
	return append([]podman.ProjectContainer(nil), f.containers...), nil
}

func (f *fakeRuntime) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, id)
	return nil
}

func (f *fakeRuntime) Restart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarted = append(f.restarted, id)
	return nil
}

const managedSecretCompose = "services:\n  app:\n    image: x\n    secrets: [db]\n  worker:\n    image: x\n    secrets:\n      - source: token\n        target: /run/secrets/t\n  plain:\n    image: x\nsecrets:\n  db:\n  token:\n"

func secretManager(t *testing.T, verify func(int, []secrets.Mount) error) (*Manager, *fakeRuntime, *secrets.Vault, *recordingExecutor) {
	t.Helper()
	executor := &recordingExecutor{}
	m := New(t.TempDir(), "agent-", executor)
	runtime := &fakeRuntime{containers: []podman.ProjectContainer{
		{ID: "id-app", Name: "agent-web_app_1", Service: "app", State: "running", Pid: 101},
		{ID: "id-worker", Name: "agent-web_worker_1", Service: "worker", State: "running", Pid: 102},
		{ID: "id-plain", Name: "agent-web_plain_1", Service: "plain", State: "running", Pid: 103},
	}}
	vault := secrets.NewVault()
	if verify == nil {
		verify = func(int, []secrets.Mount) error { return nil }
	}
	m.EnableSecrets(SecretSupport{HostID: "h1", Vault: vault, Containers: runtime, Verify: verify})
	if _, err := m.Save(context.Background(), "web", managedSecretCompose, nil, nil); err != nil {
		t.Fatal(err)
	}
	return m, runtime, vault, executor
}

func TestSaveRejectsSecretsWithoutSupport(t *testing.T) {
	m := New(t.TempDir(), "agent-", &recordingExecutor{})
	if _, err := m.Save(context.Background(), "web", managedSecretCompose, nil, nil); !errors.Is(err, errSecretsUnsupported) {
		t.Fatalf("save: %v", err)
	}
	if _, err := m.Save(context.Background(), "bad", "services: {}\nsecrets:\n  s:\n    file: ./x\n", nil, nil); err == nil {
		t.Fatal("accepted file secret")
	}
}

func TestUpRequiresDeliveredSecrets(t *testing.T) {
	m, _, vault, executor := secretManager(t, nil)
	_, err := m.Up(context.Background(), "web")
	if err == nil || !strings.Contains(err.Error(), "db") || !strings.Contains(err.Error(), "token") {
		t.Fatalf("up without values: %v", err)
	}
	if len(executor.callsSnapshot()) != 0 {
		t.Fatal("compose ran without secrets")
	}
	// Down needs no values.
	if _, err := m.Down(context.Background(), "web"); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, _, err := vault.Replace("web", map[string][]byte{"db": []byte("pw"), "token": []byte("tk")}); err != nil {
		t.Fatal(err)
	}
	op, err := m.Up(context.Background(), "web")
	if err != nil || !op.Success {
		t.Fatalf("up: %+v %v", op, err)
	}
	calls := executor.callsSnapshot()
	if got := calls[len(calls)-1].args; !reflect.DeepEqual(got, []string{"-p", "agent-web", "-f", runtimeComposeFile, "up", "-d"}) {
		t.Fatalf("args %v", got)
	}
	runtime, err := os.ReadFile(filepath.Join(m.dir("web"), runtimeComposeFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(runtime), "pw") || strings.Contains(string(runtime), "tk") {
		t.Fatal("runtime compose contains a value")
	}
	deployed, err := deployedMounts(runtime)
	if err != nil || len(deployed) != 2 {
		t.Fatalf("deployed %+v %v", deployed, err)
	}
	stack, err := m.Inspect(context.Background(), "web")
	if err != nil || !reflect.DeepEqual(stack.Secrets, []string{"db", "token"}) {
		t.Fatalf("inspect secrets %v %v", stack.Secrets, err)
	}
}

func TestUpStopsContainersWithoutInjectedSecrets(t *testing.T) {
	verify := func(pid int, mounts []secrets.Mount) error {
		if pid == 102 {
			return errors.New("secrets [t] were not injected")
		}
		return nil
	}
	m, runtime, vault, _ := secretManager(t, verify)
	if _, _, err := vault.Replace("web", map[string][]byte{"db": []byte("pw"), "token": []byte("tk")}); err != nil {
		t.Fatal(err)
	}
	op, err := m.Up(context.Background(), "web")
	if err == nil || op.Success || !strings.Contains(op.Output, "agent-web_worker_1") {
		t.Fatalf("up: %+v %v", op, err)
	}
	if !reflect.DeepEqual(runtime.stopped, []string{"id-worker"}) {
		t.Fatalf("stopped %v", runtime.stopped)
	}
}

func TestUpReportsSecretContainerNotRunning(t *testing.T) {
	m, runtime, vault, _ := secretManager(t, nil)
	runtime.containers[0].State, runtime.containers[0].Pid = "exited", 0
	if _, _, err := vault.Replace("web", map[string][]byte{"db": []byte("pw"), "token": []byte("tk")}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(context.Background(), "web"); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("up: %v", err)
	}
}

func TestSyncRestartsOnlyAffectedRunningContainers(t *testing.T) {
	var verified []int
	var mu sync.Mutex
	verify := func(pid int, _ []secrets.Mount) error {
		mu.Lock()
		defer mu.Unlock()
		verified = append(verified, pid)
		return nil
	}
	m, runtime, _, _ := secretManager(t, verify)
	ctx := context.Background()
	// Before the stack was ever brought up, a sync only stores values.
	result, err := m.SyncSecrets(ctx, protocol.SecretSync{Stack: "web", Secrets: map[string][]byte{"db": []byte("pw"), "token": []byte("tk")}})
	if err != nil || len(result.Restarted) != 0 || !reflect.DeepEqual(result.Changed, []string{"db", "token"}) {
		t.Fatalf("first sync %+v %v", result, err)
	}
	if _, err := m.Up(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	verified = nil
	// Same values: nothing restarts.
	result, err = m.SyncSecrets(ctx, protocol.SecretSync{Stack: "web", Secrets: map[string][]byte{"db": []byte("pw"), "token": []byte("tk")}})
	if err != nil || len(result.Changed) != 0 || len(runtime.restarted) != 0 {
		t.Fatalf("unchanged sync %+v %v %v", result, err, runtime.restarted)
	}
	// Rotating db restarts only app.
	result, err = m.SyncSecrets(ctx, protocol.SecretSync{Stack: "web", Secrets: map[string][]byte{"db": []byte("new"), "token": []byte("tk")}})
	if err != nil || !reflect.DeepEqual(result.Restarted, []string{"agent-web_app_1"}) || len(result.Problems) != 0 {
		t.Fatalf("rotate %+v %v", result, err)
	}
	if !reflect.DeepEqual(runtime.restarted, []string{"id-app"}) || !reflect.DeepEqual(verified, []int{101}) {
		t.Fatalf("restarted %v verified %v", runtime.restarted, verified)
	}
	// Removing token restarts nothing and reports it.
	result, err = m.SyncSecrets(ctx, protocol.SecretSync{Stack: "web", Secrets: map[string][]byte{"db": []byte("new")}})
	if err != nil || !reflect.DeepEqual(result.Removed, []string{"token"}) || len(runtime.restarted) != 1 {
		t.Fatalf("remove %+v %v", result, err)
	}
}

func TestSyncSkipsStoppedContainersAndReportsRestartProblems(t *testing.T) {
	m, runtime, vault, _ := secretManager(t, func(int, []secrets.Mount) error { return errors.New("not injected") })
	if _, _, err := vault.Replace("web", map[string][]byte{"db": []byte("pw"), "token": []byte("tk")}); err != nil {
		t.Fatal(err)
	}
	// Bring it up with a passing check, then make checks fail.
	m.secrets.Verify = func(int, []secrets.Mount) error { return nil }
	if _, err := m.Up(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	m.secrets.Verify = func(int, []secrets.Mount) error { return errors.New("not injected") }
	runtime.containers[1].State = "exited"
	result, err := m.SyncSecrets(context.Background(), protocol.SecretSync{Stack: "web", Secrets: map[string][]byte{"db": []byte("x"), "token": []byte("y")}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Restarted, []string{"agent-web_app_1"}) || len(result.Problems) != 1 || !strings.Contains(result.Problems[0], "not injected") {
		t.Fatalf("result %+v", result)
	}
	if !reflect.DeepEqual(runtime.stopped, []string{"id-app"}) {
		t.Fatalf("stopped %v", runtime.stopped)
	}
}

func TestRetainSecrets(t *testing.T) {
	m, _, vault, _ := secretManager(t, nil)
	for _, stack := range []string{"web", "db", "old"} {
		if _, _, err := vault.Replace(stack, map[string][]byte{"s": []byte("v")}); err != nil {
			t.Fatal(err)
		}
	}
	forgotten, err := m.RetainSecrets(protocol.SecretRetain{Stacks: []string{"web", "db"}})
	if err != nil || !reflect.DeepEqual(forgotten, []string{"old"}) {
		t.Fatalf("forgotten %v %v", forgotten, err)
	}
	if missing := vault.Missing("old", []string{"s"}); len(missing) != 1 {
		t.Fatal("old stack kept")
	}
}
