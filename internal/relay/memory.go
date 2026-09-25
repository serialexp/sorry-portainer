package relay

import (
	"context"
	"errors"
	"fmt"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"strings"
	"sync"
)

type Handler interface {
	Info(context.Context) (protocol.HostInfo, error)
	Containers(context.Context) ([]protocol.Container, error)
	Volumes(context.Context) ([]protocol.Volume, error)
	Images(context.Context) ([]protocol.Image, error)
	Start(context.Context, string) error
	Stop(context.Context, string) error
}
type Memory struct {
	mu    sync.RWMutex
	hosts map[string]Handler
	infos map[string]protocol.HostInfo
}

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
	i, e := h.Info(context.Background())
	if e != nil {
		return e
	}
	i.HostID = id
	i.Prefix = prefix
	m.mu.Lock()
	m.hosts[id] = h
	m.infos[id] = i
	m.mu.Unlock()
	return nil
}
func (m *Memory) Hosts() []protocol.HostInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o := make([]protocol.HostInfo, 0, len(m.infos))
	for _, i := range m.infos {
		o = append(o, i)
	}
	return o
}
func (m *Memory) ListStacks(string) ([]protocol.Stack, error) { return []protocol.Stack{}, nil }
func (m *Memory) InspectStack(string, string) (protocol.Stack, error) {
	return protocol.Stack{}, errors.New("stacks unavailable")
}
func (m *Memory) StackVersions(string, string) ([]protocol.StackVersion, error) {
	return nil, errors.New("stacks unavailable")
}
func (m *Memory) SaveStack(string, protocol.StackSave) (protocol.Stack, error) {
	return protocol.Stack{}, errors.New("stacks unavailable")
}
func (m *Memory) StackOperation(string, string, string) (protocol.StackOperation, error) {
	return protocol.StackOperation{}, errors.New("stacks unavailable")
}
func (m *Memory) Info(id string) (protocol.HostInfo, error) {
	m.mu.RLock()
	info, ok := m.infos[id]
	m.mu.RUnlock()
	if !ok {
		return protocol.HostInfo{}, errors.New("host unavailable")
	}
	return info, nil
}
func (m *Memory) Containers(id string) ([]protocol.Container, error) {
	m.mu.RLock()
	h, ok := m.hosts[id]
	m.mu.RUnlock()
	if !ok {
		return nil, errors.New("host unavailable")
	}
	return h.Containers(context.Background())
}
func (m *Memory) Volumes(id string) ([]protocol.Volume, error) {
	m.mu.RLock()
	h, ok := m.hosts[id]
	m.mu.RUnlock()
	if !ok {
		return nil, errors.New("host unavailable")
	}
	return h.Volumes(context.Background())
}
func (m *Memory) Images(id string) ([]protocol.Image, error) {
	m.mu.RLock()
	h, ok := m.hosts[id]
	m.mu.RUnlock()
	if !ok {
		return nil, errors.New("host unavailable")
	}
	return h.Images(context.Background())
}
func (m *Memory) Stop(id, containerID string) error {
	m.mu.RLock()
	h, ok := m.hosts[id]
	info := m.infos[id]
	m.mu.RUnlock()
	if !ok {
		return errors.New("host unavailable")
	}
	if !strings.HasPrefix(containerID, info.Prefix) {
		return fmt.Errorf("container %q is outside host %s prefix %q", containerID, id, info.Prefix)
	}
	return h.Stop(context.Background(), containerID)
}
func (m *Memory) Start(id, containerID string) error {
	m.mu.RLock()
	h, ok := m.hosts[id]
	info := m.infos[id]
	m.mu.RUnlock()
	if !ok {
		return errors.New("host unavailable")
	}
	if !strings.HasPrefix(containerID, info.Prefix) {
		return fmt.Errorf("container %q is outside host %s prefix %q", containerID, id, info.Prefix)
	}
	return h.Start(context.Background(), containerID)
}
