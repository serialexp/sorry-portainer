package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// rawAgent connects to the harness as local-b and speaks the protocol by
// hand, so a test controls exactly which frames arrive and when.
func rawAgent(t *testing.T, h *harness) *websocket.Conn {
	t.Helper()
	conn := h.dial(t, "local-b")
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.WriteMessage(websocket.TextMessage, helloFrame(t)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "agent registration", func() bool { return h.sessionFor("local-b") != nil })
	return conn
}

func sendFrame(t *testing.T, conn *websocket.Conn, message protocol.Message) {
	t.Helper()
	data, err := protocol.Encode(message)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatal(err)
	}
}

func progressFrame(id, step string) protocol.Message {
	return protocol.Message{ID: id, Type: protocol.TypeProgress, Payload: json.RawMessage(fmt.Sprintf(`{"step":%q}`, step))}
}

func TestUpdateReportsTheAgentsSteps(t *testing.T) {
	h := newHarness(t, time.Minute)
	conn := rawAgent(t, h)

	steps := make(chan string, 4)
	type answer struct {
		operation protocol.StackOperation
		err       error
	}
	done := make(chan answer, 1)
	go func() {
		operation, err := h.remote.UpdateStackService(context.Background(), "local-b", "homes", "app", func(step string) { steps <- step })
		done <- answer{operation, err}
	}()
	request := readMessage(t, conn)
	if request.Operation != "stacks.update" || string(request.Payload) != `{"name":"homes","service":"app"}` {
		t.Fatalf("request = %s %s", request.Operation, request.Payload)
	}
	// Progress for another request is dropped, and the session survives it.
	sendFrame(t, conn, progressFrame("someone-else", protocol.UpdatePulling))
	sendFrame(t, conn, progressFrame(request.ID, protocol.UpdatePulling))
	if got := receive(t, "pulling", steps); got != protocol.UpdatePulling {
		t.Fatalf("first step = %q", got)
	}
	sendFrame(t, conn, progressFrame(request.ID, protocol.UpdateRecreating))
	if got := receive(t, "recreating", steps); got != protocol.UpdateRecreating {
		t.Fatalf("second step = %q", got)
	}
	sendFrame(t, conn, protocol.Message{ID: request.ID, Type: protocol.TypeResponse,
		Payload: json.RawMessage(`{"name":"homes","operation":"update","success":true}`)})
	got := receive(t, "update answer", done)
	if got.err != nil || !got.operation.Success || got.operation.Name != "homes" {
		t.Fatalf("update = %+v, %v", got.operation, got.err)
	}
	if len(steps) != 0 {
		t.Fatalf("unexpected extra step %q", <-steps)
	}
}

// A caller that is slow to take steps neither blocks the session's reader,
// which other requests share, nor makes it buffer every step: the caller
// sees the latest.
func TestSlowProgressReaderSeesTheLatestStepAndBlocksNothing(t *testing.T) {
	h := newHarness(t, time.Minute)
	conn := rawAgent(t, h)

	release := make(chan struct{})
	seen := make(chan string, 200)
	done := make(chan error, 1)
	go func() {
		first := true
		_, err := h.remote.UpdateStackService(context.Background(), "local-b", "homes", "app", func(step string) {
			seen <- step
			if first {
				first = false
				<-release
			}
		})
		done <- err
	}()
	update := readMessage(t, conn)
	sendFrame(t, conn, progressFrame(update.ID, "step-0"))
	if got := receive(t, "the first step", seen); got != "step-0" {
		t.Fatalf("first step = %q", got)
	}

	other := make(chan error, 1)
	go func() {
		_, err := h.remote.Images(context.Background(), "local-b")
		other <- err
	}()
	images := readMessage(t, conn)
	for i := 1; i <= 100; i++ {
		sendFrame(t, conn, progressFrame(update.ID, fmt.Sprintf("step-%d", i)))
	}
	sendFrame(t, conn, protocol.Message{ID: images.ID, Type: protocol.TypeResponse, Payload: json.RawMessage(`[]`)})
	if err := receive(t, "the other request's answer while steps wait", other); err != nil {
		t.Fatalf("other request = %v", err)
	}
	// The reader handled every step before the other answer, so only the
	// latest is waiting.
	close(release)
	if got := receive(t, "the latest step", seen); got != "step-100" {
		t.Fatalf("step after the slow one = %q, want the latest", got)
	}
	sendFrame(t, conn, protocol.Message{ID: update.ID, Type: protocol.TypeResponse, Payload: json.RawMessage(`{"success":true}`)})
	if err := receive(t, "update answer", done); err != nil {
		t.Fatalf("update = %v", err)
	}
	if len(seen) != 0 {
		t.Fatalf("more steps after the latest: %q", <-seen)
	}
}
