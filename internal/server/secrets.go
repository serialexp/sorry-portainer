package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/netip"
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
	// locks is shared with the registry login pusher.
	locks *hostLocks

	mu     sync.Mutex
	status map[string]map[string]Delivery
}

func newSecretPusher(store *secretstore.Store, relay Relay, locks *hostLocks) *secretPusher {
	return &secretPusher{store: store, relay: relay, locks: locks, status: map[string]map[string]Delivery{}}
}

func (p *secretPusher) hostLock(host string) *sync.Mutex { return p.locks.lock(host) }

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

// HostConnected pushes a newly connected host's registry logins, then its
// stack secrets, so a stack a secret push starts already has its logins;
// then it tests logins that no host has tested yet. It is the relay's
// OnConnect callback.
func (s *Server) HostConnected(info protocol.HostInfo) {
	if s.pusher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	s.logins.connected(ctx, info)
	s.pusher.pushHost(ctx, info.HostID)
	s.testUntested(ctx, info.HostID)
}

// HostDisconnected is the relay's OnDisconnect callback.
func (s *Server) HostDisconnected(host string) {
	if s.logins != nil {
		s.logins.disconnected(host)
	}
}

// pushAllHosts runs after an unlock: one round of registry logins to every
// connected host, then each host's stack secrets.
func (s *Server) pushAllHosts() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
		defer cancel()
		s.logins.push(ctx, onlineHosts(s.relay))
		for _, info := range s.relay.Hosts() {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
				defer cancel()
				s.pusher.pushHost(ctx, info.HostID)
				s.testUntested(ctx, info.HostID)
			}()
		}
	}()
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
		var attempt passphraseAttempt
		if action == "unlock" {
			var ok bool
			if attempt, ok = s.takePassphraseAttempt(w, r); !ok {
				return
			}
		}
		var req passphraseRequest
		if jsonDecode(w, r, &req) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if action == "initialize" {
			err = s.secrets.Initialize(req.Passphrase)
		} else {
			err = s.secrets.Unlock(req.Passphrase)
			s.settlePassphraseAttempt(attempt, err)
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

// passphraseAttempt is one attempt taken from the passphrase limiter.
type passphraseAttempt struct {
	client netip.Prefix
	at     time.Time
}

// takePassphraseAttempt spends one passphrase attempt for the request's
// client, or answers 429 and returns false.
func (s *Server) takePassphraseAttempt(w http.ResponseWriter, r *http.Request) (passphraseAttempt, bool) {
	a := passphraseAttempt{client: clientKey(r, s.sessions.opts.TrustedProxies), at: s.sessions.opts.Now()}
	if wait, ok := s.passphrases.take(a.client, a.at); !ok {
		tooManyAttempts(w, wait, "passphrase attempts")
		return a, false
	}
	return a, true
}

// settlePassphraseAttempt refunds the attempt unless the passphrase was
// wrong, so only wrong guesses count against the client. An unreadable body
// never reaches it and stays spent.
func (s *Server) settlePassphraseAttempt(a passphraseAttempt, err error) {
	if !errors.Is(err, secretstore.ErrWrongPassphrase) {
		s.passphrases.refund(a.client, a.at)
	}
}

type revealResponse struct {
	Value string `json:"value"`
}

// revealSecret answers one stored value, after checking the passphrase again.
// The log names the secret and the client, never the value.
func (s *Server) revealSecret(w http.ResponseWriter, r *http.Request, host, stack, name string) {
	attempt, ok := s.takePassphraseAttempt(w, r)
	if !ok {
		return
	}
	var req passphraseRequest
	if jsonDecode(w, r, &req) != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	value, err := s.secrets.Reveal(host, stack, name, req.Passphrase)
	s.settlePassphraseAttempt(attempt, err)
	if errors.Is(err, secretstore.ErrWrongPassphrase) {
		log.Printf("secrets: reveal of %s/%s/%s refused for %s: wrong passphrase", host, stack, name, attempt.client)
	}
	if err != nil {
		http.Error(w, err.Error(), secretStatus(err))
		return
	}
	defer clear(value)
	log.Printf("secrets: revealed %s/%s/%s to %s", host, stack, name, attempt.client)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, revealResponse{Value: string(value)})
}

// stackSecrets serves /api/hosts/{host}/stacks/{stack}/secrets[/{name}[/reveal]].
func (s *Server) stackSecrets(w http.ResponseWriter, r *http.Request, host, stack, name, action string) {
	if s.secrets == nil {
		http.Error(w, "secret store not configured", http.StatusServiceUnavailable)
		return
	}
	switch {
	case action != "":
		if name != "" && action == "reveal" && r.Method == http.MethodPost {
			s.revealSecret(w, r, host, stack, name)
			return
		}
		http.NotFound(w, r)
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
		if jsonDecode(w, r, &req) != nil {
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
