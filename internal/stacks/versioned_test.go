package stacks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

func TestManagerVersionedSaveAndPreservesEnvironment(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	created, err := m.Save(context.Background(), "web", "services: {one: {}}\n", map[string]string{"SECRET": "unchanged"}, nil)
	if err != nil || created.Version != 1 {
		t.Fatalf("create = %#v, %v", created, err)
	}
	one := 1
	if _, err := m.Save(context.Background(), "web", "services: {two: {}}\n", map[string]string{"SECRET": "replacement"}, &one); err == nil {
		t.Fatal("environment update was silently ignored")
	}
	updated, err := m.Save(context.Background(), "web", "services: {two: {}}\n", nil, &one)
	if err != nil || updated.Version != 2 {
		t.Fatalf("update = %#v, %v", updated, err)
	}
	if got, err := os.ReadFile(filepath.Join(m.dir("web"), "env.json")); err != nil || string(got) != `{"SECRET":"unchanged"}` {
		t.Fatalf("environment = %q, %v", got, err)
	}
	versions, err := m.Versions(context.Background(), "web")
	if err != nil || len(versions) != 2 || versions[0].Version != 1 || versions[1].Version != 2 {
		t.Fatalf("versions = %#v, %v", versions, err)
	}
	first, err := os.ReadFile(filepath.Join(m.dir("web"), "revisions", "1.yaml"))
	if err != nil || string(first) != "services: {one: {}}\n" {
		t.Fatalf("revision one = %q, %v", first, err)
	}
}

func TestManagerListDoesNotReturnComposeContents(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if _, err := m.Save(context.Background(), "web", "services: {private: {image: example}}", nil, nil); err != nil {
		t.Fatal(err)
	}
	stacks, err := m.List(context.Background())
	if err != nil || len(stacks) != 1 || stacks[0].ComposeYAML != "" || stacks[0].Version != 1 {
		t.Fatalf("list = %#v, %v", stacks, err)
	}
}

func BenchmarkListStacks1000(b *testing.B) {
	m := New(b.TempDir(), "agent", &recordingExecutor{})
	for i := range 1000 {
		name := fmt.Sprintf("stack-%04d", i)
		if _, err := m.Save(context.Background(), name, "services: {web: {image: example}}", nil, nil); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for range b.N {
		stacks, err := m.List(context.Background())
		if err != nil || len(stacks) != 1000 {
			b.Fatalf("list: %d stacks, %v", len(stacks), err)
		}
	}
}

func TestManagerRejectsDuplicateAndVersionMismatch(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if _, err := m.Save(context.Background(), "web", "services: {}", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(context.Background(), "web", "services: {}", nil, nil); err == nil {
		t.Fatal("duplicate create succeeded")
	}
	wrong := 2
	if _, err := m.Save(context.Background(), "web", "services: {}", nil, &wrong); err == nil {
		t.Fatal("version mismatch succeeded")
	}
}

func TestManagerLegacyStackIsLazilyVersionOne(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if err := os.MkdirAll(m.dir("legacy"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.dir("legacy"), "compose.yaml"), []byte("services: {old: {}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.dir("legacy"), "env.json"), []byte(`{"SECRET":"preserved"}`), 0600); err != nil {
		t.Fatal(err)
	}
	stack, err := m.Inspect(context.Background(), "legacy")
	if err != nil || stack.Version != 1 {
		t.Fatalf("inspect = %#v, %v", stack, err)
	}
	one := 1
	updated, err := m.Save(context.Background(), "legacy", "services: {new: {}}\n", nil, &one)
	if err != nil || updated.Version != 2 {
		t.Fatalf("update = %#v, %v", updated, err)
	}
	if got, err := os.ReadFile(filepath.Join(m.dir("legacy"), "env.json")); err != nil || string(got) != `{"SECRET":"preserved"}` {
		t.Fatalf("environment = %q, %v", got, err)
	}
}

func TestManagerConcurrentUpdatesHaveOneWinner(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if _, err := m.Save(context.Background(), "web", "services: {}", nil, nil); err != nil {
		t.Fatal(err)
	}
	one := 1
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Save(context.Background(), "web", "services: {new: {}}", nil, &one)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful updates = %d, want 1", successes)
	}
	stack, err := m.Inspect(context.Background(), "web")
	if err != nil || stack.Version != 2 {
		t.Fatalf("inspect = %#v, %v", stack, err)
	}
}

func TestManagerCreateFailureLeavesNoStack(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	// A file blocks the target directory rename after all staging writes succeed.
	if err := os.MkdirAll(m.root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.dir("web"), []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := m.Save(context.Background(), "web", "services: {}", nil, nil)
	if err == nil {
		t.Fatal("expected failed create")
	}
	if got, readErr := os.ReadFile(m.dir("web")); readErr != nil || string(got) != "block" {
		t.Fatalf("original target = %q, %v", got, readErr)
	}
}

var _ = protocol.Stack{}
