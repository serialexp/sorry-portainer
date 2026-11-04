// Package enrollment is the master's record of which agents may connect:
// the durable host registry, one-time enrollment tokens, and the enroll,
// renew and admission rules that use them.
package enrollment

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/serialexp/sorry-portainer/internal/atomicfile"
	"github.com/serialexp/sorry-portainer/internal/pki"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

const registryVersion = 1

// CertRef identifies one certificate of a host. Certificate holds the PEM
// for a pending certificate only, so a repeated renewal for the same key can
// be answered without signing again.
type CertRef struct {
	SHA256      string    `json:"sha256"`
	NotAfter    time.Time `json:"not_after"`
	Certificate string    `json:"certificate,omitempty"`
}

func refOf(cert *x509.Certificate, keepPEM bool) CertRef {
	ref := CertRef{SHA256: pki.FingerprintOf(cert).String(), NotAfter: cert.NotAfter.UTC()}
	if keepPEM {
		ref.Certificate = string(pki.EncodeCertificate(cert))
	}
	return ref
}

// Host is one enrolled host. Values are never modified once published; a
// change publishes a new registry.
type Host struct {
	EnrolledAt time.Time `json:"enrolled_at"`
	Prefix     string    `json:"prefix"`
	Current    CertRef   `json:"current"`
	Pending    CertRef   `json:"pending,omitzero"`
}

type registryFile struct {
	Version int             `json:"version"`
	Hosts   map[string]Host `json:"hosts"`
}

// ErrDegraded reports that an earlier write may not have reached the disk, so
// the registry refuses further changes until the server restarts.
var ErrDegraded = errors.New("host registry refuses changes after a failed write; restart the server")

// errPublished marks an update error after which the change was published
// anyway, because the file already holds it.
var errPublished = errors.New("change published but not durable")

// Registry is the durable list of enrolled hosts, hosts.json. The running
// server is its only reader and writer. Readers use the published snapshot
// without locking and never wait for the disk; changes are serialized,
// persisted, and only then published.
type Registry struct {
	path      string
	published atomic.Pointer[map[string]Host]

	mu       sync.Mutex
	degraded error
	// write persists the file; tests replace it to inject failures.
	write func(path string, data []byte) error
}

func writeRegistryFile(path string, data []byte) error { return atomicfile.Write(path, data, 0o600) }

// CreateRegistry writes an empty registry at path, refusing to replace one.
func CreateRegistry(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := encodeRegistry(map[string]Host{})
	if err != nil {
		return err
	}
	return writeRegistryFile(path, data)
}

// OpenRegistry loads hosts.json. A missing file is an empty registry; a file
// that cannot be trusted stops the server rather than letting every
// CA-signed certificate in.
func OpenRegistry(path string) (*Registry, error) {
	r := &Registry{path: path, write: writeRegistryFile}
	hosts := map[string]Host{}
	st, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if st.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("%s must not be group/world accessible (mode %04o)", path, st.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if hosts, err = decodeRegistry(data); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	r.published.Store(&hosts)
	return r, nil
}

func decodeRegistry(data []byte) (map[string]Host, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var f registryFile
	if err := decoder.Decode(&f); err != nil {
		return nil, err
	}
	if decoder.Decode(new(json.RawMessage)) != io.EOF {
		return nil, errors.New("trailing data")
	}
	if f.Version != registryVersion {
		return nil, fmt.Errorf("unsupported registry version %d", f.Version)
	}
	if f.Hosts == nil {
		f.Hosts = map[string]Host{}
	}
	for id, host := range f.Hosts {
		if !protocol.ValidHostID(id) {
			return nil, fmt.Errorf("invalid host ID %q", id)
		}
		if _, err := pki.ParseFingerprint(host.Current.SHA256); err != nil {
			return nil, fmt.Errorf("host %s: current certificate: %w", id, err)
		}
		if host.Pending != (CertRef{}) {
			if _, err := pki.ParseFingerprint(host.Pending.SHA256); err != nil {
				return nil, fmt.Errorf("host %s: pending certificate: %w", id, err)
			}
			if _, err := pki.ParseCertificatePEM([]byte(host.Pending.Certificate)); err != nil {
				return nil, fmt.Errorf("host %s: pending certificate: %w", id, err)
			}
		}
	}
	return f.Hosts, nil
}

func encodeRegistry(hosts map[string]Host) ([]byte, error) {
	data, err := json.MarshalIndent(registryFile{Version: registryVersion, Hosts: hosts}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (r *Registry) snapshot() map[string]Host { return *r.published.Load() }

// Get returns one host from the published registry.
func (r *Registry) Get(hostID string) (Host, bool) {
	host, ok := r.snapshot()[hostID]
	return host, ok
}

// HostEntry is a host with its ID, for listings.
type HostEntry struct {
	HostID string
	Host
}

// List returns the published hosts ordered by ID.
func (r *Registry) List() []HostEntry {
	hosts := r.snapshot()
	out := make([]HostEntry, 0, len(hosts))
	for _, id := range slices.Sorted(maps.Keys(hosts)) {
		out = append(out, HostEntry{HostID: id, Host: hosts[id]})
	}
	return out
}

// HostStatus is an enrolled host as administrators see it.
type HostStatus struct {
	HostID          string    `json:"host_id"`
	Prefix          string    `json:"prefix"`
	EnrolledAt      time.Time `json:"enrolled_at"`
	CurrentNotAfter time.Time `json:"current_not_after"`
	// Pending is true while a renewed certificate waits for the agent to
	// connect with it.
	Pending bool `json:"pending"`
	Online  bool `json:"online"`
}

// Statuses lists the enrolled hosts ordered by ID; online says which have a
// session.
func (r *Registry) Statuses(online map[string]bool) []HostStatus {
	entries := r.List()
	out := make([]HostStatus, len(entries))
	for i, e := range entries {
		out[i] = HostStatus{HostID: e.HostID, Prefix: e.Prefix, EnrolledAt: e.EnrolledAt, CurrentNotAfter: e.Current.NotAfter,
			Pending: e.Pending != (CertRef{}), Online: online[e.HostID]}
	}
	return out
}

// update applies change to a copy of the registry, persists it, and then
// publishes it. If change fails, nothing is written. If the write fails
// before the rename, nothing changes. If only the directory sync fails, the
// file holds the new content, so it is published, but the registry turns
// degraded and the caller still gets the error, wrapping errPublished:
// nothing that depends on the change may be handed out before it is durable,
// but what the published registry no longer allows must stop.
func (r *Registry) update(change func(hosts map[string]Host) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.degraded != nil {
		return r.degraded
	}
	next := maps.Clone(r.snapshot())
	if err := change(next); err != nil {
		return err
	}
	data, err := encodeRegistry(next)
	if err != nil {
		return err
	}
	if err := r.write(r.path, data); err != nil {
		if errors.Is(err, atomicfile.ErrDirectorySync) {
			r.published.Store(&next)
			r.degraded = fmt.Errorf("%w (%v)", ErrDegraded, err)
			return fmt.Errorf("%w: %w", errPublished, r.degraded)
		}
		return err
	}
	r.published.Store(&next)
	return nil
}
