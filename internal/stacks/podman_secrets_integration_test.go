package stacks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/agentsetup"
	"github.com/serialexp/sorry-portainer/internal/podman"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/secrets"
)

// TestRealPodmanStackSecrets runs the whole secret path against rootless
// Podman: the setup drop-in (in a throwaway XDG_CONFIG_HOME), the real agent
// binary as OCI hook, podman-compose up, the post-start check, rotation, and
// a restart by Podman's restart policy. It uses random canary values and
// checks they reach neither Podman's disk storage nor `podman inspect`.
//
// Run with SORRY_PORTAINER_TEST_PODMAN=1 in a disposable rootless account
// that has quay.io/libpod/busybox:latest.
func TestRealPodmanStackSecrets(t *testing.T) {
	if os.Getenv("SORRY_PORTAINER_TEST_PODMAN") != "1" {
		t.Skip("set SORRY_PORTAINER_TEST_PODMAN=1 to run the rootless Podman integration test")
	}
	const image = "quay.io/libpod/busybox:latest"
	if output, err := exec.Command("podman", "image", "exists", image).CombinedOutput(); err != nil {
		t.Fatalf("image %s not present: %v: %s", image, err, output)
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		t.Fatal("XDG_RUNTIME_DIR is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	work := t.TempDir()
	agentBinary := filepath.Join(work, "sorry-portainer-agent")
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", agentBinary, "../../cmd/sorry-portainer-agent")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent: %v: %s", err, output)
	}

	// Setup's drop-in, in a throwaway config home Podman reads instead of ~/.config.
	configHome := filepath.Join(work, "config")
	hooksDir := filepath.Join(work, "oci-hooks")
	dropIn, err := agentsetup.HooksDropIn(hooksDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(configHome, "containers", "containers.conf.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "containers", "containers.conf.d", agentsetup.HooksDropInName), []byte(dropIn), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)

	stamp := strings.ToLower(time.Now().Format("150405"))
	hostID := "sp-secret-test-" + stamp
	socketDir, err := os.MkdirTemp(runtimeDir, "sp-secret-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "agent.sock")
	if _, err := secrets.WriteHookConfig(hooksDir, agentBinary, hostID, socket); err != nil {
		t.Fatal(err)
	}
	listener, err := secrets.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	vault := secrets.NewVault()
	go func() {
		_ = (&secrets.SocketServer{HostID: hostID, Vault: vault, Logf: t.Logf}).Serve(t.Context(), listener)
	}()

	prefix := hostID + "-"
	client, err := podman.NewWithPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	stacksRoot := filepath.Join(work, "stacks")
	// startAgent builds a manager the way a starting agent does. The socket
	// server keeps serving vault; emptying vault stands in for the fresh
	// memory of a restarted agent.
	startAgent := func(boot string) *Manager {
		m := New(stacksRoot, prefix, NewComposeExecutor("/usr/bin/podman-compose"))
		m.EnableSecrets(SecretSupport{HostID: hostID, Vault: vault, Containers: client})
		m.Logf = t.Logf
		m.SetBootID(boot)
		return m
	}
	manager := startAgent("boot-1")

	compose := `services:
  app:
    image: ` + image + `
    restart: on-failure
    command: ["sh", "-c", "rm -f /tmp/fail; echo started; while [ ! -e /tmp/fail ]; do sleep 0.1; done; exit 3"]
    secrets:
      - db_password
      - source: api_key
        target: /run/secrets/key
        uid: "1000"
        gid: "1000"
        mode: 0400
  plain:
    image: ` + image + `
    command: ["sleep", "3600"]
secrets:
  db_password: {}
  api_key: {}
`
	// solo has no secrets, so it comes back right after a reboot.
	solo := "services:\n  sleeper:\n    image: " + image + "\n    command: [\"sleep\", \"3600\"]\n"
	for name, source := range map[string]string{"web": compose, "solo": solo} {
		if _, err := manager.Save(ctx, name, source, nil, nil); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
			defer cleanupCancel()
			if result, err := manager.Down(cleanupCtx, name); err != nil {
				t.Errorf("clean up %s: %v: %s", name, err, result.Output)
			}
			// podman-compose 1.0.6 `down` leaves the project network behind.
			if out, err := exec.CommandContext(cleanupCtx, "podman", "network", "rm", manager.project(name)+"_default").CombinedOutput(); err != nil {
				t.Errorf("remove network: %v: %s", err, out)
			}
		})
	}
	project := manager.project("web")
	if result, err := manager.Up(ctx, "solo"); err != nil {
		t.Fatalf("up solo: %v\n%s", err, result.Output)
	}

	canary := func() []byte {
		b := make([]byte, 12)
		_, _ = rand.Read(b)
		return []byte("SPCANARY" + hex.EncodeToString(b))
	}
	db, key := canary(), canary()
	if _, err := manager.SyncSecrets(ctx, protocol.SecretSync{Stack: "web", Secrets: map[string][]byte{"db_password": append([]byte(nil), db...), "api_key": append([]byte(nil), key...)}}); err != nil {
		t.Fatal(err)
	}
	if result, err := manager.Up(ctx, "web"); err != nil {
		t.Fatalf("up: %v\n%s", err, result.Output)
	}
	app := project + "_app_1"
	exec1 := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "podman", append([]string{"exec", app}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("podman exec %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	if got := exec1("cat", "/run/secrets/db_password"); got != string(db) {
		t.Fatalf("db_password = %q", got)
	}
	if got := exec1("stat", "-c", "%u:%g %a", "/run/secrets/key"); strings.TrimSpace(got) != "1000:1000 400" {
		t.Fatalf("key owner/mode = %q", got)
	}
	if got := exec1("sh", "-c", "grep ' /run/secrets ' /proc/mounts"); !strings.Contains(got, "tmpfs") {
		t.Fatalf("mounts: %q", got)
	}
	assertNotStored(ctx, t, app, db, key)

	// Rotation restarts the container and injects the new value.
	newDB := canary()
	result, err := manager.SyncSecrets(ctx, protocol.SecretSync{Stack: "web", Secrets: map[string][]byte{"db_password": append([]byte(nil), newDB...), "api_key": append([]byte(nil), key...)}})
	if err != nil || len(result.Restarted) != 1 || len(result.Problems) != 0 {
		t.Fatalf("rotate: %+v %v", result, err)
	}
	if got := exec1("cat", "/run/secrets/db_password"); got != string(newDB) {
		t.Fatalf("rotated db_password = %q", got)
	}

	// Podman's restart policy restarts the app after a failure exit; the hook
	// must run on that path too. (`podman kill` would count as a user stop,
	// which the policy ignores, so the app fails by itself.)
	exec1("touch", "/tmp/fail")
	deadline := time.Now().Add(30 * time.Second)
	for {
		logs, _ := exec.CommandContext(ctx, "podman", "logs", app).CombinedOutput()
		state, _ := exec.CommandContext(ctx, "podman", "inspect", "--format", "{{.State.Status}} {{.RestartCount}}", app).CombinedOutput()
		if strings.Count(string(logs), "started") >= 3 && strings.HasPrefix(string(state), "running") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restart policy did not restart the app: logs=%q state=%q", logs, state)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got := exec1("cat", "/run/secrets/db_password"); got != string(newDB) {
		t.Fatalf("after policy restart db_password = %q", got)
	}
	assertNotStored(ctx, t, app, db, newDB, key)
	if got := exec1("ls", "-a", "/run/secrets"); !strings.Contains(got, secrets.DigestFile) {
		t.Fatalf("no digest in the tmpfs: %q", got)
	}

	inspect := func(container, format string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "podman", "inspect", "--format", format, container).CombinedOutput()
		if err != nil {
			t.Fatalf("inspect %s: %v: %s", container, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	current := map[string][]byte{"db_password": newDB, "api_key": key}
	push := func(m *Manager) protocol.SecretSyncResult {
		t.Helper()
		values := map[string][]byte{}
		for name, value := range current {
			values[name] = append([]byte(nil), value...)
		}
		result, err := m.SyncSecrets(ctx, protocol.SecretSync{Stack: "web", Secrets: values})
		if err != nil || len(result.Problems) != 0 {
			t.Fatalf("push: %+v %v", result, err)
		}
		return result
	}

	// Agent restart in the same boot: its vault starts empty, so the
	// reconnect push changes every name, but the containers already hold the
	// values (the digest matches) and keep running untouched.
	startedAt := inspect(app, "{{.State.StartedAt}}")
	if _, _, err := vault.Replace("web", nil); err != nil {
		t.Fatal(err)
	}
	restarted := startAgent("boot-1")
	restarted.Resume(ctx)
	if result := push(restarted); len(result.Changed) != 2 || len(result.Restarted) != 0 || result.Started {
		t.Fatalf("push after agent restart: %+v", result)
	}
	if again := inspect(app, "{{.State.StartedAt}}"); again != startedAt {
		t.Fatalf("app restarted after an agent restart: %s -> %s", startedAt, again)
	}

	// Host reboot: every container is stopped and the agent's vault is
	// empty. Resume brings solo back at once; web waits for its secrets and
	// starts when the master pushes them.
	sleeper := manager.project("solo") + "_sleeper_1"
	for _, container := range []string{app, project + "_plain_1", sleeper} {
		if out, err := exec.CommandContext(ctx, "podman", "stop", "-t", "1", container).CombinedOutput(); err != nil {
			t.Fatalf("stop %s: %v: %s", container, err, out)
		}
	}
	if _, _, err := vault.Replace("web", nil); err != nil {
		t.Fatal(err)
	}
	rebooted := startAgent("boot-2")
	rebooted.Resume(ctx)
	if state := inspect(sleeper, "{{.State.Status}}"); state != "running" {
		t.Fatalf("solo after reboot: %s", state)
	}
	if state := inspect(app, "{{.State.Status}}"); state == "running" {
		t.Fatal("web started without its secrets")
	}
	web, err := rebooted.Inspect(ctx, "web")
	if err != nil || !strings.HasPrefix(web.Waiting, "waiting for secrets api_key, db_password") {
		t.Fatalf("web while waiting: %+v %v", web, err)
	}
	if result := push(rebooted); !result.Started {
		t.Fatalf("push after reboot did not start web: %+v", result)
	}
	for _, container := range []string{app, project + "_plain_1"} {
		if state := inspect(container, "{{.State.Status}}"); state != "running" {
			t.Fatalf("%s after the push: %s", container, state)
		}
	}
	if got := exec1("cat", "/run/secrets/db_password"); got != string(newDB) {
		t.Fatalf("after reboot db_password = %q", got)
	}
	if web, _ := rebooted.Inspect(ctx, "web"); web.Waiting != "" || web.Desired != "up" {
		t.Fatalf("web after start: %+v", web)
	}

	// Without its values the container must not start at all.
	if _, _, err := vault.Replace("web", nil); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, "podman", "restart", app).CombinedOutput(); err == nil {
		t.Fatalf("container restarted without its secrets: %s", out)
	}
}

// assertNotStored checks that no canary appears in `podman inspect` or in the
// container's directories in Podman's disk and run storage.
func assertNotStored(ctx context.Context, t *testing.T, container string, values ...[]byte) {
	t.Helper()
	inspect, err := exec.CommandContext(ctx, "podman", "inspect", container).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect: %v: %s", err, inspect)
	}
	id, err := exec.CommandContext(ctx, "podman", "inspect", "--format", "{{.Id}}", container).Output()
	if err != nil {
		t.Fatal(err)
	}
	info, err := exec.CommandContext(ctx, "podman", "info", "--format", "{{.Store.GraphRoot}} {{.Store.RunRoot}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	roots := strings.Fields(string(info))
	containerID := strings.TrimSpace(string(id))
	if len(roots) != 2 || containerID == "" {
		t.Fatalf("unexpected podman info %q / id %q", info, id)
	}
	var dirs []string
	for _, root := range roots {
		dir := filepath.Join(root, "overlay-containers", containerID, "userdata")
		// A missing directory would make the scan below vacuous.
		if out, err := exec.CommandContext(ctx, "podman", "unshare", "test", "-d", dir).CombinedOutput(); err != nil {
			t.Fatalf("container storage %s missing: %v: %s", dir, err, out)
		}
		dirs = append(dirs, dir)
	}
	for _, value := range values {
		if strings.Contains(string(inspect), string(value)) {
			t.Fatal("podman inspect shows a secret value")
		}
		// grep exits 1 when nothing matches, 0 on a match, 2 on errors.
		args := append([]string{"unshare", "grep", "-rlF", string(value)}, dirs...)
		out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("secret value found in Podman storage, or scan failed: %v: %s", err, out)
		}
	}
}
