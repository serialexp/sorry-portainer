package relay

import (
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

func TestRegistryOperationsReachTheAgent(t *testing.T) {
	h := newHarness(t, time.Minute)
	registries := &fakeRegistries{synced: make(chan protocol.RegistrySync, 2), tested: make(chan protocol.RegistryLogin, 1)}
	agent := testAgent(&fakeHost{})
	agent.Registries = registries
	h.connect(t, agent, "local-b")
	ctx := context.Background()

	set := protocol.RegistrySync{SetID: "set-1", Logins: []protocol.RegistryLogin{{Key: "ghcr.io/myorg", Username: "bart", Password: []byte("pw")}}}
	synced, err := h.remote.SyncRegistries(ctx, "local-b", set)
	if err != nil || synced.SetID != "set-1" {
		t.Fatalf("sync = %+v, %v", synced, err)
	}
	if got := <-registries.synced; !reflect.DeepEqual(got, set) {
		t.Fatalf("agent got %+v, want %+v", got, set)
	}
	if _, err := h.remote.SyncRegistries(ctx, "local-b", protocol.RegistrySync{SetID: "refuse"}); err == nil {
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
	if _, err := h.remote.SyncRegistries(context.Background(), "local-b", protocol.RegistrySync{SetID: "x"}); err == nil {
		t.Fatal("sync without registry logins succeeded")
	}
}
