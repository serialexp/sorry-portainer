package stacks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/registries"
)

// RegistryLogins is what stacks need from the agent's registry logins;
// registryvault.Vault is the implementation.
type RegistryLogins interface {
	// LoginFor is the key of the login Podman uses for ref, or "".
	LoginFor(ref string) string
	// Scrubber removes the held passwords from text.
	Scrubber() *registries.Scrubber
}

// EnableRegistryLogins makes image checks report the login each image uses
// and scrubs the logins' passwords from Podman's output. Call it before
// serving requests. Call LoginsChanged whenever the logins change.
func (m *Manager) EnableRegistryLogins(logins RegistryLogins) {
	m.logins = logins
}

// missingLoginError means a registry refused to pull a stack's image, so
// the stack waits for a registry login, as missingSecretsError waits for
// secrets.
type missingLoginError struct {
	// repository is the image's repository as registries.Repository names it.
	repository string
	// pull is the failed pull, its Podman text scrubbed.
	pull error
}

func (e *missingLoginError) Error() string {
	return fmt.Sprintf("%s refused to pull %s: %v", registries.RegistryOf(e.repository), e.repository, e.pull)
}

func (e *missingLoginError) Unwrap() error { return e.pull }

// scrub removes the held registry passwords from Podman's output.
func (m *Manager) scrub(text string) string {
	if m.logins == nil {
		return text
	}
	return m.logins.Scrubber().Scrub(text)
}

// pullError is a failed pull of ref with Podman's text scrubbed.
func (m *Manager) pullError(ref string, err error) error {
	return fmt.Errorf("pull %s: %s", ref, m.scrub(err.Error()))
}

// bringUpPullError is pullError for a bring-up: a missingLoginError when the
// registry refused the pull, so the stack waits for a login.
func (m *Manager) bringUpPullError(ref string, err error) error {
	failure := m.pullError(ref, err)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || !registries.Refused(failure.Error()) {
		return failure
	}
	repository, repoErr := registries.Repository(ref)
	if repoErr != nil {
		return failure
	}
	return &missingLoginError{repository: repository, pull: failure}
}

// pullMissing pulls, one at a time, each image of the stack Podman does not
// have, and returns what it did. It does nothing without image checks. The
// caller holds the stack lock.
func (m *Manager) pullMissing(ctx context.Context, n string) (string, error) {
	if m.images == nil {
		return "", nil
	}
	services, err := m.bringUpImages(ctx, n)
	if err != nil {
		return "", err
	}
	var output strings.Builder
	seen := make(map[string]bool, len(services))
	for _, s := range services {
		if seen[s.ref] || (s.status != "" && s.status != protocol.ImagePinned) || len(s.containers) > 0 {
			continue
		}
		seen[s.ref] = true
		exists, err := m.images.ImageExists(ctx, s.ref)
		if err != nil {
			return output.String(), fmt.Errorf("look for image %s: %s", s.ref, m.scrub(err.Error()))
		}
		if exists {
			continue
		}
		fmt.Fprintf(&output, "$ podman pull %s\n", s.ref)
		pullCtx, cancel := context.WithTimeout(ctx, pullTimeout)
		id, err := m.images.Pull(pullCtx, s.ref)
		cancel()
		if m.checker != nil {
			m.checker.Forget(s.ref)
		}
		if err != nil {
			return output.String(), m.bringUpPullError(s.ref, err)
		}
		output.WriteString(id + "\n")
	}
	return output.String(), nil
}

// bringUpImages resolves the images a bring-up needs: the containers'
// images when runtime status is on, else the Compose file's.
func (m *Manager) bringUpImages(ctx context.Context, n string) ([]serviceImage, error) {
	source, err := os.ReadFile(filepath.Join(m.dir(n), "compose.yaml"))
	if err != nil {
		return nil, err
	}
	declared, err := declaredServices(source)
	if err != nil {
		return nil, fmt.Errorf("the Compose file does not parse: %w", err)
	}
	containers, _, err := m.containersByProject(ctx)
	if err != nil {
		return nil, err
	}
	return resolveImages(declared, containers[m.project(n)]), nil
}

// LoginsChanged starts, in the background and one at a time, each stack
// that waits for a registry login. It returns at once. A call while a round
// runs makes one more round run after it, so no change is missed and no two
// rounds overlap.
func (m *Manager) LoginsChanged(ctx context.Context) {
	m.mu.Lock()
	if m.loginRound {
		m.loginAgain = true
		m.mu.Unlock()
		return
	}
	m.loginRound = true
	m.mu.Unlock()
	go func() {
		for {
			m.startWaitingForLogins(ctx)
			m.mu.Lock()
			if !m.loginAgain || ctx.Err() != nil {
				m.loginRound, m.loginAgain = false, false
				m.mu.Unlock()
				return
			}
			m.loginAgain = false
			m.mu.Unlock()
		}
	}()
}

// startWaitingForLogins tries once to bring up each stack waiting for a
// registry login.
func (m *Manager) startWaitingForLogins(ctx context.Context) {
	m.mu.Lock()
	var queue []string
	for n, w := range m.waiting {
		if w.login != "" {
			queue = append(queue, n)
		}
	}
	m.mu.Unlock()
	slices.Sort(queue)
	for _, n := range queue {
		if ctx.Err() != nil {
			return
		}
		m.resumeOne(ctx, n, "its registry login arrived")
	}
}
