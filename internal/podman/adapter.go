// Package podman provides a bounded, command-line adapter for Podman.
package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

const maxCommandOutput = 1 << 20

// Runner executes Podman with the supplied argument vector. Implementations must
// honor ctx; stdout and stderr retain at most the client's configured limit.
type Runner interface {
	Run(ctx context.Context, stdout, stderr io.Writer, args ...string) error
}

type execRunner struct {
	path string
}

func (r execRunner) Run(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, r.path, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// Client invokes Podman without a shell and maps its JSON output to protocol
// types.
type Client struct {
	runner Runner
	prefix string
}

func New() (*Client, error) {
	return &Client{runner: execRunner{path: "podman"}}, nil
}

func NewWithPrefix(prefix string) (*Client, error) {
	return &Client{runner: execRunner{path: "podman"}, prefix: prefix}, nil
}

// NewWithRunner constructs a client with an injectable command runner. The
// optional prefix is useful to tests and embedders that partition Podman names.
func NewWithRunner(runner Runner, prefix ...string) *Client {
	client := &Client{runner: runner}
	if len(prefix) > 0 {
		client.prefix = prefix[0]
	}
	return client
}

type limitedOutput struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int
	exceeded bool
	cancel   context.CancelFunc
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	remaining := w.limit - w.buf.Len()
	if remaining < len(p) {
		if remaining > 0 {
			_, _ = w.buf.Write(p[:remaining])
		}
		w.exceeded = true
		w.cancel()
		// Report the input consumed: CommandContext performs cancellation, while
		// this writer never retains more than the configured bound.
		return len(p), nil
	}
	_, _ = w.buf.Write(p)
	return len(p), nil
}

func (w *limitedOutput) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Bytes()
}

func (w *limitedOutput) tooLarge() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.exceeded
}

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.runner == nil {
		return nil, errors.New("podman runner is nil")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &limitedOutput{limit: maxCommandOutput, cancel: cancel}
	stderr := &limitedOutput{limit: maxCommandOutput, cancel: cancel}
	err := c.runner.Run(runCtx, stdout, stderr, args...)
	if stdout.tooLarge() {
		return nil, fmt.Errorf("podman %s: stdout exceeds %d bytes", args[0], maxCommandOutput)
	}
	if stderr.tooLarge() {
		return nil, fmt.Errorf("podman %s: stderr exceeds %d bytes", args[0], maxCommandOutput)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		message := strings.TrimSpace(string(stderr.bytes()))
		if message == "" {
			return nil, fmt.Errorf("podman %s: %w", args[0], err)
		}
		return nil, fmt.Errorf("podman %s: %w: %s", args[0], err, message)
	}
	return stdout.bytes(), nil
}

func decodeJSON[T any](operation string, data []byte) (T, error) {
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("decode podman %s JSON: %w", operation, err)
	}
	return result, nil
}

type infoJSON struct {
	Host struct {
		Hostname string `json:"hostname"`
	} `json:"host"`
	Version struct {
		Version string `json:"Version"`
	} `json:"version"`
}

func (c *Client) Info(ctx context.Context) (protocol.HostInfo, error) {
	data, err := c.run(ctx, "info", "--format", "json")
	if err != nil {
		return protocol.HostInfo{}, err
	}
	item, err := decodeJSON[infoJSON]("info", data)
	if err != nil {
		return protocol.HostInfo{}, err
	}
	if !supportedVersion(item.Version.Version) {
		return protocol.HostInfo{}, fmt.Errorf("Podman %s is unsupported; version 4.7 or later is required", item.Version.Version)
	}
	return protocol.HostInfo{Hostname: item.Host.Hostname, EngineVersion: item.Version.Version}, nil
}

func supportedVersion(version string) bool {
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(parts) < 2 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	return majorErr == nil && minorErr == nil && (major > 4 || major == 4 && minor >= 7)
}

type containerJSON struct {
	ID     string   `json:"Id"`
	Names  []string `json:"Names"`
	Image  string   `json:"Image"`
	State  string   `json:"State"`
	Status string   `json:"Status"`
}

func (c *Client) Containers(ctx context.Context) ([]protocol.Container, error) {
	data, err := c.run(ctx, "ps", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	items, err := decodeJSON[[]containerJSON]("ps", data)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.Container, 0, len(items))
	for i := range items {
		item := &items[i]
		name := ""
		if len(item.Names) != 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		if c.prefix != "" && !strings.HasPrefix(name, c.prefix) {
			continue
		}
		out = append(out, protocol.Container{ID: item.ID, Name: name, Image: item.Image, State: item.State, Status: item.Status})
	}
	return out, nil
}

