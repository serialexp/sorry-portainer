package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/serialexp/sorry-portainer/internal/podman"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

type Executor interface {
	Run(context.Context, []string, string, map[string]string) (string, error)
}

const (
	defaultComposeProvider = "podman-compose"
	maxComposeOutput       = 1 << 20
)

// ComposeExecutor executes a Compose command through Podman. Provider is
// deliberately applied after the stack environment so stack input cannot select
// a different Compose implementation.
type ComposeExecutor struct {
	Provider string
}

// NewComposeExecutor creates a Podman Compose executor pinned to provider.
// An empty provider selects the supported default, podman-compose.
func NewComposeExecutor(provider string) ComposeExecutor {
	if provider == "" {
		provider = defaultComposeProvider
	}
	return ComposeExecutor{Provider: provider}
}

func (e ComposeExecutor) Run(ctx context.Context, args []string, dir string, env map[string]string) (string, error) {
	provider := e.Provider
	if provider == "" {
		provider = defaultComposeProvider
	}

	argv := make([]string, 1, len(args)+1)
	argv[0] = "compose"
	argv = append(argv, args...)
	c := exec.CommandContext(ctx, "podman", argv...)
	c.Dir = dir
	c.Env = composeEnvironment(os.Environ(), env, provider)
	// Compose providers can spawn child processes. Give Podman its own process
	// group and cancel the entire group rather than leaving a child holding the
	// stdout/stderr pipes open after Podman exits.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
	c.WaitDelay = time.Second

	output := &cappedBuffer{limit: maxComposeOutput}
	c.Stdout = output
	c.Stderr = output
	err := c.Run()
	return output.String(), err
}

