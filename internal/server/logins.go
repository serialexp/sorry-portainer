package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/relay"
	"github.com/serialexp/sorry-portainer/internal/secretstore"
)

// Registry login delivery. See docs/design/registry-logins.md, "Delivery".
const (
	// loginFanOut bounds how many hosts one round pushes to at a time.
	loginFanOut = 8
	// loginRetryMin and loginRetryMax bound the backoff after a failed push.
	loginRetryMin = 5 * time.Second
	loginRetryMax = 5 * time.Minute
	// loginTestTimeout is how long the master waits for one host's sign-in
	// test before trying the next; the agent's own limit is 30 seconds.
	loginTestTimeout = 35 * time.Second
)

// Host states of the login delivery, as the dashboard shows them.
const (
	LoginsCurrent = "current" // the host has the current set
	LoginsSending = "sending"
	LoginsFailed  = "failed"  // the last push failed; retried with backoff
	LoginsMissing = "missing" // online without the current set (store locked, or not sent yet)
	LoginsOffline = "offline"
)

// hostLocks serializes everything the master pushes to one host: a registry
// push and a secret push to one host never run at the same time.
type hostLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newHostLocks() *hostLocks { return &hostLocks{locks: map[string]*sync.Mutex{}} }

func (h *hostLocks) lock(host string) *sync.Mutex {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.locks[host]
	if l == nil {
		l = &sync.Mutex{}
		h.locks[host] = l
	}
	return l
}

// loginHost is what the master knows about one host's login set.
type loginHost struct {
	// setID is the set the host last reported (hello) or acknowledged.
	setID string
	// receivedAt is when it acknowledged setID; zero when it came from the
	// hello.
	receivedAt time.Time
	sending    bool
	failures   int
	err        string
	retryAt    time.Time
	timer      *time.Timer
	lastSeen   time.Time
}

// loginPusher sends the registry login set to agents.
type loginPusher struct {
	store *secretstore.Store
	relay Relay
	locks *hostLocks
	now   func() time.Time
	// fanOut, retryMin and retryMax are the constants above; tests shrink them.
	fanOut             int
	retryMin, retryMax time.Duration

	mu    sync.Mutex
	hosts map[string]*loginHost
	// round and again coalesce changes: a change during a round makes one
	// more round run after it.
	round, again bool
	// testing holds the keys a connecting host is testing.
	testing map[string]bool
	// rounds counts finished pushes rounds; tests wait on it.
	rounds int
}

func newLoginPusher(store *secretstore.Store, relay Relay, locks *hostLocks) *loginPusher {
	return &loginPusher{store: store, relay: relay, locks: locks, now: time.Now, fanOut: loginFanOut,
		retryMin: loginRetryMin, retryMax: loginRetryMax, hosts: map[string]*loginHost{}, testing: map[string]bool{}}
}

// host returns id's record, creating it. The caller holds mu.
func (p *loginPusher) host(id string) *loginHost {
	h := p.hosts[id]
	if h == nil {
		h = &loginHost{}
		p.hosts[id] = h
	}
	return h
}

// connected records the set a host reports in its hello and sends the
// current one if it differs. It returns once the push is done, so the
// caller can push stack secrets after it.
func (p *loginPusher) connected(ctx context.Context, info protocol.HostInfo) {
	p.mu.Lock()
	h := p.host(info.HostID)
	if h.setID != info.RegistrySetID {
		h.receivedAt = time.Time{}
	}
	h.setID = info.RegistrySetID
	p.mu.Unlock()
	p.push(ctx, []string{info.HostID})
}

// disconnected records when a host was last seen and stops its retries.
// It runs after the session ended; a host that has reconnected since is
// left alone.
func (p *loginPusher) disconnected(host string) {
	if _, err := p.relay.Info(context.Background(), host); err == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.host(host)
	h.lastSeen = p.now().UTC()
	h.sending = false
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
	h.retryAt = time.Time{}
}

// changed starts a background round to every connected host. It returns at
// once; changes while a round runs make one more round run after it.
func (p *loginPusher) changed() {
	p.mu.Lock()
	if p.round {
		p.again = true
		p.mu.Unlock()
		return
	}
	p.round = true
	p.mu.Unlock()
	go func() {
		for {
			p.push(context.Background(), onlineHosts(p.relay))
			p.mu.Lock()
			p.rounds++
			if !p.again {
				p.round = false
				p.mu.Unlock()
				return
			}
			p.again = false
			p.mu.Unlock()
		}
	}()
}

// retryNow pushes to host at once, skipping its backoff.
func (p *loginPusher) retryNow(host string) {
	p.mu.Lock()
	h := p.host(host)
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
	h.retryAt = time.Time{}
	p.mu.Unlock()
	go p.push(context.Background(), []string{host})
}

func onlineHosts(r Relay) []string {
	infos := r.Hosts()
	out := make([]string, len(infos))
	for i, info := range infos {
		out[i] = info.HostID
	}
	return out
}

// push sends the current set to each of hosts that lacks it, at most
// fanOut at a time, decrypting the set once for all of them, and returns
// how many times it decrypted (more than once only when the set changed
// during the round). It does nothing while the store is locked: agents keep
// what they have.
func (p *loginPusher) push(ctx context.Context, hosts []string) int {
	if len(hosts) == 0 || p.store.State() != secretstore.Unlocked {
		return 0
	}
	snapshot := &loginSnapshot{store: p.store}
	defer snapshot.clear()
	work := make(chan string)
	var workers sync.WaitGroup
	for range min(p.fanOut, len(hosts)) {
		workers.Go(func() {
			for host := range work {
				p.pushOne(ctx, host, snapshot)
			}
		})
	}
	for _, host := range hosts {
		work <- host
	}
	close(work)
	workers.Wait()
	return snapshot.decrypts
}

