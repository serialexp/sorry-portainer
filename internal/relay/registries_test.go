package relay

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// fakeRegistries records what the relay hands the agent's login set.
type fakeRegistries struct {
	synced chan protocol.RegistrySync
	tested chan protocol.RegistryLogin
}

func (f *fakeRegistries) Sync(set protocol.RegistrySync) (protocol.RegistrySynced, error) {
	f.synced <- set
	if set.SetID == "refuse" {
		return protocol.RegistrySynced{}, errors.New("invalid login set")
	}
	return protocol.RegistrySynced{SetID: set.SetID}, nil
}

func (f *fakeRegistries) Test(_ context.Context, login protocol.RegistryLogin) (protocol.RegistryTestResult, error) {
	f.tested <- login
	return protocol.RegistryTestResult{Failure: protocol.RegistryTestDenied, Message: login.Key + " refused the login"}, nil
}

func encodeSet(t *testing.T, set protocol.RegistrySync) protocol.EncodedRegistrySync {
	t.Helper()
	encoded, err := protocol.EncodeRegistrySync(set)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// TestOneEncodedSetReachesManyAgents: the bytes of one encoding are sent to
// several hosts as they are, and every agent decodes the same set.
func TestOneEncodedSetReachesManyAgents(t *testing.T) {
	h := newHarness(t, time.Minute)
	hosts := []string{"local-b", "local-c", "local-d"}
	fakes := make([]*fakeRegistries, len(hosts))
	for i, host := range hosts {
		fakes[i] = &fakeRegistries{synced: make(chan protocol.RegistrySync, 1), tested: make(chan protocol.RegistryLogin, 1)}
		agent := testAgent(&fakeHost{})
		agent.HostID, agent.Prefix, agent.Registries = host, host+"-", fakes[i]
		h.connect(t, agent, host)
	}
	set := protocol.RegistrySync{SetID: "set-1", Logins: []protocol.RegistryLogin{
		{Key: "docker.io", Username: "bart", Password: []byte(`pw "quoted" <&> é`)},
		{Key: "ghcr.io/myorg", Username: "ci", Password: []byte("token")},
	}}
	encoded := encodeSet(t, set)
	before := append([]byte(nil), encoded.JSON...)
	for _, host := range hosts {
		if synced, err := h.remote.SyncRegistries(context.Background(), host, encoded); err != nil || synced.SetID != "set-1" {
			t.Fatalf("%s: sync = %+v, %v", host, synced, err)
		}
	}
	for i, fake := range fakes {
		if got := <-fake.synced; !reflect.DeepEqual(got, set) {
			t.Fatalf("%s got %+v, want %+v", hosts[i], got, set)
		}
	}
	if !bytes.Equal(encoded.JSON, before) {
		t.Fatal("sending changed the shared encoding")
	}
}

func TestRegistryOperationsReachTheAgent(t *testing.T) {
	h := newHarness(t, time.Minute)
	registries := &fakeRegistries{synced: make(chan protocol.RegistrySync, 2), tested: make(chan protocol.RegistryLogin, 1)}
	agent := testAgent(&fakeHost{})
	agent.Registries = registries
	h.connect(t, agent, "local-b")
	ctx := context.Background()

	set := protocol.RegistrySync{SetID: "set-1", Logins: []protocol.RegistryLogin{{Key: "ghcr.io/myorg", Username: "bart", Password: []byte("pw")}}}
	synced, err := h.remote.SyncRegistries(ctx, "local-b", encodeSet(t, set))
	if err != nil || synced.SetID != "set-1" {
		t.Fatalf("sync = %+v, %v", synced, err)
	}
	if got := <-registries.synced; !reflect.DeepEqual(got, set) {
		t.Fatalf("agent got %+v, want %+v", got, set)
	}
	if _, err := h.remote.SyncRegistries(ctx, "local-b", encodeSet(t, protocol.RegistrySync{SetID: "refuse"})); err == nil {
		t.Fatal("a refused sync succeeded")
	}
	<-registries.synced

	result, err := h.remote.TestRegistry(ctx, "local-b", set.Logins[0])
	if err != nil || result.OK || result.Failure != protocol.RegistryTestDenied || result.Message != "ghcr.io/myorg refused the login" {
		t.Fatalf("test = %+v, %v", result, err)
	}
	if got := <-registries.tested; !reflect.DeepEqual(got, set.Logins[0]) {
		t.Fatalf("agent tested %+v", got)
	}
}

func TestRegistryOperationsWithoutLogins(t *testing.T) {
	h := newHarness(t, time.Minute)
	h.connect(t, testAgent(&fakeHost{}), "local-b")
	if _, err := h.remote.SyncRegistries(context.Background(), "local-b", encodeSet(t, protocol.RegistrySync{SetID: "x"})); err == nil {
		t.Fatal("sync without registry logins succeeded")
	}
}
