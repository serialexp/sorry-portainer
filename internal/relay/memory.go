package relay

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// Handler is one host's local runtime, as the agent sees it.
type Handler interface {
	Info(context.Context) (protocol.HostInfo, error)
	Containers(context.Context) ([]protocol.Container, error)
	Volumes(context.Context) ([]protocol.Volume, error)
	Images(context.Context) ([]protocol.Image, error)
	Start(context.Context, string) error
	Stop(context.Context, string) error
}

// Memory is an in-process relay that calls handlers directly. It serves tests
// and single-process development without WebSocket transport.
type Memory struct {
	mu    sync.RWMutex
	hosts map[string]Handler
	infos map[string]protocol.HostInfo
}

var errStacksUnsupported = errors.New("stacks unavailable")

func New() *Memory {
	return &Memory{hosts: map[string]Handler{}, infos: map[string]protocol.HostInfo{}}
}

func (m *Memory) Register(id string, h Handler) error {
	return m.RegisterWithPrefix(id, id+"-", h)
}

func (m *Memory) RegisterWithPrefix(id, prefix string, h Handler) error {
	if id == "" || prefix == "" || h == nil {
		return errors.New("invalid agent")
	}
	info, err := h.Info(context.Background())
	if err != nil {
		return err
	}
	info.HostID = id
	info.Prefix = prefix
	m.mu.Lock()
	m.hosts[id] = h
	m.infos[id] = info
	m.mu.Unlock()
	return nil
}

// Hosts lists registered hosts ordered by host ID.
func (m *Memory) Hosts() []protocol.HostInfo {
	m.mu.RLock()
	out := make([]protocol.HostInfo, 0, len(m.infos))
	for _, info := range m.infos {
		out = append(out, info)
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].HostID < out[j].HostID })
	return out
}

func (m *Memory) host(id string) (Handler, protocol.HostInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h, ok := m.hosts[id]
	if !ok {
		return nil, protocol.HostInfo{}, ErrHostUnavailable
	}
	return h, m.infos[id], nil
}

func (m *Memory) Info(_ context.Context, id string) (protocol.HostInfo, error) {
	_, info, err := m.host(id)
	return info, err
}

func (m *Memory) Containers(ctx context.Context, id string) ([]protocol.Container, error) {
	h, _, err := m.host(id)
	if err != nil {
		return nil, err
	}
	return h.Containers(ctx)
}

func (m *Memory) Volumes(ctx context.Context, id string) ([]protocol.Volume, error) {
	h, _, err := m.host(id)
	if err != nil {
		return nil, err
	}
	return h.Volumes(ctx)
}

func (m *Memory) Images(ctx context.Context, id string) ([]protocol.Image, error) {
	h, _, err := m.host(id)
	if err != nil {
		return nil, err
	}
	return h.Images(ctx)
}

func (m *Memory) Start(ctx context.Context, id, containerID string) error {
	h, info, err := m.host(id)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(containerID, info.Prefix) {
		return fmt.Errorf("container %q is outside host %s prefix %q", containerID, id, info.Prefix)
	}
	return h.Start(ctx, containerID)
}

func (m *Memory) Stop(ctx context.Context, id, containerID string) error {
	h, info, err := m.host(id)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(containerID, info.Prefix) {
		return fmt.Errorf("container %q is outside host %s prefix %q", containerID, id, info.Prefix)
	}
	return h.Stop(ctx, containerID)
}

func (m *Memory) ListStacks(_ context.Context, id string) ([]protocol.Stack, error) {
	if _, _, err := m.host(id); err != nil {
		return nil, err
	}
	return []protocol.Stack{}, nil
}

func (m *Memory) InspectStack(context.Context, string, string) (protocol.Stack, error) {
	return protocol.Stack{}, errStacksUnsupported
}

func (m *Memory) StackVersions(context.Context, string, string) ([]protocol.StackVersion, error) {
	return nil, errStacksUnsupported
}

func (m *Memory) SaveStack(context.Context, string, protocol.StackSave) (protocol.Stack, error) {
	return protocol.Stack{}, errStacksUnsupported
}

func (m *Memory) StackOperation(context.Context, string, string, string) (protocol.StackOperation, error) {
	return protocol.StackOperation{}, errStacksUnsupported
}
