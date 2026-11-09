package podman

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Run with SORRY_PORTAINER_TEST_PODMAN=1 under a disposable rootless Podman
// account with quay.io/libpod/busybox:latest present. It runs two tiny
// containers, one that the restart policy keeps restarting and one whose
// health check fails, and always removes both.
func TestRealPodmanRecentActivity(t *testing.T) {
	if os.Getenv("SORRY_PORTAINER_TEST_PODMAN") != "1" {
		t.Skip("set SORRY_PORTAINER_TEST_PODMAN=1 to run the rootless Podman integration test")
	}
	const image = "quay.io/libpod/busybox:latest"
	if output, err := exec.Command("podman", "image", "exists", image).CombinedOutput(); err != nil {
		t.Fatalf("image %s not present: %v: %s", image, err, output)
	}
	prefix := "sorry-events-test-" + time.Now().Format("150405") + "-"
	flappy, sick := prefix+"flappy", prefix+"sick"
	t.Cleanup(func() {
		if output, err := exec.Command("podman", "rm", "--force", "--time", "0", flappy, sick).CombinedOutput(); err != nil {
			t.Errorf("remove test containers: %v: %s", err, output)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	podman := func(args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("podman %s: %v: %s", strings.Join(args, " "), err, output)
		}
		return string(output)
	}
	podman("run", "-d", "--name", flappy, "--restart", "always", image, "sh", "-c", "sleep 1; exit 1")
	podman("run", "-d", "--name", sick, "--health-cmd", "false", "--health-retries", "1", "--health-interval", "1h", image, "sleep", "300")
	// Health timers need a reachable systemd user manager; run the check
	// directly so the test does not depend on it. With one retry, one failure
	// makes the container unhealthy, and the command exits 1.
	if output, err := exec.CommandContext(ctx, "podman", "healthcheck", "run", sick).CombinedOutput(); err == nil || strings.TrimSpace(string(output)) != "unhealthy" {
		t.Fatalf("healthcheck run = %v: %s", err, output)
	}
	client, err := NewWithPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		containers, err := client.Containers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		byName := make(map[string]int, len(containers))
		for i, c := range containers {
			byName[c.Name] = i
		}
		f, s := containers[byName[flappy]], containers[byName[sick]]
		if len(containers) == 2 && f.RecentRestarts >= 2 {
			if f.RecentRestarts > f.Restarts || f.LastRestartAgo < 1 || f.LastRestartAgo > 60 {
				t.Fatalf("flappy = %+v", f)
			}
			if s.Health != "unhealthy" || s.UnhealthyAgo < 1 || s.UnhealthyAgo > 120 || s.RecentRestarts != 0 {
				t.Fatalf("sick = %+v", s)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no restarts seen within a minute: %+v", containers)
		}
		time.Sleep(time.Second)
	}
}
