package relay

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/stacks"
)

const (
	DefaultRequestTimeout = 10 * time.Second
	// Stack operations run podman-compose and may legitimately take several minutes.
	StackOperationTimeout = 6 * time.Minute
)

type Remote struct {
	mu    sync.RWMutex
	hosts map[string]*remoteHost
}

func (r *Remote) ListStacks(id string) ([]protocol.Stack, error) {
	var out []protocol.Stack
	if e := r.call(id, "stacks.list", nil, &out, DefaultRequestTimeout); e != nil {
		return nil, e
	}
	return out, nil
}
func (r *Remote) InspectStack(id, name string) (protocol.Stack, error) {
	var out protocol.Stack
	if e := r.call(id, "stacks.inspect", protocol.StackSave{Name: name}, &out, DefaultRequestTimeout); e != nil {
		return out, e
	}
	return out, nil
}
func (r *Remote) StackVersions(id, name string) ([]protocol.StackVersion, error) {
	var out []protocol.StackVersion
	if e := r.call(id, "stacks.versions", protocol.StackSave{Name: name}, &out, DefaultRequestTimeout); e != nil {
		return nil, e
	}
	return out, nil
}
func (r *Remote) SaveStack(id string, s protocol.StackSave) (protocol.Stack, error) {
	var out protocol.Stack
	if e := r.call(id, "stacks.save", s, &out, DefaultRequestTimeout); e != nil {
		return out, e
	}
	return out, nil
}
func (r *Remote) StackOperation(id, name, op string) (protocol.StackOperation, error) {
	var out protocol.StackOperation
	if e := r.call(id, "stacks."+op, protocol.StackSave{Name: name}, &out, StackOperationTimeout); e != nil {
		return out, e
	}
	return out, nil
}

type remoteHost struct {
	conn *websocket.Conn
	info protocol.HostInfo
	mu   sync.Mutex
}

func NewRemote() *Remote { return &Remote{hosts: map[string]*remoteHost{}} }
func (r *Remote) AcceptAgent(conn *websocket.Conn) (protocol.HostInfo, error) {
	return r.AcceptAgentForCertificate(conn, "")
}
func (r *Remote) AcceptAgentForCertificate(conn *websocket.Conn, certificateHostID string) (protocol.HostInfo, error) {
	_ = conn.SetReadDeadline(time.Now().Add(DefaultRequestTimeout))
	var hello protocol.Message
	if err := conn.ReadJSON(&hello); err != nil {
		return protocol.HostInfo{}, err
	}
	if hello.Type != "hello" || hello.HostID == "" {
		return protocol.HostInfo{}, errors.New("invalid agent hello")
	}
	var info protocol.HostInfo
	if err := json.Unmarshal(hello.Payload, &info); err != nil {
		return protocol.HostInfo{}, err
	}
	if info.HostID != hello.HostID {
		return protocol.HostInfo{}, errors.New("agent hello identity mismatch")
	}
	if certificateHostID != "" && certificateHostID != hello.HostID {
		return protocol.HostInfo{}, errors.New("agent certificate identity mismatch")
	}
	_ = conn.SetReadDeadline(time.Time{})
	if err := r.Register(conn, info); err != nil {
		return protocol.HostInfo{}, err
	}
	return info, nil
}

func (r *Remote) Register(conn *websocket.Conn, info protocol.HostInfo) error {
	if info.HostID == "" || info.Prefix == "" {
		return errors.New("agent hello missing host identity")
	}
	h := &remoteHost{conn: conn, info: info}
	r.mu.Lock()
	old := r.hosts[info.HostID]
	r.hosts[info.HostID] = h
	r.mu.Unlock()
	if old != nil {
		_ = old.conn.Close()
	}
	return nil
}
func (r *Remote) Remove(id string, conn *websocket.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h := r.hosts[id]; h != nil && h.conn == conn {
		delete(r.hosts, id)
	}
}
func (r *Remote) Hosts() []protocol.HostInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]protocol.HostInfo, 0, len(r.hosts))
	for _, h := range r.hosts {
		out = append(out, h.info)
	}
	return out
}
func (r *Remote) Info(id string) (protocol.HostInfo, error) {
	r.mu.RLock()
	h, ok := r.hosts[id]
	r.mu.RUnlock()
	if !ok {
		return protocol.HostInfo{}, errors.New("host unavailable")
	}
	return h.info, nil
}
func (r *Remote) Containers(id string) ([]protocol.Container, error) {
	var out []protocol.Container
	if e := r.call(id, "containers.list", nil, &out, DefaultRequestTimeout); e != nil {
		return nil, e
	}
	return out, nil
}
func (r *Remote) Volumes(id string) ([]protocol.Volume, error) {
	var out []protocol.Volume
	if err := r.call(id, "volumes.list", nil, &out, DefaultRequestTimeout); err != nil {
		return nil, err
	}
	return out, nil
}
func (r *Remote) Images(id string) ([]protocol.Image, error) {
	var out []protocol.Image
	if err := r.call(id, "images.list", nil, &out, DefaultRequestTimeout); err != nil {
		return nil, err
	}
	return out, nil
}
func (r *Remote) Stop(id, containerID string) error {
	var out protocol.StartResult
	return r.call(id, "container.stop", protocol.StartRequest{ContainerID: containerID}, &out, DefaultRequestTimeout)
}
func (r *Remote) Start(id, containerID string) error {
	var out protocol.StartResult
	return r.call(id, "container.start", protocol.StartRequest{ContainerID: containerID}, &out, DefaultRequestTimeout)
}
func (r *Remote) call(hostID, operation string, payload any, result any, timeout time.Duration) error {
	r.mu.RLock()
	h, ok := r.hosts[hostID]
	r.mu.RUnlock()
	if !ok {
		return errors.New("host unavailable")
	}
	b, _ := json.Marshal(payload)
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.conn.WriteJSON(protocol.Message{Version: protocol.Version, ID: id, Type: "request", Operation: operation, HostID: hostID, Payload: b}); e != nil {
		return e
	}
	_ = h.conn.SetReadDeadline(time.Now().Add(timeout))
	var m protocol.Message
	if e := h.conn.ReadJSON(&m); e != nil {
		return e
	}
	if m.ID != id {
		return errors.New("relay response ID mismatch")
	}
	if m.Error != nil {
		return errors.New(m.Error.Message)
	}
	return json.Unmarshal(m.Payload, result)
}

