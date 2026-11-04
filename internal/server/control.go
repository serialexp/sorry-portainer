package server

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/pki"
	"github.com/serialexp/sorry-portainer/internal/relay"
)

const (
	// maxControlBody bounds enroll and renew requests.
	maxControlBody = 64 << 10
	// maxUnauthenticated is how many requests without a client certificate
	// may be handled at once.
	maxUnauthenticated = 8
	// failureLogEntries bounds the per-address table that rate-limits
	// failure logs; failureLogEvery is how often one address is logged.
	failureLogEntries = 1024
	failureLogEvery   = time.Minute
)

// Control serves the mTLS control listener: agent sessions, enrollment and
// renewal.
type Control struct {
	Remote     *relay.Remote
	Enrollment *enrollment.Service
	Logf       func(string, ...any)

	unauthenticated chan struct{}
	failures        *failureLog
	once            sync.Once
}

func (c *Control) init() {
	c.once.Do(func() {
		c.unauthenticated = make(chan struct{}, maxUnauthenticated)
		if c.Logf == nil {
			c.Logf = log.Printf
		}
		c.failures = newFailureLog(failureLogEntries, failureLogEvery, c.Logf)
	})
}

func (c *Control) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.init()
	if r.TLS == nil {
		writeControlError(w, http.StatusBadRequest, "invalid_request", "TLS required")
		return
	}
	if len(r.TLS.PeerCertificates) == 0 {
		select {
		case c.unauthenticated <- struct{}{}:
			defer func() { <-c.unauthenticated }()
		default:
			writeControlError(w, http.StatusServiceUnavailable, "busy", "too many enrollment requests; retry shortly")
			return
		}
	}
	switch r.URL.Path {
	case "/control/agent":
		c.agent(w, r)
	case "/control/enroll":
		c.enroll(w, r)
	case "/control/renew":
		c.renew(w, r)
	default:
		http.NotFound(w, r)
	}
}

// agentIdentity returns the verified client certificate and its host ID.
// The TLS layer has verified the chain; validity and enrollment are checked
// by the enrollment service on every request.
func agentIdentity(r *http.Request) (*x509.Certificate, string, error) {
	if len(r.TLS.PeerCertificates) == 0 {
		return nil, "", errors.New("client certificate required")
	}
	cert := r.TLS.PeerCertificates[0]
	hostID, err := pki.AgentHostID(cert)
	return cert, hostID, err
}

func (c *Control) agent(w http.ResponseWriter, r *http.Request) {
	cert, hostID, err := agentIdentity(r)
	if err != nil {
		writeControlError(w, http.StatusUnauthorized, "invalid_request", err.Error())
		return
	}
	standing, err := c.Enrollment.Check(hostID, cert)
	if err != nil {
		c.failures.log(r, "agent %s refused: %v", hostID, err)
		writeEnrollmentError(w, err)
		return
	}
	if standing == enrollment.StandingPending {
		// The handshake proved the agent holds the renewed key: make it
		// current, durably, before the session counts.
		if err := c.Enrollment.Promote(hostID, cert); err != nil {
			writeEnrollmentError(w, err)
			return
		}
		c.Logf("agent %s connected with its renewed certificate; it is now current", hostID)
	}
	// The control listener is mTLS-only and is never exposed as a browser
	// surface; agent clients do not send browser Origin headers.
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	// The WebSocket manages its own deadlines from here on.
	_ = conn.NetConn().SetDeadline(time.Time{})
	// Serve owns the hijacked connection and returns when it ends, so a
	// disconnected agent is removed promptly rather than on request cancel.
	admit := func() error { return c.Enrollment.Authorize(hostID, cert) }
	if err := c.Remote.Serve(conn, hostID, admit); err != nil {
		c.Logf("agent %s disconnected: %v", hostID, err)
	}
}

type renewRequest struct {
	CSR string `json:"csr"`
}

type renewResponse struct {
	Certificate string `json:"certificate"`
}

