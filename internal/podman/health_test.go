package podman

import (
	"context"
	"testing"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

func TestSplitHealth(t *testing.T) {
	for _, tc := range []struct{ in, status, health string }{
		{"Up 2 hours (unhealthy)", "Up 2 hours", "unhealthy"},
		{"Up About an hour (healthy)", "Up About an hour", "healthy"},
		{"Up 3 seconds (starting)", "Up 3 seconds", "starting"},
		{"Exited (1) 5 minutes ago (unhealthy)", "Exited (1) 5 minutes ago", "unhealthy"},
		// No health check: the status is kept whole.
		{"Up 2 minutes", "Up 2 minutes", ""},
		{"Exited (0) 5 minutes ago", "Exited (0) 5 minutes ago", ""},
		{"Exited (0)", "Exited (0)", ""},
		{"Created", "Created", ""},
		{"", "", ""},
		// Only one lowercase word counts as a health result.
		{"Up 2 hours ()", "Up 2 hours ()", ""},
		{"Up 2 hours (Healthy)", "Up 2 hours (Healthy)", ""},
		{"Up 2 hours (not ok)", "Up 2 hours (not ok)", ""},
		{"(healthy)", "(healthy)", ""},
	} {
		status, health := splitHealth(tc.in)
		if status != tc.status || health != tc.health {
			t.Errorf("splitHealth(%q) = %q, %q; want %q, %q", tc.in, status, health, tc.status, tc.health)
		}
	}
}

func TestContainersReportHealthAndRestarts(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `[{"Id":"a","Names":["owned-web"],"Image":"example/web:1","State":"running","Status":"Up 2 hours (unhealthy)","Restarts":44},` +
			`{"Id":"b","Names":["owned-db"],"Image":"example/db:1","State":"running","Status":"Up 2 hours"}]`},
	}}
	containers, err := NewWithRunner(runner, "owned-").Containers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.Container{
		{ID: "a", Name: "owned-web", Image: "example/web:1", State: "running", Status: "Up 2 hours", Health: "unhealthy", Restarts: 44},
		{ID: "b", Name: "owned-db", Image: "example/db:1", State: "running", Status: "Up 2 hours"},
	}
	if len(containers) != len(want) || containers[0] != want[0] || containers[1] != want[1] {
		t.Fatalf("Containers() = %#v, want %#v", containers, want)
	}
}

func TestComposeContainersReportHealthAndRestarts(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `[{"Id":"a","Names":["owned-app-web-1"],"Image":"example/web:1","State":"running","Status":"Up 1 minute (starting)","Restarts":2,"Pid":42,` +
			`"Labels":{"com.docker.compose.project":"owned-app","com.docker.compose.service":"web"}}]`},
	}}
	all, err := NewWithRunner(runner, "owned-").ComposeContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := ProjectContainer{ID: "a", Name: "owned-app-web-1", Service: "web", Project: "owned-app", Image: "example/web:1",
		State: "running", Status: "Up 1 minute", Health: "starting", Restarts: 2, Pid: 42}
	if got := all["owned-app"]; len(got) != 1 || got[0] != want {
		t.Fatalf("ComposeContainers() = %#v, want %#v", all, want)
	}
}
