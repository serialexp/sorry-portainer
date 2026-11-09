package stacks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/serialexp/sorry-portainer/internal/podman"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

type fakeLister struct {
	containers map[string][]podman.ProjectContainer
	err        error
	calls      int
}

func (f *fakeLister) ComposeContainers(context.Context) (map[string][]podman.ProjectContainer, error) {
	f.calls++
	return f.containers, f.err
}

const twoServices = "services:\n  web:\n    image: caddy:latest\n  db:\n    image: postgres:16\n"

func TestServicesReportContainerStates(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if _, err := m.Save(context.Background(), "web", twoServices, nil, nil); err != nil {
		t.Fatal(err)
	}
	lister := &fakeLister{containers: map[string][]podman.ProjectContainer{
		m.project("web"): {
			{Name: "agent-web-web-1", Service: "web", Image: "docker.io/library/caddy:latest", State: "running", Status: "Up 2 hours"},
		},
		// Another project's containers never reach this stack.
		m.project("other"): {{Name: "agent-other-web-1", Service: "web", State: "running"}},
	}}
	m.EnableRuntimeStatus(lister)

	want := []protocol.StackService{
		{Service: "web", Container: "agent-web-web-1", Image: "docker.io/library/caddy:latest", State: "running", Status: "Up 2 hours"},
		{Service: "db", Image: "postgres:16", State: "absent"},
	}
	stacks, err := m.List(context.Background())
	if err != nil || len(stacks) != 1 || !reflect.DeepEqual(stacks[0].Services, want) {
		t.Fatalf("list services = %#v, %v", stacks, err)
	}
	inspected, err := m.Inspect(context.Background(), "web")
	if err != nil || !reflect.DeepEqual(inspected.Services, want) {
		t.Fatalf("inspect services = %#v, %v", inspected.Services, err)
	}
	if lister.calls != 2 {
		t.Fatalf("container listings = %d, want one per call", lister.calls)
	}
}

func TestServicesReportHealthAndRestarts(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if _, err := m.Save(context.Background(), "web", twoServices, nil, nil); err != nil {
		t.Fatal(err)
	}
	m.EnableRuntimeStatus(&fakeLister{containers: map[string][]podman.ProjectContainer{
		m.project("web"): {
			{Name: "agent-web-web-1", Service: "web", Image: "caddy:latest", State: "running", Status: "Up 2 hours", Health: "unhealthy", Restarts: 3},
			{Name: "agent-web-db-1", Service: "db", Image: "postgres:16", State: "running", Status: "Up 2 hours", Health: "healthy"},
		},
	}})
	want := []protocol.StackService{
		{Service: "web", Container: "agent-web-web-1", Image: "caddy:latest", State: "running", Status: "Up 2 hours", Health: "unhealthy", Restarts: 3},
		{Service: "db", Container: "agent-web-db-1", Image: "postgres:16", State: "running", Status: "Up 2 hours", Health: "healthy"},
	}
	stacks, err := m.List(context.Background())
	if err != nil || len(stacks) != 1 || !reflect.DeepEqual(stacks[0].Services, want) {
		t.Fatalf("services = %#v, %v", stacks, err)
	}
}

func TestServicesWithSeveralContainersListEachOne(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if _, err := m.Save(context.Background(), "web", "services: {app: {image: example}}", nil, nil); err != nil {
		t.Fatal(err)
	}
	m.EnableRuntimeStatus(&fakeLister{containers: map[string][]podman.ProjectContainer{
		m.project("web"): {
			{Name: "web-app-2", Service: "app", State: "exited"},
			{Name: "web-app-1", Service: "app", State: "running"},
		},
	}})
	stacks, err := m.List(context.Background())
	want := []protocol.StackService{
		{Service: "app", Container: "web-app-1", Image: "example", State: "running"},
		{Service: "app", Container: "web-app-2", Image: "example", State: "exited"},
	}
	if err != nil || len(stacks) != 1 || !reflect.DeepEqual(stacks[0].Services, want) {
		t.Fatalf("services = %#v, %v", stacks, err)
	}
}

func TestServicesAreUnknownWithoutRuntimeStatus(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if _, err := m.Save(context.Background(), "web", twoServices, nil, nil); err != nil {
		t.Fatal(err)
	}
	stack, err := m.Inspect(context.Background(), "web")
	if err != nil || len(stack.Services) != 2 || stack.Services[0].State != "unknown" || stack.Services[1].State != "unknown" {
		t.Fatalf("inspect = %#v, %v", stack.Services, err)
	}
}

func TestRuntimeFailureIsReturnedNotHidden(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if _, err := m.Save(context.Background(), "web", twoServices, nil, nil); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("podman ps failed")
	m.EnableRuntimeStatus(&fakeLister{err: boom})
	if _, err := m.List(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("list error = %v, want %v", err, boom)
	}
	if _, err := m.Inspect(context.Background(), "web"); !errors.Is(err, boom) {
		t.Fatalf("inspect error = %v, want %v", err, boom)
	}
}

func TestUnparseableComposeReportsInvalidStatus(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if _, err := m.Save(context.Background(), "web", twoServices, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.dir("web"), "compose.yaml"), []byte("services: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.EnableRuntimeStatus(&fakeLister{})
	stacks, err := m.List(context.Background())
	if err != nil || len(stacks) != 1 || stacks[0].Status != "invalid" || stacks[0].Services != nil {
		t.Fatalf("list = %#v, %v", stacks, err)
	}
}
