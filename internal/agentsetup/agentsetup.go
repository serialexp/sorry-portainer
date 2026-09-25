// Package agentsetup plans and performs the explicit host preparation needed
// by a rootless Podman agent. Planning is pure; only Installer executes commands.
package agentsetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	DefaultUser    = "sorry-portainer"
	MinPodmanMajor = 4
	MinPodmanMinor = 7
)

// Command is an executable plus an argument vector. It is intentionally not a
// shell command: arguments are always passed directly to exec.CommandContext.
type Command struct {
	Name string
	Args []string
}

type Action struct {
	Progress string
	Command  Command
}

type State struct {
	OSID                   string
	OSVersion              string
	PodmanInstalled        bool
	PodmanComposeInstalled bool
	PodmanMajor            int
	PodmanMinor            int
	UserExists             bool
	LingerEnabled          bool
	SocketEnabled          bool
	SocketActive           bool
}

type Plan struct {
	User    string
	Actions []Action
}

type Platform struct {
	DisplayName string
	Mode        string
	ID          string
	Version     string
}

func PlatformFromOSRelease(data []byte) (Platform, error) {
	values, err := ParseOSRelease(data)
	if err != nil {
		return Platform{}, err
	}
	id := values["ID"]
	mode := id
	if id != "ubuntu" && strings.Contains(" "+values["ID_LIKE"]+" ", " ubuntu ") {
		mode = "ubuntu"
	}
	if !SupportedUbuntu(mode, values["VERSION_ID"]) {
		return Platform{}, fmt.Errorf("unsupported operating system %q %q: only Ubuntu-compatible 24.04 and 26.04 hosts are supported", id, values["VERSION_ID"])
	}
	displayName := values["NAME"]
	if displayName == "" {
		displayName = id
	}
	return Platform{DisplayName: displayName + " " + values["VERSION_ID"], Mode: mode, ID: id, Version: values["VERSION_ID"]}, nil
}

func SupportedUbuntu(id, version string) bool {
	return id == "ubuntu" && (version == "24.04" || version == "26.04")
}

func PodmanSupported(major, minor int) bool {
	return major > MinPodmanMajor || major == MinPodmanMajor && minor >= MinPodmanMinor
}

// BuildPlan validates the platform and returns only actions not already
// reflected in state. Calling it with the resulting state is idempotent.
func BuildPlan(state State, user string) (Plan, error) {
	if user == "" {
		user = DefaultUser
	}
	if !validUser(user) {
		return Plan{}, fmt.Errorf("invalid service user %q", user)
	}
	if !SupportedUbuntu(state.OSID, state.OSVersion) {
		return Plan{}, fmt.Errorf("unsupported operating system %q %q: only Ubuntu 24.04 and 26.04 are supported", state.OSID, state.OSVersion)
	}

	plan := Plan{User: user}
	if !state.PodmanInstalled || !state.PodmanComposeInstalled || !PodmanSupported(state.PodmanMajor, state.PodmanMinor) {
		plan.Actions = append(plan.Actions,
			Action{"Updating apt", Command{"apt-get", []string{"update"}}},
			Action{"Installing podman and podman-compose", Command{"apt-get", []string{"install", "-y", "podman", "podman-compose"}}},
		)
	}
	if !state.UserExists {
		plan.Actions = append(plan.Actions, Action{"Creating rootless service user", Command{"useradd", []string{"--create-home", "--user-group", "--shell", "/usr/sbin/nologin", user}}})
	}
	if !state.LingerEnabled {
		plan.Actions = append(plan.Actions, Action{"Enabling persistent user services", Command{"loginctl", []string{"enable-linger", user}}})
	}
	machine := user + "@"
	if !state.SocketEnabled {
		plan.Actions = append(plan.Actions, Action{"Enabling podman sockets", Command{"systemctl", []string{"--user", "--machine", machine, "enable", "podman.socket"}}})
	}
	if !state.SocketActive {
		plan.Actions = append(plan.Actions, Action{"Enabling podman sockets", Command{"systemctl", []string{"--user", "--machine", machine, "start", "podman.socket"}}})
	}
	return plan, nil
}

