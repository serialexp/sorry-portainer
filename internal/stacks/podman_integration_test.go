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
