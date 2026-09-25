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
	manager := New(filepath.Join(work, "stacks"), prefix, NewComposeExecutor("/usr/bin/podman-compose"))
	manager.EnableSecrets(SecretSupport{HostID: hostID, Vault: vault, Containers: client})

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
	if _, err := manager.Save(ctx, "web", compose, nil, nil); err != nil {
		t.Fatal(err)
	}
	project := manager.project("web")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if result, err := manager.Down(cleanupCtx, "web"); err != nil {
			t.Errorf("clean up %s: %v: %s", project, err, result.Output)
		}
		// podman-compose 1.0.6 `down` leaves the project network behind.
		if out, err := exec.CommandContext(cleanupCtx, "podman", "network", "rm", project+"_default").CombinedOutput(); err != nil {
			t.Errorf("remove network: %v: %s", err, out)
		}
	})

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
