package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

var (
	hostIDPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	stackNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	secretNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// ValidHostID reports whether id is a valid host identity. Host IDs appear in
// certificate URIs, relay messages, and master-side directory names.
func ValidHostID(id string) bool { return hostIDPattern.MatchString(id) }

// ValidHostPrefix reports whether p is a valid resource prefix: the start of
// every Podman name an agent manages.
func ValidHostPrefix(p string) bool { return hostIDPattern.MatchString(p) }

// ValidStackName reports whether n is a valid stack name. Stack names become
// directory names and Compose project suffixes.
func ValidStackName(n string) bool { return stackNamePattern.MatchString(n) }

// ValidSecretName reports whether n is a valid stack secret name. Secret names
// become file names on the master and in a container's /run/secrets.
func ValidSecretName(n string) bool { return secretNamePattern.MatchString(n) }

// Secret limits. A stack's whole secret set travels in one relay message, so
// the total is kept well under MaxMessageSize after base64 expansion.
const (
	MaxSecretSize        = 64 << 10
	MaxSecretsPerStack   = 64
	MaxStackSecretsTotal = 1 << 20
)

// Version 4 added stack secrets (secrets.sync, secrets.retain, and the
// secret fields of HostInfo).
const Version = 4

// MaxMessageSize bounds one encoded relay message in either direction. Stack
// operation output is capped at 1 MiB before JSON escaping, and inventories are
// one message per request, so 4 MiB leaves room for escaping without admitting
// unbounded payloads.
const MaxMessageSize = 4 << 20

// MaxInFlight bounds concurrent requests on one agent session. The server
// refuses requests beyond it and the agent refuses to run more than it.
const MaxInFlight = 32

// Message types.
const (
	TypeHello    = "hello"
	TypeRequest  = "request"
	TypeResponse = "response"
	// TypeCancel asks the agent to cancel the in-flight request with the same ID.
	// Cancellation is best effort; the server has already stopped waiting.
	TypeCancel = "cancel"
)

// Error codes carried in Error.Code.
const (
	CodeOperationFailed  = "operation_failed"
	CodeUnknownOperation = "unknown_operation"
	CodeBusy             = "busy"
	CodeResponseTooLarge = "response_too_large"
)

type Message struct {
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Type      string `json:"type"`
	Operation string `json:"operation,omitempty"`
	HostID    string `json:"host_id,omitempty"`
	// TimeoutMS is the request's remaining time budget. It is relative, not an
	// absolute deadline, so host clock skew cannot shorten or extend it.
	TimeoutMS int64           `json:"timeout_ms,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     *Error          `json:"error,omitempty"`
}
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// ErrMessageTooLarge reports an encoded message above MaxMessageSize.
var ErrMessageTooLarge = errors.New("protocol message too large")

func Encode(m Message) ([]byte, error) {
	if m.Version == 0 {
		m.Version = Version
	}
	b, e := json.Marshal(m)
	if e != nil {
		return nil, e
	}
	if len(b) > MaxMessageSize {
		return nil, ErrMessageTooLarge
	}
	return b, nil
}
func Decode(b []byte) (Message, error) {
	if len(b) > MaxMessageSize {
		return Message{}, ErrMessageTooLarge
	}
	var m Message
	if e := json.Unmarshal(b, &m); e != nil {
		return m, fmt.Errorf("decode protocol message: %w", e)
	}
	if m.Version != Version || m.ID == "" || m.Type == "" {
		return m, errors.New("invalid protocol message")
	}
	return m, nil
}

type HostInfo struct {
	HostID        string `json:"host_id"`
	Prefix        string `json:"prefix"`
	Hostname      string `json:"hostname"`
	EngineVersion string `json:"engine_version"`
	// SwapActive warns that tmpfs pages and agent memory, which hold
	// secrets, may be written to a swap device.
	SwapActive bool `json:"swap_active"`
	// SecretsReady is true when the agent's secret hook and socket are in
	// place; SecretsProblem says why not.
	SecretsReady   bool   `json:"secrets_ready"`
	SecretsProblem string `json:"secrets_problem,omitempty"`
}

// SecretSync replaces one stack's secret set in the agent's memory. An empty
// set forgets the stack. Values are never logged.
type SecretSync struct {
	Stack   string            `json:"stack"`
	Secrets map[string][]byte `json:"secrets"`
}

// SecretSyncResult reports what a sync changed on the agent.
type SecretSyncResult struct {
	Stack   string   `json:"stack"`
	Changed []string `json:"changed,omitempty"`
	Removed []string `json:"removed,omitempty"`
	// Restarted lists containers restarted to pick up changed values.
	Restarted []string `json:"restarted,omitempty"`
	// Problems lists restarts or checks that failed; the values were stored.
	Problems []string `json:"problems,omitempty"`
	// Started is true when the stack was waiting for these secrets to come
	// back up after a reboot and was brought up.
	Started bool `json:"started,omitempty"`
}

// SecretRetain makes the agent forget every stack not listed. The master
// sends it after pushing a host's full set.
type SecretRetain struct {
	Stacks []string `json:"stacks"`
}
type Container struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	State  string `json:"state"`
	Status string `json:"status"`
}
type StartRequest struct {
	ContainerID string `json:"container_id"`
}
type StartResult struct {
	ContainerID string `json:"container_id"`
	HostID      string `json:"host_id"`
	Started     bool   `json:"started"`
}
type Stack struct {
	Name        string `json:"name"`
	Project     string `json:"project"`
	ComposeYAML string `json:"compose_yaml,omitempty"`
	Status      string `json:"status"`
	Version     int    `json:"version"`
	// Secrets lists the stack secrets the Compose file uses (Inspect only).
	Secrets []string `json:"secrets,omitempty"`
	// Desired is "up" or "down": the state the agent brings the stack back to
	// after the host reboots. Empty for a stack that was never started.
	Desired string `json:"desired,omitempty"`
	// Waiting says why a stack whose desired state is up has not been brought
	// up since the host booted, for example because its secrets have not
	// arrived.
	Waiting string `json:"waiting,omitempty"`
	// Environment is the active revision's environment (Inspect and saves
	// only). Values are configuration, shown and edited in the dashboard;
	// secrets belong in stack secrets.
	Environment map[string]string `json:"environment,omitempty"`
	// Services lists the Compose services with the state of their containers,
	// in file order. A service with several containers has one entry each.
	Services []StackService `json:"services,omitempty"`
}

// StackService is one container of a stack's service, or the service itself
// when it has none. State is Podman's container state ("running", "exited",
// ...), "absent" for a service with no container, or "unknown" when the agent
// cannot read Podman.
type StackService struct {
	Service   string `json:"service"`
	Container string `json:"container,omitempty"`
	Image     string `json:"image,omitempty"`
	State     string `json:"state"`
	Status    string `json:"status,omitempty"`
}

// StackSaved answers a save: the saved stack and, when the save changed the
// environment of a stack whose desired state is up, the `up` that applied
// it. A failed `up` is reported here, not as an error: the revision exists.
type StackSaved struct {
	Stack
	Operation *StackOperation `json:"operation,omitempty"`
}

type StackVersion struct {
	Version int `json:"version"`
}

// StackRevisionRequest names one revision of a stack to read.
type StackRevisionRequest struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

type StackOperation struct {
	Name      string `json:"name"`
	Operation string `json:"operation"`
	Output    string `json:"output,omitempty"`
	Success   bool   `json:"success"`
}
type StackSave struct {
	Name        string `json:"name"`
	ComposeYAML string `json:"compose_yaml"`
	// Environment is configuration, stored in plain text on the agent host
	// with each revision; use stack secrets for passwords and keys. Absent or
	// null keeps the current environment (none for a new stack); an object,
	// even an empty one, replaces it.
	Environment     map[string]string `json:"environment"`
	ExpectedVersion *int              `json:"expected_version,omitempty"`
}
type Volume struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Mountpoint string `json:"mountpoint"`
	// Mounts lists the containers of this agent's prefix that mount the volume.
	Mounts []VolumeMount `json:"mounts,omitempty"`
}

// VolumeMount is one container's use of a named volume.
type VolumeMount struct {
	ContainerID string `json:"container_id"`
	Container   string `json:"container"`
	Destination string `json:"destination"`
	Mode        string `json:"mode"`
	State       string `json:"state"`
}

// VolumeDetail is a volume with what podman inspect adds to the list entry.
type VolumeDetail struct {
	Volume
	Created string            `json:"created,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

type VolumeRequest struct {
	Name string `json:"name"`
}
type Image struct {
	ID      string   `json:"id"`
	Tags    []string `json:"tags,omitempty"`
	Size    int64    `json:"size"`
	Created int64    `json:"created"`
}
