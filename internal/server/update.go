package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// updateService pulls a service's image and recreates it, telling the
// dashboard each step as the agent starts it. The answer is a stream of
// server-sent events once the agent reports its first step:
//
//	event: step     data: {"step":"pulling"}, then {"step":"recreating"}
//	event: result   data: the protocol.StackOperation
//	event: error    data: {"message":"...","status":502}
//
// A failure before the first step (an unknown host, an agent that refuses
// the request) is an ordinary HTTP error, as for the other routes.
func (s *Server) updateService(w http.ResponseWriter, r *http.Request, host, stack string) {
	var request protocol.StackUpdateRequest
	if jsonDecode(w, r, &request) != nil || request.Service == "" {
		http.Error(w, "service is required", http.StatusBadRequest)
		return
	}
	events := &eventStream{w: w}
	operation, err := s.relay.UpdateStackService(r.Context(), host, stack, request.Service, func(step string) {
		events.send("step", protocol.Progress{Step: step})
	})
	if !events.started {
		respond(w, operation, err, http.StatusBadGateway)
		return
	}
	if err != nil {
		events.send("error", streamError{Message: err.Error(), Status: relayStatus(err, http.StatusBadGateway)})
		return
	}
	events.send("result", operation)
}

// streamError is a failure after an event stream started, when the HTTP
// status is already sent.
type streamError struct {
	Message string `json:"message"`
	// Status is what the failure would have been as an HTTP status.
	Status int `json:"status"`
}

// eventStream writes server-sent events, starting the response with the
// first. Each event is flushed at once; the reverse proxy in front of the
// server (Caddy) passes text/event-stream through unbuffered.
type eventStream struct {
	w       http.ResponseWriter
	started bool
	failed  bool
}

func (e *eventStream) send(event string, value any) {
	if e.failed {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		e.failed = true
		return
	}
	if !e.started {
		header := e.w.Header()
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-store")
		header.Set("X-Accel-Buffering", "no")
		e.w.WriteHeader(http.StatusOK)
		e.started = true
	}
	// json.Marshal output has no newlines, so one data line carries it.
	if _, err := fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		e.failed = true
		return
	}
	if err := http.NewResponseController(e.w).Flush(); err != nil {
		e.failed = true
	}
}
