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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
	// sizes holds the volume sizes that the background scan measured.
	sizes volumeSizes
	// now is the clock for event windows; nil means time.Now.
	now func() time.Time
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
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

// outputSink receives a Podman command's output. A sink that cannot accept
// more cancels the command and reports why through failure.
type outputSink interface {
	io.Writer
	failure() error
}

type limitedOutput struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int
	exceeded bool
	cancel   context.CancelFunc
	// stream names the output in errors, for example "podman ps: stdout".
	stream string
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

func (w *limitedOutput) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.exceeded {
		return fmt.Errorf("%s exceeds %d bytes", w.stream, w.limit)
	}
	return nil
}

// run runs Podman and returns its stdout, at most maxCommandOutput bytes.
func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	var stdout *limitedOutput
	err := c.runWith(ctx, func(cancel context.CancelFunc) outputSink {
		stdout = &limitedOutput{limit: maxCommandOutput, cancel: cancel, stream: "podman " + args[0] + ": stdout"}
		return stdout
	}, args...)
	if err != nil {
		return nil, err
	}
	return stdout.bytes(), nil
}

// runWith runs Podman with stdout going to the sink newStdout builds; the
// sink gets the function that cancels the command. Stderr is kept, at most
// maxCommandOutput bytes, for the error message.
func (c *Client) runWith(ctx context.Context, newStdout func(cancel context.CancelFunc) outputSink, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.runner == nil {
		return errors.New("podman runner is nil")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := newStdout(cancel)
	stderr := &limitedOutput{limit: maxCommandOutput, cancel: cancel, stream: "podman " + args[0] + ": stderr"}
	err := c.runner.Run(runCtx, stdout, stderr, args...)
	if failure := stdout.failure(); failure != nil {
		return failure
	}
	if failure := stderr.failure(); failure != nil {
		return failure
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		message := strings.TrimSpace(string(stderr.bytes()))
		if message == "" {
			return fmt.Errorf("podman %s: %w", args[0], err)
		}
		return fmt.Errorf("podman %s: %w: %s", args[0], err, message)
	}
	return nil
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
	ID       string   `json:"Id"`
	Names    []string `json:"Names"`
	Image    string   `json:"Image"`
	ImageID  string   `json:"ImageID"`
	State    string   `json:"State"`
	Status   string   `json:"Status"`
	Restarts int      `json:"Restarts"`
}

// container is item as the protocol reports it, under name.
func (item *containerJSON) container(name string) protocol.Container {
	status, health := splitHealth(item.Status)
	return protocol.Container{ID: item.ID, Name: name, Image: item.Image, State: item.State,
		Status: status, Health: health, Restarts: item.Restarts}
}

// splitHealth separates the health check's result from a status in
// `podman ps --format json`, which since Podman 4.7 is the human-readable
// status with the result appended in parentheses: "Up 2 hours (unhealthy)".
// The result is one lowercase word; anything else stays part of the status.
func splitHealth(status string) (rest, health string) {
	if !strings.HasSuffix(status, ")") {
		return status, ""
	}
	i := strings.LastIndex(status, " (")
	if i < 0 {
		return status, ""
	}
	word := status[i+2 : len(status)-1]
	if word == "" {
		return status, ""
	}
	for _, r := range word {
		if r < 'a' || r > 'z' {
			return status, ""
		}
	}
	return status[:i], word
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
		out = append(out, item.container(name))
	}
	if len(out) == 0 {
		return out, nil
	}
	now := c.clock()
	recent, err := c.recentActivity(ctx, now)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Recent = recent[out[i].ID].recent(now, out[i].Restarts)
	}
	return out, nil
}

type volumeJSON struct {
	Name       string `json:"Name"`
	Driver     string `json:"Driver"`
	Mountpoint string `json:"Mountpoint"`
}

