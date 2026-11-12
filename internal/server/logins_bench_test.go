package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/registries"
	"github.com/serialexp/sorry-portainer/internal/secretstore"
)

// encodingRelay stands in for 1000 connected agents: each push is framed
// and written as relay.Remote does, to io.Discard.
type encodingRelay struct {
	stackRelay
	hosts      []protocol.HostInfo
	pushes     atomic.Int64
	maxMessage atomic.Int64
	// keep makes kept collect every encoding pushed, to check it is cleared.
	keep bool
	mu   sync.Mutex
	kept [][]byte
}

func (r *encodingRelay) Hosts() []protocol.HostInfo { return r.hosts }

func (r *encodingRelay) SyncRegistries(_ context.Context, host string, set protocol.EncodedRegistrySync) (protocol.RegistrySynced, error) {
	frame, err := protocol.EncodeFrame(protocol.Message{
		ID: "0123456789abcdef0123456789abcdef", Type: protocol.TypeRequest, Operation: "registries.sync",
		HostID: host, TimeoutMS: 30_000, Payload: set.JSON,
	})
	if err != nil {
		return protocol.RegistrySynced{}, err
	}
	for _, part := range [...][]byte{frame.Head, frame.Payload, frame.Tail} {
		if _, err := io.Discard.Write(part); err != nil {
			return protocol.RegistrySynced{}, err
		}
	}
	if r.keep {
		r.mu.Lock()
		r.kept = append(r.kept, set.JSON)
		r.mu.Unlock()
	}
	r.pushes.Add(1)
	for n := int64(frame.Len()); ; {
		old := r.maxMessage.Load()
		if n <= old || r.maxMessage.CompareAndSwap(old, n) {
			break
		}
	}
	return protocol.RegistrySynced{SetID: set.SetID}, nil
}

// fullLoginStore holds 64 logins at the key, username and password limits,
// with the characters JSON escapes most expensively.
func fullLoginStore(t testing.TB) *secretstore.Store {
	t.Helper()
	r := &loginRelay{}
	_, store := loginServer(t, r)
	password := []byte(strings.Repeat("<>&", registries.MaxPassword/3) + "x")
	username := strings.Repeat("u", registries.MaxUsername)
	for i := range registries.MaxLogins {
		prefix := fmt.Sprintf("registry.example.com/team%02d/", i)
		key := prefix + strings.Repeat("a", registries.MaxKey-len(prefix))
		if _, err := store.AddLogin(key, username, password, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func thousandHosts() []protocol.HostInfo {
	hosts := make([]protocol.HostInfo, 1000)
	for i := range hosts {
		hosts[i] = protocol.HostInfo{HostID: fmt.Sprintf("host-%04d", i)}
	}
	return hosts
}

// TestLargestRoundDecryptsOnceAndFitsAMessage: a round to 1000 hosts with
// the largest set decrypts it once, pushes each host once, and every
// message fits the relay's limit.
func TestLargestRoundDecryptsOnceAndFitsAMessage(t *testing.T) {
	store := fullLoginStore(t)
	r := &encodingRelay{hosts: thousandHosts()}
	p := newLoginPusher(store, r, newHostLocks())
	if decrypts := p.push(context.Background(), onlineHosts(r)); decrypts != 1 {
		t.Fatalf("decrypts = %d, want 1", decrypts)
	}
	if got := r.pushes.Load(); got != 1000 {
		t.Fatalf("pushes = %d, want 1000", got)
	}
	if size := r.maxMessage.Load(); size > protocol.MaxMessageSize {
		t.Fatalf("a push encodes to %d bytes, over the %d limit", size, protocol.MaxMessageSize)
	}
	// Everyone is current now: another round sends nothing.
	if p.push(context.Background(), onlineHosts(r)); r.pushes.Load() != 1000 {
		t.Fatalf("a round to current hosts pushed %d more", r.pushes.Load()-1000)
	}
}

// TestRoundSendsOneEncodingAndClearsIt: every host of a round is sent the
// same encoded bytes, and the round overwrites them once it is done.
func TestRoundSendsOneEncodingAndClearsIt(t *testing.T) {
	store := fullLoginStore(t)
	r := &encodingRelay{hosts: thousandHosts()[:50], keep: true}
	p := newLoginPusher(store, r, newHostLocks())
	p.push(context.Background(), onlineHosts(r))
	if len(r.kept) != 50 {
		t.Fatalf("kept %d encodings, want 50", len(r.kept))
	}
	first := r.kept[0]
	for _, data := range r.kept {
		if len(data) == 0 || &data[0] != &first[0] {
			t.Fatal("hosts of one round were sent different encodings")
		}
	}
	if bytes.ContainsFunc(first, func(c rune) bool { return c != 0 }) {
		t.Fatal("the round left the encoded passwords in memory")
	}
}

// TestRapidChangesToThousandHosts: 20 changes in a row cost at most two
// pushes per host.
func TestRapidChangesToThousandHosts(t *testing.T) {
	store := fullLoginStore(t)
	r := &encodingRelay{hosts: thousandHosts()}
	p := newLoginPusher(store, r, newHostLocks())
	for i := range 20 {
		if _, err := store.ReplaceLogin(fmt.Sprintf("registry.example.com/team%02d/", i%registries.MaxLogins)+strings.Repeat("a", registries.MaxKey-len("registry.example.com/team00/")), "u", nil, time.Now().Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
		p.changed()
	}
	waitForRounds(t, p)
	if got := r.pushes.Load(); got > 2000 {
		t.Fatalf("20 changes made %d pushes to 1000 hosts, want at most 2000", got)
	}
}

// BenchmarkLoginRound pushes the largest set to 1000 hosts once per
// iteration. Run with -benchmem: allocations per round are one decrypted
// and encoded set plus a small envelope per host.
func BenchmarkLoginRound(b *testing.B) {
	store := fullLoginStore(b)
	r := &encodingRelay{hosts: thousandHosts()}
	p := newLoginPusher(store, r, newHostLocks())
	key := "registry.example.com/team00/" + strings.Repeat("a", registries.MaxKey-len("registry.example.com/team00/"))
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		b.StopTimer()
		if _, err := store.ReplaceLogin(key, "u", nil, time.Now().Add(time.Duration(i)*time.Second)); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		p.push(context.Background(), onlineHosts(r))
	}
	b.ReportMetric(float64(r.maxMessage.Load()), "max-message-bytes")
}
