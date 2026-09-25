// Package secrets is the agent side of stack secrets: the in-memory vault the
// master fills, the Unix socket the OCI hook asks, the hook itself, and the
// checks that injected files really sit in a container's tmpfs.
//
// Secret values never touch the agent's disk. They live in agent memory and
// in the /run/secrets tmpfs of the containers that use them.
package secrets

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// Annotations the compose rewrite puts on secret-bearing services. The prefix
// is reserved: stacks may not set any io.sorry-portainer.* annotation.
const (
	AnnotationPrefix  = "io.sorry-portainer."
	AnnotationAgent   = AnnotationPrefix + "agent"
	AnnotationStack   = AnnotationPrefix + "stack"
	AnnotationSecrets = AnnotationPrefix + "secrets"
)

// Dir is where secrets appear inside a container.
const Dir = "/run/secrets"

// TmpfsOptions is the tmpfs the rewrite mounts on Dir. 2 MiB holds a stack's
// 1 MiB limit plus one page of rounding per file. Mode 0755 lets non-root
// container users reach files whose own mode allows it.
const TmpfsOptions = "rw,noexec,nosuid,nodev,size=2m,mode=0755"

// DefaultMode matches Docker Compose's default for secret files.
const DefaultMode = 0o444

// Mount is one secret file in a container: stack secret Source written to
// Dir/Target with the given owner and mode. UID and GID are container IDs.
type Mount struct {
	Source string `json:"source"`
	Target string `json:"target"`
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
	Mode   uint32 `json:"mode"`
}

// Validate checks one mount.
func (m Mount) Validate() error {
	if !protocol.ValidSecretName(m.Source) {
		return fmt.Errorf("invalid secret name %q", m.Source)
	}
	if !protocol.ValidSecretName(m.Target) {
		return fmt.Errorf("invalid secret target %q", m.Target)
	}
	if m.UID < 0 || m.GID < 0 || m.UID > 1<<31-2 || m.GID > 1<<31-2 {
		return fmt.Errorf("secret %q: invalid owner %d:%d", m.Target, m.UID, m.GID)
	}
	if m.Mode > 0o777 {
		return fmt.Errorf("secret %q: invalid mode %04o", m.Target, m.Mode)
	}
	return nil
}

// EncodeMounts renders mounts as the io.sorry-portainer.secrets annotation:
// "source:target:uid:gid:mode" entries joined by ";", mode in octal.
//
// The format avoids commas and quotes on purpose. podman-compose passes the
// value to `podman run --annotation`, which parses its argument as CSV, so
// JSON there fails with "bare \" in non-quoted-field". Secret names cannot
// contain ":" or ";".
func EncodeMounts(mounts []Mount) (string, error) {
	if err := validateMounts(mounts); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, m := range mounts {
		if i > 0 {
			b.WriteByte(';')
		}
		fmt.Fprintf(&b, "%s:%s:%d:%d:%04o", m.Source, m.Target, m.UID, m.GID, m.Mode)
	}
	return b.String(), nil
}

// DecodeMounts parses and validates the io.sorry-portainer.secrets annotation.
func DecodeMounts(value string) ([]Mount, error) {
	if len(value) > 64<<10 {
		return nil, errors.New("secret annotation too large")
	}
	entries := strings.Split(value, ";")
	if len(entries) > protocol.MaxSecretsPerStack {
		return nil, fmt.Errorf("at most %d secrets per service", protocol.MaxSecretsPerStack)
	}
	mounts := make([]Mount, 0, len(entries))
	for _, entry := range entries {
		fields := strings.Split(entry, ":")
		if len(fields) != 5 {
			return nil, fmt.Errorf("decode secret annotation: malformed entry %q", entry)
		}
		uid, uidErr := strconv.ParseUint(fields[2], 10, 31)
		gid, gidErr := strconv.ParseUint(fields[3], 10, 31)
		mode, modeErr := strconv.ParseUint(fields[4], 8, 32)
		if err := errors.Join(uidErr, gidErr, modeErr); err != nil {
			return nil, fmt.Errorf("decode secret annotation: entry %q: %w", entry, err)
		}
		mounts = append(mounts, Mount{Source: fields[0], Target: fields[1], UID: int(uid), GID: int(gid), Mode: uint32(mode)})
	}
	if err := validateMounts(mounts); err != nil {
		return nil, err
	}
	return mounts, nil
}

func validateMounts(mounts []Mount) error {
	if len(mounts) == 0 {
		return errors.New("no secret mounts")
	}
	if len(mounts) > protocol.MaxSecretsPerStack {
		return fmt.Errorf("at most %d secrets per service", protocol.MaxSecretsPerStack)
	}
	targets := make(map[string]bool, len(mounts))
	for _, m := range mounts {
		if err := m.Validate(); err != nil {
			return err
		}
		if targets[m.Target] {
			return fmt.Errorf("secret target %q used twice", m.Target)
		}
		targets[m.Target] = true
	}
	return nil
}

// Sources returns the distinct secret names mounts use, sorted.
func Sources(mounts []Mount) []string {
	seen := make(map[string]bool, len(mounts))
	out := make([]string, 0, len(mounts))
	for _, m := range mounts {
		if !seen[m.Source] {
			seen[m.Source] = true
			out = append(out, m.Source)
		}
	}
	sort.Strings(out)
	return out
}