// Volumes lists the volumes of this agent's prefix with their mounts and the
// size the last background scan measured.
func (c *Client) Volumes(ctx context.Context) ([]protocol.Volume, error) {
	out, err := c.listVolumes(ctx)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	usage, err := c.volumeMounts(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Mounts = usage[out[i].Name].mounts
		out[i].MountedOutsidePrefix = usage[out[i].Name].elsewhere
		c.applySize(&out[i])
	}
	return out, nil
}

// listVolumes lists the volumes of this agent's prefix without their mounts.
// The size scan uses it, because it needs names and mountpoints only.
func (c *Client) listVolumes(ctx context.Context) ([]protocol.Volume, error) {
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
	return out, nil
}

type mountJSON struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

// volumeUsage is how the containers of one volume relate to this agent's prefix.
type volumeUsage struct {
	// mounts are the mounts of the volume by containers of this agent's prefix.
	mounts []protocol.VolumeMount
	// elsewhere is set when a container outside the prefix mounts the volume.
	elsewhere bool
}

// volumeMounts maps each named volume to the containers that mount it. Mounts by
// containers outside the prefix are not listed, but they make the volume mounted
// elsewhere. Every container's mounts come from one podman call. A template
// prints only each container's mounts: the full inspect document of a busy host
// would not fit the command output bound.
func (c *Client) volumeMounts(ctx context.Context) (map[string]volumeUsage, error) {
	data, err := c.run(ctx, "ps", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	items, err := decodeJSON[[]containerJSON]("ps", data)
	if err != nil {
		return nil, err
	}
	out := make(map[string]volumeUsage)
	if len(items) == 0 {
		return out, nil
	}
	type owner struct {
		protocol.Container
		inPrefix bool
	}
	byID := make(map[string]owner, len(items))
	args := make([]string, 0, len(items)+5)
	args = append(args, "container", "inspect", "--format", `{{.ID}}{{"\t"}}{{json .Mounts}}`, "--")
	for i := range items {
		item := &items[i]
		name := containerName(item)
		byID[item.ID] = owner{
			Container: item.container(name),
			inPrefix:  c.prefix == "" || strings.HasPrefix(name, c.prefix),
		}
		args = append(args, item.ID)
	}
	data, err = c.run(ctx, args...)
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
		owned, known := byID[id]
		if !known {
			continue
		}
		if err := json.Unmarshal([]byte(mountsJSON), &mounts); err != nil {
			return nil, fmt.Errorf("podman container inspect: mounts of %q: %w", owned.Name, err)
		}
		for _, mount := range mounts {
			if mount.Type != "volume" || mount.Name == "" {
				continue
			}
			usage := out[mount.Name]
			if !owned.inPrefix {
				usage.elsewhere = true
				out[mount.Name] = usage
				continue
			}
			mode := "ro"
			if mount.RW {
				mode = "rw"
			}
			usage.mounts = append(usage.mounts, protocol.VolumeMount{
				ContainerID: owned.ID,
				Container:   owned.Name,
				Destination: mount.Destination,
				Mode:        mode,
				State:       owned.State,
			})
			out[mount.Name] = usage
		}
	}
	for name, usage := range out {
		sort.Slice(usage.mounts, func(i, j int) bool { return usage.mounts[i].Container < usage.mounts[j].Container })
		out[name] = usage
	}
	return out, nil
}

// containerName is the canonical name of a container, without Podman's leading slash.
func containerName(item *containerJSON) string {
	if len(item.Names) == 0 {
		return ""
	}
	return strings.TrimPrefix(item.Names[0], "/")
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
	usage, err := c.volumeMounts(ctx)
	if err != nil {
		return protocol.VolumeDetail{}, err
	}
	item := items[0]
	volume := protocol.Volume{
		Name:                 item.Name,
		Driver:               item.Driver,
		Mountpoint:           item.Mountpoint,
		Mounts:               usage[item.Name].mounts,
		MountedOutsidePrefix: usage[item.Name].elsewhere,
	}
	c.applySize(&volume)
	return protocol.VolumeDetail{Volume: volume, Created: item.CreatedAt, Labels: item.Labels}, nil
}

