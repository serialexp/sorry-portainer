package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/relay"
	"github.com/serialexp/sorry-portainer/internal/secretstore"
)

// loginRelay records registry pushes and tests, and the order of every
// push to each host.
type loginRelay struct {
	secretRelay
	lmu sync.Mutex
	// received are the set IDs each host got, in order.
	received map[string][]string
	// events are "host:registries" and "host:secrets", in order.
	events []string
	// failSync fails that many syncs to a host before working.
	failSync map[string]int
	// gate, when set, holds every sync until it is closed.
	gate chan struct{}
	// testErr makes a host's sign-in test fail to answer; testAnswer is
	// the answer of the others.
	testErr    map[string]error
	testAnswer protocol.RegistryTestResult
	tested     []string
	online     map[string]bool
}

func newLoginRelay(hosts ...string) *loginRelay {
	r := &loginRelay{received: map[string][]string{}, failSync: map[string]int{}, testErr: map[string]error{},
		testAnswer: protocol.RegistryTestResult{OK: true}, online: map[string]bool{}}
	r.offline = map[string]bool{}
	for _, h := range hosts {
		r.hosts = append(r.hosts, protocol.HostInfo{HostID: h})
		r.online[h] = true
	}
	return r
}

func (r *loginRelay) Hosts() []protocol.HostInfo {
	r.lmu.Lock()
	defer r.lmu.Unlock()
	var out []protocol.HostInfo
	for _, h := range r.hosts {
		if r.online[h.HostID] {
			out = append(out, h)
		}
	}
	return out
}

func (r *loginRelay) Info(_ context.Context, host string) (protocol.HostInfo, error) {
	r.lmu.Lock()
	defer r.lmu.Unlock()
	if !r.online[host] {
		return protocol.HostInfo{}, relay.ErrHostUnavailable
	}
	return protocol.HostInfo{HostID: host}, nil
}

func (r *loginRelay) SyncSecrets(ctx context.Context, host string, sync protocol.SecretSync) (protocol.SecretSyncResult, error) {
	r.lmu.Lock()
	r.events = append(r.events, host+":secrets")
	r.lmu.Unlock()
	return r.secretRelay.SyncSecrets(ctx, host, sync)
}

func (r *loginRelay) SyncRegistries(_ context.Context, host string, set protocol.EncodedRegistrySync) (protocol.RegistrySynced, error) {
	if r.gate != nil {
		<-r.gate
	}
	r.lmu.Lock()
	defer r.lmu.Unlock()
	if !r.online[host] {
		return protocol.RegistrySynced{}, relay.ErrHostUnavailable
	}
	r.events = append(r.events, host+":registries")
	if r.failSync[host] > 0 {
		r.failSync[host]--
		return protocol.RegistrySynced{}, errors.New("agent: write auth file: no space left on device")
	}
	r.received[host] = append(r.received[host], set.SetID)
	return protocol.RegistrySynced{SetID: set.SetID}, nil
}

func (r *loginRelay) TestRegistry(_ context.Context, host string, login protocol.RegistryLogin) (protocol.RegistryTestResult, error) {
	r.lmu.Lock()
	defer r.lmu.Unlock()
	r.tested = append(r.tested, host+":"+login.Key+":"+string(login.Password))
	if err := r.testErr[host]; err != nil {
		return protocol.RegistryTestResult{}, err
	}
	return r.testAnswer, nil
}

func (r *loginRelay) receivedBy(host string) []string {
	r.lmu.Lock()
	defer r.lmu.Unlock()
	return slices.Clone(r.received[host])
}

func (r *loginRelay) eventsSnapshot() []string {
	r.lmu.Lock()
	defer r.lmu.Unlock()
	return slices.Clone(r.events)
}

func (r *loginRelay) testedSnapshot() []string {
	r.lmu.Lock()
	defer r.lmu.Unlock()
	return slices.Clone(r.tested)
}

