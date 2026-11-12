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
	// StackImagesTimeout covers an image check: registry requests of 15
	// seconds each, four at a time, and build-time lookups for moved tags.
	StackImagesTimeout = 45 * time.Second
	helloTimeout       = 10 * time.Second
)

// Secret operations. Their payloads carry secret values, which must never be
// logged on either side.
const (
	opSecretsSync    = "secrets.sync"
	opSecretsRetain  = "secrets.retain"
	opRegistriesSync = "registries.sync"
	opRegistriesTest = "registries.test"
)

// RegistryTestTimeout covers the agent's 30-second sign-in test.
const RegistryTestTimeout = 40 * time.Second

// Remote is the server's registry of connected agents.
type Remote struct {
	heartbeat    time.Duration
	onConnect    func(protocol.HostInfo)
	onDisconnect func(hostID string)

	mu       sync.RWMutex
	sessions map[string]*session
}

// RemoteOptions configures a Remote. Zero values select the defaults.
type RemoteOptions struct {
	Heartbeat time.Duration
	// OnConnect runs in its own goroutine once an agent's session is ready to
	// take requests. The master uses it to push the host's secrets.
	OnConnect func(protocol.HostInfo)
	// OnDisconnect runs in its own goroutine when a host's session ends and
	// no newer session replaced it.
	OnDisconnect func(hostID string)
}

func NewRemote(options RemoteOptions) *Remote {
	if options.Heartbeat <= 0 {
		options.Heartbeat = DefaultHeartbeat
	}
	return &Remote{heartbeat: options.Heartbeat, onConnect: options.OnConnect, onDisconnect: options.OnDisconnect, sessions: map[string]*session{}}
}

// Serve runs one agent connection whose authenticated identity is hostID, until
// the connection ends. The agent's hello must claim exactly that identity. A new
// session for a host replaces, and fails the pending requests of, the previous one.
//
// admit, when not nil, is the final admission check. It runs while the
// session is being registered, under the lock Disconnect takes, so a host
// whose admission is revoked before Disconnect is called either is refused
// here or is closed by Disconnect. It must not block.
func (r *Remote) Serve(conn *websocket.Conn, hostID string, admit func() error) error {
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
	if admit != nil {
		if err := admit(); err != nil {
			r.mu.Unlock()
			_ = conn.Close()
			return fmt.Errorf("admission refused: %w", err)
		}
	}
	previous := r.sessions[hostID]
	r.sessions[hostID] = current
	r.mu.Unlock()
	if previous != nil {
		previous.close(errors.New("replaced by a new session for the same host"))
	}
	if r.onConnect != nil {
		// Requests queue on the session until run starts reading replies.
		go r.onConnect(info)
	}

	err = current.run()

	r.mu.Lock()
	gone := r.sessions[hostID] == current
	if gone {
		delete(r.sessions, hostID)
	}
	r.mu.Unlock()
	if gone && r.onDisconnect != nil {
		go r.onDisconnect(hostID)
	}
	return err
}