type imageJSON struct {
	ID      string   `json:"Id"`
	Tags    []string `json:"RepoTags"`
	Size    int64    `json:"Size"`
	Created int64    `json:"Created"`
}

// Images lists every image on the host. Images are shared by the whole Podman
// installation, so they are not filtered by prefix; only the containers that
// use them are, because those belong to this agent.
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
	if len(out) == 0 {
		return out, nil
	}
	users, err := c.imageUsers(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		usage := users[out[i].ID]
		out[i].Containers = usage.containers
		out[i].UsedOutsidePrefix = usage.elsewhere
	}
	return out, nil
}

// imageUsage is how the containers of one image relate to this agent's prefix.
type imageUsage struct {
	// containers are the containers of this agent's prefix that run the image.
	containers []protocol.ImageUse
	// elsewhere is set when a container outside the prefix runs the image.
	elsewhere bool
}

// imageUsers maps each image ID to the containers that run it. Containers
// outside the prefix are not listed, but they make the image used elsewhere.
func (c *Client) imageUsers(ctx context.Context) (map[string]imageUsage, error) {
	data, err := c.run(ctx, "ps", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	items, err := decodeJSON[[]containerJSON]("ps", data)
	if err != nil {
		return nil, err
	}
	out := make(map[string]imageUsage)
	for i := range items {
		item := &items[i]
		name := ""
		if len(item.Names) != 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		usage := out[item.ImageID]
		if c.prefix != "" && !strings.HasPrefix(name, c.prefix) {
			usage.elsewhere = true
			out[item.ImageID] = usage
			continue
		}
		usage.containers = append(usage.containers, protocol.ImageUse{ContainerID: item.ID, Container: name, State: item.State})
		out[item.ImageID] = usage
	}
	for id, usage := range out {
		sort.Slice(usage.containers, func(i, j int) bool { return usage.containers[i].Container < usage.containers[j].Container })
		out[id] = usage
	}
	return out, nil
}

// imageIDPattern accepts a full image ID or a short prefix of one, never anything
// that podman could read as an option or a reference.
var imageIDPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

type imageInspectJSON struct {
	ID           string   `json:"Id"`
	RepoTags     []string `json:"RepoTags"`
	Created      string   `json:"Created"`
	Size         int64    `json:"Size"`
	OS           string   `json:"Os"`
	Architecture string   `json:"Architecture"`
	Config       struct {
		Cmd          []string            `json:"Cmd"`
		Entrypoint   []string            `json:"Entrypoint"`
		WorkingDir   string              `json:"WorkingDir"`
		Env          []string            `json:"Env"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
	} `json:"Config"`
}

type imageHistoryJSON struct {
	CreatedBy string `json:"CreatedBy"`
	Size      int64  `json:"size"`
}

// ImageDetail inspects one image, its build steps and the containers of this
// agent's prefix that run it. The ID is a full ID or a short prefix of one.
func (c *Client) ImageDetail(ctx context.Context, id string) (protocol.ImageDetail, error) {
	if !imageIDPattern.MatchString(id) {
		return protocol.ImageDetail{}, fmt.Errorf("image %q is not a hexadecimal image ID", id)
	}
	data, err := c.run(ctx, "image", "inspect", "--format", "json", "--", id)
	if err != nil {
		return protocol.ImageDetail{}, err
	}
	items, err := decodeJSON[[]imageInspectJSON]("image inspect", data)
	if err != nil {
		return protocol.ImageDetail{}, err
	}
	if len(items) != 1 || items[0].ID == "" {
		return protocol.ImageDetail{}, fmt.Errorf("podman image inspect returned no unique image for %q", id)
	}
	item := items[0]
	created, err := time.Parse(time.RFC3339Nano, item.Created)
	if err != nil {
		return protocol.ImageDetail{}, fmt.Errorf("podman image inspect: created time of %s: %w", item.ID, err)
	}
	historyData, err := c.run(ctx, "history", "--format", "json", "--", item.ID)
	if err != nil {
		return protocol.ImageDetail{}, err
	}
	history, err := decodeJSON[[]imageHistoryJSON]("history", historyData)
	if err != nil {
		return protocol.ImageDetail{}, err
	}
	// podman lists the newest step first.
	layers := make([]protocol.ImageLayer, 0, len(history))
	for i := len(history) - 1; i >= 0; i-- {
		layers = append(layers, protocol.ImageLayer{CreatedBy: history[i].CreatedBy, Size: history[i].Size})
	}
	users, err := c.imageUsers(ctx)
	if err != nil {
		return protocol.ImageDetail{}, err
	}
	usage := users[item.ID]
	var ports []string
	for port := range item.Config.ExposedPorts {
		ports = append(ports, port)
	}
	sort.Strings(ports)
	return protocol.ImageDetail{
		Image: protocol.Image{
			ID:                item.ID,
			Tags:              item.RepoTags,
			Size:              item.Size,
			Created:           created.Unix(),
			Containers:        usage.containers,
			UsedOutsidePrefix: usage.elsewhere,
		},
		Platform:   item.OS + "/" + item.Architecture,
		Command:    item.Config.Cmd,
		Entrypoint: item.Config.Entrypoint,
		WorkingDir: item.Config.WorkingDir,
		Ports:      ports,
		Env:        item.Config.Env,
		Layers:     layers,
	}, nil
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
	// Status is Podman's human-readable status, for example "Up 2 hours",
	// without the health check's result, which is Health.
	Status string
	// Health is the health check's result, empty without a health check.
	Health string
	// Restarts counts restarts by the container's restart policy.
	Restarts int
	// Recent is the container's restarts and health failures of the last
	// protocol.RecentWindow; ComposeContainers fills it, ProjectContainers
	// does not.
	Recent protocol.Recent
	// Pid is the container's main process on the host, 0 when not running.
	Pid int
}

type projectContainerJSON struct {
	ID       string            `json:"Id"`
	Names    []string          `json:"Names"`
	Image    string            `json:"Image"`
	State    string            `json:"State"`
	Status   string            `json:"Status"`
	Restarts int               `json:"Restarts"`
	Pid      int               `json:"Pid"`
	Labels   map[string]string `json:"Labels"`
}

const composeProjectLabel = "com.docker.compose.project"

// ProjectContainers lists the containers podman-compose created for project.
func (c *Client) ProjectContainers(ctx context.Context, project string) ([]ProjectContainer, error) {
	all, err := c.composeContainers(ctx, "label="+composeProjectLabel+"="+project, false)
	if err != nil {
		return nil, err
	}
	return all[project], nil
}

// ComposeContainers lists the containers of every compose project on this
// agent, grouped by project, with their recent restarts and health failures:
// one `podman ps` and, when there are containers, one `podman events`.
func (c *Client) ComposeContainers(ctx context.Context) (map[string][]ProjectContainer, error) {
	return c.composeContainers(ctx, "label="+composeProjectLabel, true)
}

// composeContainers runs one `podman ps` with filter and groups the containers
// by compose project. Containers outside the agent's name prefix are skipped.
// With withRecent it also reads the window's events.
func (c *Client) composeContainers(ctx context.Context, filter string, withRecent bool) (map[string][]ProjectContainer, error) {
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
		status, health := splitHealth(item.Status)
		out[project] = append(out[project], ProjectContainer{
			ID:       item.ID,
			Name:     name,
			Service:  item.Labels["com.docker.compose.service"],
			Project:  project,
			Image:    item.Image,
			State:    item.State,
			Status:   status,
			Health:   health,
			Restarts: item.Restarts,
			Pid:      item.Pid,
		})
	}
	if !withRecent || len(out) == 0 {
		return out, nil
	}
	now := c.clock()
	recent, err := c.recentActivity(ctx, now)
	if err != nil {
		return nil, err
	}
	for _, containers := range out {
		for i := range containers {
			containers[i].Recent = recent[containers[i].ID].recent(now, containers[i].Restarts)
		}
	}
	return out, nil
}
