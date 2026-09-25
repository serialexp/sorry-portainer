package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// HookTimeoutSeconds bounds one hook run; the runtime kills a slower hook and
// refuses to start the container.
const HookTimeoutSeconds = 15

type hookFile struct {
	Version string   `json:"version"`
	Hook    hookSpec `json:"hook"`
	When    hookWhen `json:"when"`
	Stages  []string `json:"stages"`
}

type hookSpec struct {
	Path    string   `json:"path"`
	Args    []string `json:"args"`
	Timeout int      `json:"timeout"`
}

type hookWhen struct {
	Annotations map[string]string `json:"annotations"`
}

// HookFileName is the hook JSON's name for hostID. One file per host lets
// several agents share a user and a hooks directory, as in development.
func HookFileName(hostID string) string {
	return "sorry-portainer-" + hostID + ".json"
}

// WriteHookConfig writes the OCI hook JSON that makes Podman run
// `<executable> oci-hook` at createRuntime for this agent's containers.
func WriteHookConfig(dir, executable, hostID, socket string) (string, error) {
	if !protocol.ValidHostID(hostID) {
		return "", fmt.Errorf("invalid host ID %q", hostID)
	}
	if !filepath.IsAbs(executable) || !filepath.IsAbs(socket) {
		return "", errors.New("hook executable and socket paths must be absolute")
	}
	config := hookFile{
		Version: "1.0.0",
		Hook: hookSpec{
			Path:    executable,
			Args:    []string{filepath.Base(executable), "oci-hook", "--host-id", hostID, "--socket", socket},
			Timeout: HookTimeoutSeconds,
		},
		When: hookWhen{Annotations: map[string]string{
			"^" + regexp.QuoteMeta(AnnotationAgent) + "$": "^" + regexp.QuoteMeta(hostID) + "$",
		}},
		Stages: []string{"createRuntime"},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, HookFileName(hostID))
	tmp, err := os.CreateTemp(dir, ".hook-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	// Podman watches hooks directories; a rename is seen as one complete file.
	return path, os.Rename(tmp.Name(), path)
}
