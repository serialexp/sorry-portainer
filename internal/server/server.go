package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

func jsonDecode(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return d.Decode(v)
}

type Relay interface {
	Hosts() []protocol.HostInfo
	Containers(string) ([]protocol.Container, error)
	Volumes(string) ([]protocol.Volume, error)
	Images(string) ([]protocol.Image, error)
	Start(string, string) error
	Stop(string, string) error
	Info(string) (protocol.HostInfo, error)
	ListStacks(string) ([]protocol.Stack, error)
	InspectStack(string, string) (protocol.Stack, error)
	StackVersions(string, string) ([]protocol.StackVersion, error)
	SaveStack(string, protocol.StackSave) (protocol.Stack, error)
	StackOperation(string, string, string) (protocol.StackOperation, error)
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
func (s *Server) hostOperation(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/hosts/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 1 && r.Method == http.MethodGet {
		if info, e := s.relay.Info(parts[0]); e == nil {
			writeJSON(w, info)
		} else {
			http.Error(w, e.Error(), http.StatusBadGateway)
		}
		return
	}
	if len(parts) == 2 && parts[1] == "stacks" && r.Method == http.MethodGet {
		xs, e := s.relay.ListStacks(parts[0])
		if e != nil {
			http.Error(w, e.Error(), 502)
			return
		}
		writeJSON(w, xs)
		return
	}
	if len(parts) == 2 && parts[1] == "stacks" && r.Method == http.MethodPost {
		var req protocol.StackSave
		if jsonDecode(r, &req) != nil {
			http.Error(w, "invalid stack", http.StatusBadRequest)
			return
		}
		stack, e := s.relay.SaveStack(parts[0], req)
		if e != nil {
			http.Error(w, e.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, stack)
		return
	}
	if len(parts) == 3 && parts[1] == "stacks" && r.Method == http.MethodGet {
		stack, e := s.relay.InspectStack(parts[0], parts[2])
		if e != nil {
			http.Error(w, e.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, stack)
		return
	}
	if len(parts) == 4 && parts[1] == "stacks" && parts[3] == "versions" && r.Method == http.MethodGet {
		versions, e := s.relay.StackVersions(parts[0], parts[2])
		if e != nil {
			http.Error(w, e.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, versions)
		return
	}
	if len(parts) == 4 && parts[1] == "stacks" {
		if r.Method == http.MethodPost && (parts[3] == "up" || parts[3] == "down" || parts[3] == "restart") {
			op, e := s.relay.StackOperation(parts[0], parts[2], parts[3])
			if e != nil {
				http.Error(w, e.Error(), 502)
				return
			}
			writeJSON(w, op)
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
	if operation == "info" && r.Method == http.MethodGet {
		info, e := s.relay.Info(id)
		if e != nil {
			http.Error(w, e.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, info)
		return
	}
	if operation == "containers" && r.Method == http.MethodGet {
		xs, e := s.relay.Containers(id)
		if e != nil {
			http.Error(w, e.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, xs)
		return
	}
	if operation == "volumes" && r.Method == http.MethodGet {
		xs, e := s.relay.Volumes(id)
		if e != nil {
			http.Error(w, e.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, xs)
		return
	}
	if operation == "images" && r.Method == http.MethodGet {
		xs, e := s.relay.Images(id)
		if e != nil {
			http.Error(w, e.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, xs)
		return
	}
	if (operation == "start" || operation == "stop") && r.Method == http.MethodPost {
		var request protocol.StartRequest
		if jsonDecode(r, &request) != nil || request.ContainerID == "" {
			http.Error(w, "container_id is required", http.StatusBadRequest)
			return
		}
		var e error
		if operation == "start" {
			e = s.relay.Start(id, request.ContainerID)
		} else {
			e = s.relay.Stop(id, request.ContainerID)
		}
		if e != nil {
			http.Error(w, e.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, protocol.StartResult{ContainerID: request.ContainerID, HostID: id, Started: true})
		return
	}
	http.NotFound(w, r)
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
