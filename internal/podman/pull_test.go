package podman

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestLocalImagesAndProjectContainerImageID(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `[{"Id":"abc","Names":["docker.io/aeolun/homes:latest"],"RepoTags":null,"RepoDigests":["docker.io/aeolun/homes@sha256:1","docker.io/aeolun/homes@sha256:2"],"Digest":"sha256:1"},{"Id":"def","Names":null,"RepoDigests":null}]`},
		{stdout: `[{"Id":"c1","Names":["owned-app_1"],"Image":"docker.io/aeolun/homes:latest","ImageID":"abc","State":"running","Labels":{"com.docker.compose.project":"owned-app","com.docker.compose.service":"app"}}]`},
	}}
	client := NewWithRunner(runner, "owned-")
	images, err := client.LocalImages(context.Background())
	want := []LocalImage{
		{ID: "abc", Names: []string{"docker.io/aeolun/homes:latest"}, RepoDigests: []string{"docker.io/aeolun/homes@sha256:1", "docker.io/aeolun/homes@sha256:2"}, Digest: "sha256:1"},
		{ID: "def"},
	}
	if err != nil || !reflect.DeepEqual(images, want) {
		t.Fatalf("LocalImages = %#v, %v", images, err)
	}
	containers, err := client.ProjectContainers(context.Background(), "owned-app")
	if err != nil || len(containers) != 1 || containers[0].ImageID != "abc" {
		t.Fatalf("ProjectContainers = %#v, %v", containers, err)
	}
	if !reflect.DeepEqual(runner.calls[0], []string{"images", "--format", "json"}) {
		t.Fatalf("images call = %v", runner.calls[0])
	}
}

func TestPull(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: "0123456789abcdef\n", stderr: "Trying to pull docker.io/aeolun/homes:latest...\n"},
		{stderr: "Error: initializing source: unauthorized", err: errors.New("exit status 125")},
	}}
	client := NewWithRunner(runner)
	id, err := client.Pull(context.Background(), "docker.io/aeolun/homes:latest")
	if err != nil || id != "0123456789abcdef" {
		t.Fatalf("Pull = %q, %v", id, err)
	}
	if !reflect.DeepEqual(runner.calls[0], []string{"pull", "--quiet", "--", "docker.io/aeolun/homes:latest"}) {
		t.Fatalf("pull call = %v", runner.calls[0])
	}
	if _, err := client.Pull(context.Background(), "docker.io/aeolun/private:latest"); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("failed pull = %v, want podman's message", err)
	}
	for _, bad := range []string{"", "--all-tags"} {
		if _, err := client.Pull(context.Background(), bad); err == nil {
			t.Fatalf("Pull(%q) accepted", bad)
		}
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %d, invalid references must not run podman", len(runner.calls))
	}
}

func TestImageExists(t *testing.T) {
	missing := exec.Command("sh", "-c", "exit 1").Run()
	broken := exec.Command("sh", "-c", "exit 125").Run()
	runner := &fakeRunner{responses: []response{
		{},
		{err: missing},
		{stderr: "Error: database is locked", err: broken},
	}}
	client := NewWithRunner(runner)
	ctx := context.Background()
	if ok, err := client.ImageExists(ctx, "redis:7"); !ok || err != nil {
		t.Fatalf("present image = %v, %v", ok, err)
	}
	if !reflect.DeepEqual(runner.calls[0], []string{"image", "exists", "--", "redis:7"}) {
		t.Fatalf("call = %v", runner.calls[0])
	}
	if ok, err := client.ImageExists(ctx, "redis:8"); ok || err != nil {
		t.Fatalf("missing image = %v, %v", ok, err)
	}
	if ok, err := client.ImageExists(ctx, "redis:9"); ok || err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("failure = %v, %v", ok, err)
	}
	if _, err := client.ImageExists(ctx, "--all"); err == nil || len(runner.calls) != 3 {
		t.Fatalf("an option was passed as a reference: %v", err)
	}
}
