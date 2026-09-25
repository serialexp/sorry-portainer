package secrets

import (
	"bytes"
	"fmt"
	"sort"
	"sync"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// Vault holds the secret values the master pushed, in memory only. It is kept
// across relay disconnects so containers can restart while the master is
// unreachable or locked.
type Vault struct {
	mu     sync.RWMutex
	stacks map[string]map[string][]byte
}

func NewVault() *Vault {
	return &Vault{stacks: map[string]map[string][]byte{}}
}

// Replace sets a stack's whole secret set and returns the names whose value
// is new or different. Names missing from values are forgotten. Replace takes
// ownership of the value slices; old values are zeroed.
func (v *Vault) Replace(stack string, values map[string][]byte) (changed, removed []string, err error) {
	if !protocol.ValidStackName(stack) {
		return nil, nil, fmt.Errorf("invalid stack name %q", stack)
	}
	if len(values) > protocol.MaxSecretsPerStack {
		return nil, nil, fmt.Errorf("stack %s has more than %d secrets", stack, protocol.MaxSecretsPerStack)
	}
	total := 0
	for name, value := range values {
		if !protocol.ValidSecretName(name) {
			return nil, nil, fmt.Errorf("invalid secret name %q", name)
		}
		if len(value) == 0 || len(value) > protocol.MaxSecretSize {
			return nil, nil, fmt.Errorf("secret %s has an invalid size", name)
		}
		total += len(value)
	}
	if total > protocol.MaxStackSecretsTotal {
		return nil, nil, fmt.Errorf("stack %s secrets exceed %d bytes", stack, protocol.MaxStackSecretsTotal)
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	old := v.stacks[stack]
	for name, value := range values {
		if previous, ok := old[name]; !ok || !bytes.Equal(previous, value) {
			changed = append(changed, name)
		}
	}
	for name, value := range old {
		if _, ok := values[name]; !ok {
			removed = append(removed, name)
		}
		clear(value)
	}
	if len(values) == 0 {
		delete(v.stacks, stack)
	} else {
		v.stacks[stack] = values
	}
	sort.Strings(changed)
	sort.Strings(removed)
	return changed, removed, nil
}

// Retain forgets every stack not in keep and returns the forgotten stacks.
func (v *Vault) Retain(keep []string) []string {
	wanted := make(map[string]bool, len(keep))
	for _, stack := range keep {
		wanted[stack] = true
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var forgotten []string
	for stack, values := range v.stacks {
		if wanted[stack] {
			continue
		}
		for _, value := range values {
			clear(value)
		}
		delete(v.stacks, stack)
		forgotten = append(forgotten, stack)
	}
	sort.Strings(forgotten)
	return forgotten
}

// Missing returns the names of stack that the vault does not hold.
func (v *Vault) Missing(stack string, names []string) []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	values := v.stacks[stack]
	var missing []string
	for _, name := range names {
		if _, ok := values[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

// Copy returns copies of the named values, or an error naming the missing
// ones. Callers should clear the copies when done.
func (v *Vault) Copy(stack string, names []string) (map[string][]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	values := v.stacks[stack]
	out := make(map[string][]byte, len(names))
	var missing []string
	for _, name := range names {
		value, ok := values[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		out[name] = bytes.Clone(value)
	}
	if len(missing) > 0 {
		for _, value := range out {
			clear(value)
		}
		return nil, fmt.Errorf("stack %s secrets %v have not been delivered to this agent (is the master unlocked and the secret set?)", stack, missing)
	}
	return out, nil
}

// Digest returns what the hook would record for mounts filled from stack's
// values (see Digest), without copying the values.
func (v *Vault) Digest(stack string, mounts []Mount) (string, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return Digest(mounts, v.stacks[stack])
}

// Names lists the stored names of stack, sorted.
func (v *Vault) Names(stack string) []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]string, 0, len(v.stacks[stack]))
	for name := range v.stacks[stack] {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
