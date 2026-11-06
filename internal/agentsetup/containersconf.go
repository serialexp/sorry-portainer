package agentsetup

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultHooksDirs are Podman's built-in OCI hook directories. Setting
// hooks_dir replaces them, so the drop-in lists them too.
var DefaultHooksDirs = []string{"/usr/share/containers/oci/hooks.d", "/etc/containers/oci/hooks.d"}

// ContainersConfName is the containers.conf drop-in setup writes for the
// agent user.
const ContainersConfName = "50-sorry-portainer.conf"

// Port forwarders for rootless bridge networks (containers.conf
// rootless_port_forwarder).
const (
	ForwarderRootlessport = "rootlessport"
	ForwarderPasta        = "pasta"
)

// MinPastaForwarderMajor is the first Podman with rootless_port_forwarder.
const MinPastaForwarderMajor = 6

// MinHostLoopbackMajor is the first Podman whose rootless networking is
// pasta, so pasta_options apply to it.
const MinHostLoopbackMajor = 5

// pastaForwarderLine is how the drop-in selects pasta. Its presence is how
// an existing drop-in's forwarder is recognised.
const pastaForwarderLine = `rootless_port_forwarder = "pasta"`

// podmanHostAddress is the address Podman maps to the host's external
// address with pasta (--map-guest-addr), host.containers.internal.
var podmanHostAddress = netip.MustParseAddr("169.254.1.2")

// DefaultOCIHooksDir is where the agent writes its hook JSON unless
// configured otherwise: $HOME/.local/share/sorry-portainer/oci-hooks.
func DefaultOCIHooksDir(home string) string {
	return filepath.Join(home, ".local", "share", "sorry-portainer", "oci-hooks")
}

// ContainersConf is the agent user's Podman configuration.
type ContainersConf struct {
	// HooksDir is added to Podman's hook directories. It must come from
	// containers.conf rather than --hooks-dir: Podman's restart-policy
	// cleanup process only reads the file.
	HooksDir string
	// Forwarder is ForwarderRootlessport (Podman's default) or
	// ForwarderPasta, which keeps clients' source addresses on published
	// ports and needs Podman 6.
	Forwarder string
	// HostLoopback, when set, is the address at which the user's containers
	// reach the host's 127.0.0.1 (pasta --map-host-loopback).
	HostLoopback string
}

// ParseHostLoopback validates a --map-host-loopback address and returns
// its canonical form. Addresses that already mean something to the
// container are refused.
func ParseHostLoopback(value string) (string, error) {
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return "", fmt.Errorf("host loopback address %q: %w", value, err)
	}
	if addr.Zone() != "" || addr.IsLoopback() || addr.IsUnspecified() || addr.IsMulticast() {
		return "", fmt.Errorf("host loopback address %q must be a unicast, non-loopback address", value)
	}
	if addr == podmanHostAddress {
		return "", fmt.Errorf("host loopback address %s is Podman's host.containers.internal, the host's external address", addr)
	}
	return addr.String(), nil
}

// Validate reports a configuration Render would refuse.
func (c ContainersConf) Validate() error {
	_, err := c.Render()
	return err
}

// Render returns the drop-in's content.
func (c ContainersConf) Render() (string, error) {
	if !filepath.IsAbs(c.HooksDir) || filepath.Clean(c.HooksDir) != c.HooksDir {
		return "", fmt.Errorf("OCI hooks directory %q must be a clean absolute path", c.HooksDir)
	}
	dirs := append(append([]string(nil), DefaultHooksDirs...), c.HooksDir)
	quoted := make([]string, len(dirs))
	for i, dir := range dirs {
		// strconv.Quote output is a valid TOML basic string for these paths.
		if strings.ContainsAny(dir, "\"\\\n") {
			return "", fmt.Errorf("OCI hooks directory %q contains unsupported characters", dir)
		}
		quoted[i] = strconv.Quote(dir)
	}
	var b strings.Builder
	b.WriteString("# Written by sorry-portainer-agent setup. The agent's OCI hook injects\n" +
		"# stack secrets into containers' /run/secrets tmpfs.\n" +
		"[engine]\nhooks_dir = [" + strings.Join(quoted, ", ") + "]\n")
	var network []string
	switch c.Forwarder {
	case "", ForwarderRootlessport:
	case ForwarderPasta:
		network = append(network,
			"# Published ports see clients' own addresses. Podman requires that no",
			"# container runs while this changes.",
			pastaForwarderLine)
	default:
		return "", fmt.Errorf("unknown port forwarder %q: want %s or %s", c.Forwarder, ForwarderRootlessport, ForwarderPasta)
	}
	if c.HostLoopback != "" {
		addr, err := ParseHostLoopback(c.HostLoopback)
		if err != nil {
			return "", err
		}
		if addr != c.HostLoopback {
			return "", fmt.Errorf("host loopback address %q is not in canonical form (%s)", c.HostLoopback, addr)
		}
		network = append(network,
			"# Containers reach the host's 127.0.0.1 at "+addr+".",
			`pasta_options = ["--map-host-loopback", `+strconv.Quote(addr)+`]`)
	}
	if len(network) > 0 {
		b.WriteString("\n[network]\n" + strings.Join(network, "\n") + "\n")
	}
	return b.String(), nil
}

// ContainersConfPath is the drop-in's path under home.
func ContainersConfPath(home string) string {
	return filepath.Join(home, ".config", "containers", "containers.conf.d", ContainersConfName)
}

// ContainersConfCurrent reports whether home's drop-in already has conf's
// content. Like WriteContainersConf, it runs as the agent user.
func ContainersConfCurrent(home string, conf ContainersConf) (bool, error) {
	content, err := conf.Render()
	if err != nil {
		return false, err
	}
	existing, err := os.ReadFile(ContainersConfPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return string(existing) == content, nil
}

// WriteContainersConf writes the drop-in under home and creates the hooks
// directory. It runs as the agent user, never as root, so the user's own
// files cannot redirect the writes. Podman must not switch port forwarders
// under running containers, so when the forwarder changes running is asked
// first and a running container refuses the write.
func WriteContainersConf(home string, conf ContainersConf, running func() (bool, error)) (string, error) {
	if os.Geteuid() == 0 {
		return "", errors.New("write the containers.conf drop-in as the agent user, not root")
	}
	content, err := conf.Render()
	if err != nil {
		return "", err
	}
	path := ContainersConfPath(home)
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if string(existing) == content {
		return path, os.MkdirAll(conf.HooksDir, 0o700)
	}
	if strings.Contains(string(existing), pastaForwarderLine) != strings.Contains(content, pastaForwarderLine) {
		busy, err := running()
		if err != nil {
			return "", fmt.Errorf("check for running containers before changing the port forwarder: %w", err)
		}
		if busy {
			return "", errors.New("the port forwarder changes, and Podman requires that no container runs while it does: stop this user's containers and rerun setup")
		}
	}
	if err := os.MkdirAll(conf.HooksDir, 0o700); err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".sorry-portainer-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(tmp.Name(), path)
}