func (c *Control) renew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeControlError(w, http.StatusMethodNotAllowed, "invalid_request", "POST required")
		return
	}
	cert, hostID, err := agentIdentity(r)
	if err != nil {
		writeControlError(w, http.StatusUnauthorized, "invalid_request", err.Error())
		return
	}
	var req renewRequest
	if err := decodeControl(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	renewed, err := c.Enrollment.Renew(hostID, cert, []byte(req.CSR))
	if err != nil {
		c.failures.log(r, "renewal for %s refused: %v", hostID, err)
		writeEnrollmentError(w, err)
		return
	}
	c.Logf("renewed the certificate of %s until %s", hostID, renewed.NotAfter.UTC().Format(time.RFC3339))
	writeJSON(w, renewResponse{Certificate: string(pki.EncodeCertificate(renewed))})
}

func (c *Control) enroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeControlError(w, http.StatusMethodNotAllowed, "invalid_request", "POST required")
		return
	}
	var req enrollment.EnrollRequest
	if err := decodeControl(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	resp, done, err := c.Enrollment.Enroll(req)
	if err != nil {
		// Only the token ID is logged, never the token.
		c.failures.log(r, "enrollment of %q with token %s refused: %v", done.HostID, done.TokenID, err)
		writeEnrollmentError(w, err)
		return
	}
	if done.Retry {
		c.Logf("enrollment of %s with token %s repeated; returned the same certificate", done.HostID, done.TokenID)
	} else {
		c.Logf("enrolled %s with token %s from %s", done.HostID, done.TokenID, remoteHost(r))
	}
	writeJSON(w, resp)
}

func decodeControl(w http.ResponseWriter, r *http.Request, v any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	if decoder.Decode(new(json.RawMessage)) != io.EOF {
		return errors.New("decode request: trailing data")
	}
	return nil
}

// writeDecodeError answers a request whose body could not be decoded; an
// oversized one gets 413 so the caller knows retrying will not help.
func writeDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeControlError(w, http.StatusRequestEntityTooLarge, "invalid_request", fmt.Sprintf("request body exceeds %d bytes", maxControlBody))
		return
	}
	writeControlError(w, http.StatusBadRequest, "invalid_request", err.Error())
}

type controlError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeControlError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(controlError{Error: message, Code: code})
}

func writeEnrollmentError(w http.ResponseWriter, err error) {
	var coded *enrollment.Error
	if errors.As(err, &coded) {
		writeControlError(w, coded.Status, coded.Code, coded.Message)
		return
	}
	writeControlError(w, http.StatusInternalServerError, "internal", "internal error")
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// failureLog logs refusals at most once per address per interval, counting
// what it suppressed, from a bounded table, so unauthenticated requests
// cannot fill the disk with log lines.
type failureLog struct {
	mu       sync.Mutex
	entries  map[string]*failureEntry
	order    []string // insertion order, for dropping the oldest
	limit    int
	interval time.Duration
	now      func() time.Time
	logf     func(string, ...any)
}

type failureEntry struct {
	last       time.Time
	suppressed int
}

func newFailureLog(limit int, interval time.Duration, logf func(string, ...any)) *failureLog {
	return &failureLog{entries: map[string]*failureEntry{}, limit: limit, interval: interval, now: time.Now, logf: logf}
}

func (f *failureLog) log(r *http.Request, format string, args ...any) {
	addr := remoteHost(r)
	now := f.now()
	f.mu.Lock()
	entry := f.entries[addr]
	if entry == nil {
		if len(f.order) >= f.limit {
			oldest := f.order[0]
			f.order = f.order[1:]
			delete(f.entries, oldest)
		}
		entry = &failureEntry{}
		f.entries[addr] = entry
		f.order = append(f.order, addr)
	} else if now.Sub(entry.last) < f.interval {
		entry.suppressed++
		f.mu.Unlock()
		return
	}
	suppressed := entry.suppressed
	entry.last, entry.suppressed = now, 0
	f.mu.Unlock()
	message := fmt.Sprintf(format, args...)
	if suppressed > 0 {
		message += fmt.Sprintf(" (%d similar from this address suppressed)", suppressed)
	}
	f.logf("control %s: %s", addr, strings.TrimSpace(message))
}
