package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/relay"
	"github.com/serialexp/sorry-portainer/internal/secretstore"
)

// maxBody bounds a JSON request body on the web listener.
const maxBody = 1 << 20

func jsonDecode(w http.ResponseWriter, r *http.Request, v any) error {
	boundBodyRead(w)
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(v)
}

// boundBodyRead gives the handler bodyReadTimeout to read the request body.
// The web server has no ReadTimeout, because stack operations keep a
// response open for minutes, so each handler that reads a body bounds it.
// The deadline does not outlive the body: net/http clears it when the body
// reaches EOF and it starts watching the connection for the client going
// away. A body left unread keeps the deadline, which then bounds the drain
// after the handler returns.
func boundBodyRead(w http.ResponseWriter) {
	// A ResponseRecorder in tests has no connection to bound.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(bodyReadTimeout))
}

// Relay reaches host agents. Every call takes the HTTP request's context, so a
// client that goes away stops waiting and the agent is asked to cancel.
type Relay interface {
	Hosts() []protocol.HostInfo
	Info(context.Context, string) (protocol.HostInfo, error)
	Containers(context.Context, string) ([]protocol.Container, error)
	Volumes(context.Context, string) ([]protocol.Volume, error)
	VolumeDetail(context.Context, string, string) (protocol.VolumeDetail, error)
	Images(context.Context, string) ([]protocol.Image, error)
	ImageDetail(context.Context, string, string) (protocol.ImageDetail, error)
	RemoveVolume(context.Context, string, string) error
	RemoveImage(context.Context, string, string) error
	Start(context.Context, string, string) error
	Stop(context.Context, string, string) error
	ListStacks(context.Context, string) ([]protocol.Stack, error)
	InspectStack(context.Context, string, string) (protocol.Stack, error)
	StackVersions(context.Context, string, string) ([]protocol.StackVersion, error)
	StackRevision(context.Context, string, string, int) (protocol.Stack, error)
	SaveStack(context.Context, string, protocol.StackSave) (protocol.StackSaved, error)
	StackOperation(context.Context, string, string, string) (protocol.StackOperation, error)
	StackImages(context.Context, string, string, bool) (protocol.StackImages, error)
	UpdateStackService(context.Context, string, string, string, func(string)) (protocol.StackOperation, error)
	SyncSecrets(context.Context, string, protocol.SecretSync) (protocol.SecretSyncResult, error)
	RetainSecrets(context.Context, string, []string) error
	SyncRegistries(context.Context, string, protocol.EncodedRegistrySync) (protocol.RegistrySynced, error)
	TestRegistry(context.Context, string, protocol.RegistryLogin) (protocol.RegistryTestResult, error)
}

type Server struct {
	sessions *Sessions
	relay    Relay
	// secrets, pusher and logins are nil when no secret store is configured.
	secrets *secretstore.Store
	pusher  *secretPusher
	logins  *loginPusher
	// passphrases limits wrong secret-store passphrases (unlock and reveal)
	// per client, as sessions limit sign-ins: a stolen session must not be
	// able to guess the passphrase at the speed of the key derivation.
	passphrases *loginLimiter
	// enrollment is nil until EnableEnrollment.
	enrollment *enrollment.Service
	controlURL string
}

// New creates the web API. store may be nil, which disables stack secrets.
func New(sessions SessionOptions, relay Relay, store *secretstore.Store) *Server {
	s := &Server{sessions: NewSessions(sessions), relay: relay, secrets: store, passphrases: newLoginLimiter(maxLoginClients)}
	if store != nil {
		locks := newHostLocks()
		s.pusher = newSecretPusher(store, relay, locks)
		s.logins = newLoginPusher(store, relay, locks)
	}
	return s
}

