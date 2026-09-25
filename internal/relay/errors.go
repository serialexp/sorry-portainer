package relay

import (
	"errors"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

var (
	// ErrHostUnavailable reports that no agent session exists for the host.
	ErrHostUnavailable = errors.New("host unavailable")
	// ErrHostBusy reports that the host already has protocol.MaxInFlight requests.
	ErrHostBusy = errors.New("host busy")
	// ErrDisconnected reports that the agent session ended before it answered.
	ErrDisconnected = errors.New("host disconnected")
	// ErrTimeout reports that the request's time budget elapsed before an answer.
	ErrTimeout = errors.New("host request timed out")
)

// RemoteError is an error the agent reported for an operation it received. It
// means the relay worked and the operation itself failed.
type RemoteError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *RemoteError) Error() string { return e.Message }

// Is reports an agent-side busy refusal as ErrHostBusy. The server caps its own
// in-flight requests, but an operation it stopped waiting for may still be
// finishing on the agent, so the agent can briefly be fuller than the server.
func (e *RemoteError) Is(target error) bool {
	return target == ErrHostBusy && e.Code == protocol.CodeBusy
}

func remoteError(err *protocol.Error) *RemoteError {
	return &RemoteError{Code: err.Code, Message: err.Message, Retryable: err.Retryable}
}
