package stacks

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Run with SORRY_PORTAINER_TEST_PODMAN=1 under a disposable rootless Podman
// account. It starts one tiny container and always brings its project down.
func TestRealPodmanComposeStack(t *testing.T) {
	if os.Getenv("SORRY_PORTAINER_TEST_PODMAN") != "1" {
		t.Skip("set SORRY_PORTAINER_TEST_PODMAN=1 to run the rootless Podman integration test")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Fatal(err)
	}
	const image = "quay.io/libpod/busybox:latest"
	// The integration test must never pull implicitly; run it after preparing
	// this disposable public image in the intended rootless account.
	if output, err := exec.Command("podman", "image", "exists", image).CombinedOutput(); err != nil {
		t.Fatalf("image %s not present: %v: %s", image, err, output)
	}
	prefix := "sorry-podman-test-" + strings.ToLower(time.Now().Format("150405")) + "-"
	manager := New(t.TempDir(), prefix, NewComposeExecutor("/usr/bin/podman-compose"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := manager.Save(ctx, "smoke", "services:\n  sleeper:\n    image: "+image+"\n    command: [\"sleep\", \"120\"]\n", nil, nil); err != nil {
		t.Fatal(err)
	}
	project := manager.project("smoke")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if result, err := manager.Down(cleanupCtx, "smoke"); err != nil {
			t.Errorf("clean up %s: %v: %s", project, err, result.Output)
		}
	})
	result, err := manager.Up(ctx, "smoke")
	if err != nil {
		t.Fatalf("up %s: %v: %s", project, err, result.Output)
	}
	output, err := exec.CommandContext(ctx, "podman", "ps", "--filter", "label=io.podman.compose.project="+project, "--format", "{{.Names}}").CombinedOutput()
	if err != nil || !strings.Contains(string(output), project+"_sleeper_1") {
		t.Fatalf("project %s not running: %v: %s", project, err, output)
	}
}

// TestRealPodmanEnvironmentChangeRecreates: saving a new environment for a
// running stack recreates its container with the new value. podman-compose
// hashes the compose file after substitution, so `up -d` notices.
func TestRealPodmanEnvironmentChangeRecreates(t *testing.T) {
	if os.Getenv("SORRY_PORTAINER_TEST_PODMAN") != "1" {
		t.Skip("set SORRY_PORTAINER_TEST_PODMAN=1 to run the rootless Podman integration test")
	}
	const image = "quay.io/libpod/busybox:latest"
	if output, err := exec.Command("podman", "image", "exists", image).CombinedOutput(); err != nil {
		t.Fatalf("image %s not present: %v: %s", image, err, output)
	}
	prefix := "sorry-podman-env-" + strings.ToLower(time.Now().Format("150405")) + "-"
	manager := New(t.TempDir(), prefix, NewComposeExecutor("/usr/bin/podman-compose"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	compose := "services:\n  sleeper:\n    image: " + image + "\n    command: [\"sleep\", \"120\"]\n    environment:\n      MODE: ${MODE}\n"
	if _, err := manager.Save(ctx, "env", compose, map[string]string{"MODE": "first"}, nil); err != nil {
		t.Fatal(err)
	}
	project := manager.project("env")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if result, err := manager.Down(cleanupCtx, "env"); err != nil {
			t.Errorf("clean up %s: %v: %s", project, err, result.Output)
		}
	})
	if result, err := manager.Up(ctx, "env"); err != nil {
		t.Fatalf("up: %v: %s", err, result.Output)
	}
	containerEnv := func() string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "podman", "exec", project+"_sleeper_1", "printenv", "MODE").CombinedOutput()
		if err != nil {
			t.Fatalf("printenv: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := containerEnv(); got != "first" {
		t.Fatalf("MODE = %q, want first", got)
	}
	one := 1
	saved, err := manager.Save(ctx, "env", compose, map[string]string{"MODE": "second"}, &one)
	if err != nil || saved.Operation == nil || !saved.Operation.Success {
		t.Fatalf("save = %+v, %v", saved, err)
	}
	if got := containerEnv(); got != "second" {
		t.Fatalf("MODE after save = %q, want second", got)
	}
}
