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
				return secretPlan{}, fmt.Errorf("secrets %v of stack %s are not available on this agent; set them in sorry-portainer and make sure the master is unlocked", missing, n)
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

// SyncSecrets stores a stack's secret set from the master and restarts the
// running containers that use a changed value, so the hook injects it.
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
	if len(changed) == 0 {
		return result, nil
	}
	runtime, err := os.ReadFile(filepath.Join(m.dir(sync.Stack), runtimeComposeFile))
	if errors.Is(err, os.ErrNotExist) {
		return result, nil // never brought up
	}
	if err != nil {
		result.Problems = append(result.Problems, err.Error())
		return result, nil
	}
	deployed, err := deployedMounts(runtime)
	if err != nil {
		result.Problems = append(result.Problems, err.Error())
		return result, nil
	}
	affected := map[string][]secrets.Mount{}
	for service, mounts := range deployed {
		if usesAny(mounts, changed) {
			affected[service] = mounts
		}
	}
	if len(affected) == 0 {
		return result, nil
	}
	containers, err := m.secrets.Containers.ProjectContainers(ctx, m.project(sync.Stack))
	if err != nil {
		result.Problems = append(result.Problems, fmt.Sprintf("list stack containers: %v", err))
		return result, nil
	}
	restarted := map[string][]secrets.Mount{}
	for _, c := range containers {
		mounts, ok := affected[c.Service]
		if !ok || c.State != "running" {
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
		if err := m.checkInjected(ctx, sync.Stack, restarted); err != nil {
			result.Problems = append(result.Problems, err.Error())
		}
	}
	return result, nil
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
