package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

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
}

func New(root, prefix string, e Executor) *Manager {
	return &Manager{root: root, prefix: prefix, executor: e, locks: map[string]*sync.Mutex{}}
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
	ents, e := os.ReadDir(m.root)
	if os.IsNotExist(e) {
		return []protocol.Stack{}, nil
	}
	if e != nil {
		return nil, e
	}
	out := make([]protocol.Stack, 0, len(ents))
	for _, x := range ents {
		if !x.IsDir() || !ValidName(x.Name()) {
			continue
		}
		l := m.lock(x.Name())
		l.Lock()
		version, e := m.version(x.Name())
		if e == nil {
			_, e = os.Stat(filepath.Join(m.dir(x.Name()), "compose.yaml"))
		}
		l.Unlock()
		if e == nil {
			out = append(out, protocol.Stack{Name: x.Name(), Project: m.project(x.Name()), Status: "saved", Version: version})
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
	return m.inspect(n)
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
	stack := protocol.Stack{Name: n, Project: m.project(n), ComposeYAML: string(b), Status: "saved", Version: version}
	// A stack saved before secret validation may not parse; it still inspects.
	if doc, e := parseCompose(b); e == nil {
		stack.Secrets = doc.plan.Names
	}
	return stack, nil
}

// Versions returns revision metadata only; Compose documents remain available only
// through Inspect, so this endpoint cannot disclose stack environment secrets.
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

// Save creates when expected is nil and updates only when it equals the current
// Compose revision. Environment is accepted solely at creation time.
func (m *Manager) Save(ctx context.Context, n, y string, env map[string]string, expected *int) (protocol.Stack, error) {
	if !ValidName(n) {
		return protocol.Stack{}, errors.New("invalid stack name")
	}
	if len(y) > 1<<20 || len(env) > 128 {
		return protocol.Stack{}, errors.New("stack payload too large")
	}
	if expected != nil && len(env) != 0 {
		return protocol.Stack{}, errors.New("stack environment updates are not supported")
	}
	doc, e := parseCompose([]byte(y))
	if e != nil {
		return protocol.Stack{}, e
	}
	if !doc.plan.empty() && m.secrets == nil {
		return protocol.Stack{}, errSecretsUnsupported
	}
	l := m.lock(n)
	l.Lock()
	defer l.Unlock()
	dir := m.dir(n)
	if expected == nil {
		if _, e := os.Lstat(dir); e == nil {
			return protocol.Stack{}, errors.New("stack already exists")
		} else if !os.IsNotExist(e) {
			return protocol.Stack{}, e
		}
		if e := os.MkdirAll(m.root, 0700); e != nil {
			return protocol.Stack{}, e
		}
		staging, e := os.MkdirTemp(m.root, ".stack-")
		if e != nil {
			return protocol.Stack{}, e
		}
		defer os.RemoveAll(staging)
		if e := os.Mkdir(filepath.Join(staging, "revisions"), 0700); e != nil {
			return protocol.Stack{}, e
		}
		b, e := json.Marshal(env)
		if e != nil {
			return protocol.Stack{}, e
		}
		if e := m.writeAtomic(filepath.Join(staging, "env.json"), b); e != nil {
			return protocol.Stack{}, e
		}
		if e := m.writeAtomic(filepath.Join(staging, "revisions", "1.yaml"), []byte(y)); e != nil {
			return protocol.Stack{}, e
		}
		if e := m.writeComposePointer(staging, 1); e != nil {
			return protocol.Stack{}, e
		}
		if e := os.Rename(staging, dir); e != nil {
			if os.IsExist(e) {
				return protocol.Stack{}, errors.New("stack already exists")
			}
			return protocol.Stack{}, e
		}
		return protocol.Stack{Name: n, Project: m.project(n), ComposeYAML: y, Status: "saved", Version: 1}, nil
	}
	if _, e := os.Stat(filepath.Join(dir, "compose.yaml")); e != nil {
		return protocol.Stack{}, e
	}
	current, e := m.version(n)
	if e != nil {
		return protocol.Stack{}, e
	}
	if *expected != current {
		return protocol.Stack{}, fmt.Errorf("stack version mismatch: expected %d, current %d", *expected, current)
	}
	revisions := filepath.Join(dir, "revisions")
	if current == 1 {
		if _, e := os.Lstat(revisions); os.IsNotExist(e) {
			legacy, e := os.ReadFile(filepath.Join(dir, "compose.yaml"))
			if e != nil {
				return protocol.Stack{}, e
			}
			if e := os.Mkdir(revisions, 0700); e != nil {
				return protocol.Stack{}, e
			}
			if e := m.writeAtomic(filepath.Join(revisions, "1.yaml"), legacy); e != nil {
				return protocol.Stack{}, e
			}
		}
	}
	version := current + 1
	if e := m.writeAtomic(filepath.Join(revisions, fmt.Sprintf("%d.yaml", version)), []byte(y)); e != nil {
		return protocol.Stack{}, e
	}
	if e := m.writeComposePointer(dir, version); e != nil {
		return protocol.Stack{}, e
	}
	return protocol.Stack{Name: n, Project: m.project(n), ComposeYAML: y, Status: "saved", Version: version}, nil
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
	if _, e := m.inspect(n); e != nil {
		return protocol.StackOperation{}, e
	}
	plan, e := m.prepareRuntime(n, op != "down")
	if e != nil {
		return protocol.StackOperation{Name: n, Operation: op, Output: e.Error()}, e
	}
	// podman-compose 1.0.6 accepts -p/-f, but not Docker Compose's
	// --project-directory option. The executor sets its working directory.
	args := []string{"-p", m.project(n), "-f", runtimeComposeFile}
	env := map[string]string{}
	b, _ := os.ReadFile(filepath.Join(m.dir(n), "env.json"))
	_ = json.Unmarshal(b, &env)
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