func validUser(user string) bool {
	if user == "" || len(user) > 32 || user[0] == '-' {
		return false
	}
	for _, r := range user {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// Probe runs a command without a shell. Exit is the process exit status;
// Err is reserved for failures to start or observe the process.
type Probe interface {
	Run(context.Context, string, ...string) (output []byte, exit int, err error)
}

type ExecProbe struct{}

func (ExecProbe) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err == nil {
		return out, 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out, exitErr.ExitCode(), nil
	}
	return out, -1, err
}

// Detect inspects the host without changing it.
func Detect(ctx context.Context, probe Probe, osRelease []byte, user string) (State, error) {
	if user == "" {
		user = DefaultUser
	}
	platform, err := PlatformFromOSRelease(osRelease)
	if err != nil {
		return State{}, err
	}
	state := State{OSID: platform.Mode, OSVersion: platform.Version}
	state.PodmanInstalled, err = packageInstalled(ctx, probe, "podman")
	if err != nil {
		return State{}, err
	}
	state.PodmanComposeInstalled, err = packageInstalled(ctx, probe, "podman-compose")
	if err != nil {
		return State{}, err
	}
	if state.PodmanInstalled {
		out, exit, runErr := probe.Run(ctx, "podman", "--version")
		if runErr != nil {
			return State{}, fmt.Errorf("probe podman version: %w", runErr)
		}
		if exit != 0 {
			return State{}, fmt.Errorf("podman --version exited with status %d: %s", exit, strings.TrimSpace(string(out)))
		}
		state.PodmanMajor, state.PodmanMinor, err = ParsePodmanVersion(string(out))
		if err != nil {
			return State{}, err
		}
	}
	state.UserExists, err = commandSucceeds(ctx, probe, "id", "-u", user)
	if err != nil {
		return State{}, err
	}
	if !state.UserExists {
		return state, nil
	}
	out, exit, runErr := probe.Run(ctx, "loginctl", "show-user", user, "--property=Linger", "--value")
	if runErr != nil {
		return State{}, fmt.Errorf("probe linger: %w", runErr)
	}
	state.LingerEnabled = exit == 0 && strings.TrimSpace(string(out)) == "yes"
	machine := user + "@"
	state.SocketEnabled, err = commandSucceeds(ctx, probe, "systemctl", "--user", "--machine", machine, "is-enabled", "podman.socket")
	if err != nil {
		return State{}, err
	}
	state.SocketActive, err = commandSucceeds(ctx, probe, "systemctl", "--user", "--machine", machine, "is-active", "podman.socket")
	if err != nil {
		return State{}, err
	}
	return state, nil
}

func packageInstalled(ctx context.Context, probe Probe, name string) (bool, error) {
	out, exit, err := probe.Run(ctx, "dpkg-query", "--show", "--showformat=${db:Status-Abbrev}", name)
	if err != nil {
		return false, fmt.Errorf("probe package %s: %w", name, err)
	}
	status := strings.TrimSpace(string(out))
	return exit == 0 && status == "ii", nil
}

func commandSucceeds(ctx context.Context, probe Probe, name string, args ...string) (bool, error) {
	_, exit, err := probe.Run(ctx, name, args...)
	if err != nil {
		return false, fmt.Errorf("probe %s: %w", name, err)
	}
	return exit == 0, nil
}

func ParsePodmanVersion(output string) (int, int, error) {
	fields := strings.Fields(output)
	for _, field := range fields {
		parts := strings.Split(strings.TrimPrefix(field, "v"), ".")
		if len(parts) < 2 {
			continue
		}
		major, e1 := strconv.Atoi(parts[0])
		minor, e2 := strconv.Atoi(parts[1])
		if e1 == nil && e2 == nil {
			return major, minor, nil
		}
	}
	return 0, 0, fmt.Errorf("cannot parse Podman version from %q", strings.TrimSpace(output))
}

func ParseOSRelease(data []byte) (map[string]string, error) {
	values := make(map[string]string)
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		values[strings.TrimSpace(key)] = value
	}
	if values["ID"] == "" || values["VERSION_ID"] == "" {
		return nil, errors.New("/etc/os-release does not contain ID and VERSION_ID")
	}
	return values, nil
}

// Installer is the privileged boundary. It never runs as a side effect of
// detection or planning and refuses execution unless EUID is zero.
type Installer struct {
	Probe    Probe
	EUID     func() int
	Progress func(string)
}

func (i Installer) Apply(ctx context.Context, plan Plan) error {
	if i.EUID == nil {
		i.EUID = os.Geteuid
	}
	if i.Probe == nil {
		i.Probe = ExecProbe{}
	}
	if i.EUID() != 0 {
		return errors.New("agent host setup requires root; rerun the explicit setup command as root")
	}
	lastProgress := ""
	for _, action := range plan.Actions {
		if i.Progress != nil && action.Progress != lastProgress {
			i.Progress(action.Progress)
			lastProgress = action.Progress
		}
		out, exit, err := i.Probe.Run(ctx, action.Command.Name, action.Command.Args...)
		if err != nil {
			return fmt.Errorf("%s: %w", action.Progress, err)
		}
		if exit != 0 {
			return fmt.Errorf("%s: %s exited with status %d: %s", action.Progress, action.Command.Name, exit, strings.TrimSpace(string(out)))
		}
	}
	out, exit, err := i.Probe.Run(ctx, "podman", "--version")
	if err != nil {
		return fmt.Errorf("verify installed Podman: %w", err)
	}
	if exit != 0 {
		return fmt.Errorf("verify installed Podman: exited with status %d: %s", exit, strings.TrimSpace(string(out)))
	}
	major, minor, err := ParsePodmanVersion(string(out))
	if err != nil {
		return fmt.Errorf("verify installed Podman: %w", err)
	}
	if !PodmanSupported(major, minor) {
		return fmt.Errorf("installed Podman %d.%d is unsupported: version 4.7 or later is required", major, minor)
	}
	return nil
}
