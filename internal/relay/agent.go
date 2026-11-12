package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/stacks"
)

// Agent answers relay requests for one host against its local Podman runtime.
type Agent struct {
	HostID, Prefix string
	Handler        Handler
	Stacks         *stacks.Manager
	// Registries holds the host's registry logins; nil refuses the
	// registries operations.
	Registries Registries
	// Heartbeat is the server's ping interval; zero selects DefaultHeartbeat.
	// The agent treats heartbeatMisses silent intervals as a dead connection.
	Heartbeat time.Duration
	// Describe, when set, adds agent-side facts (swap, secret readiness) to
	// the host info sent in each hello.
	Describe func(*protocol.HostInfo)
	// DrainTimeout bounds how long a requested reconnect waits for in-flight
	// operations; zero selects DefaultDrainTimeout.
	DrainTimeout time.Duration

	reconnectOnce sync.Once
	reconnect     chan struct{}
}

// DefaultDrainTimeout is how long a reconnect waits for running operations.
const DefaultDrainTimeout = 5 * time.Minute

// ErrReconnectRequested ends a session that drained because RequestReconnect
// was called; RunAgent dials again at once.
var ErrReconnectRequested = errors.New("reconnect requested")

func (a *Agent) reconnectSignal() chan struct{} {
	a.reconnectOnce.Do(func() { a.reconnect = make(chan struct{}, 1) })
	return a.reconnect
}

// RequestReconnect makes the current session stop taking requests, wait for
// the running ones (up to DrainTimeout), and end, so the next connection is
// made with the agent's current credentials. A request made while no session
// runs applies to the next one.
func (a *Agent) RequestReconnect() {
	select {
	case a.reconnectSignal() <- struct{}{}:
	default:
	}
}

var errStacksUnavailable = errors.New("stack agent unavailable")

var errRegistriesUnavailable = errors.New("this agent was started without registry logins")

// Registries is the agent's registry login set; registryvault.Vault is the
// implementation, behind an adapter that picks how Test signs in.
type Registries interface {
	Sync(protocol.RegistrySync) (protocol.RegistrySynced, error)
	Test(ctx context.Context, login protocol.RegistryLogin) (protocol.RegistryTestResult, error)
}

// agentSession is the agent side of one connection. The reader goroutine
// dispatches each request to its own worker, bounded by protocol.MaxInFlight;
// workers share one write lock.
type agentSession struct {
	agent *Agent
	conn  *websocket.Conn

	writeMu sync.Mutex

	mu       sync.Mutex
	inFlight map[string]context.CancelFunc
	workers  sync.WaitGroup
	draining bool
}

// Serve runs one connection until it fails or ctx ends. In-flight operations are
// cancelled and awaited before Serve returns, so no Podman child outlives it.
func (a *Agent) Serve(ctx context.Context, conn *websocket.Conn) error {
	if conn == nil || a.Handler == nil || a.HostID == "" || a.Prefix == "" {
		return errors.New("invalid agent")
	}
	defer conn.Close()
	heartbeat := a.Heartbeat
	if heartbeat <= 0 {
		heartbeat = DefaultHeartbeat
	}

	info, err := a.Handler.Info(ctx)
	if err != nil {
		return fmt.Errorf("read host info: %w", err)
	}
	info.HostID, info.Prefix = a.HostID, a.Prefix
	if a.Describe != nil {
		a.Describe(&info)
	}
	payload, err := json.Marshal(info)
	if err != nil {
		return err
	}
	s := &agentSession{agent: a, conn: conn, inFlight: map[string]context.CancelFunc{}}
	if err := s.write(protocol.Message{Version: protocol.Version, ID: "hello", Type: protocol.TypeHello, HostID: a.HostID, Payload: payload}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		s.workers.Wait()
	}()
	// Closing the connection is the only way to interrupt a blocked read.
	stopWatch := context.AfterFunc(sessionCtx, func() { _ = conn.Close() })
	defer stopWatch()
	drained := make(chan struct{})
	go func() {
		select {
		case <-sessionCtx.Done():
			return
		case <-a.reconnectSignal():
		}
		closing := s.drain(sessionCtx, a.drainTimeout())
		// Marked drained before the close frame goes out: the server's
		// answering close ends the read loop, which must then report the
		// reconnect.
		close(drained)
		if closing {
			// WriteControl may run concurrently with other writes.
			_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "reconnecting"), time.Now().Add(writeTimeout))
		}
		cancel()
	}()
	ended := func(err error) error {
		select {
		case <-drained:
			return ErrReconnectRequested
		default:
			return err
		}
	}

	conn.SetReadLimit(protocol.MaxMessageSize)
	alive := func() error { return conn.SetReadDeadline(time.Now().Add(heartbeatMisses * heartbeat)) }
	conn.SetPingHandler(func(data string) error {
		if err := alive(); err != nil {
			return err
		}
		err := conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(writeTimeout))
		if errors.Is(err, websocket.ErrCloseSent) {
			return nil
		}
		return err
	})
	for {
		if err := alive(); err != nil {
			return err
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ended(err)
		}
		message, err := protocol.Decode(data)
		if err != nil {
			return err
		}
		switch message.Type {
		case protocol.TypeRequest:
			s.start(sessionCtx, message)
		case protocol.TypeCancel:
			s.cancel(message.ID)
		default:
			return fmt.Errorf("unexpected server message type %q", message.Type)
		}
	}
}