// Handler serves the web API under /api/ and /healthz. Every other path goes
// to ui (the dashboard), or is 404 when ui is nil. An unknown /api/ path is
// always 404, never the dashboard page.
func (s *Server) Handler(ui http.Handler) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	m.HandleFunc("/api/session/login", s.sessions.Login)
	m.HandleFunc("/api/session/logout", s.sessions.Logout)
	m.Handle("/api/secrets/", s.sessions.Require(http.HandlerFunc(s.secretStoreRoute)))
	m.Handle("/api/enrollment/", s.sessions.Require(http.HandlerFunc(s.enrollmentRoute)))
	m.Handle("/api/registries", s.sessions.Require(http.HandlerFunc(s.registriesRoute)))
	m.Handle("/api/registries/", s.sessions.Require(http.HandlerFunc(s.registriesRoute)))
	m.Handle("/api/hosts", s.sessions.Require(http.HandlerFunc(s.hosts)))
	m.Handle("/api/hosts/", s.sessions.Require(http.HandlerFunc(s.hostOperation)))
	m.Handle("/api/", http.NotFoundHandler())
	if ui != nil {
		m.Handle("/", ui)
	}
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
		if err == nil {
			s.explainWaiting(parts[0], stacks)
		}
		respond(w, stacks, err, http.StatusBadGateway)
		return
	}
	if len(parts) == 2 && parts[1] == "stacks" && r.Method == http.MethodPost {
		var req protocol.StackSave
		if jsonDecode(w, r, &req) != nil {
			http.Error(w, "invalid stack", http.StatusBadRequest)
			return
		}
		stack, err := s.relay.SaveStack(ctx, parts[0], req)
		respond(w, stack, err, http.StatusConflict)
		return
	}
	if len(parts) == 3 && parts[1] == "stacks" && r.Method == http.MethodGet {
		stack, err := s.relay.InspectStack(ctx, parts[0], parts[2])
		if err == nil {
			one := []protocol.Stack{stack}
			s.explainWaiting(parts[0], one)
			stack = one[0]
		}
		respond(w, stack, err, http.StatusNotFound)
		return
	}
	if len(parts) >= 4 && len(parts) <= 6 && parts[1] == "stacks" && parts[3] == "secrets" {
		name, action := "", ""
		if len(parts) >= 5 {
			name = parts[4]
		}
		if len(parts) == 6 {
			action = parts[5]
		}
		s.stackSecrets(w, r, parts[0], parts[2], name, action)
		return
	}
	if len(parts) == 4 && parts[1] == "stacks" && parts[3] == "versions" && r.Method == http.MethodGet {
		versions, err := s.relay.StackVersions(ctx, parts[0], parts[2])
		respond(w, versions, err, http.StatusNotFound)
		return
	}
	if len(parts) == 5 && parts[1] == "stacks" && parts[3] == "versions" && r.Method == http.MethodGet {
		version, err := strconv.Atoi(parts[4])
		if err != nil || version < 1 {
			http.Error(w, "revision must be a positive number", http.StatusBadRequest)
			return
		}
		revision, err := s.relay.StackRevision(ctx, parts[0], parts[2], version)
		respond(w, revision, err, http.StatusNotFound)
		return
	}
	if len(parts) == 3 && parts[1] == "images" && r.Method == http.MethodGet {
		image, err := s.relay.ImageDetail(ctx, parts[0], parts[2])
		respond(w, image, err, http.StatusNotFound)
		return
	}
	if len(parts) == 3 && parts[1] == "volumes" && r.Method == http.MethodGet {
		volume, err := s.relay.VolumeDetail(ctx, parts[0], parts[2])
		respond(w, volume, err, http.StatusNotFound)
		return
	}
	// A refused removal (a volume or image in use) is a conflict, not a failure of the route.
	if len(parts) == 3 && parts[1] == "volumes" && r.Method == http.MethodDelete {
		err := s.relay.RemoveVolume(ctx, parts[0], parts[2])
		respond(w, protocol.Removed{ID: parts[2]}, err, http.StatusConflict)
		return
	}
	if len(parts) == 3 && parts[1] == "images" && r.Method == http.MethodDelete {
		err := s.relay.RemoveImage(ctx, parts[0], parts[2])
		respond(w, protocol.Removed{ID: parts[2]}, err, http.StatusConflict)
		return
	}
	if len(parts) == 4 && parts[1] == "stacks" {
		if r.Method == http.MethodGet && parts[3] == "images" {
			images, err := s.relay.StackImages(ctx, parts[0], parts[2], r.URL.Query().Get("force") == "1")
			if err == nil {
				s.explainImages(parts[0], &images)
			}
			respond(w, images, err, http.StatusBadGateway)
			return
		}
		if r.Method == http.MethodPost && parts[3] == "update" {
			s.updateService(w, r, parts[0], parts[2])
			return
		}
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
		if jsonDecode(w, r, &request) != nil || request.ContainerID == "" {
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
