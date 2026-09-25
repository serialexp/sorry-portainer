package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

const (
	DefaultRequestTimeout = 10 * time.Second
	// Stack operations run podman-compose and may legitimately take several minutes.
	StackOperationTimeout = 6 * time.Minute
	helloTimeout          = 10 * time.Second
)

// Remote is the server's registry of connected agents.
type Remote struct {
	heartbeat time.Duration

	mu       sync.RWMutex
	sessions map[string]*session
}

// RemoteOptions configures a Remote. Zero values select the defaults.
type RemoteOptions struct {
	Heartbeat time.Duration
}

func NewRemote(options RemoteOptions) *Remote {
	if options.Heartbeat <= 0 {
		options.Heartbeat = DefaultHeartbeat
	}
	return &Remote{heartbeat: options.Heartbeat, sessions: map[string]*session{}}
}

// Serve runs one agent connection whose authenticated identity is hostID, until
// the connection ends. The agent's hello must claim exactly that identity. A new
// session for a host replaces, and fails the pending requests of, the previous one.
func (r *Remote) Serve(conn *websocket.Conn, hostID string) error {
	if hostID == "" {
		_ = conn.Close()
		return errors.New("agent connection has no authenticated host identity")
	}
	info, err := readHello(conn, hostID)
	if err != nil {
		_ = conn.Close()
		return err
	}
	current := newSession(conn, info, r.heartbeat)
	r.mu.Lock()
	previous := r.sessions[hostID]
	r.sessions[hostID] = current
	r.mu.Unlock()
	if previous != nil {
		previous.close(errors.New("replaced by a new session for the same host"))
	}

	err = current.run()

	r.mu.Lock()
	if r.sessions[hostID] == current {
		delete(r.sessions, hostID)
	}
	r.mu.Unlock()
	return err
}

func readHello(conn *websocket.Conn, hostID string) (protocol.HostInfo, error) {
	conn.SetReadLimit(protocol.MaxMessageSize)
	if err := conn.SetReadDeadline(time.Now().Add(helloTimeout)); err != nil {
		return protocol.HostInfo{}, err
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		return protocol.HostInfo{}, fmt.Errorf("read agent hello: %w", err)
	}
	hello, err := protocol.Decode(data)
	if err != nil {
		return protocol.HostInfo{}, err
	}
	if hello.Type != protocol.TypeHello {
		return protocol.HostInfo{}, errors.New("invalid agent hello")
	}
	var info protocol.HostInfo
	if err := json.Unmarshal(hello.Payload, &info); err != nil {
		return protocol.HostInfo{}, fmt.Errorf("decode agent hello: %w", err)
	}
	if hello.HostID != hostID || info.HostID != hostID {
		return protocol.HostInfo{}, errors.New("agent hello identity does not match its certificate")
	}
	if info.Prefix == "" {
		return protocol.HostInfo{}, errors.New("agent hello missing resource prefix")
	}
	return info, nil
}

func (r *Remote) session(hostID string) (*session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := r.sessions[hostID]
	if s == nil {
		return nil, ErrHostUnavailable
	}
	return s, nil
}

func (r *Remote) call(ctx context.Context, hostID, operation string, payload, result any, timeout time.Duration) error {
	s, err := r.session(hostID)
	if err != nil {
		return err
	}
	return s.call(ctx, operation, payload, result, timeout)
}

// Hosts lists connected hosts ordered by host ID.
func (r *Remote) Hosts() []protocol.HostInfo {
	r.mu.RLock()
	out := make([]protocol.HostInfo, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s.info)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].HostID < out[j].HostID })
	return out
}

func (r *Remote) Info(_ context.Context, hostID string) (protocol.HostInfo, error) {
	s, err := r.session(hostID)
	if err != nil {
		return protocol.HostInfo{}, err
	}
	return s.info, nil
}

func (r *Remote) Containers(ctx context.Context, hostID string) ([]protocol.Container, error) {
	var out []protocol.Container
	if err := r.call(ctx, hostID, "containers.list", nil, &out, DefaultRequestTimeout); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) Volumes(ctx context.Context, hostID string) ([]protocol.Volume, error) {
	var out []protocol.Volume
	if err := r.call(ctx, hostID, "volumes.list", nil, &out, DefaultRequestTimeout); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) Images(ctx context.Context, hostID string) ([]protocol.Image, error) {
	var out []protocol.Image
	if err := r.call(ctx, hostID, "images.list", nil, &out, DefaultRequestTimeout); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) Start(ctx context.Context, hostID, containerID string) error {
	return r.call(ctx, hostID, "container.start", protocol.StartRequest{ContainerID: containerID}, nil, DefaultRequestTimeout)
}

func (r *Remote) Stop(ctx context.Context, hostID, containerID string) error {
	return r.call(ctx, hostID, "container.stop", protocol.StartRequest{ContainerID: containerID}, nil, DefaultRequestTimeout)
}

func (r *Remote) ListStacks(ctx context.Context, hostID string) ([]protocol.Stack, error) {
	var out []protocol.Stack
	if err := r.call(ctx, hostID, "stacks.list", nil, &out, DefaultRequestTimeout); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) InspectStack(ctx context.Context, hostID, name string) (protocol.Stack, error) {
	var out protocol.Stack
	err := r.call(ctx, hostID, "stacks.inspect", protocol.StackSave{Name: name}, &out, DefaultRequestTimeout)
	return out, err
}

func (r *Remote) StackVersions(ctx context.Context, hostID, name string) ([]protocol.StackVersion, error) {
	var out []protocol.StackVersion
	if err := r.call(ctx, hostID, "stacks.versions", protocol.StackSave{Name: name}, &out, DefaultRequestTimeout); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) SaveStack(ctx context.Context, hostID string, save protocol.StackSave) (protocol.Stack, error) {
	var out protocol.Stack
	err := r.call(ctx, hostID, "stacks.save", save, &out, DefaultRequestTimeout)
	return out, err
}

func (r *Remote) StackOperation(ctx context.Context, hostID, name, operation string) (protocol.StackOperation, error) {
	var out protocol.StackOperation
	err := r.call(ctx, hostID, "stacks."+operation, protocol.StackSave{Name: name}, &out, StackOperationTimeout)
	return out, err
}
