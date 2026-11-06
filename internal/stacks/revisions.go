package stacks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// ErrRevisionNotFound means the stack has no revision with the requested number.
var ErrRevisionNotFound = errors.New("revision not found")

// Revision returns revision version of a stack: its Compose document and its
// environment. Reading is side-effect free; restoring a revision saves its
// contents as a new revision.
func (m *Manager) Revision(ctx context.Context, n string, version int) (protocol.Stack, error) {
	if !ValidName(n) {
		return protocol.Stack{}, errors.New("invalid stack name")
	}
	l := m.lock(n)
	l.Lock()
	defer l.Unlock()
	known, err := m.versions(n)
	if err != nil {
		return protocol.Stack{}, err
	}
	if !slices.ContainsFunc(known, func(v protocol.StackVersion) bool { return v.Version == version }) {
		return protocol.Stack{}, fmt.Errorf("%w: version %d", ErrRevisionNotFound, version)
	}
	compose, err := m.revisionCompose(n, version)
	if err != nil {
		return protocol.Stack{}, err
	}
	env, err := m.environment(n, version)
	if err != nil {
		return protocol.Stack{}, err
	}
	return protocol.Stack{Name: n, Project: m.project(n), ComposeYAML: string(compose), Version: version, Environment: env}, nil
}

// revisionCompose reads revision version's Compose document. A stack saved
// before revisions existed keeps its only document in compose.yaml as a
// regular file, which is then revision one.
func (m *Manager) revisionCompose(n string, version int) ([]byte, error) {
	path := filepath.Join(m.dir(n), "revisions", fmt.Sprintf("%d.yaml", version))
	b, err := os.ReadFile(path)
	if err == nil {
		return b, nil
	}
	if !errors.Is(err, os.ErrNotExist) || version != 1 {
		return nil, err
	}
	info, statErr := os.Lstat(filepath.Join(m.dir(n), "compose.yaml"))
	if statErr != nil {
		return nil, statErr
	}
	if !info.Mode().IsRegular() {
		return nil, err
	}
	return os.ReadFile(filepath.Join(m.dir(n), "compose.yaml"))
}
