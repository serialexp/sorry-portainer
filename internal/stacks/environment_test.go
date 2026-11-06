package stacks

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// upEnvironments returns the environment of each `up` the executor ran.
func upEnvironments(e *recordingExecutor) []map[string]string {
	var out []map[string]string
	for _, call := range e.callsSnapshot() {
		for _, arg := range call.args {
			if arg == "up" {
				out = append(out, call.env)
				break
			}
		}
	}
	return out
}

func TestEnvironmentIsVisibleAndReplaceable(t *testing.T) {
	ctx := context.Background()
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	created, err := m.Save(ctx, "web", "services: {}\n", map[string]string{"TZ": "UTC", "MODE": "a"}, nil)
	if err != nil || !maps.Equal(created.Environment, map[string]string{"TZ": "UTC", "MODE": "a"}) {
		t.Fatalf("create = %+v, %v", created, err)
	}
	stack, err := m.Inspect(ctx, "web")
	if err != nil || stack.Environment["MODE"] != "a" {
		t.Fatalf("inspect = %+v, %v", stack, err)
	}
	one := 1
	if _, err := m.Save(ctx, "web", "services: {}\n", map[string]string{"MODE": "b"}, &one); err != nil {
		t.Fatal(err)
	}
	if stack, _ = m.Inspect(ctx, "web"); !maps.Equal(stack.Environment, map[string]string{"MODE": "b"}) || stack.Version != 2 {
		t.Fatalf("after replace = %+v", stack)
	}
	two := 2
	if _, err := m.Save(ctx, "web", "services: {}\n", map[string]string{}, &two); err != nil {
		t.Fatal(err)
	}
	if stack, _ = m.Inspect(ctx, "web"); len(stack.Environment) != 0 {
		t.Fatalf("an empty environment did not clear: %+v", stack.Environment)
	}
	// Each revision keeps the environment it was saved with.
	if env, err := m.environment("web", 1); err != nil || env["TZ"] != "UTC" {
		t.Fatalf("revision 1 = %v, %v", env, err)
	}
	list, err := m.List(ctx)
	if err != nil || len(list) != 1 || list[0].Environment != nil {
		t.Fatalf("list carries environments: %+v, %v", list, err)
	}
}

func TestEnvironmentIsChecked(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	for name, env := range map[string]map[string]string{
		"bad key":      {"1A": "x"},
		"space in key": {"A B": "x"},
		"newline":      {"A": "line\nbreak"},
		"cr":           {"A": "x\ry"},
		"nul":          {"A": "x\x00y"},
	} {
		_, err := m.Save(context.Background(), "web", "services: {}\n", env, nil)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		for _, v := range env {
			if strings.Contains(err.Error(), v) {
				t.Errorf("%s: error %q shows the value", name, err)
			}
		}
	}
	many := map[string]string{}
	for i := range maxEnvironment + 1 {
		many["K"+strings.Repeat("A", i+1)] = "v"
	}
	if _, err := m.Save(context.Background(), "web", "services: {}\n", many, nil); err == nil {
		t.Error("more than the limit was accepted")
	}
	if _, err := os.Stat(m.dir("web")); !os.IsNotExist(err) {
		t.Fatalf("a refused save left a stack: %v", err)
	}
}

func TestChangedEnvironmentBringsARunningStackUp(t *testing.T) {
	ctx := context.Background()
	e := &recordingExecutor{}
	m := New(t.TempDir(), "agent", e)
	if _, err := m.Save(ctx, "web", "services: {}\n", map[string]string{"MODE": "a"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	one := 1
	saved, err := m.Save(ctx, "web", "services: {}\n", map[string]string{"MODE": "b"}, &one)
	if err != nil || saved.Operation == nil || !saved.Operation.Success || saved.Operation.Operation != "up" {
		t.Fatalf("save = %+v, %v", saved, err)
	}
	ups := upEnvironments(e)
	if len(ups) != 2 || ups[1]["MODE"] != "b" {
		t.Fatalf("ups = %v", ups)
	}
	// The same values again, or no environment at all, change nothing.
	two, three := 2, 3
	if saved, err = m.Save(ctx, "web", "services: {}\n", map[string]string{"MODE": "b"}, &two); err != nil || saved.Operation != nil {
		t.Fatalf("unchanged save = %+v, %v", saved, err)
	}
	if saved, err = m.Save(ctx, "web", "services: {a: {}}\n", nil, &three); err != nil || saved.Operation != nil {
		t.Fatalf("compose-only save = %+v, %v", saved, err)
	}
	if len(upEnvironments(e)) != 2 {
		t.Fatalf("unchanged saves ran up: %v", upEnvironments(e))
	}
}

func TestChangedEnvironmentLeavesAStoppedStackAlone(t *testing.T) {
	ctx := context.Background()
	e := &recordingExecutor{}
	m := New(t.TempDir(), "agent", e)
	if _, err := m.Save(ctx, "never", "services: {}\n", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(ctx, "stopped", "services: {}\n", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx, "stopped"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Down(ctx, "stopped"); err != nil {
		t.Fatal(err)
	}
	one := 1
	for _, n := range []string{"never", "stopped"} {
		saved, err := m.Save(ctx, n, "services: {}\n", map[string]string{"MODE": "b"}, &one)
		if err != nil || saved.Operation != nil {
			t.Fatalf("%s: save = %+v, %v", n, saved, err)
		}
	}
	if len(upEnvironments(e)) != 1 {
		t.Fatalf("ups = %v", upEnvironments(e))
	}
}

func TestFailedUpAfterSaveIsReportedAndTheRevisionStays(t *testing.T) {
	ctx := context.Background()
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if _, err := m.Save(ctx, "web", "services: {}\n", map[string]string{"MODE": "a"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	m.executor = &failingExecutor{}
	one := 1
	saved, err := m.Save(ctx, "web", "services: {}\n", map[string]string{"MODE": "b"}, &one)
	if err != nil {
		t.Fatalf("a failed up became a failed save: %v", err)
	}
	if saved.Operation == nil || saved.Operation.Success || !strings.Contains(saved.Operation.Output, "image not known") {
		t.Fatalf("operation = %+v", saved.Operation)
	}
	if stack, _ := m.Inspect(ctx, "web"); stack.Version != 2 || stack.Environment["MODE"] != "b" {
		t.Fatalf("revision = %+v", stack)
	}
}

func TestLegacyEnvironmentAppliesToOldRevisions(t *testing.T) {
	ctx := context.Background()
	e := &recordingExecutor{}
	m := New(t.TempDir(), "agent", e)
	// A stack saved before environments were versioned, as foundryvtt on jp1.
	if _, err := m.Save(ctx, "web", "services: {}\n", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(environmentFile(m.dir("web"), 1)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.dir("web"), "env.json"), []byte(`{"MODE":"legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if stack, err := m.Inspect(ctx, "web"); err != nil || stack.Environment["MODE"] != "legacy" {
		t.Fatalf("inspect = %+v, %v", stack, err)
	}
	if _, err := m.Up(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if ups := upEnvironments(e); len(ups) != 1 || ups[0]["MODE"] != "legacy" {
		t.Fatalf("ups = %v", ups)
	}
}

func TestBrokenEnvironmentFileIsAnError(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if _, err := m.Save(context.Background(), "web", "services: {}\n", map[string]string{"A": "1"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(environmentFile(m.dir("web"), 1), []byte(`{"A": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(context.Background(), "web"); err == nil {
		t.Fatal("up ran with an unreadable environment")
	}
}
