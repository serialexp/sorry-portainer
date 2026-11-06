package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/relay"
	"github.com/serialexp/sorry-portainer/internal/unixsock"
)

// Relay reaches the agents; *relay.Remote is one.
type Relay interface {
	Hosts() []protocol.HostInfo
	ListStacks(ctx context.Context, hostID string) ([]protocol.Stack, error)
	InspectStack(ctx context.Context, hostID, name string) (protocol.Stack, error)
	SaveStack(ctx context.Context, hostID string, save protocol.StackSave) (protocol.Stack, error)
	StackOperation(ctx context.Context, hostID, name, operation string) (protocol.StackOperation, error)
}

// Server answers the admin socket.
type Server struct {
	enrollment *enrollment.Service
	relay      Relay
	// connected lists the hosts with a session, for the online column.
	connected func() []protocol.HostInfo
	logf      func(format string, args ...any)
	// allowPeer decides which client uids are answered.
	allowPeer func(uid int) bool
	// timeout bounds the arrival of a request; actionTimeout the rest of
	// the exchange once its action is known.
	timeout       time.Duration
	actionTimeout func(action string) time.Duration
}

// NewServer answers requests from svc, and reaches agents through r. A nil
// logf uses log.Printf.
func NewServer(svc *enrollment.Service, r Relay, logf func(format string, args ...any)) *Server {
	if logf == nil {
		logf = log.Printf
	}
	self := os.Getuid()
	return &Server{enrollment: svc, relay: r, connected: r.Hosts, logf: logf, timeout: Timeout, actionTimeout: ActionTimeout,
		allowPeer: func(uid int) bool { return peerAllowed(uid, self) }}
}

// peerAllowed: the server answers its own account and root.
func peerAllowed(uid, self int) bool { return uid == self || uid == 0 }

// Listen creates the socket in stateDir. The caller must hold the state
// lock, which is what makes replacing a stale socket safe.
func Listen(stateDir string) (*net.UnixListener, error) {
	return unixsock.Listen(socketPath(stateDir))
}

// Serve answers connections until ctx ends.
func (s *Server) Serve(ctx context.Context, listener *net.UnixListener) error {
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	slots := make(chan struct{}, MaxConnections)
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			// Saying why is cheaper than leaving the client to time out.
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			_ = json.NewEncoder(conn).Encode(errorBody{Error: "the admin socket is busy; try again", Code: CodeBusy})
			_ = conn.Close()
			continue
		}
		go func() {
			defer func() { <-slots }()
			s.handle(conn)
		}()
	}
}

func (s *Server) handle(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(s.timeout))
	response := s.answer(conn)
	// A request that ran out of time still gets told why.
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	data, err := json.Marshal(response)
	if err != nil {
		data, _ = json.Marshal(errorBody{Error: "cannot encode the response", Code: enrollment.CodeUnavailable})
	}
	_, _ = conn.Write(append(data, '\n'))
}

func (s *Server) answer(conn *net.UnixConn) any {
	uid, err := unixsock.PeerUID(conn)
	if err != nil {
		return errorBody{Error: err.Error(), Code: enrollment.CodeUnavailable}
	}
	if !s.allowPeer(uid) {
		s.logf("admin socket: refused peer uid %d", uid)
		return errorBody{Error: "the admin socket answers only the server's account and root", Code: "forbidden"}
	}
	var request Request
	decoder := json.NewDecoder(io.LimitReader(conn, MaxRequest))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return invalid("invalid request: %v", err)
	}
	timeout := s.actionTimeout(request.Action)
	_ = conn.SetDeadline(time.Now().Add(timeout))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// The client sends nothing after its request and keeps the connection
	// open for the answer, so a read that ends means it went away (or the
	// deadline passed): stop waiting, and let the relay cancel the agent's
	// work.
	go func() {
		var b [1]byte
		_, _ = conn.Read(b[:])
		cancel()
	}()
	result, err := s.dispatch(ctx, request)
	if err != nil {
		return failure(err)
	}
	return result
}

