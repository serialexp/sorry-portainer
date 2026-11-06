// Package admin is the server's local administration socket,
// <state_dir>/admin.sock, and its client. One JSON request and one JSON
// response travel per connection. Running as the server's account or as root
// on the master is the authority to enroll and remove hosts.
package admin

import (
	"time"

	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/relay"
)

const (
	// SocketFile is the socket's name inside the state directory.
	SocketFile = "admin.sock"
	// MaxRequest bounds a request. A stack save carries a Compose file of up
	// to 1 MiB, which JSON escaping can double or more, and its environment;
	// the relay carries it in one message, so its limit is the useful one.
	MaxRequest = protocol.MaxMessageSize
	// MaxResponse bounds a response. A listed host takes about 160 bytes,
	// so this holds the list of some hundred thousand hosts.
	MaxResponse = 16 << 20
	// Timeout bounds the arrival of a request, and the whole exchange of the
	// enrollment actions.
	Timeout = 5 * time.Second
	// StackTimeout bounds a stack list, inspect or save; just above the
	// relay's own limit, so the relay's error arrives first.
	StackTimeout = relay.DefaultRequestTimeout + 5*time.Second
	// StackOperationTimeout bounds a stack up, down or restart.
	StackOperationTimeout = relay.StackOperationTimeout + time.Minute
	// MaxConnections is how many connections are answered at once. A stack
	// operation can hold one for minutes.
	MaxConnections = 8
)

const (
	ActionToken  = "token"
	ActionList   = "list"
	ActionRemove = "remove"

	ActionStackList    = "stacks.list"
	ActionStackInspect = "stacks.inspect"
	ActionStackSave    = "stacks.save"
	ActionStackUp      = "stacks.up"
	ActionStackDown    = "stacks.down"
	ActionStackRestart = "stacks.restart"
)

const (
	// CodeBusy is returned when MaxConnections are already being answered.
	CodeBusy = "busy"
	// CodeTimeout: the host did not answer in time.
	CodeTimeout = "timeout"
	// CodeFailed: the host ran the operation and reported a failure.
	CodeFailed = "failed"
)

// ActionTimeout is how long an exchange for action may take once the request
// has arrived.
func ActionTimeout(action string) time.Duration {
	switch action {
	case ActionStackList, ActionStackInspect, ActionStackSave:
		return StackTimeout
	case ActionStackUp, ActionStackDown, ActionStackRestart:
		return StackOperationTimeout
	default:
		return Timeout
	}
}

// Request is the one message a client sends.
type Request struct {
	Action string `json:"action"`
	HostID string `json:"host_id,omitempty"`
	// TTL is a Go duration; empty means the default token lifetime.
	TTL string `json:"ttl,omitempty"`
	// Stack names the stack for inspect, up, down and restart.
	Stack string `json:"stack,omitempty"`
	// Save is the stack to create or update. Its environment values are
	// never logged.
	Save *protocol.StackSave `json:"save,omitempty"`
}

// TokenResponse answers ActionToken. The token is a secret.
type TokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ListResponse answers ActionList.
type ListResponse struct {
	Hosts  []enrollment.HostStatus `json:"hosts"`
	Tokens []enrollment.TokenInfo  `json:"tokens"`
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