func loginServer(t testing.TB, r *loginRelay) (*Server, *secretstore.Store) {
	t.Helper()
	store, err := secretstore.OpenWithKDF(filepath.Join(t.TempDir(), "secrets"), cheapKDF)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Initialize("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	s := New(testSessions, r, store)
	s.logins.retryMin, s.logins.retryMax = 5*time.Millisecond, 20*time.Millisecond
	return s, store
}

// waitForRounds waits until no change round runs.
func waitForRounds(t testing.TB, p *loginPusher) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		p.mu.Lock()
		running := p.round
		p.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the push round did not end")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRegistryLoginsOverHTTP(t *testing.T) {
	r := newLoginRelay("h1", "h2")
	r.testAnswer = protocol.RegistryTestResult{Failure: protocol.RegistryTestDenied, Message: "ghcr.io refused the login"}
	s, store := loginServer(t, r)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		return authenticatedStackRequest(t, s, method, path, body)
	}

	w := call(http.MethodPost, "/api/registries", `{"key":"GHCR.io/myorg","username":"bot","password":"tok3n"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("add %d %s", w.Code, w.Body.String())
	}
	saved := decodeBody[loginSaved](t, w)
	if saved.Login.Key != "ghcr.io/myorg" || saved.Test.State != TestFailed || saved.Test.Host != "h1" || saved.Test.Failure != protocol.RegistryTestDenied {
		t.Fatalf("saved %+v", saved)
	}
	if saved.Login.LastTest == nil || saved.Login.LastTest.Host != "h1" || saved.Login.LastTest.State != TestFailed {
		t.Fatalf("last test %+v", saved.Login.LastTest)
	}
	if w := call(http.MethodPost, "/api/registries", `{"key":"ghcr.io/myorg","username":"x","password":"y"}`); w.Code != http.StatusConflict {
		t.Fatalf("duplicate add %d", w.Code)
	}
	if w := call(http.MethodPost, "/api/registries", `{"key":"https://ghcr.io","username":"x","password":"y"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad key %d", w.Code)
	}
	waitForRounds(t, s.logins)
	setID, _ := store.RegistrySetID()
	list := decodeBody[registriesResponse](t, call(http.MethodGet, "/api/registries", ""))
	if list.SetID != setID || len(list.Logins) != 1 || len(list.Hosts) != 2 {
		t.Fatalf("list %+v", list)
	}
	for _, h := range list.Hosts {
		if h.State != LoginsCurrent || h.At == nil || h.SetID != setID {
			t.Fatalf("host %+v", h)
		}
	}

	// Replace with no password keeps the stored one; the test sees it.
	r.testAnswer = protocol.RegistryTestResult{OK: true}
	w = call(http.MethodPost, "/api/registries/replace", `{"key":"ghcr.io/myorg","username":"bot2"}`)
	if saved := decodeBody[loginSaved](t, w); w.Code != http.StatusOK || saved.Login.Username != "bot2" || saved.Test.State != TestOK {
		t.Fatalf("replace %d %+v", w.Code, saved)
	}
	if tested := r.testedSnapshot(); tested[len(tested)-1] != "h1:ghcr.io/myorg:tok3n" {
		t.Fatalf("tested %v", tested)
	}
	if w := call(http.MethodPost, "/api/registries/replace", `{"key":"quay.io","username":"x","password":"y"}`); w.Code != http.StatusNotFound {
		t.Fatalf("replace missing %d", w.Code)
	}

	if w := call(http.MethodPost, "/api/registries/reveal", `{"key":"ghcr.io/myorg","passphrase":"wrong one, really"}`); w.Code != http.StatusForbidden {
		t.Fatalf("reveal with a wrong passphrase %d", w.Code)
	}
	w = call(http.MethodPost, "/api/registries/reveal", `{"key":"ghcr.io/myorg","passphrase":"correct horse battery"}`)
	if revealed := decodeBody[loginRevealed](t, w); revealed.Password != "tok3n" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reveal %+v", revealed)
	}

	// Locked: the list works, changes don't.
	store.Lock()
	if list := decodeBody[registriesResponse](t, call(http.MethodGet, "/api/registries", "")); list.State != secretstore.Locked || len(list.Logins) != 1 {
		t.Fatalf("locked list %+v", list)
	}
	for _, path := range []string{"/api/registries", "/api/registries/delete", "/api/registries/retry"} {
		body := `{"key":"ghcr.io/myorg","username":"x","password":"y","host":"h1"}`
		if w := call(http.MethodPost, path, body); w.Code != http.StatusLocked {
			t.Fatalf("locked %s: %d", path, w.Code)
		}
	}
	if err := store.Unlock("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if w := call(http.MethodPost, "/api/registries/delete", `{"key":"ghcr.io/myorg"}`); w.Code != http.StatusNoContent {
		t.Fatalf("delete %d", w.Code)
	}
	waitForRounds(t, s.logins)
	final, _ := store.RegistrySetID()
	for _, h := range []string{"h1", "h2"} {
		if got := r.receivedBy(h); got[len(got)-1] != final {
			t.Fatalf("%s last got %v, want %s", h, got, final)
		}
	}
}

