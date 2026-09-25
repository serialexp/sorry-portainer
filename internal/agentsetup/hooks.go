package agentsetup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultHooksDirs are Podman's built-in OCI hook directories. Setting
// hooks_dir replaces them, so the drop-in lists them too.
var DefaultHooksDirs = []string{"/usr/share/containers/oci/hooks.d", "/etc/containers/oci/hooks.d"}

// HooksDropInName is the containers.conf drop-in setup writes for the agent
// user.
const HooksDropInName = "50-sorry-portainer.conf"

// DefaultOCIHooksDir is where the agent writes its hook JSON unless
// configured otherwise: $HOME/.local/share/sorry-portainer/oci-hooks.
func DefaultOCIHooksDir(home string) string {
	return filepath.Join(home, ".local", "share", "sorry-portainer", "oci-hooks")
}

// HooksDropIn renders a containers.conf drop-in that adds hooksDir to
// Podman's hook directories. It must come from containers.conf rather than
// --hooks-dir: Podman's restart-policy cleanup process only reads the file.
func HooksDropIn(hooksDir string) (string, error) {
	if !filepath.IsAbs(hooksDir) || filepath.Clean(hooksDir) != hooksDir {
		return "", fmt.Errorf("OCI hooks directory %q must be a clean absolute path", hooksDir)
	}
	dirs := append(append([]string(nil), DefaultHooksDirs...), hooksDir)
	quoted := make([]string, len(dirs))
	for i, dir := range dirs {
		// strconv.Quote output is a valid TOML basic string for these paths.
		if strings.ContainsAny(dir, "\"\\\n") {
			return "", fmt.Errorf("OCI hooks directory %q contains unsupported characters", dir)
		}
		quoted[i] = strconv.Quote(dir)
	}
	return "# Written by sorry-portainer-agent setup. The agent's OCI hook injects\n" +
		"# stack secrets into containers' /run/secrets tmpfs.\n" +
		"[engine]\nhooks_dir = [" + strings.Join(quoted, ", ") + "]\n", nil
}

// WriteHooksDropIn writes the drop-in under home and creates hooksDir. It
// runs as the agent user, never as root, so the user's own files cannot
// redirect the writes.
func WriteHooksDropIn(home, hooksDir string) (string, error) {
	if os.Geteuid() == 0 {
		return "", errors.New("write the hooks drop-in as the agent user, not root")
	}
	content, err := HooksDropIn(hooksDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "containers", "containers.conf.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, HooksDropInName)
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
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(tmp.Name(), path)
}