type Agent struct {
	Conn           *websocket.Conn
	HostID, Prefix string
	Handler        Handler
	Stacks         *stacks.Manager
}

func (a *Agent) StacksOperation(ctx context.Context, name, op string) (protocol.StackOperation, error) {
	if a.Stacks == nil {
		return protocol.StackOperation{}, errors.New("stack agent unavailable")
	}
	switch op {
	case "up":
		return a.Stacks.Up(ctx, name)
	case "down":
		return a.Stacks.Down(ctx, name)
	default:
		return a.Stacks.Restart(ctx, name)
	}
}
func (a *Agent) Serve(ctx context.Context) error {
	if a.Conn == nil || a.Handler == nil || a.HostID == "" || a.Prefix == "" {
		return errors.New("invalid agent")
	}
	info, err := a.Handler.Info(ctx)
	if err != nil {
		return err
	}
	info.HostID, info.Prefix = a.HostID, a.Prefix
	b, _ := json.Marshal(info)
	if e := a.Conn.WriteJSON(protocol.Message{Version: protocol.Version, ID: "hello", Type: "hello", HostID: a.HostID, Payload: b}); e != nil {
		return e
	}
	done := make(chan error, 1)
	go func() {
		for {
			var m protocol.Message
			if e := a.Conn.ReadJSON(&m); e != nil {
				done <- e
				return
			}
			if m.Type != "request" {
				continue
			}
			var payload protocol.Message
			_ = payload
			var value any
			var e error
			switch m.Operation {
			case "stacks.list":
				if a.Stacks == nil {
					e = errors.New("stack agent unavailable")
				} else {
					value, e = a.Stacks.List(ctx)
				}
			case "stacks.inspect":
				var req protocol.StackSave
				e = json.Unmarshal(m.Payload, &req)
				if e == nil && a.Stacks != nil {
					value, e = a.Stacks.Inspect(ctx, req.Name)
				}
			case "stacks.versions":
				var req protocol.StackSave
				e = json.Unmarshal(m.Payload, &req)
				if e == nil && a.Stacks != nil {
					value, e = a.Stacks.Versions(ctx, req.Name)
				}
			case "stacks.save":
				var req protocol.StackSave
				e = json.Unmarshal(m.Payload, &req)
				if e == nil && a.Stacks != nil {
					value, e = a.Stacks.Save(ctx, req.Name, req.ComposeYAML, req.Environment, req.ExpectedVersion)
				}
			case "stacks.up", "stacks.down", "stacks.restart":
				var req protocol.StackSave
				e = json.Unmarshal(m.Payload, &req)
				var result protocol.StackOperation
				if e == nil && a.Stacks != nil {
					result, e = a.StacksOperation(ctx, req.Name, strings.TrimPrefix(m.Operation, "stacks."))
				}
				value = result
			case "containers.list":
				value, e = a.Handler.Containers(ctx)
			case "volumes.list":
				value, e = a.Handler.Volumes(ctx)
			case "images.list":
				value, e = a.Handler.Images(ctx)
			case "container.start", "container.stop":
				var req protocol.StartRequest
				if e = json.Unmarshal(m.Payload, &req); e == nil {
					if m.Operation == "container.start" {
						e = a.Handler.Start(ctx, req.ContainerID)
					} else {
						e = a.Handler.Stop(ctx, req.ContainerID)
					}
				}
				value = protocol.StartResult{ContainerID: req.ContainerID, HostID: a.HostID, Started: e == nil}
			default:
				e = errors.New("unknown operation")
			}
			resp := protocol.Message{Version: protocol.Version, ID: m.ID, Type: "response"}
			if e != nil {
				resp.Error = &protocol.Error{Code: "operation_failed", Message: e.Error(), Retryable: true}
			} else {
				resp.Payload, _ = json.Marshal(value)
			}
			if e = a.Conn.WriteJSON(resp); e != nil {
				done <- e
				return
			}
		}
	}()
	select {
	case e := <-done:
		return e
	case <-ctx.Done():
		return ctx.Err()
	}
}

func DialAgent(url string, tlsConfig *tls.Config) (*websocket.Conn, error) {
	d := websocket.DefaultDialer
	d.TLSClientConfig = tlsConfig
	c, _, e := d.Dial(url, http.Header{})
	return c, e
}
