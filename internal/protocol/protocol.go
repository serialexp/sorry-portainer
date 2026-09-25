package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
)

const Version = 2
const MaxMessageSize = 1 << 20

type Message struct {
	Version   int             `json:"version"`
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Operation string          `json:"operation,omitempty"`
	HostID    string          `json:"host_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     *Error          `json:"error,omitempty"`
}
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func Encode(m Message) ([]byte, error) {
	if m.Version == 0 {
		m.Version = Version
	}
	b, e := json.Marshal(m)
	if e != nil {
		return nil, e
	}
	if len(b) > MaxMessageSize {
		return nil, errors.New("protocol message too large")
	}
	return b, nil
}
func Decode(b []byte) (Message, error) {
	if len(b) > MaxMessageSize {
		return Message{}, errors.New("protocol message too large")
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
}

type StackVersion struct {
	Version int `json:"version"`
}

type StackOperation struct {
	Name      string `json:"name"`
	Operation string `json:"operation"`
	Output    string `json:"output,omitempty"`
	Success   bool   `json:"success"`
}
type StackSave struct {
	Name            string            `json:"name"`
	ComposeYAML     string            `json:"compose_yaml"`
	Environment     map[string]string `json:"environment,omitempty"`
	ExpectedVersion *int              `json:"expected_version,omitempty"`
}
type Volume struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Mountpoint string `json:"mountpoint"`
}
type Image struct {
	ID      string   `json:"id"`
	Tags    []string `json:"tags,omitempty"`
	Size    int64    `json:"size"`
	Created int64    `json:"created"`
}