func TestLoginTestTriesHostsInOrderAndRecordsNoAgent(t *testing.T) {
	r := newLoginRelay("h1", "h2")
	r.testErr["h1"] = relay.ErrHostBusy
	s, store := loginServer(t, r)
	if _, err := store.AddLogin("docker.io", "bart", []byte("pw"), time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err := s.testLogin(context.Background(), "docker.io")
	if err != nil || result.State != TestOK || result.Host != "h2" {
		t.Fatalf("test %+v, %v", result, err)
	}

	// No host answers: no_agent, nothing recorded, and the first host to
	// connect tests it.
	if _, err := store.ReplaceLogin("docker.io", "bart", []byte("pw2"), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	r.testErr["h2"] = relay.ErrTimeout
	if result, _ := s.testLogin(context.Background(), "docker.io"); result.State != TestNoAgent {
		t.Fatalf("test with no answer %+v", result)
	}
	if logins, _ := store.Logins(); logins[0].LastTest != nil {
		t.Fatalf("a skipped test was recorded: %+v", logins[0].LastTest)
	}
	delete(r.testErr, "h2")
	s.HostConnected(protocol.HostInfo{HostID: "h2"})
	logins, _ := store.Logins()
	if logins[0].LastTest == nil || logins[0].LastTest.Host != "h2" || logins[0].LastTest.State != TestOK {
		t.Fatalf("the connecting host did not test it: %+v", logins[0].LastTest)
	}
}

func TestConnectPushesRegistriesBeforeSecretsAndSkipsCurrentHosts(t *testing.T) {
	r := newLoginRelay("h1")
	s, store := loginServer(t, r)
	if err := store.Put("h1", "web", "db", []byte("pw")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddLogin("ghcr.io", "bot", []byte("t"), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.HostConnected(protocol.HostInfo{HostID: "h1"})
	if events := r.eventsSnapshot(); !slices.Equal(events[:2], []string{"h1:registries", "h1:secrets"}) {
		t.Fatalf("events %v", events)
	}
	// A host whose hello reports the current set gets no push.
	current, _ := store.RegistrySetID()
	before := len(r.receivedBy("h1"))
	s.HostConnected(protocol.HostInfo{HostID: "h1", RegistrySetID: current})
	if got := len(r.receivedBy("h1")); got != before {
		t.Fatalf("a current host was pushed again (%d pushes)", got)
	}
}

func TestNothingIsPushedWhileLocked(t *testing.T) {
	r := newLoginRelay("h1")
	s, store := loginServer(t, r)
	if _, err := store.AddLogin("ghcr.io", "bot", []byte("t"), time.Now()); err != nil {
		t.Fatal(err)
	}
	store.Lock()
	s.logins.changed()
	waitForRounds(t, s.logins)
	s.HostConnected(protocol.HostInfo{HostID: "h1"})
	if got := r.receivedBy("h1"); len(got) != 0 {
		t.Fatalf("pushed while locked: %v", got)
	}
	hosts, online := s.knownHosts()
	current, _ := store.RegistrySetID()
	if rows := s.logins.statuses(hosts, online, current); rows[0].State != LoginsMissing {
		t.Fatalf("host row %+v", rows[0])
	}
	// Unlocking pushes.
	if err := store.Unlock("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	s.pushAllHosts()
	waitUntil(t, "push after unlock", func() bool { return len(r.receivedBy("h1")) == 1 })
}

func TestFailedPushIsRetriedWithBackoff(t *testing.T) {
	r := newLoginRelay("h1")
	r.failSync["h1"] = 2
	s, store := loginServer(t, r)
	if _, err := store.AddLogin("ghcr.io", "bot", []byte("t"), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.logins.push(context.Background(), []string{"h1"})
	hosts, online := s.knownHosts()
	current, _ := store.RegistrySetID()
	row := s.logins.statuses(hosts, online, current)[0]
	if row.State != LoginsFailed || !strings.Contains(row.Error, "no space left") || row.RetryAt == nil {
		t.Fatalf("after a failure %+v", row)
	}
	waitUntil(t, "retried push", func() bool {
		return s.logins.statuses(hosts, online, current)[0].State == LoginsCurrent
	})
	if events := r.eventsSnapshot(); len(events) != 3 {
		t.Fatalf("pushes %v, want 2 failures and a success", events)
	}
}

func TestRetryNowSkipsTheBackoff(t *testing.T) {
	r := newLoginRelay("h1")
	r.failSync["h1"] = 1
	s, store := loginServer(t, r)
	s.logins.retryMin, s.logins.retryMax = time.Hour, time.Hour
	if _, err := store.AddLogin("ghcr.io", "bot", []byte("t"), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.logins.push(context.Background(), []string{"h1"})
	if w := authenticatedStackRequest(t, s, http.MethodPost, "/api/registries/retry", `{"host":"h1"}`); w.Code != http.StatusAccepted {
		t.Fatalf("retry %d", w.Code)
	}
	waitUntil(t, "retry now", func() bool { return len(r.receivedBy("h1")) == 1 })
}

// Changes racing with pushes never deliver an older set after a newer one,
// and each host ends with the newest.
func TestPushesNeverDeliverAnOlderSet(t *testing.T) {
	hosts := []string{"h1", "h2", "h3", "h4"}
	r := newLoginRelay(hosts...)
	s, store := loginServer(t, r)
	var order []string
	connects := sync.WaitGroup{}
	for i := range 30 {
		if _, err := store.AddLogin(fmt.Sprintf("registry.example.com/team%02d", i), "u", []byte("p"), time.Now()); err != nil {
			t.Fatal(err)
		}
		id, _ := store.RegistrySetID()
		order = append(order, id)
		s.logins.changed()
		if i%7 == 0 {
			connects.Go(func() { s.HostConnected(protocol.HostInfo{HostID: hosts[i%len(hosts)]}) })
		}
	}
	connects.Wait()
	waitForRounds(t, s.logins)
	for _, h := range hosts {
		got := r.receivedBy(h)
		last := -1
		for _, id := range got {
			at := slices.Index(order, id)
			if at < last {
				t.Fatalf("%s got %v: an older set after a newer one", h, got)
			}
			last = at
		}
		if last != len(order)-1 {
			t.Fatalf("%s ended with set %d of %d", h, last, len(order))
		}
	}
}

// A round whose snapshot went stale before a host's turn sends that host
// the set current under its lock, not the snapshot.
func TestAStaleSnapshotIsNotSent(t *testing.T) {
	r := newLoginRelay("h1")
	s, store := loginServer(t, r)
	if _, err := store.AddLogin("ghcr.io", "bot", []byte("t"), time.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot := &loginSnapshot{store: store}
	defer snapshot.clear()
	first, _ := store.RegistrySetID()
	if _, err := snapshot.get(first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddLogin("quay.io", "bot", []byte("t"), time.Now()); err != nil {
		t.Fatal(err)
	}
	second, _ := store.RegistrySetID()
	s.logins.pushOne(context.Background(), "h1", snapshot)
	if got := r.receivedBy("h1"); !slices.Equal(got, []string{second}) {
		t.Fatalf("h1 got %v, want only %s", got, second)
	}
	if snapshot.decrypts != 2 {
		t.Fatalf("decrypts = %d, want one per set", snapshot.decrypts)
	}
}

// Twenty rapid changes reach each host at most twice: the round running and
// one more with whatever is current.
func TestChangesAreCoalesced(t *testing.T) {
	r := newLoginRelay("h1", "h2")
	r.gate = make(chan struct{})
	s, store := loginServer(t, r)
	for i := range 20 {
		if _, err := store.AddLogin(fmt.Sprintf("registry.example.com/team%02d", i), "u", []byte("p"), time.Now()); err != nil {
			t.Fatal(err)
		}
		s.logins.changed()
	}
	close(r.gate)
	waitForRounds(t, s.logins)
	s.logins.mu.Lock()
	rounds := s.logins.rounds
	s.logins.mu.Unlock()
	if rounds > 2 {
		t.Fatalf("20 changes ran %d rounds, want at most 2", rounds)
	}
	final, _ := store.RegistrySetID()
	for _, h := range []string{"h1", "h2"} {
		got := r.receivedBy(h)
		if len(got) > 2 || got[len(got)-1] != final {
			t.Fatalf("%s got %d pushes %v, want at most 2 ending with %s", h, len(got), got, final)
		}
	}
}

func TestDisconnectedHostShowsOfflineWithLastSeen(t *testing.T) {
	r := newLoginRelay("h1")
	s, store := loginServer(t, r)
	r.lmu.Lock()
	r.online["h1"] = false
	r.lmu.Unlock()
	s.HostDisconnected("h1")
	current, _ := store.RegistrySetID()
	row := s.logins.statuses([]string{"h1"}, map[string]bool{}, current)[0]
	if row.State != LoginsOffline || row.LastSeen == nil {
		t.Fatalf("row %+v", row)
	}
}

func TestStackPagesExplainARefusal(t *testing.T) {
	r := newLoginRelay("h1")
	s, store := loginServer(t, r)
	if _, err := store.AddLogin("ghcr.io/myorg", "bot", []byte("t"), time.Now()); err != nil {
		t.Fatal(err)
	}
	images := protocol.StackImages{Services: []protocol.ServiceImage{
		{Service: "worker", Image: "example/worker:1.8", Failure: protocol.ImageFailureDenied},
		{Service: "api", Image: "ghcr.io/myorg/api:2.4", Failure: protocol.ImageFailureDenied},
		{Service: "web", Image: "ghcr.io/myorg/web:1", Failure: protocol.ImageFailureUnreachable},
	}}
	s.explainImages("h1", &images)
	if got := images.Services[0]; got.LoginState != protocol.LoginNone || got.LoginKey != "" {
		t.Fatalf("no login: %+v", got)
	}
	if got := images.Services[1]; got.LoginState != protocol.LoginNotOnHost || got.LoginKey != "ghcr.io/myorg" {
		t.Fatalf("not delivered: %+v", got)
	}
	if got := images.Services[2]; got.LoginState != "" {
		t.Fatalf("an unreachable registry was explained: %+v", got)
	}
	s.HostConnected(protocol.HostInfo{HostID: "h1"})
	stacks := []protocol.Stack{{Name: "shop", WaitingLogin: "ghcr.io/myorg/api"}, {Name: "web"}}
	s.explainWaiting("h1", stacks)
	if stacks[0].WaitingLoginState != protocol.LoginRefused || stacks[0].WaitingLoginKey != "ghcr.io/myorg" || stacks[1].WaitingLoginState != "" {
		t.Fatalf("stacks %+v", stacks)
	}
}