// Disconnect closes hostID's session, if any, failing its pending requests.
// Change the admission state first, then call Disconnect.
func (r *Remote) Disconnect(hostID string, cause error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sessions[hostID]; s != nil {
		delete(r.sessions, hostID)
		s.close(cause)
		if r.onDisconnect != nil {
			go r.onDisconnect(hostID)
		}
	}
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

// VolumeDetail inspects one volume on the host; the agent refuses names outside its prefix.
func (r *Remote) VolumeDetail(ctx context.Context, hostID, name string) (protocol.VolumeDetail, error) {
	var out protocol.VolumeDetail
	err := r.call(ctx, hostID, "volumes.get", protocol.VolumeRequest{Name: name}, &out, DefaultRequestTimeout)
	return out, err
}

func (r *Remote) Images(ctx context.Context, hostID string) ([]protocol.Image, error) {
	var out []protocol.Image
	if err := r.call(ctx, hostID, "images.list", nil, &out, DefaultRequestTimeout); err != nil {
		return nil, err
	}
	return out, nil
}

// ImageDetail reads one image on the host, with its configuration and build steps.
func (r *Remote) ImageDetail(ctx context.Context, hostID, id string) (protocol.ImageDetail, error) {
	var out protocol.ImageDetail
	err := r.call(ctx, hostID, "images.get", protocol.ImageRequest{ID: id}, &out, DefaultRequestTimeout)
	return out, err
}

// RemoveVolume deletes a volume of the host's prefix. The host refuses a volume a container uses.
func (r *Remote) RemoveVolume(ctx context.Context, hostID, name string) error {
	return r.call(ctx, hostID, "volumes.remove", protocol.VolumeRequest{Name: name}, nil, DefaultRequestTimeout)
}

// RemoveImage deletes an image of the host. The host refuses an image a container uses.
func (r *Remote) RemoveImage(ctx context.Context, hostID, id string) error {
	return r.call(ctx, hostID, "images.remove", protocol.ImageRequest{ID: id}, nil, DefaultRequestTimeout)
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

// StackRevision reads one revision of a stack: its Compose document and environment.
func (r *Remote) StackRevision(ctx context.Context, hostID, name string, version int) (protocol.Stack, error) {
	var out protocol.Stack
	err := r.call(ctx, hostID, "stacks.revision", protocol.StackRevisionRequest{Name: name, Version: version}, &out, DefaultRequestTimeout)
	return out, err
}

// SaveStack may bring the stack up again (a changed environment on a stack
// that is up), so it gets the stack operation timeout.
func (r *Remote) SaveStack(ctx context.Context, hostID string, save protocol.StackSave) (protocol.StackSaved, error) {
	var out protocol.StackSaved
	err := r.call(ctx, hostID, "stacks.save", save, &out, StackOperationTimeout)
	return out, err
}

// SyncSecrets replaces one stack's secrets in the agent's memory. The agent
// may restart containers, so it gets the stack operation timeout.
func (r *Remote) SyncSecrets(ctx context.Context, hostID string, sync protocol.SecretSync) (protocol.SecretSyncResult, error) {
	var out protocol.SecretSyncResult
	err := r.call(ctx, hostID, opSecretsSync, sync, &out, StackOperationTimeout)
	return out, err
}

// RetainSecrets makes the agent forget the secrets of stacks not listed.
func (r *Remote) RetainSecrets(ctx context.Context, hostID string, stacks []string) error {
	return r.call(ctx, hostID, opSecretsRetain, protocol.SecretRetain{Stacks: stacks}, nil, DefaultRequestTimeout)
}

// SyncRegistries replaces the host's registry logins. The agent answers
// once the set is committed; stacks waiting for a login start afterwards.
func (r *Remote) SyncRegistries(ctx context.Context, hostID string, sync protocol.RegistrySync) (protocol.RegistrySynced, error) {
	var out protocol.RegistrySynced
	err := r.call(ctx, hostID, opRegistriesSync, sync, &out, DefaultRequestTimeout)
	return out, err
}

// TestRegistry has the host sign in to a registry with a login, without
// keeping it.
func (r *Remote) TestRegistry(ctx context.Context, hostID string, login protocol.RegistryLogin) (protocol.RegistryTestResult, error) {
	var out protocol.RegistryTestResult
	err := r.call(ctx, hostID, opRegistriesTest, protocol.RegistryTest{Login: login}, &out, RegistryTestTimeout)
	return out, err
}

// StackImages asks whether a stack's images are still what their tags point to.
func (r *Remote) StackImages(ctx context.Context, hostID, name string, force bool) (protocol.StackImages, error) {
	var out protocol.StackImages
	err := r.call(ctx, hostID, "stacks.images", protocol.StackImagesRequest{Name: name, Force: force}, &out, StackImagesTimeout)
	return out, err
}

// UpdateStackService pulls one service's image and recreates the service
// when the stack runs. onStep, when not nil, is told each step as the agent
// starts it (protocol.UpdatePulling, protocol.UpdateRecreating).
func (r *Remote) UpdateStackService(ctx context.Context, hostID, name, service string, onStep func(string)) (protocol.StackOperation, error) {
	s, err := r.session(hostID)
	if err != nil {
		return protocol.StackOperation{}, err
	}
	var out protocol.StackOperation
	err = s.callWithProgress(ctx, "stacks.update", protocol.StackUpdateRequest{Name: name, Service: service}, &out, StackOperationTimeout, onStep)
	return out, err
}

func (r *Remote) StackOperation(ctx context.Context, hostID, name, operation string) (protocol.StackOperation, error) {
	var out protocol.StackOperation
	err := r.call(ctx, hostID, "stacks."+operation, protocol.StackSave{Name: name}, &out, StackOperationTimeout)
	return out, err
}