func (a *Agent) drainTimeout() time.Duration {
	if a.DrainTimeout > 0 {
		return a.DrainTimeout
	}
	return DefaultDrainTimeout
}

// drain stops new requests and waits for the running ones up to timeout. It
// reports whether the session should close with a normal close frame: not
// when ctx ended first.
func (s *agentSession) drain(ctx context.Context, timeout time.Duration) bool {
	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()
	idle := make(chan struct{})
	go func() {
		s.workers.Wait()
		close(idle)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-idle:
	case <-timer.C:
	case <-ctx.Done():
		return false
	}
	return true
}

func (s *agentSession) start(ctx context.Context, request protocol.Message) {
	timeout := time.Duration(request.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		s.respondError(request.ID, &protocol.Error{Code: protocol.CodeBusy, Message: "agent is reconnecting", Retryable: true})
		return
	}
	if _, duplicate := s.inFlight[request.ID]; duplicate || len(s.inFlight) >= protocol.MaxInFlight {
		s.mu.Unlock()
		s.respondError(request.ID, &protocol.Error{Code: protocol.CodeBusy, Message: "agent is at its concurrent request limit", Retryable: true})
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	s.inFlight[request.ID] = cancel
	s.workers.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.workers.Done()
		defer s.cancel(request.ID)
		report := func(step string) { s.progress(request.ID, step) }
		value, err := s.agent.handle(opCtx, request.Operation, request.Payload, report)
		if err != nil {
			// The session is ending, which is what stopped the operation. Its
			// cancellation error says nothing about the operation, and a reply
			// would race the connection close; with no reply, the server
			// reports the disconnect instead.
			if ctx.Err() != nil {
				return
			}
			code := protocol.CodeOperationFailed
			var unknown unknownOperationError
			if errors.As(err, &unknown) {
				code = protocol.CodeUnknownOperation
			}
			s.respondError(request.ID, &protocol.Error{Code: code, Message: err.Error(), Retryable: code == protocol.CodeOperationFailed})
			return
		}
		body, err := json.Marshal(value)
		if err != nil {
			s.respondError(request.ID, &protocol.Error{Code: protocol.CodeOperationFailed, Message: err.Error()})
			return
		}
		data, err := protocol.Encode(protocol.Message{Version: protocol.Version, ID: request.ID, Type: protocol.TypeResponse, Payload: body})
		switch {
		case errors.Is(err, protocol.ErrMessageTooLarge):
			s.respondError(request.ID, &protocol.Error{Code: protocol.CodeResponseTooLarge, Message: fmt.Sprintf("%s response exceeds %d bytes", request.Operation, protocol.MaxMessageSize)})
		case err != nil:
			s.respondError(request.ID, &protocol.Error{Code: protocol.CodeOperationFailed, Message: err.Error()})
		default:
			_ = s.writeFrame(data)
		}
	}()
}

// cancel stops one in-flight operation; it is idempotent.
func (s *agentSession) cancel(id string) {
	s.mu.Lock()
	cancel := s.inFlight[id]
	delete(s.inFlight, id)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *agentSession) write(message protocol.Message) error {
	data, err := protocol.Encode(message)
	if err != nil {
		return err
	}
	return s.writeFrame(data)
}

// writeFrame sends one encoded message. A failed write poisons the connection,
// which ends the reader and therefore the session; callers need not report it
// further.
func (s *agentSession) writeFrame(data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		_ = s.conn.Close()
		return err
	}
	return nil
}

// progress tells the server how far request id got. A failed write ends the
// session, as for any other frame.
func (s *agentSession) progress(id, step string) {
	body, err := json.Marshal(protocol.Progress{Step: step})
	if err != nil {
		return
	}
	_ = s.write(protocol.Message{Version: protocol.Version, ID: id, Type: protocol.TypeProgress, Payload: body})
}

func (s *agentSession) respondError(id string, failure *protocol.Error) {
	_ = s.write(protocol.Message{Version: protocol.Version, ID: id, Type: protocol.TypeResponse, Error: failure})
}

type unknownOperationError struct{ operation string }

func (e unknownOperationError) Error() string {
	return fmt.Sprintf("unknown operation %q", e.operation)
}

func decodePayload[T any](payload json.RawMessage) (T, error) {
	var value T
	if err := json.Unmarshal(payload, &value); err != nil {
		return value, fmt.Errorf("decode request: %w", err)
	}
	return value, nil
}

