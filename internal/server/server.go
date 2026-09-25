package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/relay"
)

func jsonDecode(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return d.Decode(v)
}

// Relay reaches host agents. Every call takes the HTTP request's context, so a
// client that goes away stops waiting and the agent is asked to cancel.
type Relay interface {
	Hosts() []protocol.HostInfo
	Info(context.Context, string) (protocol.HostInfo, error)
	Containers(context.Context, string) ([]protocol.Container, error)
	Volumes(context.Context, string) ([]protocol.Volume, error)
	Images(context.Context, string) ([]protocol.Image, error)
	Start(context.Context, string, string) error
	Stop(context.Context, string, string) error
	ListStacks(context.Context, string) ([]protocol.Stack, error)
	InspectStack(context.Context, string, string) (protocol.Stack, error)
	StackVersions(context.Context, string, string) ([]protocol.StackVersion, error)
	SaveStack(context.Context, string, protocol.StackSave) (protocol.Stack, error)
	StackOperation(context.Context, string, string, string) (protocol.StackOperation, error)
}

type Server struct {
	sessions *Sessions
	relay    Relay
}

func New(password string, ttl time.Duration, relay Relay) *Server {
	return &Server{sessions: NewSessions(password, ttl), relay: relay}
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	m.HandleFunc("/api/session/login", s.sessions.Login)
	m.Handle("/api/hosts", s.sessions.Require(http.HandlerFunc(s.hosts)))
	m.Handle("/api/hosts/", s.sessions.Require(http.HandlerFunc(s.hostOperation)))
	return m
}

func (s *Server) hosts(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.relay.Hosts()) }

// relayStatus maps a relay failure to an HTTP status. Transport failures have
// fixed statuses; an operation the agent ran and reported as failed gets the
// route's own status (for example 409 for a stack save).
func relayStatus(err error, operationFailed int) int {
	var remote *relay.RemoteError
	switch {
	case errors.Is(err, relay.ErrHostUnavailable), errors.Is(err, relay.ErrHostBusy),
		errors.Is(err, relay.ErrDisconnected), errors.Is(err, context.Canceled):
		return http.StatusServiceUnavailable
	case errors.Is(err, relay.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	case errors.As(err, &remote):
		return operationFailed
	default:
		return http.StatusBadGateway
	}
}

// respond writes value as JSON, or the error with the status relayStatus picks.
func respond[T any](w http.ResponseWriter, value T, err error, operationFailed int) {
	if err != nil {
		http.Error(w, err.Error(), relayStatus(err, operationFailed))
		return
	}
	writeJSON(w, value)
}

func (s *Server) hostOperation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	path := strings.TrimPrefix(r.URL.Path, "/api/hosts/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 1 && r.Method == http.MethodGet {
		info, err := s.relay.Info(ctx, parts[0])
		respond(w, info, err, http.StatusBadGateway)
		return
	}
	if len(parts) == 2 && parts[1] == "stacks" && r.Method == http.MethodGet {
		stacks, err := s.relay.ListStacks(ctx, parts[0])
		respond(w, stacks, err, http.StatusBadGateway)
		return
	}
	if len(parts) == 2 && parts[1] == "stacks" && r.Method == http.MethodPost {
		var req protocol.StackSave
		if jsonDecode(r, &req) != nil {
			http.Error(w, "invalid stack", http.StatusBadRequest)
			return
		}
		stack, err := s.relay.SaveStack(ctx, parts[0], req)
		respond(w, stack, err, http.StatusConflict)
		return
	}
	if len(parts) == 3 && parts[1] == "stacks" && r.Method == http.MethodGet {
		stack, err := s.relay.InspectStack(ctx, parts[0], parts[2])
		respond(w, stack, err, http.StatusNotFound)
		return
	}
	if len(parts) == 4 && parts[1] == "stacks" && parts[3] == "versions" && r.Method == http.MethodGet {
		versions, err := s.relay.StackVersions(ctx, parts[0], parts[2])
		respond(w, versions, err, http.StatusNotFound)
		return
	}
	if len(parts) == 4 && parts[1] == "stacks" {
		if r.Method == http.MethodPost && (parts[3] == "up" || parts[3] == "down" || parts[3] == "restart") {
			operation, err := s.relay.StackOperation(ctx, parts[0], parts[2], parts[3])
			respond(w, operation, err, http.StatusBadGateway)
			return
		}
		http.NotFound(w, r)
		return
	}
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	id, operation := parts[0], parts[1]
	switch {
	case operation == "info" && r.Method == http.MethodGet:
		info, err := s.relay.Info(ctx, id)
		respond(w, info, err, http.StatusBadGateway)
	case operation == "containers" && r.Method == http.MethodGet:
		containers, err := s.relay.Containers(ctx, id)
		respond(w, containers, err, http.StatusBadGateway)
	case operation == "volumes" && r.Method == http.MethodGet:
		volumes, err := s.relay.Volumes(ctx, id)
		respond(w, volumes, err, http.StatusBadGateway)
	case operation == "images" && r.Method == http.MethodGet:
		images, err := s.relay.Images(ctx, id)
		respond(w, images, err, http.StatusBadGateway)
	case (operation == "start" || operation == "stop") && r.Method == http.MethodPost:
		var request protocol.StartRequest
		if jsonDecode(r, &request) != nil || request.ContainerID == "" {
			http.Error(w, "container_id is required", http.StatusBadRequest)
			return
		}
		var err error
		if operation == "start" {
			err = s.relay.Start(ctx, id, request.ContainerID)
		} else {
			err = s.relay.Stop(ctx, id, request.ContainerID)
		}
		respond(w, protocol.StartResult{ContainerID: request.ContainerID, HostID: id, Started: true}, err, http.StatusBadGateway)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
