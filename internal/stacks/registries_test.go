package stacks

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/podman"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/registries"
)

// fakeLogins holds registry logins as the agent's vault would.
type fakeLogins struct{ logins []protocol.RegistryLogin }

func (f fakeLogins) LoginFor(ref string) string {
	keys := make([]string, len(f.logins))
	for i, login := range f.logins {
		keys[i] = login.Key
	}
	return registries.MatchIn(ref, keys)
}

func (f fakeLogins) Scrubber() *registries.Scrubber { return registries.NewScrubber(f.logins) }

const pinnedRef = "quay.io/libpod/busybox@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// refusal is what Podman 4.9 says when Docker Hub refuses an anonymous pull
// of a private repository.
const refusal = "podman pull: exit status 125: Error: initializing source docker://redis:7: reading manifest 7 in docker.io/library/redis: requested access to the resource is denied"

func TestUpPullsOnlyMissingImagesBeforeCompose(t *testing.T) {
	m, images, checker, executor := imageManager(t, nil)
	images.absent = map[string]bool{"redis:7": true, pinnedRef: true}
	result, err := m.Up(context.Background(), "homes")
	if err != nil || !result.Success {
		t.Fatalf("up = %+v, %v", result, err)
	}
	// File order; services that build or use variables are compose's.
	if want := []string{"redis:7", pinnedRef}; !reflect.DeepEqual(images.pulledSnapshot(), want) {
		t.Fatalf("pulled %v, want %v", images.pulledSnapshot(), want)
	}
	if !reflect.DeepEqual(checker.forgotten, []string{"redis:7", pinnedRef}) {
		t.Fatalf("forgotten = %v", checker.forgotten)
	}
	if calls := executor.callsSnapshot(); len(calls) != 1 || !slices.Contains(calls[0].args, "up") {
		t.Fatalf("compose calls = %+v", calls)
	}
	if !strings.HasPrefix(result.Output, "$ podman pull redis:7\n0123456789ab\n$ podman pull "+pinnedRef+"\n") {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestUpDoesNotPullImagesOfRunningContainers(t *testing.T) {
	m, images, _, _ := imageManager(t, []podman.ProjectContainer{
		{Name: "agent-homes_cache_1", Service: "cache", Image: "docker.io/library/redis:7", ImageID: "redis", State: "running"},
	})
	images.absent = map[string]bool{"docker.io/library/redis:7": true}
	if _, err := m.Up(context.Background(), "homes"); err != nil {
		t.Fatal(err)
	}
	if pulled := images.pulledSnapshot(); len(pulled) != 0 {
		t.Fatalf("pulled %v; a container already uses the image", pulled)
	}
}

func TestRefusedPullWaitsForALoginAndStartsWhenItArrives(t *testing.T) {
	ctx := context.Background()
	m, images, _, executor := imageManager(t, nil)
	m.SetBootID("boot-1")
	images.absent = map[string]bool{"redis:7": true}
	images.pullErrs = map[string]error{"redis:7": errors.New(refusal)}

	result, err := m.Up(ctx, "homes")
	var missing *missingLoginError
	if !errors.As(err, &missing) || missing.repository != "docker.io/library/redis" || result.Success {
		t.Fatalf("up = %+v, %v; want a missing login for docker.io/library/redis", result, err)
	}
	if calls := executor.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("compose ran after a refused pull: %+v", calls)
	}
	stack := stackByName(t, m, "homes")
	if stack.Desired != desiredUp || stack.Waiting != "waiting for registry login for docker.io" || stack.WaitingLogin != "docker.io/library/redis" {
		t.Fatalf("stack = desired %q, waiting %q, login %q", stack.Desired, stack.Waiting, stack.WaitingLogin)
	}

	// The login arrives: the next round pulls and starts the stack.
	delete(images.pullErrs, "redis:7")
	m.LoginsChanged(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for len(executor.callsSnapshot()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stack did not start after its login arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitForLoginRound(t, m)
	stack = stackByName(t, m, "homes")
	if stack.Waiting != "" || stack.WaitingLogin != "" {
		t.Fatalf("still waiting after the start: %q, %q", stack.Waiting, stack.WaitingLogin)
	}
}

// waitForLoginRound waits for LoginsChanged's background rounds to end.
func waitForLoginRound(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		m.mu.Lock()
		running := m.loginRound
		m.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the login round did not end")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLoginsChangedStartsOnlyStacksWaitingForALogin(t *testing.T) {
	ctx := context.Background()
	m, images, _, executor := imageManager(t, nil)
	m.SetBootID("boot-1")
	images.pullErrs = map[string]error{"redis:7": errors.New("podman pull: exit status 125: Error: dial tcp: i/o timeout")}
	images.absent = map[string]bool{"redis:7": true}
	_, err := m.Up(ctx, "homes")
	if err == nil || errors.As(err, new(*missingLoginError)) {
		t.Fatalf("up = %v, want a plain pull failure", err)
	}
	stack := stackByName(t, m, "homes")
	if !strings.HasPrefix(stack.Waiting, "start failed: pull redis:7: ") || stack.WaitingLogin != "" {
		t.Fatalf("waiting %q, login %q", stack.Waiting, stack.WaitingLogin)
	}
	delete(images.pullErrs, "redis:7")
	m.startWaitingForLogins(ctx)
	if calls := executor.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("a stack that does not wait for a login was started: %+v", calls)
	}
}

// secretEchoExecutor answers with a registry password, as a misbehaving
// Compose provider might.
type secretEchoExecutor struct{ recordingExecutor }

func (e *secretEchoExecutor) Run(ctx context.Context, args []string, dir string, env map[string]string) (string, error) {
	_, _ = e.recordingExecutor.Run(ctx, args, dir, env)
	return "login hunter2-secret ok", nil
}

func TestPodmanOutputIsScrubbed(t *testing.T) {
	ctx := context.Background()
	m, images, _, _ := imageManager(t, nil)
	m.EnableRegistryLogins(fakeLogins{logins: []protocol.RegistryLogin{{Key: "docker.io", Username: "bart", Password: []byte("hunter2-secret")}}})
	images.absent = map[string]bool{"redis:7": true}
	images.pullErrs = map[string]error{"redis:7": errors.New("podman pull: exit status 125: Error: dial tcp: proxy said hunter2-secret")}
	result, err := m.Up(ctx, "homes")
	if err == nil || strings.Contains(err.Error(), "hunter2") || strings.Contains(result.Output, "hunter2") {
		t.Fatalf("pull failure not scrubbed: %q, %v", result.Output, err)
	}
	if !strings.Contains(result.Output, registries.Mask) {
		t.Fatalf("output %q lacks the mask", result.Output)
	}

	delete(images.pullErrs, "redis:7")
	m.executor = &secretEchoExecutor{}
	result, err = m.Up(ctx, "homes")
	if err != nil || strings.Contains(result.Output, "hunter2") || !strings.Contains(result.Output, registries.Mask) {
		t.Fatalf("compose output not scrubbed: %q, %v", result.Output, err)
	}
}

func TestImagesReportTheLoginEachImageUses(t *testing.T) {
	m, _, _, _ := imageManager(t, nil)
	m.EnableRegistryLogins(fakeLogins{logins: []protocol.RegistryLogin{{Key: "docker.io/aeolun", Username: "bart", Password: []byte("x")}}})
	got, err := m.Images(context.Background(), "homes", false)
	if err != nil {
		t.Fatal(err)
	}
	logins := map[string]string{}
	for _, s := range got.Services {
		logins[s.Service] = s.Login
	}
	want := map[string]string{"app": "docker.io/aeolun", "worker": "docker.io/aeolun", "db": "", "cache": "", "pinned": "", "built": "", "templated": "", "mirror": ""}
	if !reflect.DeepEqual(logins, want) {
		t.Fatalf("logins = %v, want %v", logins, want)
	}
}

func TestStacksCannotChooseRegistryLogins(t *testing.T) {
	for _, key := range []string{"REGISTRY_AUTH_FILE", "DOCKER_CONFIG"} {
		if err := checkEnvironment(map[string]string{key: "/tmp/x"}); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("checkEnvironment(%s) = %v", key, err)
		}
	}
	base := []string{"PATH=/bin", "REGISTRY_AUTH_FILE=/run/user/1000/sorry-portainer/host-auth.json"}
	env := composeEnvironment(base, map[string]string{"REGISTRY_AUTH_FILE": "/tmp/mine.json", "DOCKER_CONFIG": "/tmp/docker", "APP": "1"}, "podman-compose")
	want := []string{"APP=1", "PATH=/bin", "PODMAN_COMPOSE_PROVIDER=podman-compose", "REGISTRY_AUTH_FILE=/run/user/1000/sorry-portainer/host-auth.json"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
}