type volumeJSON struct {
	Name       string `json:"Name"`
	Driver     string `json:"Driver"`
	Mountpoint string `json:"Mountpoint"`
}

func (c *Client) Volumes(ctx context.Context) ([]protocol.Volume, error) {
	data, err := c.run(ctx, "volume", "ls", "--format", "json")
	if err != nil {
		return nil, err
	}
	items, err := decodeJSON[[]volumeJSON]("volume ls", data)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.Volume, 0, len(items))
	for i := range items {
		item := &items[i]
		if c.prefix != "" && !strings.HasPrefix(item.Name, c.prefix) {
			continue
		}
		out = append(out, protocol.Volume{Name: item.Name, Driver: item.Driver, Mountpoint: item.Mountpoint})
	}
	if len(out) == 0 {
		return out, nil
	}
	mounts, err := c.volumeMounts(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Mounts = mounts[out[i].Name]
	}
	return out, nil
}

type mountJSON struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

// volumeMounts maps each named volume to the containers of this agent's prefix
// that mount it. The mounts of every such container come from one podman call.
// A template prints only each container's mounts: the full inspect document of
// a busy host would not fit the command output bound.
func (c *Client) volumeMounts(ctx context.Context) (map[string][]protocol.VolumeMount, error) {
	containers, err := c.Containers(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]protocol.VolumeMount)
	if len(containers) == 0 {
		return out, nil
	}
	byID := make(map[string]protocol.Container, len(containers))
	args := make([]string, 0, len(containers)+5)
	args = append(args, "container", "inspect", "--format", `{{.ID}}{{"\t"}}{{json .Mounts}}`, "--")
	for _, item := range containers {
		byID[item.ID] = item
		args = append(args, item.ID)
	}
	data, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	// One scratch slice serves every container: Unmarshal resets its length.
	var mounts []mountJSON
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		id, mountsJSON, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("podman container inspect: unexpected line %q", line)
		}
		owner, known := byID[id]
		if !known {
			continue
		}
		if err := json.Unmarshal([]byte(mountsJSON), &mounts); err != nil {
			return nil, fmt.Errorf("podman container inspect: mounts of %q: %w", owner.Name, err)
		}
		for _, mount := range mounts {
			if mount.Type != "volume" || mount.Name == "" {
				continue
			}
			mode := "ro"
			if mount.RW {
				mode = "rw"
			}
			out[mount.Name] = append(out[mount.Name], protocol.VolumeMount{
				ContainerID: owner.ID,
				Container:   owner.Name,
				Destination: mount.Destination,
				Mode:        mode,
				State:       owner.State,
			})
		}
	}
	for name := range out {
		mounts := out[name]
		sort.Slice(mounts, func(i, j int) bool { return mounts[i].Container < mounts[j].Container })
	}
	return out, nil
}

type volumeInspectJSON struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Mountpoint string            `json:"Mountpoint"`
	CreatedAt  string            `json:"CreatedAt"`
	Labels     map[string]string `json:"Labels"`
}

// VolumeDetail inspects one volume of this agent's prefix, with the containers that mount it.
func (c *Client) VolumeDetail(ctx context.Context, name string) (protocol.VolumeDetail, error) {
	if c.prefix != "" && !strings.HasPrefix(name, c.prefix) {
		return protocol.VolumeDetail{}, fmt.Errorf("volume %q is outside agent prefix %q", name, c.prefix)
	}
	data, err := c.run(ctx, "volume", "inspect", "--format", "json", "--", name)
	if err != nil {
		return protocol.VolumeDetail{}, err
	}
	items, err := decodeJSON[[]volumeInspectJSON]("volume inspect", data)
	if err != nil {
		return protocol.VolumeDetail{}, err
	}
	if len(items) != 1 || items[0].Name != name {
		return protocol.VolumeDetail{}, fmt.Errorf("podman volume inspect returned no unique volume for %q", name)
	}
	mounts, err := c.volumeMounts(ctx)
	if err != nil {
		return protocol.VolumeDetail{}, err
	}
	item := items[0]
	return protocol.VolumeDetail{
		Volume:  protocol.Volume{Name: item.Name, Driver: item.Driver, Mountpoint: item.Mountpoint, Mounts: mounts[item.Name]},
		Created: item.CreatedAt,
		Labels:  item.Labels,
	}, nil
}

