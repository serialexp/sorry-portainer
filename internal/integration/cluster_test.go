package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/podman"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/relay"
	"github.com/serialexp/sorry-portainer/internal/server"
)

// TestLocalClusterInventory models several independently identified hosts while
// deliberately sharing one Podman store. This exercises the central API and
// host-selection boundary without creating or mutating Podman resources.
func TestLocalClusterInventory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	podmanClient, err := podman.New()
	if err != nil {
		t.Skipf("Podman client unavailable: %v", err)
	}
	if _, err := podmanClient.Info(ctx); err != nil {
		t.Skipf("local Podman runtime unavailable: %v", err)
	}

	const password = "integration-test-admin-password"
	registry := relay.New()
	for _, hostID := range []string{"local-a", "local-b", "local-c"} {
		if err := registry.RegisterWithPrefix(hostID, hostID+"-", podmanClient); err != nil {
			t.Fatalf("register %s: %v", hostID, err)
		}
	}

	api := httptest.NewServer(server.New(password, time.Hour, registry, nil).Handler(nil))
	defer api.Close()
	client := mustAuthenticatedClient(t, api.URL, password)

	var hosts []protocol.HostInfo
	getJSON(t, client, api.URL+"/api/hosts", &hosts)
	if len(hosts) != 3 {
		t.Fatalf("got %d hosts, want 3: %#v", len(hosts), hosts)
	}
	for _, host := range hosts {
		if host.HostID == "" || host.Hostname == "" || host.EngineVersion == "" {
			t.Fatalf("incomplete host info: %#v", host)
		}
	}

	var info protocol.HostInfo
	getJSON(t, client, api.URL+"/api/hosts/local-a/info", &info)
	if info.HostID != "local-a" || info.Prefix != "local-a-" {
		t.Fatalf("unexpected host info: %#v", info)
	}
	var first []protocol.Container
	getJSON(t, client, api.URL+"/api/hosts/local-a/containers", &first)
	for _, hostID := range []string{"local-b", "local-c"} {
		var containers []protocol.Container
		getJSON(t, client, api.URL+"/api/hosts/"+hostID+"/containers", &containers)
		if !sameContainers(first, containers) {
			t.Fatalf("host %s did not expose the shared Podman inventory: first=%#v got=%#v", hostID, first, containers)
		}
	}
}

func TestLocalClusterStartRoutesByHostPrefix(t *testing.T) {
	cluster := newFakeCluster()
	api := httptest.NewServer(server.New("integration-test-admin-password", time.Hour, cluster.registry, nil).Handler(nil))
	defer api.Close()
	client := mustAuthenticatedClient(t, api.URL, "integration-test-admin-password")

	response, err := client.Post(api.URL+"/api/hosts/local-b/start", "application/json", strings.NewReader(`{"container_id":"local-b-worker-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("start status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if cluster.agents["local-a"].started != "" {
		t.Fatalf("local-a received a start: %#v", cluster.agents["local-a"].started)
	}
	if got := cluster.agents["local-b"].started; got != "local-b-worker-1" {
		t.Fatalf("local-b started %q", got)
	}

	response, err = client.Post(api.URL+"/api/hosts/local-a/start", "application/json", strings.NewReader(`{"container_id":"local-b-worker-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("wrong-prefix status = %d, want %d", response.StatusCode, http.StatusBadGateway)
	}
}

type fakeCluster struct {
	registry *relay.Memory
	agents   map[string]*fakeAgent
}

func newFakeCluster() *fakeCluster {
	c := &fakeCluster{registry: relay.New(), agents: map[string]*fakeAgent{}}
	for _, id := range []string{"local-a", "local-b", "local-c"} {
		a := &fakeAgent{id: id}
		c.agents[id] = a
		if err := c.registry.RegisterWithPrefix(id, id+"-", a); err != nil {
			panic(err)
		}
	}
	return c
}

type fakeAgent struct {
	id      string
	started string
}

func (a *fakeAgent) Info(context.Context) (protocol.HostInfo, error) {
	return protocol.HostInfo{Hostname: a.id, EngineVersion: "fake"}, nil
}
func (a *fakeAgent) Containers(context.Context) ([]protocol.Container, error) {
	return []protocol.Container{{ID: a.id + "-worker-1", Name: a.id + "-worker-1", State: "running"}}, nil
}
func (a *fakeAgent) Volumes(context.Context) ([]protocol.Volume, error) {
	return []protocol.Volume{{Name: a.id + "-data"}}, nil
}
func (a *fakeAgent) Images(context.Context) ([]protocol.Image, error) {
	return []protocol.Image{{ID: "sha256:test"}}, nil
}
func (a *fakeAgent) Stop(_ context.Context, id string) error  { a.started = "stop:" + id; return nil }
func (a *fakeAgent) Start(_ context.Context, id string) error { a.started = id; return nil }

func mustAuthenticatedClient(t *testing.T, baseURL, password string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	response, err := client.Post(baseURL+"/api/session/login", "application/json", strings.NewReader(`{"password":"`+password+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("login status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
	return client
}

func getJSON(t *testing.T, client *http.Client, url string, destination any) {
	t.Helper()
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", url, response.StatusCode, http.StatusOK)
	}
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}

func sameContainers(left, right []protocol.Container) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
