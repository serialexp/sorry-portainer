package stacks

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/podman"
)

// taggingImages lists the real host's images but "pulls" by answering the
// ID the tag names: the test moves the tag itself, as a registry push
// followed by a pull would.
type taggingImages struct{ *podman.Client }

func (taggingImages) Pull(ctx context.Context, ref string) (string, error) {
	out, err := exec.CommandContext(ctx, "podman", "image", "inspect", "--format", "{{.Id}}", ref).Output()
	return strings.TrimSpace(string(out)), err
}

// TestRealPodmanUpdateRecreatesOnlyTheService: after the tag moves, an
// update recreates the service's container from the new image with real
// podman-compose 1.0.6, and leaves a service it does not depend on alone.
// Plain `up -d app` would not: the Compose file, and so its hash, did not
// change.
func TestRealPodmanUpdateRecreatesOnlyTheService(t *testing.T) {
	if os.Getenv("SORRY_PORTAINER_TEST_PODMAN") != "1" {
		t.Skip("set SORRY_PORTAINER_TEST_PODMAN=1 to run the rootless Podman integration test")
	}
	const base = "quay.io/libpod/busybox:latest"
	if output, err := exec.Command("podman", "image", "exists", base).CombinedOutput(); err != nil {
		t.Fatalf("image %s not present: %v: %s", base, err, output)
	}
	stamp := strings.ToLower(time.Now().Format("150405"))
	tag := "quay.io/sorry-portainer-test/update-" + stamp + ":latest"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	podmanOut := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("podman %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	podmanOut("tag", base, tag)
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", "--force", tag).Run() })

	prefix := "sorry-podman-update-" + stamp + "-"
	manager := New(t.TempDir(), prefix, NewComposeExecutor("/usr/bin/podman-compose"))
	client, err := podman.NewWithPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	manager.EnableRuntimeStatus(client)
	manager.EnableImageChecks(taggingImages{client}, &fakeChecker{})
	compose := "services:\n  app:\n    image: " + tag + "\n    command: [\"sleep\", \"120\"]\n  other:\n    image: " + base + "\n    command: [\"sleep\", \"120\"]\n"
	if _, err := manager.Save(ctx, "upd", compose, nil, nil); err != nil {
		t.Fatal(err)
	}
	project := manager.project("upd")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if result, err := manager.Down(cleanupCtx, "upd"); err != nil {
			t.Errorf("clean up %s: %v: %s", project, err, result.Output)
		}
	})
	if result, err := manager.Up(ctx, "upd"); err != nil {
		t.Fatalf("up: %v: %s", err, result.Output)
	}
	inspect := func(container string) (id, image string) {
		t.Helper()
		fields := strings.Fields(podmanOut("container", "inspect", "--format", "{{.Id}} {{.Image}}", container))
		if len(fields) != 2 {
			t.Fatalf("inspect %s: %v", container, fields)
		}
		return fields[0], fields[1]
	}
	appBefore, _ := inspect(project + "_app_1")
	otherBefore, _ := inspect(project + "_other_1")

	// Move the tag to a new image, as a push and pull would.
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/Containerfile", []byte("FROM "+base+"\nLABEL sorry-portainer-test="+stamp+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	podmanOut("build", "--pull=never", "--quiet", "--tag", tag, dir)
	newImage := podmanOut("image", "inspect", "--format", "{{.Id}}", tag)

	result, err := manager.Update(ctx, "upd", "app")
	if err != nil || !result.Success {
		t.Fatalf("update: %v: %s", err, result.Output)
	}
	appAfter, appImage := inspect(project + "_app_1")
	otherAfter, _ := inspect(project + "_other_1")
	if appAfter == appBefore || appImage != newImage {
		t.Fatalf("app container %s → %s runs %s, want a new container from %s\n%s", appBefore, appAfter, appImage, newImage, result.Output)
	}
	if otherAfter != otherBefore {
		t.Fatalf("other was recreated (%s → %s)\n%s", otherBefore, otherAfter, result.Output)
	}
	if state := podmanOut("container", "inspect", "--format", "{{.State.Status}}", project+"_app_1"); state != "running" {
		t.Fatalf("app is %s after the update", state)
	}
}
