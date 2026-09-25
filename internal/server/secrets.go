package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/relay"
	"github.com/serialexp/sorry-portainer/internal/secretstore"
)

// pushTimeout bounds one host's full push. A sync may restart containers.
const pushTimeout = relay.StackOperationTimeout + time.Minute

// Delivery is the outcome of the last push of one stack's secrets.
type Delivery struct {
	// State is "delivered", "pending" (host offline or master locked, retried
	// on connect or unlock) or "failed".
	State  string                     `json:"state"`
	At     time.Time                  `json:"at"`
	Error  string                     `json:"error,omitempty"`
	Result *protocol.SecretSyncResult `json:"result,omitempty"`
}

// secretPusher sends stack secrets from the store to agents. Pushes to one
// host are serialized and read the store at send time, so the newest value
// always arrives last.
type secretPusher struct {
	store *secretstore.Store
	relay Relay

	mu        sync.Mutex
	hostLocks map[string]*sync.Mutex
	status    map[string]map[string]Delivery
}

func newSecretPusher(store *secretstore.Store, relay Relay) *secretPusher {
	return &secretPusher{store: store, relay: relay, hostLocks: map[string]*sync.Mutex{}, status: map[string]map[string]Delivery{}}
}

func (p *secretPusher) hostLock(host string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.hostLocks[host]
	if l == nil {
		l = &sync.Mutex{}
		p.hostLocks[host] = l
	}
	return l
}

func (p *secretPusher) record(host, stack string, d Delivery) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status[host] == nil {
		p.status[host] = map[string]Delivery{}
	}
	p.status[host][stack] = d
}

func (p *secretPusher) delivery(host, stack string) *Delivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	d, ok := p.status[host][stack]
	if !ok {
		return nil
	}
	return &d
}

// pushStack sends one stack's current secret set to host.
func (p *secretPusher) pushStack(ctx context.Context, host, stack string) Delivery {
	l := p.hostLock(host)
	l.Lock()
	defer l.Unlock()
	return p.pushStackLocked(ctx, host, stack)
}

func (p *secretPusher) pushStackLocked(ctx context.Context, host, stack string) Delivery {
	d := p.sync(ctx, host, stack)
	p.record(host, stack, d)
	return d
}

func (p *secretPusher) sync(ctx context.Context, host, stack string) Delivery {
	now := time.Now().UTC()
	values, err := p.store.Stack(host, stack)
	if errors.Is(err, secretstore.ErrLocked) || errors.Is(err, secretstore.ErrUninitialized) {
		return Delivery{State: "pending", At: now, Error: "the secret store is locked; secrets are sent once it is unlocked"}
	}
	if err != nil {
		return Delivery{State: "failed", At: now, Error: err.Error()}
	}
	defer func() {
		for _, value := range values {
			clear(value)
		}
	}()
	result, err := p.relay.SyncSecrets(ctx, host, protocol.SecretSync{Stack: stack, Secrets: values})
	if errors.Is(err, relay.ErrHostUnavailable) {
		return Delivery{State: "pending", At: now, Error: "the host is offline; secrets are sent when its agent connects"}
	}
	if err != nil {
		return Delivery{State: "failed", At: now, Error: err.Error()}
	}
	if len(result.Problems) > 0 {
		return Delivery{State: "failed", At: now, Error: fmt.Sprintf("stored on the agent, but: %v", result.Problems), Result: &result}
	}
	return Delivery{State: "delivered", At: now, Result: &result}
}

// pushHost sends every stack's secrets to host, then makes the agent forget
// stacks the store no longer has. It does nothing while the store is locked:
// the agent keeps what it had.
func (p *secretPusher) pushHost(ctx context.Context, host string) {
	if p.store.State() != secretstore.Unlocked {
		return
	}
	l := p.hostLock(host)
	l.Lock()
	defer l.Unlock()
	stackNames, err := p.store.Stacks(host)
	if err != nil {
		log.Printf("secrets: list stacks of %s: %v", host, err)
		return
	}
	complete := true
	for _, stack := range stackNames {
		if d := p.pushStackLocked(ctx, host, stack); d.State != "delivered" {
			complete = false
			log.Printf("secrets: push %s/%s: %s: %s", host, stack, d.State, d.Error)
		}
	}
	// Forgetting is only safe after a full, successful push.
	if !complete {
		return
	}
	if err := p.relay.RetainSecrets(ctx, host, stackNames); err != nil {
		log.Printf("secrets: retain on %s: %v", host, err)
	}
}