// handle runs one operation and returns its response value. Operations that
// report progress call report with each step.
func (a *Agent) handle(ctx context.Context, operation string, payload json.RawMessage, report func(step string)) (any, error) {
	switch operation {
	case "containers.list":
		return a.Handler.Containers(ctx)
	case "volumes.list":
		return a.Handler.Volumes(ctx)
	case "volumes.get":
		request, err := decodePayload[protocol.VolumeRequest](payload)
		if err != nil {
			return nil, err
		}
		return a.Handler.VolumeDetail(ctx, request.Name)
	case "images.list":
		return a.Handler.Images(ctx)
	case "images.get":
		request, err := decodePayload[protocol.ImageRequest](payload)
		if err != nil {
			return nil, err
		}
		return a.Handler.ImageDetail(ctx, request.ID)
	case "volumes.remove":
		request, err := decodePayload[protocol.VolumeRequest](payload)
		if err != nil {
			return nil, err
		}
		if err := a.Handler.RemoveVolume(ctx, request.Name); err != nil {
			return nil, err
		}
		return protocol.Removed{ID: request.Name}, nil
	case "images.remove":
		request, err := decodePayload[protocol.ImageRequest](payload)
		if err != nil {
			return nil, err
		}
		if err := a.Handler.RemoveImage(ctx, request.ID); err != nil {
			return nil, err
		}
		return protocol.Removed{ID: request.ID}, nil
	case "container.start", "container.stop":
		request, err := decodePayload[protocol.StartRequest](payload)
		if err != nil {
			return nil, err
		}
		if operation == "container.start" {
			err = a.Handler.Start(ctx, request.ContainerID)
		} else {
			err = a.Handler.Stop(ctx, request.ContainerID)
		}
		if err != nil {
			return nil, err
		}
		return protocol.StartResult{ContainerID: request.ContainerID, HostID: a.HostID, Started: true}, nil
	case opSecretsSync, opSecretsRetain:
		if a.Stacks == nil {
			return nil, errStacksUnavailable
		}
		if operation == opSecretsSync {
			request, err := decodePayload[protocol.SecretSync](payload)
			if err != nil {
				return nil, err
			}
			return a.Stacks.SyncSecrets(ctx, request)
		}
		request, err := decodePayload[protocol.SecretRetain](payload)
		if err != nil {
			return nil, err
		}
		return a.Stacks.RetainSecrets(request)
	case opRegistriesSync, opRegistriesTest:
		if a.Registries == nil {
			return nil, errRegistriesUnavailable
		}
		if operation == opRegistriesSync {
			request, err := decodePayload[protocol.RegistrySync](payload)
			if err != nil {
				return nil, err
			}
			return a.Registries.Sync(request)
		}
		request, err := decodePayload[protocol.RegistryTest](payload)
		if err != nil {
			return nil, err
		}
		return a.Registries.Test(ctx, request.Login)
	}
	if !strings.HasPrefix(operation, "stacks.") {
		return nil, unknownOperationError{operation}
	}
	if a.Stacks == nil {
		return nil, errStacksUnavailable
	}
	switch operation {
	case "stacks.list":
		return a.Stacks.List(ctx)
	case "stacks.images":
		request, err := decodePayload[protocol.StackImagesRequest](payload)
		if err != nil {
			return nil, err
		}
		return a.Stacks.Images(ctx, request.Name, request.Force)
	case "stacks.update":
		request, err := decodePayload[protocol.StackUpdateRequest](payload)
		if err != nil {
			return nil, err
		}
		return operationResult(a.Stacks.Update(ctx, request.Name, request.Service, report))
	}
	request, err := decodePayload[protocol.StackSave](payload)
	if err != nil {
		return nil, err
	}
	switch operation {
	case "stacks.inspect":
		return a.Stacks.Inspect(ctx, request.Name)
	case "stacks.versions":
		return a.Stacks.Versions(ctx, request.Name)
	case "stacks.revision":
		revision, err := decodePayload[protocol.StackRevisionRequest](payload)
		if err != nil {
			return nil, err
		}
		return a.Stacks.Revision(ctx, revision.Name, revision.Version)
	case "stacks.save":
		return a.Stacks.Save(ctx, request.Name, request.ComposeYAML, request.Environment, request.ExpectedVersion)
	case "stacks.up":
		return operationResult(a.Stacks.Up(ctx, request.Name))
	case "stacks.down":
		return operationResult(a.Stacks.Down(ctx, request.Name))
	case "stacks.restart":
		return operationResult(a.Stacks.Restart(ctx, request.Name))
	}
	return nil, unknownOperationError{operation}
}

// operationResult answers a stack operation that ran and failed with its
// result (Success false and the Compose output), which is what the web API
// and the dashboard show. Sent as an error, only the error's text, often
// just "exit status 1", would arrive. A stack that could not be run at all
// (unknown, unreadable) is still an error.
func operationResult(result protocol.StackOperation, err error) (any, error) {
	if err != nil && result.Operation == "" {
		return nil, err
	}
	return result, nil
}
