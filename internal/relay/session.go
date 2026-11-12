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
	pending map[string]waiter
	closed  bool
	err     error
	done    chan struct{}
}

// waiter is one pending request: where its response goes and, for a
// request that reads progress, its latest progress step.
type waiter struct {
	answer chan protocol.Message
	// progress holds at most the latest step; nil when the caller does not
	// read progress.
	progress chan string
}

// offer hands step to the waiter, replacing an unread older one, so the
// session's reader never waits for a slow caller. The reader is the only
// sender, so the second send cannot block.
func (w waiter) offer(step string) {
	select {
	case w.progress <- step:
	default:
		select {
		case <-w.progress:
		default:
		}
		w.progress <- step
	}
}

func newSession(conn *websocket.Conn, info protocol.HostInfo, heartbeat time.Duration) *session {
	return &session{
		conn:      conn,
		info:      info,
		heartbeat: heartbeat,
		pending:   map[string]waiter{},
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
		switch message.Type {
		case protocol.TypeResponse:
			s.mu.Lock()
			w, ok := s.pending[message.ID]
			delete(s.pending, message.ID)
			s.mu.Unlock()
			// A response without a waiter belongs to a request that already timed out
			// or was cancelled; its caller has been answered.
			if ok {
				w.answer <- message
			}
		case protocol.TypeProgress:
			var progress protocol.Progress
			if err := json.Unmarshal(message.Payload, &progress); err != nil {
				return s.close(fmt.Errorf("decode agent progress: %w", err))
			}
			s.mu.Lock()
			w, ok := s.pending[message.ID]
			s.mu.Unlock()
			// Progress for a request that is no longer pending, or whose caller
			// does not read progress, is dropped.
			if ok && w.progress != nil {
				w.offer(progress.Step)
			}
		default:
			return s.close(fmt.Errorf("unexpected agent message type %q", message.Type))
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

// writeFrame sends one encoded message, writing its parts in turn so a
// shared payload is never copied. Callers close the session on failure,
// because a partly written frame leaves the stream unusable.
func (s *session) writeFrame(f protocol.Frame) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	w, err := s.conn.NextWriter(websocket.TextMessage)
	if err != nil {
		return err
	}
	for _, part := range [...][]byte{f.Head, f.Payload, f.Tail} {
		if _, err := w.Write(part); err != nil {
			_ = w.Close()
			return err
		}
	}
	return w.Close()
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
	return s.callWithProgress(ctx, operation, payload, result, timeout, nil)
}

// callWithProgress is call for an operation that reports progress: onStep,
// when not nil, runs in the caller's goroutine with each step the agent
// reports, before the response. A slow onStep sees only the latest step.
func (s *session) callWithProgress(ctx context.Context, operation string, payload, result any, timeout time.Duration, onStep func(string)) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.send(ctx, operation, body, result, timeout, onStep)
}

// callEncoded is call with a payload already encoded as JSON by
// encoding/json, sent as is: many sessions can send the same bytes, which
// must not change until the call returns.
func (s *session) callEncoded(ctx context.Context, operation string, body json.RawMessage, result any, timeout time.Duration) error {
	return s.send(ctx, operation, body, result, timeout, nil)
}

func (s *session) send(ctx context.Context, operation string, body json.RawMessage, result any, timeout time.Duration, onStep func(string)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	id, err := newRequestID()
	if err != nil {
		return err
	}
	deadline, _ := ctx.Deadline()
	// Encode before registering so an oversized request fails alone rather than
	// taking the session down.
	frame, err := protocol.EncodeFrame(protocol.Message{
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

	w := waiter{answer: make(chan protocol.Message, 1)}
	if onStep != nil {
		w.progress = make(chan string, 1)
	}
	s.mu.Lock()
	switch {
	case s.closed:
		s.mu.Unlock()
		return ErrHostUnavailable
	case len(s.pending) >= protocol.MaxInFlight:
		s.mu.Unlock()
		return ErrHostBusy
	}
	s.pending[id] = w
	s.mu.Unlock()
	defer s.forget(id)

	if err := s.writeFrame(frame); err != nil {
		s.close(err)
		return fmt.Errorf("%w: %v", ErrDisconnected, err)
	}

	// w.progress is nil, and so never ready, when the caller reads no progress.
	for {
		select {
		case step := <-w.progress:
			onStep(step)
		case response := <-w.answer:
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
			if cancel, err := protocol.EncodeFrame(protocol.Message{Version: protocol.Version, ID: id, Type: protocol.TypeCancel}); err == nil {
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
