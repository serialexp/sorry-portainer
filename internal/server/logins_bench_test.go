package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/registries"
	"github.com/serialexp/sorry-portainer/internal/secretstore"
)

// encodingRelay stands in for 1000 connected agents: each push is encoded
// as the relay would and then dropped.
type encodingRelay struct {
	stackRelay
	hosts      []protocol.HostInfo
	pushes     atomic.Int64
	maxMessage atomic.Int64
}

func (r *encodingRelay) Hosts() []protocol.HostInfo { return r.hosts }

func (r *encodingRelay) SyncRegistries(_ context.Context, _ string, set protocol.RegistrySync) (protocol.RegistrySynced, error) {
	data, err := json.Marshal(set)
	if err != nil {
		return protocol.RegistrySynced{}, err
	}
	r.pushes.Add(1)
	for n := int64(len(data)); ; {
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
// set plus the encoded messages in flight.
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
