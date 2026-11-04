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
	"time"

	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/unixsock"
)

// Server answers the admin socket.
type Server struct {
	enrollment *enrollment.Service
	connected  func() []protocol.HostInfo
	logf       func(format string, args ...any)
	// allowPeer decides which client uids are answered.
	allowPeer func(uid int) bool
	timeout   time.Duration
}

// NewServer answers requests from svc. connected lists the hosts with a
// session, for the online column. A nil logf uses log.Printf.
func NewServer(svc *enrollment.Service, connected func() []protocol.HostInfo, logf func(format string, args ...any)) *Server {
	if logf == nil {
		logf = log.Printf
	}
	self := os.Getuid()
	return &Server{enrollment: svc, connected: connected, logf: logf, timeout: Timeout,
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
	decoder := json.NewDecoder(io.LimitReader(conn, MaxMessage))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return invalid("invalid request: %v", err)
	}
	result, err := s.dispatch(request)
	if err != nil {
		var e *enrollment.Error
		if errors.As(err, &e) {
			return errorBody{Error: e.Message, Code: e.Code}
		}
		return errorBody{Error: err.Error(), Code: enrollment.CodeUnavailable}
	}
	return result
}

func invalid(format string, args ...any) errorBody {
	return errorBody{Error: fmt.Sprintf(format, args...), Code: enrollment.CodeInvalidRequest}
}

func (s *Server) dispatch(r Request) (any, error) {
	if r.Action != ActionList && !protocol.ValidHostID(r.HostID) {
		return invalid("host_id %q is not a valid host ID", r.HostID), nil
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
	default:
		return invalid("unknown action %q", r.Action), nil
	}
}

func (s *Server) list() ListResponse {
	online := map[string]bool{}
	for _, info := range s.connected() {
		online[info.HostID] = true
	}
	entries := s.enrollment.Registry().List()
	hosts := make([]HostStatus, len(entries))
	for i, e := range entries {
		hosts[i] = HostStatus{HostID: e.HostID, Prefix: e.Prefix, EnrolledAt: e.EnrolledAt, CurrentNotAfter: e.Current.NotAfter,
			Pending: e.Pending != (enrollment.CertRef{}), Online: online[e.HostID]}
	}
	tokens := s.enrollment.Tokens()
	if tokens == nil {
		tokens = []enrollment.TokenInfo{}
	}
	return ListResponse{Hosts: hosts, Tokens: tokens}
}
