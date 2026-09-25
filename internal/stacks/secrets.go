package stacks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/serialexp/sorry-portainer/internal/podman"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/secrets"
)

// ContainerRuntime is the Podman access stack secrets need beyond Compose.
type ContainerRuntime interface {
	ProjectContainers(ctx context.Context, project string) ([]podman.ProjectContainer, error)
	Stop(ctx context.Context, id string) error
	Restart(ctx context.Context, id string) error
}

// SecretSupport enables stack secrets on a Manager.
type SecretSupport struct {
	HostID     string
	Vault      *secrets.Vault
	Containers ContainerRuntime
	// Verify checks a running container's injected files; nil uses
	// secrets.Verify against /proc.
	Verify func(pid int, mounts []secrets.Mount) error
	// ReadDigest reads the digest the hook left in a running container; nil
	// uses secrets.ReadDigest against /proc.
	ReadDigest func(pid int) (string, error)
}

// runtimeComposeFile is the file podman-compose runs: compose.yaml after the
// secret rewrite. It holds secret names, never values.
const runtimeComposeFile = "runtime-compose.yaml"

var errSecretsUnsupported = errors.New("this agent was started without secret support")

// EnableSecrets turns on stack secrets. Call it before serving requests.
func (m *Manager) EnableSecrets(support SecretSupport) {
	if support.Verify == nil {
		support.Verify = func(pid int, mounts []secrets.Mount) error { return secrets.Verify("/proc", pid, mounts) }
	}
	if support.ReadDigest == nil {
		support.ReadDigest = func(pid int) (string, error) { return secrets.ReadDigest("/proc", pid) }
	}
	m.secrets = &support
}

// prepareRuntime writes the runtime compose file for stack n and returns its
// secret plan. The caller holds the stack lock.
func (m *Manager) prepareRuntime(n string, needValues bool) (secretPlan, error) {
	source, err := os.ReadFile(filepath.Join(m.dir(n), "compose.yaml"))
	if err != nil {
		return secretPlan{}, err
	}
	doc, err := parseCompose(source)
	if err != nil {
		return secretPlan{}, err
	}
	if !doc.plan.empty() {
		if m.secrets == nil {
			return secretPlan{}, errSecretsUnsupported
		}
		if needValues {
			if missing := m.secrets.Vault.Missing(n, doc.plan.Names); len(missing) > 0 {
				return secretPlan{}, &missingSecretsError{stack: n, names: missing}
			}
		}
	}
	hostID := ""
	if m.secrets != nil {
		hostID = m.secrets.HostID
	}
	runtime, err := doc.runtimeCompose(source, hostID, n)
	if err != nil {
		return secretPlan{}, err
	}
	if err := m.writeAtomic(filepath.Join(m.dir(n), runtimeComposeFile), runtime); err != nil {
		return secretPlan{}, err
	}
	return doc.plan, nil
}

// checkInjected verifies every container of a secret-bearing service after
// up or restart. A container that is not running, or that runs without its
// files (the hook did not run), is stopped and reported.
func (m *Manager) checkInjected(ctx context.Context, n string, services map[string][]secrets.Mount) error {
	containers, err := m.secrets.Containers.ProjectContainers(ctx, m.project(n))
	if err != nil {
		return fmt.Errorf("list stack containers: %w", err)
	}
	var problems []string
	seen := map[string]bool{}
	for _, c := range containers {
		mounts, ok := services[c.Service]
		if !ok {
			continue
		}
		seen[c.Service] = true
		if c.State != "running" || c.Pid <= 0 {
			problems = append(problems, fmt.Sprintf("%s is %s, not running", c.Name, c.State))
			continue
		}
		if err := m.secrets.Verify(c.Pid, mounts); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v; stopped it", c.Name, err))
			if stopErr := m.secrets.Containers.Stop(ctx, c.ID); stopErr != nil {
				problems = append(problems, fmt.Sprintf("%s: stop failed: %v", c.Name, stopErr))
			}
		}
	}
	for service := range services {
		if !seen[service] {
			problems = append(problems, fmt.Sprintf("service %s has no container", service))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("secret check failed: %s", strings.Join(problems, "; "))
	}
	return nil
}

// SyncSecrets stores a stack's secret set from the master, restarts the
// running containers whose injected values are out of date, so the hook
// injects the new ones, and brings up a stack that was waiting for them.
func (m *Manager) SyncSecrets(ctx context.Context, sync protocol.SecretSync) (protocol.SecretSyncResult, error) {
	if m.secrets == nil {
		return protocol.SecretSyncResult{}, errSecretsUnsupported
	}
	if !ValidName(sync.Stack) {
		return protocol.SecretSyncResult{}, errors.New("invalid stack name")
	}
	l := m.lock(sync.Stack)
	l.Lock()
	defer l.Unlock()
	changed, removed, err := m.secrets.Vault.Replace(sync.Stack, sync.Secrets)
	if err != nil {
		return protocol.SecretSyncResult{}, err
	}
	result := protocol.SecretSyncResult{Stack: sync.Stack, Changed: changed, Removed: removed}
	if len(changed) > 0 {
		m.restartOutdated(ctx, sync.Stack, changed, &result)
	}
	m.bringUpIfWaiting(ctx, sync.Stack, &result)
	return result, nil
}

