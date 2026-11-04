package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/serialexp/sorry-portainer/internal/agentsetup"
	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// maxEnrollmentBody bounds a token request.
const maxEnrollmentBody = 4 << 10

// EnableEnrollment serves host enrollment under /api/enrollment/. serverURL
// is the relay URL agents dial, shown in the enroll command.
func (s *Server) EnableEnrollment(svc *enrollment.Service, serverURL string) {
	s.enrollment = svc
	s.controlURL = serverURL
}

// TokenRequest asks for an enrollment token. TTL is a Go duration; empty is
// the default lifetime.
type TokenRequest struct {
	HostID string `json:"host_id"`
	TTL    string `json:"ttl"`
}

// TokenResponse carries a new token. It is shown once and never stored.
type TokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	// Command enrolls the host once setup has run there; it asks for the
	// token.
	Command string `json:"command"`
	// Reenroll is true when the host is already enrolled: Command replaces
	// the agent's configuration, and the running agent must be restarted.
	Reenroll bool `json:"reenroll"`
}

// enrollmentRoute serves:
//
//	GET    /api/enrollment/hosts         enrolled hosts, online or not
//	DELETE /api/enrollment/hosts/{host}  remove a host, revoke its token, disconnect it
//	GET    /api/enrollment/tokens        outstanding tokens (never their secrets)
//	POST   /api/enrollment/tokens        create a token
//	DELETE /api/enrollment/tokens/{host} revoke a token
func (s *Server) enrollmentRoute(w http.ResponseWriter, r *http.Request) {
	if s.enrollment == nil {
		http.Error(w, "host enrollment is not configured on this server", http.StatusServiceUnavailable)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/enrollment/"), "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] == "hosts":
		if !allow(w, r, http.MethodGet) {
			return
		}
		online := map[string]bool{}
		for _, info := range s.relay.Hosts() {
			online[info.HostID] = true
		}
		writeJSON(w, s.enrollment.Registry().Statuses(online))
	case len(parts) == 2 && parts[0] == "hosts":
		if !allow(w, r, http.MethodDelete) {
			return
		}
		if err := s.enrollment.Remove(parts[1]); err != nil {
			enrollmentError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 1 && parts[0] == "tokens":
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, s.enrollment.Tokens())
		case http.MethodPost:
			s.createToken(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case len(parts) == 2 && parts[0] == "tokens":
		if !allow(w, r, http.MethodDelete) {
			return
		}
		if !s.enrollment.RevokeToken(parts[1]) {
			http.Error(w, "no outstanding token for "+parts[1], http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func allow(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func decodeTokenRequest(w http.ResponseWriter, r *http.Request, req *TokenRequest) error {
	boundBodyRead(w)
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEnrollmentBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(req); err != nil {
		return err
	}
	if decoder.Decode(new(json.RawMessage)) != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var req TokenRequest
	if decodeTokenRequest(w, r, &req) != nil {
		http.Error(w, "invalid token request", http.StatusBadRequest)
		return
	}
	var ttl time.Duration
	if req.TTL != "" {
		var err error
		if ttl, err = time.ParseDuration(req.TTL); err != nil || ttl <= 0 {
			http.Error(w, "ttl must be a positive duration such as 1h", http.StatusBadRequest)
			return
		}
	}
	if !protocol.ValidHostID(req.HostID) {
		http.Error(w, "host ID must be 1-128 letters, digits, '.', '_' or '-', starting with a letter or digit", http.StatusBadRequest)
		return
	}
	token, expires, err := s.enrollment.CreateToken(req.HostID, ttl)
	if err != nil {
		enrollmentError(w, err)
		return
	}
	_, reenroll := s.enrollment.Registry().Get(req.HostID)
	// The response holds a secret.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, TokenResponse{Token: token, ExpiresAt: expires, Reenroll: reenroll,
		Command: agentsetup.EnrollCommand(agentsetup.DefaultUser, s.controlURL, req.HostID, reenroll)})
}

func enrollmentError(w http.ResponseWriter, err error) {
	var e *enrollment.Error
	if errors.As(err, &e) {
		http.Error(w, e.Message, e.Status)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