func composeEnvironment(base []string, stack map[string]string, provider string) []string {
	values := make(map[string]string, len(base)+len(stack)+1)
	for _, entry := range base {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	for key, value := range stack {
		values[key] = value
	}
	values["PODMAN_COMPOSE_PROVIDER"] = provider

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

// cappedBuffer retains at most limit bytes while accepting the rest. It is safe
// for the independent stdout and stderr copier goroutines used by os/exec.
type cappedBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if remaining := b.limit - len(b.buf); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.buf = append(b.buf, p...)
	}
	return n, nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

type Manager struct {
	root     string
	prefix   string
	executor Executor
	mu       sync.Mutex
	locks    map[string]*sync.Mutex
	// secrets is nil when the agent runs without secret support; stacks that
	// use secrets then refuse to start.
	secrets *SecretSupport
	// runtime is nil unless EnableRuntimeStatus was called.
	runtime ContainerLister
	// images and checker are nil unless EnableImageChecks was called.
	images  ImageSource
	checker RemoteChecker
	// bootID (see SetBootID) and waiting are guarded by mu. waiting holds why
	// a stack that should be up has not come up since boot.
	bootID  string
	waiting map[string]string
	// Logf reports background bring-ups; nil discards them.
	Logf func(format string, args ...any)
}

func New(root, prefix string, e Executor) *Manager {
	return &Manager{root: root, prefix: prefix, executor: e, locks: map[string]*sync.Mutex{}, waiting: map[string]string{}}
}
func ValidName(n string) bool { return protocol.ValidStackName(n) }
func (m *Manager) lock(n string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l := m.locks[n]; l != nil {
		return l
	}
	l := &sync.Mutex{}
	m.locks[n] = l
	return l
}
func (m *Manager) dir(n string) string { return filepath.Join(m.root, n) }
func (m *Manager) List(ctx context.Context) ([]protocol.Stack, error) {
	names, e := m.stackNames()
	if e != nil {
		return nil, e
	}
	containers, known, e := m.containersByProject(ctx)
	if e != nil {
		return nil, e
	}
	out := make([]protocol.Stack, 0, len(names))
	for _, n := range names {
		l := m.lock(n)
		l.Lock()
		stack := protocol.Stack{Name: n, Project: m.project(n), Status: "saved"}
		stack.Version, e = m.version(n)
		var compose []byte
		if e == nil {
			compose, e = os.ReadFile(filepath.Join(m.dir(n), "compose.yaml"))
		}
		if e == nil {
			if stateErr := m.describeState(&stack); stateErr != nil {
				stack.Waiting = stateErr.Error()
			}
			stack.Status, stack.Services = m.describeServices(compose, containers[stack.Project], known)
		}
		l.Unlock()
		if e == nil {
			out = append(out, stack)
		}
	}
	return out, nil
}

func (m *Manager) Inspect(ctx context.Context, n string) (protocol.Stack, error) {
	if !ValidName(n) {
		return protocol.Stack{}, errors.New("invalid stack name")
	}
	l := m.lock(n)
	l.Lock()
	defer l.Unlock()
	stack, err := m.inspect(n)
	if err != nil {
		return protocol.Stack{}, err
	}
	containers, known, err := m.containersByProject(ctx)
	if err != nil {
		return protocol.Stack{}, err
	}
	stack.Status, stack.Services = m.describeServices([]byte(stack.ComposeYAML), containers[stack.Project], known)
	return stack, nil
}

// describeServices lists a stack's services with their container states. A
// Compose file that does not parse gives status "invalid" and no services, so
// the list shows the problem instead of an empty stack.
func (m *Manager) describeServices(compose []byte, containers []podman.ProjectContainer, known bool) (string, []protocol.StackService) {
	declared, err := declaredServices(compose)
	if err != nil {
		return "invalid", nil
	}
	return "saved", serviceStates(declared, containers, known)
}

func (m *Manager) inspect(n string) (protocol.Stack, error) {
	b, e := os.ReadFile(filepath.Join(m.dir(n), "compose.yaml"))
	if e != nil {
		return protocol.Stack{}, e
	}
	version, e := m.version(n)
	if e != nil {
		return protocol.Stack{}, e
	}
	env, e := m.environment(n, version)
	if e != nil {
		return protocol.Stack{}, e
	}
	stack := protocol.Stack{Name: n, Project: m.project(n), ComposeYAML: string(b), Status: "saved", Version: version, Environment: env}
	// A stack saved before secret validation may not parse; it still inspects.
	if doc, e := parseCompose(b); e == nil {
		stack.Secrets = doc.plan.Names
	}
	if e := m.describeState(&stack); e != nil {
		stack.Waiting = e.Error()
	}
	return stack, nil
}

// Versions returns revision metadata only; Compose documents and environments
// are available through Inspect.
func (m *Manager) Versions(ctx context.Context, n string) ([]protocol.StackVersion, error) {
	if !ValidName(n) {
		return nil, errors.New("invalid stack name")
	}
	l := m.lock(n)
	l.Lock()
	defer l.Unlock()
	return m.versions(n)
}

// versions requires the stack lock to be held.
func (m *Manager) versions(n string) ([]protocol.StackVersion, error) {
	if _, e := os.Stat(filepath.Join(m.dir(n), "compose.yaml")); e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(filepath.Join(m.dir(n), "revisions"))
	if os.IsNotExist(e) {
		return []protocol.StackVersion{{Version: 1}}, nil
	}
	if e != nil {
		return nil, e
	}
	out := make([]protocol.StackVersion, 0, len(entries)+1)
	hasOne := false
	for _, entry := range entries {
		var version int
		if _, e := fmt.Sscanf(entry.Name(), "%d.yaml", &version); e == nil && version > 0 {
			out = append(out, protocol.StackVersion{Version: version})
			hasOne = hasOne || version == 1
		}
	}
	// Stacks created before revisions were introduced have an implicit v1.
	if !hasOne {
		out = append(out, protocol.StackVersion{Version: 1})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func (m *Manager) version(n string) (int, error) {
	// A revision-aware stack points compose.yaml at the active immutable revision.
	// A regular file is a legacy stack and is implicitly revision one.
	target, e := os.Readlink(filepath.Join(m.dir(n), "compose.yaml"))
	if e == nil {
		var version int
		if _, e := fmt.Sscanf(filepath.Base(target), "%d.yaml", &version); e == nil && version > 0 {
			return version, nil
		}
		return 0, errors.New("invalid stack revision pointer")
	}
	if errors.Is(e, os.ErrInvalid) || errors.Is(e, syscall.EINVAL) {
		return 1, nil
	}
	return 0, e
}

// maxEnvironment bounds a stack's environment entries.
const maxEnvironment = 128

var environmentKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkEnvironment refuses keys that are not variable names and values an
// env file or the dashboard's KEY=VALUE field could not show back. Errors
// name keys, never values.
func checkEnvironment(env map[string]string) error {
	if len(env) > maxEnvironment {
		return fmt.Errorf("stack environment has %d entries; at most %d", len(env), maxEnvironment)
	}
	for key, value := range env {
		if !environmentKey.MatchString(key) {
			return fmt.Errorf("environment key %q is not a valid name", key)
		}
		if strings.ContainsAny(value, "\n\r\x00") {
			return fmt.Errorf("environment %s: values cannot contain line breaks or NUL", key)
		}
	}
	return nil
}

// environmentFile is revision version's environment.
func environmentFile(dir string, version int) string {
	return filepath.Join(dir, "revisions", fmt.Sprintf("%d.env.json", version))
}

// environment returns revision version's environment: its own file, else the
// stack-level env.json of stacks saved before environments were versioned,
// else none. The caller holds the stack lock.
func (m *Manager) environment(n string, version int) (map[string]string, error) {
	for _, p := range []string{environmentFile(m.dir(n), version), filepath.Join(m.dir(n), "env.json")} {
		b, e := os.ReadFile(p)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, e
		}
		env := map[string]string{}
		if e := json.Unmarshal(b, &env); e != nil {
			return nil, fmt.Errorf("stack %s: %s is not a JSON object of strings", n, filepath.Base(p))
		}
		return env, nil
	}
	return map[string]string{}, nil
}

// Save creates when expected is nil and updates only when it equals the
// current revision. A nil env keeps the current environment (none for a new
// stack); a non-nil one replaces it. When an update changes the environment
// of a stack whose desired state is up, Save brings it up again so its
// containers get the new values.
func (m *Manager) Save(ctx context.Context, n, y string, env map[string]string, expected *int) (protocol.StackSaved, error) {
	if !ValidName(n) {
		return protocol.StackSaved{}, errors.New("invalid stack name")
	}
	if len(y) > 1<<20 {
		return protocol.StackSaved{}, errors.New("stack payload too large")
	}
	if e := checkEnvironment(env); e != nil {
		return protocol.StackSaved{}, e
	}
	doc, e := parseCompose([]byte(y))
	if e != nil {
		return protocol.StackSaved{}, e
	}
	if !doc.plan.empty() && m.secrets == nil {
		return protocol.StackSaved{}, errSecretsUnsupported
	}
	l := m.lock(n)
	l.Lock()
	defer l.Unlock()
	dir := m.dir(n)
	if expected == nil {
		if _, e := os.Lstat(dir); e == nil {
			return protocol.StackSaved{}, errors.New("stack already exists")
		} else if !os.IsNotExist(e) {
			return protocol.StackSaved{}, e
		}
		if env == nil {
			env = map[string]string{}
		}
		if e := os.MkdirAll(m.root, 0700); e != nil {
			return protocol.StackSaved{}, e
		}
		staging, e := os.MkdirTemp(m.root, ".stack-")
		if e != nil {
			return protocol.StackSaved{}, e
		}
		defer os.RemoveAll(staging)
		if e := os.Mkdir(filepath.Join(staging, "revisions"), 0700); e != nil {
			return protocol.StackSaved{}, e
		}
		if e := m.writeRevision(staging, 1, y, env); e != nil {
			return protocol.StackSaved{}, e
		}
		if e := m.writeComposePointer(staging, 1); e != nil {
			return protocol.StackSaved{}, e
		}
		if e := os.Rename(staging, dir); e != nil {
			if os.IsExist(e) {
				return protocol.StackSaved{}, errors.New("stack already exists")
			}
			return protocol.StackSaved{}, e
		}
		return protocol.StackSaved{Stack: protocol.Stack{Name: n, Project: m.project(n), ComposeYAML: y, Status: "saved", Version: 1, Environment: env}}, nil
	}
	if _, e := os.Stat(filepath.Join(dir, "compose.yaml")); e != nil {
		return protocol.StackSaved{}, e
	}
	current, e := m.version(n)
	if e != nil {
		return protocol.StackSaved{}, e
	}
	if *expected != current {
		return protocol.StackSaved{}, fmt.Errorf("stack version mismatch: expected %d, current %d", *expected, current)
	}
	previous, e := m.environment(n, current)
	if e != nil {
		return protocol.StackSaved{}, e
	}
	if env == nil {
		env = previous
	}
	revisions := filepath.Join(dir, "revisions")
	if current == 1 {
		if _, e := os.Lstat(revisions); os.IsNotExist(e) {
			legacy, e := os.ReadFile(filepath.Join(dir, "compose.yaml"))
			if e != nil {
				return protocol.StackSaved{}, e
			}
			if e := os.Mkdir(revisions, 0700); e != nil {
				return protocol.StackSaved{}, e
			}
			if e := m.writeAtomic(filepath.Join(revisions, "1.yaml"), legacy); e != nil {
				return protocol.StackSaved{}, e
			}
		}
	}
	version := current + 1
	if e := m.writeRevision(dir, version, y, env); e != nil {
		return protocol.StackSaved{}, e
	}
	if e := m.writeComposePointer(dir, version); e != nil {
		return protocol.StackSaved{}, e
	}
	saved := protocol.StackSaved{Stack: protocol.Stack{Name: n, Project: m.project(n), ComposeYAML: y, Status: "saved", Version: version, Environment: env}}
	if maps.Equal(previous, env) {
		return saved, nil
	}
	state, e := m.readState(n)
	if e != nil {
		// Saved; the caller can still start it by hand.
		saved.Operation = &protocol.StackOperation{Name: n, Operation: "up", Output: "the environment changed, but the stack's desired state could not be read: " + e.Error()}
		return saved, nil
	}
	if state.Desired != desiredUp {
		return saved, nil
	}
	op, e := m.operateLocked(ctx, n, "up")
	if e != nil && op.Operation == "" {
		op = protocol.StackOperation{Name: n, Operation: "up", Output: e.Error()}
	}
	saved.Operation = &op
	if desc := m.describeState(&saved.Stack); desc != nil {
		saved.Waiting = desc.Error()
	}
	return saved, nil
}

// writeRevision writes revision version's compose file and environment into
// dir. Neither is live until the compose pointer moves to it.
func (m *Manager) writeRevision(dir string, version int, y string, env map[string]string) error {
	b, e := json.Marshal(env)
	if e != nil {
		return e
	}
	if e := m.writeAtomic(environmentFile(dir, version), b); e != nil {
		return e
	}
	return m.writeAtomic(filepath.Join(dir, "revisions", fmt.Sprintf("%d.yaml", version)), []byte(y))
}

func (m *Manager) writeAtomic(path string, value []byte) error {
	tmp, e := os.CreateTemp(filepath.Dir(path), ".stack-")
	if e != nil {
		return e
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if e := tmp.Chmod(0600); e != nil {
		_ = tmp.Close()
		return e
	}
	if _, e := tmp.Write(value); e != nil {
		_ = tmp.Close()
		return e
	}
	if e := tmp.Close(); e != nil {
		return e
	}
	return os.Rename(tmpName, path)
}

func (m *Manager) writeComposePointer(dir string, version int) error {
	tmp := filepath.Join(dir, ".compose.yaml")
	if e := os.Symlink(filepath.Join("revisions", fmt.Sprintf("%d.yaml", version)), tmp); e != nil {
		return e
	}
	if e := os.Rename(tmp, filepath.Join(dir, "compose.yaml")); e != nil {
		_ = os.Remove(tmp)
		return e
	}
	return nil
}
func (m *Manager) project(n string) string {
	return strings.Trim(strings.ReplaceAll(m.prefix, "_", "-"), "-") + "-" + n
}
func (m *Manager) operate(ctx context.Context, n, op string) (protocol.StackOperation, error) {
	if !ValidName(n) {
		return protocol.StackOperation{}, errors.New("invalid stack name")
	}
	l := m.lock(n)
	l.Lock()
	defer l.Unlock()
	return m.operateLocked(ctx, n, op)
}

// operateLocked runs a Compose operation with the stack lock held. Up and
// Down record the desired state first, so a reboot brings the stack back to
// what was last asked even when the operation itself failed.
func (m *Manager) operateLocked(ctx context.Context, n, op string) (protocol.StackOperation, error) {
	stack, e := m.inspect(n)
	if e != nil {
		return protocol.StackOperation{}, e
	}
	var state stackState
	if op == "up" || op == "down" {
		var e error
		if state, e = m.readState(n); e != nil {
			return protocol.StackOperation{}, e
		}
		state.Desired = desiredUp
		if op == "down" {
			state.Desired = desiredDown
		}
		if e := m.writeState(n, state); e != nil {
			return protocol.StackOperation{}, fmt.Errorf("record desired state: %w", e)
		}
		m.setWaiting(n, "")
	}
	plan, e := m.prepareRuntime(n, op != "down")
	if e != nil {
		if op == "up" {
			m.setWaiting(n, waitingReason(e))
		}
		return protocol.StackOperation{Name: n, Operation: op, Output: e.Error()}, e
	}
	// podman-compose 1.0.6 accepts -p/-f, but not Docker Compose's
	// --project-directory option. The executor sets its working directory.
	args := []string{"-p", m.project(n), "-f", runtimeComposeFile}
	env := stack.Environment
	if op == "up" {
		args = append(args, "up", "-d")
	} else {
		args = append(args, op)
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, e := m.executor.Run(runCtx, args, m.dir(n), env)
	if e == nil && op != "down" && !plan.empty() {
		if e = m.checkInjected(runCtx, n, plan.Services); e != nil {
			out += "\n" + e.Error()
		}
	}
	if op == "up" {
		if e != nil {
			m.setWaiting(n, waitingReason(e))
		} else if boot := m.currentBoot(); boot != "" {
			state.UpBoot = boot
			if e = m.writeState(n, state); e != nil {
				e = fmt.Errorf("record bring-up: %w", e)
				out += "\n" + e.Error()
			}
		}
	}
	return protocol.StackOperation{Name: n, Operation: op, Output: out, Success: e == nil}, e
}
func (m *Manager) Up(c context.Context, n string) (protocol.StackOperation, error) {
	return m.operate(c, n, "up")
}
func (m *Manager) Down(c context.Context, n string) (protocol.StackOperation, error) {
	return m.operate(c, n, "down")
}
func (m *Manager) Restart(c context.Context, n string) (protocol.StackOperation, error) {
	return m.operate(c, n, "restart")
}
