package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

type testHandler struct{}

func (testHandler) Info(context.Context) (protocol.HostInfo, error) {
	return protocol.HostInfo{Hostname: "fake", EngineVersion: "test"}, nil
}
func (testHandler) Containers(context.Context) ([]protocol.Container, error) {
	return []protocol.Container{{ID: "local-b-worker-1", Name: "local-b-worker-1", State: "created"}}, nil
}
func (testHandler) Volumes(context.Context) ([]protocol.Volume, error) {
	return []protocol.Volume{{Name: "local-b-data"}}, nil
}
func (testHandler) Images(context.Context) ([]protocol.Image, error) {
	return []protocol.Image{{ID: "sha256:test"}}, nil
}
func (testHandler) Stop(context.Context, string) error  { return nil }
func (testHandler) Start(context.Context, string) error { return nil }

func TestWebSocketAgentRoundTrip(t *testing.T) {
	remote := NewRemote()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := upgrader.Upgrade(w, r, nil)
		if e != nil {
			t.Error(e)
			return
		}
		info, e := remote.AcceptAgent(c)
		if e != nil {
			t.Error(e)
			return
		}
		if info.HostID != "local-b" {
			t.Errorf("host %q", info.HostID)
		}
		defer remote.Remove(info.HostID, c)
		<-r.Context().Done()
	}))
	defer server.Close()
	conn, _, e := websocket.DefaultDialer.Dial("ws"+server.URL[4:], nil)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent := &Agent{Conn: conn, HostID: "local-b", Prefix: "local-b-", Handler: testHandler{}}
	go func() {
		if e := agent.Serve(ctx); e != nil && e != context.Canceled {
			t.Errorf("agent: %v", e)
		}
	}()
	deadline := time.Now().Add(time.Second)
	for len(remote.Hosts()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(remote.Hosts()) != 1 {
		t.Fatal("agent did not register")
	}
	containers, e := remote.Containers("local-b")
	if e != nil {
		t.Fatal(e)
	}
	if len(containers) != 1 || containers[0].ID != "local-b-worker-1" {
		t.Fatalf("containers %#v", containers)
	}
}