// pushOne sends the set to host under the host's lock, if the host lacks
// the current one. The set ID is read under the lock, so a set older than
// one already sent never arrives after it.
func (p *loginPusher) pushOne(ctx context.Context, host string, snapshot *loginSnapshot) {
	l := p.locks.lock(host)
	l.Lock()
	defer l.Unlock()
	current, err := p.store.RegistrySetID()
	if err != nil {
		p.fail(host, err.Error())
		return
	}
	p.mu.Lock()
	h := p.host(host)
	behind := h.setID != current
	if behind {
		h.sending = true
	}
	p.mu.Unlock()
	if !behind {
		return
	}
	set, err := snapshot.get(current)
	if errors.Is(err, secretstore.ErrLocked) || errors.Is(err, secretstore.ErrUninitialized) {
		p.setSending(host, false)
		return
	}
	if err != nil {
		p.fail(host, err.Error())
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, relay.DefaultRequestTimeout)
	synced, err := p.relay.SyncRegistries(callCtx, host, set)
	cancel()
	switch {
	case errors.Is(err, relay.ErrHostUnavailable):
		// Offline: the push on connect sends it.
		p.setSending(host, false)
	case err != nil:
		p.fail(host, err.Error())
	case synced.SetID != set.SetID:
		p.fail(host, fmt.Sprintf("the host reported set %q instead of %q", synced.SetID, set.SetID))
	default:
		p.mu.Lock()
		h := p.host(host)
		h.setID, h.receivedAt, h.sending = set.SetID, p.now().UTC(), false
		h.failures, h.err, h.retryAt = 0, "", time.Time{}
		if h.timer != nil {
			h.timer.Stop()
			h.timer = nil
		}
		p.mu.Unlock()
	}
}

func (p *loginPusher) setSending(host string, sending bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.host(host).sending = sending
}

// fail records a failed push and schedules a retry: 5 s, doubling to 5 min.
func (p *loginPusher) fail(host, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.host(host)
	h.sending = false
	h.failures++
	h.err = message
	delay := p.retryMax
	if shift := h.failures - 1; shift < 16 {
		delay = min(p.retryMin<<shift, p.retryMax)
	}
	h.retryAt = p.now().UTC().Add(delay)
	if h.timer != nil {
		h.timer.Stop()
	}
	h.timer = time.AfterFunc(delay, func() { p.retry(host) })
	log.Printf("registry logins: push to %s failed (%d in a row), retrying in %s: %s", host, h.failures, delay, message)
}

func (p *loginPusher) retry(host string) {
	p.mu.Lock()
	h := p.host(host)
	h.timer, h.retryAt = nil, time.Time{}
	p.mu.Unlock()
	p.push(context.Background(), []string{host})
}

// hostSetID is the set host last reported or acknowledged.
func (p *loginPusher) hostSetID(host string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if h := p.hosts[host]; h != nil {
		return h.setID
	}
	return ""
}

// LoginHostStatus is one host's row on the Registry logins page.
type LoginHostStatus struct {
	Host     string     `json:"host"`
	Online   bool       `json:"online"`
	SetID    string     `json:"set_id"`
	State    string     `json:"state"`
	At       *time.Time `json:"at,omitempty"`
	Error    string     `json:"error,omitempty"`
	RetryAt  *time.Time `json:"retry_at,omitempty"`
	LastSeen *time.Time `json:"last_seen,omitempty"`
}

func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// statuses describes each host in hosts, given which are online and the
// current set ID.
func (p *loginPusher) statuses(hosts []string, online map[string]bool, current string) []LoginHostStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]LoginHostStatus, 0, len(hosts))
	for _, id := range hosts {
		h := p.hosts[id]
		if h == nil {
			h = &loginHost{}
		}
		row := LoginHostStatus{Host: id, Online: online[id], SetID: h.setID, LastSeen: timeOrNil(h.lastSeen)}
		switch {
		case !online[id]:
			row.State = LoginsOffline
		case h.sending:
			row.State = LoginsSending
		case h.setID == current:
			row.State, row.At = LoginsCurrent, timeOrNil(h.receivedAt)
		case h.err != "":
			row.State, row.Error, row.RetryAt = LoginsFailed, h.err, timeOrNil(h.retryAt)
		default:
			row.State = LoginsMissing
		}
		out = append(out, row)
	}
	return out
}

// loginSnapshot is the set a round sends, decrypted once. A change during
// the round makes a host's current set ID newer than the snapshot's; that
// host gets a freshly read set instead. Every decrypted set is cleared when
// the round ends, not before: other workers may still be sending it.
type loginSnapshot struct {
	store *secretstore.Store
	mu    sync.Mutex
	sets  []protocol.RegistrySync
	// decrypts counts LoginSet calls, for the benchmark.
	decrypts int
}

func (s *loginSnapshot) get(current string) (protocol.RegistrySync, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.sets); n > 0 && s.sets[n-1].SetID == current {
		return s.sets[n-1], nil
	}
	set, err := s.store.LoginSet()
	if err != nil {
		return protocol.RegistrySync{}, err
	}
	s.decrypts++
	s.sets = append(s.sets, set)
	return set, nil
}

func (s *loginSnapshot) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, set := range s.sets {
		secretstore.ClearLoginSet(set)
	}
	s.sets = nil
}
