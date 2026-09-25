package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// stackState is what the agent remembers about a stack across its own
// restarts and host reboots. It holds no secrets.
type stackState struct {
	// Desired is "up" after Up and "down" after Down.
	Desired string `json:"desired"`
	// UpBoot is the host boot ID when the stack last came up successfully.
	UpBoot string `json:"up_boot,omitempty"`
}

const (
	stateFile    = "state.json"
	desiredUp    = "up"
	desiredDown  = "down"
	maxStateSize = 4 << 10
)

// missingSecretsError means a stack cannot start yet because the master has
// not delivered all of its secrets.
type missingSecretsError struct {
	stack string
	names []string
}

func (e *missingSecretsError) Error() string {
	return fmt.Sprintf("secrets %v of stack %s are not available on this agent; set them in sorry-portainer and make sure the master is unlocked", e.names, e.stack)
}

// SetBootID records the host's boot ID (/proc/sys/kernel/random/boot_id).
// Call it before serving requests. Without it no stack counts as waiting and
// Resume does nothing.
func (m *Manager) SetBootID(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bootID = id
}

func (m *Manager) currentBoot() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bootID
}

// readState requires the stack lock. A stack without a state file was never
// started through this agent version and has the zero state.
func (m *Manager) readState(n string) (stackState, error) {
	data, err := os.ReadFile(filepath.Join(m.dir(n), stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return stackState{}, nil
	}
	if err != nil {
		return stackState{}, err
	}
	if len(data) > maxStateSize {
		return stackState{}, fmt.Errorf("stack %s: %s is too large", n, stateFile)
	}
	var s stackState
	if err := json.Unmarshal(data, &s); err != nil {
		return stackState{}, fmt.Errorf("stack %s: %s: %w", n, stateFile, err)
	}
	return s, nil
}

// writeState requires the stack lock.
func (m *Manager) writeState(n string, s stackState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return m.writeAtomic(filepath.Join(m.dir(n), stateFile), data)
}

// pending reports whether the stack should be up but has not come up since
// the host booted.
func pending(s stackState, boot string) bool {
	return boot != "" && s.Desired == desiredUp && s.UpBoot != boot
}

func (m *Manager) setWaiting(n, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if reason == "" {
		delete(m.waiting, n)
	} else {
		m.waiting[n] = reason
	}
}

// describeState fills a stack's Desired and Waiting fields. It requires the
// stack lock.
func (m *Manager) describeState(stack *protocol.Stack) error {
	s, err := m.readState(stack.Name)
	if err != nil {
		return err
	}
	stack.Desired = s.Desired
	if !pending(s, m.currentBoot()) {
		return nil
	}
	m.mu.Lock()
	stack.Waiting = m.waiting[stack.Name]
	m.mu.Unlock()
	if stack.Waiting == "" {
		stack.Waiting = "waiting to start after the agent started"
	}
	return nil
}

// waitingReason turns a failed bring-up into the short text the UI shows.
func waitingReason(err error) string {
	if missing, ok := errors.AsType[*missingSecretsError](err); ok {
		return fmt.Sprintf("waiting for secrets %s from the master (is it unlocked?)", strings.Join(missing.names, ", "))
	}
	first, _, _ := strings.Cut(err.Error(), "\n")
	return "start failed: " + first
}

// Resume brings up, one at a time, every stack whose desired state is up and
// that has not come up since the host booted: after a reboot all of them,
// after an agent restart only those still waiting. A stack whose secrets have
// not arrived keeps waiting; SyncSecrets starts it once they do.
func (m *Manager) Resume(ctx context.Context) {
	boot := m.currentBoot()
	if boot == "" {
		return
	}
	names, err := m.stackNames()
	if err != nil {
		m.logf("resume stacks: %v", err)
		return
	}
	var queue []string
	for _, n := range names {
		l := m.lock(n)
		l.Lock()
		s, err := m.readState(n)
		l.Unlock()
		if err != nil {
			m.logf("resume stack %s: %v", n, err)
			continue
		}
		if pending(s, boot) {
			queue = append(queue, n)
		}
	}
	for _, n := range queue {
		if ctx.Err() != nil {
			return
		}
		m.resumeOne(ctx, n)
	}
}

func (m *Manager) resumeOne(ctx context.Context, n string) {
	l := m.lock(n)
	l.Lock()
	defer l.Unlock()
	// Down, or another bring-up, may have happened while this one queued.
	s, err := m.readState(n)
	if err != nil || !pending(s, m.currentBoot()) {
		return
	}
	if _, err := m.operateLocked(ctx, n, "up"); err != nil {
		m.logf("stack %s did not start after the agent started: %s", n, waitingReason(err))
		return
	}
	m.logf("stack %s started after the agent started", n)
}

// stackNames lists the saved stacks.
func (m *Manager) stackNames() ([]string, error) {
	entries, err := os.ReadDir(m.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && ValidName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func (m *Manager) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}
