package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/podman"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/secrets"
	"github.com/serialexp/sorry-portainer/internal/stacks"
)

type noContainers struct{}

func (noContainers) ProjectContainers(context.Context, string) ([]podman.ProjectContainer, error) {
	return nil, nil
}
func (noContainers) Stop(context.Context, string) error    { return nil }
func (noContainers) Restart(context.Context, string) error { return nil }

// TestOnConnectPushesSecrets pushes a stack's secrets from the OnConnect
// callback, as the master does, and checks the agent's vault and hello.
func TestOnConnectPushesSecrets(t *testing.T) {
	vault := secrets.NewVault()
	manager := stacks.New(t.TempDir(), "local-b-", nil)
	manager.EnableSecrets(stacks.SecretSupport{HostID: "local-b", Vault: vault, Containers: noContainers{}})
	agent := testAgent(&fakeHost{hostname: "fake"})
	agent.Stacks = manager
	agent.Describe = func(info *protocol.HostInfo) {
		info.SwapActive = true
		info.SecretsReady = true
	}

	pushed := make(chan error, 1)
	var remote *Remote
	remote = NewRemote(RemoteOptions{Heartbeat: time.Minute, OnConnect: func(info protocol.HostInfo) {
		if !info.SwapActive || !info.SecretsReady {
			pushed <- errNotDescribed
			return
		}
		ctx := context.Background()
		result, err := remote.SyncSecrets(ctx, info.HostID, protocol.SecretSync{Stack: "web", Secrets: map[string][]byte{"db": []byte("pw")}})
		if err == nil && !reflect.DeepEqual(result.Changed, []string{"db"}) {
			err = errUnexpected(result)
		}
		if err == nil {
			err = remote.RetainSecrets(ctx, info.HostID, []string{"web"})
		}
		pushed <- err
	}})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = remote.Serve(conn, "local-b")
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = agent.Serve(ctx, conn)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	if err := receive(t, "secret push", pushed); err != nil {
		t.Fatal(err)
	}
	values, err := vault.Copy("web", []string{"db"})
	if err != nil || string(values["db"]) != "pw" {
		t.Fatalf("vault %q %v", values, err)
	}
	if info, _ := remote.Info(ctx, "local-b"); !info.SwapActive {
		t.Fatalf("info %+v", info)
	}
}

func TestSecretOperationsWithoutStacks(t *testing.T) {
	h := newHarness(t, time.Minute)
	h.connect(t, testAgent(&fakeHost{}), "local-b")
	if _, err := h.remote.SyncSecrets(context.Background(), "local-b", protocol.SecretSync{Stack: "web"}); err == nil {
		t.Fatal("sync without a stack manager succeeded")
	}
}

type testError string

func (e testError) Error() string { return string(e) }

const errNotDescribed = testError("hello lacked Describe's fields")

func errUnexpected(result protocol.SecretSyncResult) error {
	return testError("unexpected sync result: " + strings.Join(result.Changed, ","))
}