type imageJSON struct {
	ID      string   `json:"Id"`
	Tags    []string `json:"RepoTags"`
	Size    int64    `json:"Size"`
	Created int64    `json:"Created"`
}

func (c *Client) Images(ctx context.Context) ([]protocol.Image, error) {
	data, err := c.run(ctx, "images", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	items, err := decodeJSON[[]imageJSON]("images", data)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.Image, len(items))
	for i := range items {
		item := &items[i]
		out[i] = protocol.Image{ID: item.ID, Tags: item.Tags, Size: item.Size, Created: item.Created}
	}
	return out, nil
}

type inspectJSON struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

func (c *Client) resolveContainer(ctx context.Context, reference string) (inspectJSON, error) {
	data, err := c.run(ctx, "container", "inspect", "--format", "json", "--", reference)
	if err != nil {
		return inspectJSON{}, err
	}
	items, err := decodeJSON[[]inspectJSON]("container inspect", data)
	if err != nil {
		return inspectJSON{}, err
	}
	if len(items) != 1 || items[0].ID == "" || strings.TrimPrefix(items[0].Name, "/") == "" {
		return inspectJSON{}, fmt.Errorf("podman container inspect returned no unique canonical container for %q", reference)
	}
	items[0].Name = strings.TrimPrefix(items[0].Name, "/")
	if c.prefix != "" && !strings.HasPrefix(items[0].Name, c.prefix) {
		return inspectJSON{}, fmt.Errorf("container %q is outside agent prefix %q", items[0].Name, c.prefix)
	}
	return items[0], nil
}

func (c *Client) Start(ctx context.Context, reference string) error {
	item, err := c.resolveContainer(ctx, reference)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, "start", "--", item.ID)
	return err
}

func (c *Client) Stop(ctx context.Context, reference string) error {
	item, err := c.resolveContainer(ctx, reference)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, "stop", "--", item.ID)
	return err
}

// Restart stops and starts a container, so its OCI hooks run again.
func (c *Client) Restart(ctx context.Context, reference string) error {
	item, err := c.resolveContainer(ctx, reference)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, "restart", "--", item.ID)
	return err
}

// ProjectContainer is one container of a Compose project.
type ProjectContainer struct {
	ID      string
	Name    string
	Service string
	// Project is the compose project the container belongs to.
	Project string
	Image   string
	State   string
	// Status is Podman's human-readable status, for example "Up 2 hours".
	Status string
	// Pid is the container's main process on the host, 0 when not running.
	Pid int
}

type projectContainerJSON struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Pid    int               `json:"Pid"`
	Labels map[string]string `json:"Labels"`
}

const composeProjectLabel = "com.docker.compose.project"

// ProjectContainers lists the containers podman-compose created for project.
func (c *Client) ProjectContainers(ctx context.Context, project string) ([]ProjectContainer, error) {
	all, err := c.composeContainers(ctx, "label="+composeProjectLabel+"="+project)
	if err != nil {
		return nil, err
	}
	return all[project], nil
}

// ComposeContainers lists the containers of every compose project on this
// agent, grouped by project, in one Podman call.
func (c *Client) ComposeContainers(ctx context.Context) (map[string][]ProjectContainer, error) {
	return c.composeContainers(ctx, "label="+composeProjectLabel)
}

// composeContainers runs one `podman ps` with filter and groups the containers
// by compose project. Containers outside the agent's name prefix are skipped.
func (c *Client) composeContainers(ctx context.Context, filter string) (map[string][]ProjectContainer, error) {
	data, err := c.run(ctx, "ps", "--all", "--format", "json", "--filter", filter)
	if err != nil {
		return nil, err
	}
	items, err := decodeJSON[[]projectContainerJSON]("ps", data)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]ProjectContainer)
	for i := range items {
		item := &items[i]
		project := item.Labels[composeProjectLabel]
		if project == "" {
			continue
		}
		name := ""
		if len(item.Names) != 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		if c.prefix != "" && !strings.HasPrefix(name, c.prefix) {
			continue
		}
		out[project] = append(out[project], ProjectContainer{
			ID:      item.ID,
			Name:    name,
			Service: item.Labels["com.docker.compose.service"],
			Project: project,
			Image:   item.Image,
			State:   item.State,
			Status:  item.Status,
			Pid:     item.Pid,
		})
	}
	return out, nil
}