// HostConnected pushes a newly connected host's secrets. It is the relay's
// OnConnect callback.
func (s *Server) HostConnected(info protocol.HostInfo) {
	if s.pusher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	s.pusher.pushHost(ctx, info.HostID)
}

func (s *Server) pushAllHosts() {
	for _, info := range s.relay.Hosts() {
		go s.HostConnected(info)
	}
}

type passphraseRequest struct {
	Passphrase string `json:"passphrase"`
}

type secretValueRequest struct {
	Value string `json:"value"`
}

type stackSecretsResponse struct {
	State    secretstore.State   `json:"state"`
	Secrets  []secretstore.Entry `json:"secrets"`
	Delivery *Delivery           `json:"delivery"`
}

type secretChangeResponse struct {
	Delivery Delivery `json:"delivery"`
}

// secretStatus maps a store error to an HTTP status.
func secretStatus(err error) int {
	switch {
	case errors.Is(err, secretstore.ErrLocked):
		return http.StatusLocked
	case errors.Is(err, secretstore.ErrUninitialized), errors.Is(err, secretstore.ErrInitialized):
		return http.StatusConflict
	case errors.Is(err, secretstore.ErrWrongPassphrase):
		return http.StatusForbidden
	case errors.Is(err, secretstore.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, secretstore.ErrInvalidName), errors.Is(err, secretstore.ErrValueSize),
		errors.Is(err, secretstore.ErrStackLimit), errors.Is(err, secretstore.ErrWeakPassphrase):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) secretStoreRoute(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		http.Error(w, "secret store not configured", http.StatusServiceUnavailable)
		return
	}
	action := r.URL.Path[len("/api/secrets/"):]
	if action == "status" && r.Method == http.MethodGet {
		writeJSON(w, map[string]secretstore.State{"state": s.secrets.State()})
		return
	}
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var err error
	switch action {
	case "initialize", "unlock":
		var req passphraseRequest
		if jsonDecode(r, &req) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if action == "initialize" {
			err = s.secrets.Initialize(req.Passphrase)
		} else {
			err = s.secrets.Unlock(req.Passphrase)
		}
		if err == nil {
			s.pushAllHosts()
		}
	case "lock":
		s.secrets.Lock()
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), secretStatus(err))
		return
	}
	writeJSON(w, map[string]secretstore.State{"state": s.secrets.State()})
}

// stackSecrets serves /api/hosts/{host}/stacks/{stack}/secrets[/{name}].
func (s *Server) stackSecrets(w http.ResponseWriter, r *http.Request, host, stack, name string) {
	if s.secrets == nil {
		http.Error(w, "secret store not configured", http.StatusServiceUnavailable)
		return
	}
	switch {
	case name == "" && r.Method == http.MethodGet:
		entries, err := s.secrets.List(host, stack)
		if err != nil {
			http.Error(w, err.Error(), secretStatus(err))
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		writeJSON(w, stackSecretsResponse{State: s.secrets.State(), Secrets: entries, Delivery: s.pusher.delivery(host, stack)})
	case name != "" && r.Method == http.MethodPut:
		var req secretValueRequest
		if jsonDecode(r, &req) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := s.secrets.Put(host, stack, name, []byte(req.Value)); err != nil {
			http.Error(w, err.Error(), secretStatus(err))
			return
		}
		writeJSON(w, secretChangeResponse{Delivery: s.pusher.pushStack(r.Context(), host, stack)})
	case name != "" && r.Method == http.MethodDelete:
		// Deleting needs the key only to push the remaining set.
		if s.secrets.State() != secretstore.Unlocked {
			http.Error(w, secretstore.ErrLocked.Error(), http.StatusLocked)
			return
		}
		if err := s.secrets.Delete(host, stack, name); err != nil {
			http.Error(w, err.Error(), secretStatus(err))
			return
		}
		writeJSON(w, secretChangeResponse{Delivery: s.pusher.pushStack(r.Context(), host, stack)})
	default:
		http.NotFound(w, r)
	}
}