// restartOutdated restarts the running containers of services that use a
// changed secret, unless the digest the hook left in the container already
// matches the vault. That is the case after the agent restarts: its vault
// starts empty, so every pushed value looks changed although the containers
// already hold it. The caller holds the stack lock.
func (m *Manager) restartOutdated(ctx context.Context, n string, changed []string, result *protocol.SecretSyncResult) {
	runtime, err := os.ReadFile(filepath.Join(m.dir(n), runtimeComposeFile))
	if errors.Is(err, os.ErrNotExist) {
		return // never brought up
	}
	if err != nil {
		result.Problems = append(result.Problems, err.Error())
		return
	}
	deployed, err := deployedMounts(runtime)
	if err != nil {
		result.Problems = append(result.Problems, err.Error())
		return
	}
	affected := map[string][]secrets.Mount{}
	for service, mounts := range deployed {
		if usesAny(mounts, changed) {
			affected[service] = mounts
		}
	}
	if len(affected) == 0 {
		return
	}
	containers, err := m.secrets.Containers.ProjectContainers(ctx, m.project(n))
	if err != nil {
		result.Problems = append(result.Problems, fmt.Sprintf("list stack containers: %v", err))
		return
	}
	restarted := map[string][]secrets.Mount{}
	for _, c := range containers {
		mounts, ok := affected[c.Service]
		if !ok || c.State != "running" || m.injectedUpToDate(n, c, mounts) {
			continue
		}
		if err := m.secrets.Containers.Restart(ctx, c.ID); err != nil {
			result.Problems = append(result.Problems, fmt.Sprintf("restart %s: %v", c.Name, err))
			continue
		}
		result.Restarted = append(result.Restarted, c.Name)
		restarted[c.Service] = mounts
	}
	if len(restarted) > 0 {
		if err := m.checkInjected(ctx, n, restarted); err != nil {
			result.Problems = append(result.Problems, err.Error())
		}
	}
}

// injectedUpToDate reports whether running container c holds exactly the
// values the vault has for mounts. Any doubt (no digest, unreadable, a value
// missing from the vault) means no, so the container is restarted.
func (m *Manager) injectedUpToDate(n string, c podman.ProjectContainer, mounts []secrets.Mount) bool {
	if c.Pid <= 0 {
		return false
	}
	want, err := m.secrets.Vault.Digest(n, mounts)
	if err != nil {
		return false
	}
	got, err := m.secrets.ReadDigest(c.Pid)
	return err == nil && got == want
}

// bringUpIfWaiting starts a stack that should be up but has not come up
// since the host booted, once the vault holds all of its secrets. The caller
// holds the stack lock.
func (m *Manager) bringUpIfWaiting(ctx context.Context, n string, result *protocol.SecretSyncResult) {
	state, err := m.readState(n)
	if err != nil {
		result.Problems = append(result.Problems, err.Error())
		return
	}
	if !pending(state, m.currentBoot()) {
		return
	}
	source, err := os.ReadFile(filepath.Join(m.dir(n), "compose.yaml"))
	if err != nil {
		result.Problems = append(result.Problems, err.Error())
		return
	}
	doc, err := parseCompose(source)
	if err != nil {
		result.Problems = append(result.Problems, err.Error())
		return
	}
	if len(m.secrets.Vault.Missing(n, doc.plan.Names)) > 0 {
		return // still waiting; prepareRuntime would say the same
	}
	op, err := m.operateLocked(ctx, n, "up")
	if err != nil {
		result.Problems = append(result.Problems, "start after secrets arrived: "+waitingReason(err))
		m.logf("stack %s did not start after its secrets arrived: %s", n, waitingReason(err))
		return
	}
	result.Started = op.Success
	m.logf("stack %s started after its secrets arrived", n)
}

// RetainSecrets forgets the secrets of every stack not in keep.
func (m *Manager) RetainSecrets(retain protocol.SecretRetain) ([]string, error) {
	if m.secrets == nil {
		return nil, errSecretsUnsupported
	}
	return m.secrets.Vault.Retain(retain.Stacks), nil
}

func usesAny(mounts []secrets.Mount, names []string) bool {
	for _, m := range mounts {
		if slices.Contains(names, m.Source) {
			return true
		}
	}
	return false
}