// failure turns an enrollment or relay error into the socket's answer.
func failure(err error) errorBody {
	var e *enrollment.Error
	var remote *relay.RemoteError
	switch {
	case errors.As(err, &e):
		return errorBody{Error: e.Message, Code: e.Code}
	case errors.As(err, &remote):
		return errorBody{Error: remote.Message, Code: CodeFailed}
	case errors.Is(err, relay.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return errorBody{Error: err.Error(), Code: CodeTimeout}
	default:
		return errorBody{Error: err.Error(), Code: enrollment.CodeUnavailable}
	}
}

func invalid(format string, args ...any) errorBody {
	return errorBody{Error: fmt.Sprintf(format, args...), Code: enrollment.CodeInvalidRequest}
}

func (s *Server) dispatch(ctx context.Context, r Request) (any, error) {
	if r.Action != ActionList && !protocol.ValidHostID(r.HostID) {
		return invalid("host_id %q is not a valid host ID", r.HostID), nil
	}
	if !strings.HasPrefix(r.Action, "stacks.") && (r.Stack != "" || r.Save != nil) {
		return invalid("%s takes no stack or save", r.Action), nil
	}
	switch r.Action {
	case ActionToken:
		var ttl time.Duration
		if r.TTL != "" {
			var err error
			if ttl, err = time.ParseDuration(r.TTL); err != nil || ttl <= 0 {
				return invalid("ttl %q is not a positive duration", r.TTL), nil
			}
		}
		token, expires, err := s.enrollment.CreateToken(r.HostID, ttl)
		if err != nil {
			return nil, err
		}
		s.logf("admin socket: enrollment token for %s, expires %s", r.HostID, expires.Format(time.RFC3339))
		return TokenResponse{Token: token, ExpiresAt: expires}, nil
	case ActionList:
		if r.HostID != "" || r.TTL != "" {
			return invalid("list takes no host_id or ttl"), nil
		}
		return s.list(), nil
	case ActionRemove:
		if r.TTL != "" {
			return invalid("remove takes no ttl"), nil
		}
		if err := s.enrollment.Remove(r.HostID); err != nil {
			return nil, err
		}
		s.logf("admin socket: removed host %s", r.HostID)
		return RemoveResponse{}, nil
	case ActionStackList, ActionStackInspect, ActionStackSave, ActionStackUp, ActionStackDown, ActionStackRestart:
		return s.stacks(ctx, r)
	default:
		return invalid("unknown action %q", r.Action), nil
	}
}

// stacks answers the stack actions for one host, through the same relay
// calls as the web API. Environment values are never logged.
func (s *Server) stacks(ctx context.Context, r Request) (any, error) {
	if r.TTL != "" {
		return invalid("%s takes no ttl", r.Action), nil
	}
	switch r.Action {
	case ActionStackList:
		if r.Stack != "" || r.Save != nil {
			return invalid("stacks.list takes no stack or save"), nil
		}
		stacks, err := s.relay.ListStacks(ctx, r.HostID)
		if err == nil && stacks == nil {
			stacks = []protocol.Stack{}
		}
		return stacks, err
	case ActionStackSave:
		if r.Stack != "" || r.Save == nil {
			return invalid("stacks.save takes a save and no stack"), nil
		}
		if !protocol.ValidStackName(r.Save.Name) {
			return invalid("stack name %q is not valid", r.Save.Name), nil
		}
		what := "create"
		if r.Save.ExpectedVersion != nil {
			what = fmt.Sprintf("update from version %d", *r.Save.ExpectedVersion)
		}
		s.logf("admin socket: stacks.save %s/%s (%s, %d environment entries)", r.HostID, r.Save.Name, what, len(r.Save.Environment))
		return s.relay.SaveStack(ctx, r.HostID, *r.Save)
	}
	if r.Save != nil || !protocol.ValidStackName(r.Stack) {
		return invalid("%s takes a valid stack name and no save", r.Action), nil
	}
	if r.Action == ActionStackInspect {
		return s.relay.InspectStack(ctx, r.HostID, r.Stack)
	}
	s.logf("admin socket: %s %s/%s", r.Action, r.HostID, r.Stack)
	// A Compose run that failed is an answer with Success false and the
	// output, not an error.
	return s.relay.StackOperation(ctx, r.HostID, r.Stack, strings.TrimPrefix(r.Action, "stacks."))
}

func (s *Server) list() ListResponse {
	online := map[string]bool{}
	for _, info := range s.connected() {
		online[info.HostID] = true
	}
	hosts := s.enrollment.Registry().Statuses(online)
	tokens := s.enrollment.Tokens()
	if tokens == nil {
		tokens = []enrollment.TokenInfo{}
	}
	return ListResponse{Hosts: hosts, Tokens: tokens}
}
