package podman

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type response struct {
	stdout string
	stderr string
	err    error
}

type fakeRunner struct {
	mu        sync.Mutex
	responses []response
	calls     [][]string
}

func (f *fakeRunner) Run(_ context.Context, stdout, stderr io.Writer, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	var item response
	if len(f.responses) != 0 {
		item = f.responses[0]
		f.responses = f.responses[1:]
	}
	f.mu.Unlock()
	if item.stdout != "" {
		_, _ = io.WriteString(stdout, item.stdout)
	}
	if item.stderr != "" {
		_, _ = io.WriteString(stderr, item.stderr)
	}
	return item.err
}

func TestProtocolMappingsAndPrefixFiltering(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `{"host":{"hostname":"host-a"},"version":{"Version":"5.4.2"}}`},
		{stdout: `[{"Id":"abc","Names":["owned-web"],"Image":"example/web:1","State":"running","Status":"Up 2 minutes"},{"Id":"def","Names":["other-web"],"Image":"example/web:2","State":"exited","Status":"Exited (0)"}]`},
		{stdout: `[{"Name":"owned-data","Driver":"local","Mountpoint":"/volumes/owned-data"},{"Name":"other-data","Driver":"local","Mountpoint":"/volumes/other-data"}]`},
		{stdout: `[{"Id":"sha256:123","RepoTags":["example/web:1","example/web:latest"],"Size":12345,"Created":1700000000}]`},
	}}
	client := NewWithRunner(runner, "owned-")
	ctx := context.Background()

	info, err := client.Info(ctx)
	if err != nil || info.Hostname != "host-a" || info.EngineVersion != "5.4.2" {
		t.Fatalf("Info() = %#v, %v", info, err)
	}
	containers, err := client.Containers(ctx)
	if err != nil || len(containers) != 1 {
		t.Fatalf("Containers() = %#v, %v", containers, err)
	}
	if got := containers[0]; got.ID != "abc" || got.Name != "owned-web" || got.Image != "example/web:1" || got.State != "running" || got.Status != "Up 2 minutes" {
		t.Fatalf("container mapping = %#v", got)
	}
	volumes, err := client.Volumes(ctx)
	if err != nil || len(volumes) != 1 || volumes[0].Name != "owned-data" || volumes[0].Driver != "local" || volumes[0].Mountpoint != "/volumes/owned-data" {
		t.Fatalf("Volumes() = %#v, %v", volumes, err)
	}
	images, err := client.Images(ctx)
	if err != nil || len(images) != 1 || images[0].ID != "sha256:123" || images[0].Size != 12345 || images[0].Created != 1700000000 || !reflect.DeepEqual(images[0].Tags, []string{"example/web:1", "example/web:latest"}) {
		t.Fatalf("Images() = %#v, %v", images, err)
	}

	wantCalls := [][]string{
		{"info", "--format", "json"},
		{"ps", "--all", "--format", "json"},
		{"volume", "ls", "--format", "json"},
		{"images", "--all", "--format", "json"},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("direct argv calls = %#v, want %#v", runner.calls, wantCalls)
	}
}

func TestInfoRejectsUnsupportedVersion(t *testing.T) {
	client := NewWithRunner(&fakeRunner{responses: []response{{stdout: `{"host":{"hostname":"host-a"},"version":{"Version":"4.6.2"}}`}}})
	if _, err := client.Info(context.Background()); err == nil || !strings.Contains(err.Error(), "4.7 or later") {
		t.Fatalf("Info() error = %v", err)
	}
}

func TestMalformedJSON(t *testing.T) {
	client := NewWithRunner(&fakeRunner{responses: []response{{stdout: `not json`}}})
	if _, err := client.Containers(context.Background()); err == nil || !strings.Contains(err.Error(), "decode podman ps JSON") {
		t.Fatalf("Containers() error = %v", err)
	}
}

func TestOversizedOutputIsBounded(t *testing.T) {
	for _, test := range []struct {
		name     string
		response response
		contains string
	}{
		{name: "stdout", response: response{stdout: strings.Repeat("x", maxCommandOutput+1)}, contains: "stdout exceeds"},
		{name: "stderr", response: response{stderr: strings.Repeat("x", maxCommandOutput+1), err: errors.New("exit")}, contains: "stderr exceeds"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := NewWithRunner(&fakeRunner{responses: []response{test.response}})
			_, err := client.Info(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Info() error = %v", err)
			}
		})
	}
}

func TestStartResolvesCanonicalContainerAndValidatesName(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `[{"Id":"0123456789abcdef","Name":"/owned-web"}]`},
		{},
	}}
	client := NewWithRunner(runner, "owned-")
	if err := client.Start(context.Background(), "01234567"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"container", "inspect", "--format", "json", "--", "01234567"},
		{"start", "--", "0123456789abcdef"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestDirectArgvDoesNotInterpretContainerReference(t *testing.T) {
	reference := "owned-web; echo not-a-shell"
	runner := &fakeRunner{responses: []response{
		{stdout: `[{"Id":"canonical-id","Name":"owned-web"}]`},
		{},
	}}
	client := NewWithRunner(runner, "owned-")
	if err := client.Stop(context.Background(), reference); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"container", "inspect", "--format", "json", "--", reference},
		{"stop", "--", "canonical-id"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestStartAndStopRejectResolvedCrossPrefixContainer(t *testing.T) {
	for _, operation := range []string{"start", "stop"} {
		t.Run(operation, func(t *testing.T) {
			runner := &fakeRunner{responses: []response{{stdout: `[{"Id":"deadbeef","Name":"other-web"}]`}}}
			client := NewWithRunner(runner, "owned-")
			var err error
			if operation == "start" {
				err = client.Start(context.Background(), "owned-looking-input")
			} else {
				err = client.Stop(context.Background(), "owned-looking-input")
			}
			if err == nil || !strings.Contains(err.Error(), "outside agent prefix") {
				t.Fatalf("%s error = %v", operation, err)
			}
			if len(runner.calls) != 1 || runner.calls[0][0] != "container" {
				t.Fatalf("calls after rejection = %#v", runner.calls)
			}
		})
	}
}

type cancellationRunner struct{}

func (cancellationRunner) Run(ctx context.Context, _, _ io.Writer, _ ...string) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := NewWithRunner(cancellationRunner{}).Info(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Info() error = %v", err)
	}
}

func BenchmarkContainers1000(b *testing.B) {
	var data strings.Builder
	data.Grow(150_000)
	data.WriteByte('[')
	for i := 0; i < 1000; i++ {
		if i != 0 {
			data.WriteByte(',')
		}
		data.WriteString(`{"Id":"0123456789abcdef","Names":["owned-web"],"Image":"example/web:latest","State":"running","Status":"Up 1 hour"}`)
	}
	data.WriteByte(']')
	payload := data.String()
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client := NewWithRunner(&fakeRunner{responses: []response{{stdout: payload}}}, "owned-")
		containers, err := client.Containers(context.Background())
		if err != nil || len(containers) != 1000 {
			b.Fatalf("Containers() len = %d, err = %v", len(containers), err)
		}
	}
}
