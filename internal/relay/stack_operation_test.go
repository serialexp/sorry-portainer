package relay

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/stacks"
)

// failingCompose fails every run the way Compose does: output, then an exit
// status.
type failingCompose struct{}

func (failingCompose) Run(context.Context, []string, string, map[string]string) (string, error) {
	return "Error: image docker.io/library/nope:1 not found", errors.New("exit status 125")
}

// TestFailedStackOperationKeepsItsOutput: a Compose run that fails reaches
// the master as a result with Success false and Compose's output, not as an
// error carrying only "exit status 125".
func TestFailedStackOperationKeepsItsOutput(t *testing.T) {
	manager := stacks.New(t.TempDir(), "jp1-", failingCompose{})
	ctx := context.Background()
	if _, err := manager.Save(ctx, "web", "services:\n  web:\n    image: docker.io/library/nope:1\n", nil, nil); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{HostID: "jp1", Stacks: manager}
	payload, _ := json.Marshal(protocol.StackSave{Name: "web"})
	for _, operation := range []string{"stacks.up", "stacks.down", "stacks.restart"} {
		value, err := agent.handle(ctx, operation, payload)
		if err != nil {
			t.Fatalf("%s = %v, want a result", operation, err)
		}
		result, ok := value.(protocol.StackOperation)
		if !ok || result.Success || result.Name != "web" || !strings.Contains(result.Output, "not found") {
			t.Fatalf("%s = %+v", operation, value)
		}
	}
}

// TestUnknownStackOperationIsAnError: a stack that could not be run at all
// is still an error.
func TestUnknownStackOperationIsAnError(t *testing.T) {
	agent := &Agent{HostID: "jp1", Stacks: stacks.New(t.TempDir(), "jp1-", failingCompose{})}
	payload, _ := json.Marshal(protocol.StackSave{Name: "ghost"})
	if value, err := agent.handle(context.Background(), "stacks.up", payload); err == nil {
		t.Fatalf("up of a missing stack = %+v", value)
	}
}

// TestSaveKeepsOrClearsTheEnvironment: through the agent's handler, a save
// without an environment keeps the stack's, and an empty one clears it.
func TestSaveKeepsOrClearsTheEnvironment(t *testing.T) {
	manager := stacks.New(t.TempDir(), "jp1-", failingCompose{})
	agent := &Agent{HostID: "jp1", Stacks: manager}
	ctx := context.Background()
	save := func(body string) protocol.StackSaved {
		t.Helper()
		value, err := agent.handle(ctx, "stacks.save", json.RawMessage(body))
		if err != nil {
			t.Fatalf("save %s: %v", body, err)
		}
		return value.(protocol.StackSaved)
	}
	save(`{"name":"web","compose_yaml":"services: {}\n","environment":{"MODE":"a"}}`)
	if got := save(`{"name":"web","compose_yaml":"services: {}\n","expected_version":1}`); got.Environment["MODE"] != "a" {
		t.Fatalf("absent environment: %+v", got.Environment)
	}
	if got := save(`{"name":"web","compose_yaml":"services: {}\n","expected_version":2,"environment":null}`); got.Environment["MODE"] != "a" {
		t.Fatalf("null environment: %+v", got.Environment)
	}
	if got := save(`{"name":"web","compose_yaml":"services: {}\n","expected_version":3,"environment":{}}`); len(got.Environment) != 0 {
		t.Fatalf("empty environment: %+v", got.Environment)
	}
	// What the master encodes for a save without an environment decodes as "keep".
	encoded, _ := json.Marshal(protocol.StackSave{Name: "web", ComposeYAML: "x"})
	if !strings.Contains(string(encoded), `"environment":null`) {
		t.Fatalf("encoded save = %s", encoded)
	}
}
