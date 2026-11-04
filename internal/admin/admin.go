// Package admin is the server's local administration socket,
// <state_dir>/admin.sock, and its client. One JSON request and one JSON
// response travel per connection. Running as the server's account or as root
// on the master is the authority to enroll and remove hosts.
package admin

import (
	"time"

	"github.com/serialexp/sorry-portainer/internal/enrollment"
)

const (
	// SocketFile is the socket's name inside the state directory.
	SocketFile = "admin.sock"
	// MaxMessage bounds a request and a response.
	MaxMessage = 64 << 10
	// Timeout bounds a whole exchange.
	Timeout = 5 * time.Second
	// MaxConnections is how many connections are answered at once.
	MaxConnections = 4
)

const (
	ActionToken  = "token"
	ActionList   = "list"
	ActionRemove = "remove"
)

// CodeBusy is returned when MaxConnections are already being answered.
const CodeBusy = "busy"

// Request is the one message a client sends.
type Request struct {
	Action string `json:"action"`
	HostID string `json:"host_id,omitempty"`
	// TTL is a Go duration; empty means the default token lifetime.
	TTL string `json:"ttl,omitempty"`
}

// TokenResponse answers ActionToken. The token is a secret.
type TokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// HostStatus is one enrolled host.
type HostStatus struct {
	HostID          string    `json:"host_id"`
	Prefix          string    `json:"prefix"`
	EnrolledAt      time.Time `json:"enrolled_at"`
	CurrentNotAfter time.Time `json:"current_not_after"`
	// Pending is true while a renewed certificate waits for the agent to
	// connect with it.
	Pending bool `json:"pending"`
	Online  bool `json:"online"`
}

// ListResponse answers ActionList.
type ListResponse struct {
	Hosts  []HostStatus           `json:"hosts"`
	Tokens []enrollment.TokenInfo `json:"tokens"`
}

// RemoveResponse answers ActionRemove.
type RemoveResponse struct{}

// errorBody is the response to any failure.
type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// Error is a failure the server reported.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }
