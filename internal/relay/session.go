package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

const (
	// DefaultHeartbeat is the server's ping interval. A peer that shows no sign of
	// life for heartbeatMisses intervals is treated as gone.
	DefaultHeartbeat = 15 * time.Second
	heartbeatMisses  = 3
	// writeTimeout bounds every frame write so a stalled peer cannot hold the
	// session's write lock.
	writeTimeout = 10 * time.Second
)

// session is the server side of one agent connection. A single reader goroutine
// owns all reads and routes each response to the request waiting for its ID, so
// requests run concurrently and a late response can never be mistaken for the
// answer to a different request.
type session struct {
	conn      *websocket.Conn
	info      protocol.HostInfo
	heartbeat time.Duration

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[string]chan protocol.Message
	closed  bool
	err     error
	done    chan struct{}
}

func newSession(conn *websocket.Conn, info protocol.HostInfo, heartbeat time.Duration) *session {
	return &session{
		conn:      conn,
		info:      info,
		heartbeat: heartbeat,
		pending:   map[string]chan protocol.Message{},
		done:      make(chan struct{}),
	}
}

// run reads until the connection fails, then fails every pending request.
func (s *session) run() error {
	stopPing := make(chan struct{})
	defer close(stopPing)
	go s.ping(stopPing)

	s.conn.SetReadLimit(protocol.MaxMessageSize)
	alive := func() error {
		return s.conn.SetReadDeadline(time.Now().Add(heartbeatMisses * s.heartbeat))
	}
	s.conn.SetPongHandler(func(string) error { return alive() })
	for {
		if err := alive(); err != nil {
			return s.close(err)
		}
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			return s.close(err)
		}
		message, err := protocol.Decode(data)
		if err != nil {
			return s.close(err)
		}
		if message.Type != protocol.TypeResponse {
			return s.close(fmt.Errorf("unexpected agent message type %q", message.Type))
		}
		s.mu.Lock()
		waiter := s.pending[message.ID]
		delete(s.pending, message.ID)
		s.mu.Unlock()
		// A response without a waiter belongs to a request that already timed out
		// or was cancelled; its caller has been answered.
		if waiter != nil {
			waiter <- message
		}
	}
}

func (s *session) ping(stop <-chan struct{}) {
	ticker := time.NewTicker(s.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			// WriteControl is safe to call concurrently with other writes.
			if err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				s.close(err)
				return
			}
		}
	}
}

// close ends the session once. Every waiting request observes done.
func (s *session) close(cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.err
	}
	s.closed = true
	s.err = cause
	s.pending = nil
	close(s.done)
	_ = s.conn.Close()
	return cause
}

// writeFrame sends one encoded message. Callers close the session on failure,
// because a partly written frame leaves the stream unusable.
func (s *session) writeFrame(data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func newRequestID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

// call sends one request and waits for its response, the session's end, or the
// context. On timeout or cancellation it asks the agent to stop the operation.
func (s *session) call(ctx context.Context, operation string, payload, result any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	id, err := newRequestID()
	if err != nil {
		return err
	}
	deadline, _ := ctx.Deadline()
	// Encode before registering so an oversized request fails alone rather than
	// taking the session down.
	data, err := protocol.Encode(protocol.Message{
		Version:   protocol.Version,
		ID:        id,
		Type:      protocol.TypeRequest,
		Operation: operation,
		HostID:    s.info.HostID,
		TimeoutMS: max(time.Until(deadline).Milliseconds(), 1),
		Payload:   body,
	})
	if err != nil {
		return fmt.Errorf("encode %s request: %w", operation, err)
	}

	answer := make(chan protocol.Message, 1)
	s.mu.Lock()
	switch {
	case s.closed:
		s.mu.Unlock()
		return ErrHostUnavailable
	case len(s.pending) >= protocol.MaxInFlight:
		s.mu.Unlock()
		return ErrHostBusy
	}
	s.pending[id] = answer
	s.mu.Unlock()
	defer s.forget(id)

	if err := s.writeFrame(data); err != nil {
		s.close(err)
		return fmt.Errorf("%w: %v", ErrDisconnected, err)
	}

	select {
	case response := <-answer:
		if response.Error != nil {
			return remoteError(response.Error)
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(response.Payload, result)
	case <-s.done:
		return fmt.Errorf("%w: %v", ErrDisconnected, s.cause())
	case <-ctx.Done():
		// Best effort: the agent may already be finishing. Its late response is
		// dropped because this request is no longer pending.
		if cancel, err := protocol.Encode(protocol.Message{Version: protocol.Version, ID: id, Type: protocol.TypeCancel}); err == nil {
			if err := s.writeFrame(cancel); err != nil {
				s.close(err)
			}
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%w: %s after %s", ErrTimeout, operation, timeout)
		}
		return ctx.Err()
	}
}

func (s *session) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != nil {
		delete(s.pending, id)
	}
}

func (s *session) cause() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
