package podman

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
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
		{stdout: `[{"Id":"abc","Names":["owned-web"],"Image":"example/web:1","State":"running","Status":"Up 2 minutes"},{"Id":"def","Names":["other-web"],"Image":"example/web:2","State":"exited","Status":"Exited (0)"}]`},
		{stdout: "abc\t[{\"Type\":\"volume\",\"Name\":\"owned-data\",\"Destination\":\"/data\",\"RW\":true}]\n"},
		{stdout: `[{"Id":"sha256:123","RepoTags":["example/web:1","example/web:latest"],"Size":12345,"Created":1700000000}]`},
		{stdout: `[]`},
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
		{"ps", "--all", "--format", "json"},
		{"container", "inspect", "--format", `{{.ID}}{{"\t"}}{{json .Mounts}}`, "--", "abc"},
		{"images", "--all", "--format", "json"},
		{"ps", "--all", "--format", "json"},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("direct argv calls = %#v, want %#v", runner.calls, wantCalls)
	}
}

func TestVolumeDetailInspectsOnePrefixedVolume(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `[{"Name":"owned-data","Driver":"local","Mountpoint":"/volumes/owned-data","CreatedAt":"2026-10-12T10:00:00Z","Labels":{"com.docker.compose.volume":"data"}}]`},
		{stdout: `[{"Id":"abc","Names":["owned-web"],"Image":"example/web:1","State":"running","Status":"Up"}]`},
		{stdout: "abc\t[{\"Type\":\"volume\",\"Name\":\"owned-data\",\"Destination\":\"/data\",\"RW\":false},{\"Type\":\"bind\",\"Source\":\"/srv\",\"Destination\":\"/srv\",\"RW\":true}]\n"},
	}}
	client := NewWithRunner(runner, "owned-")
	detail, err := client.VolumeDetail(context.Background(), "owned-data")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Name != "owned-data" || detail.Driver != "local" || detail.Mountpoint != "/volumes/owned-data" || detail.Created != "2026-10-12T10:00:00Z" || detail.Labels["com.docker.compose.volume"] != "data" {
		t.Fatalf("VolumeDetail() = %#v", detail)
	}
	want := []protocol.VolumeMount{{ContainerID: "abc", Container: "owned-web", Destination: "/data", Mode: "ro", State: "running"}}
	if !reflect.DeepEqual(detail.Mounts, want) {
		t.Fatalf("mounts = %#v, want %#v", detail.Mounts, want)
	}
}

func TestVolumeDetailRefusesVolumesOutsidePrefix(t *testing.T) {
	runner := &fakeRunner{}
	client := NewWithRunner(runner, "owned-")
	if _, err := client.VolumeDetail(context.Background(), "other-data"); err == nil || !strings.Contains(err.Error(), "outside agent prefix") {
		t.Fatalf("VolumeDetail() error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("calls = %#v, want none", runner.calls)
	}
}

func TestVolumesWithoutContainersSkipInspect(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `[{"Name":"owned-data","Driver":"local","Mountpoint":"/volumes/owned-data"}]`},
		{stdout: `[]`},
	}}
	client := NewWithRunner(runner, "owned-")
	volumes, err := client.Volumes(context.Background())
	if err != nil || len(volumes) != 1 || len(volumes[0].Mounts) != 0 {
		t.Fatalf("Volumes() = %#v, %v", volumes, err)
	}
	want := [][]string{{"volume", "ls", "--format", "json"}, {"ps", "--all", "--format", "json"}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestImageDetailReadsConfigLayersAndUsers(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `[{"Id":"abcdef0123456789abcdef","RepoTags":["example/web:1"],"Created":"2026-10-12T10:00:00Z","Size":4096,"Os":"linux","Architecture":"amd64","Config":{"Cmd":["caddy","run"],"Entrypoint":null,"WorkingDir":"/srv","Env":["PATH=/bin"],"ExposedPorts":{"443/tcp":{},"80/tcp":{}}}}]`},
		{stdout: `[{"CreatedBy":"CMD [\"caddy\"]","size":0},{"CreatedBy":"ADD rootfs","size":4000}]`},
		{stdout: `[{"Id":"c1","Names":["owned-web"],"ImageID":"abcdef0123456789abcdef","State":"running"},{"Id":"c2","Names":["other-web"],"ImageID":"abcdef0123456789abcdef","State":"running"}]`},
	}}
	client := NewWithRunner(runner, "owned-")
	detail, err := client.ImageDetail(context.Background(), "abcdef012345")
	if err != nil {
		t.Fatal(err)
	}
	if detail.ID != "abcdef0123456789abcdef" || detail.Platform != "linux/amd64" || detail.Created != time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("ImageDetail() = %#v", detail)
	}
	if !reflect.DeepEqual(detail.Ports, []string{"443/tcp", "80/tcp"}) || !reflect.DeepEqual(detail.Command, []string{"caddy", "run"}) || detail.WorkingDir != "/srv" {
		t.Fatalf("configuration = %#v", detail)
	}
	wantLayers := []protocol.ImageLayer{{CreatedBy: "ADD rootfs", Size: 4000}, {CreatedBy: `CMD ["caddy"]`, Size: 0}}
	if !reflect.DeepEqual(detail.Layers, wantLayers) {
		t.Fatalf("layers = %#v, want oldest first %#v", detail.Layers, wantLayers)
	}
	wantUsers := []protocol.ImageUse{{ContainerID: "c1", Container: "owned-web", State: "running"}}
	if !reflect.DeepEqual(detail.Containers, wantUsers) {
		t.Fatalf("users = %#v, want %#v (other prefixes stay hidden)", detail.Containers, wantUsers)
	}
	if len(runner.calls) != 3 || runner.calls[0][0] != "image" || runner.calls[1][0] != "history" || runner.calls[2][0] != "ps" {
		t.Fatalf("calls = %#v", runner.calls)
	}
}

func TestImageDetailRejectsIDsThatAreNotHex(t *testing.T) {
	for _, bad := range []string{"--all", "abc", "owned-web", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdefa", "ABCDEF0123"} {
		runner := &fakeRunner{}
		client := NewWithRunner(runner)
		if _, err := client.ImageDetail(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "not a hexadecimal") {
			t.Fatalf("ImageDetail(%q) error = %v", bad, err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("ImageDetail(%q) ran %#v", bad, runner.calls)
		}
	}
}

// benchmarkHostFixtures writes the command output of a host with the given
// numbers of containers and volumes, each container mounting one volume.
func benchmarkHostFixtures(containers, volumes int) (ps, volumeList, inspect string) {
	var p, v, in strings.Builder
	p.WriteString("[")
	v.WriteString("[")
	for i := 0; i < volumes; i++ {
		if i > 0 {
			v.WriteString(",")
		}
		fmt.Fprintf(&v, `{"Name":"owned-v%04d","Driver":"local","Mountpoint":"/volumes/owned-v%04d"}`, i, i)
	}
	v.WriteString("]")
	for i := 0; i < containers; i++ {
		if i > 0 {
			p.WriteString(",")
		}
		fmt.Fprintf(&p, `{"Id":"id%06d","Names":["owned-c%06d"],"Image":"example/web:1","State":"running","Status":"Up"}`, i, i)
		fmt.Fprintf(&in, "id%06d\t[{\"Type\":\"volume\",\"Name\":\"owned-v%04d\",\"Destination\":\"/data\",\"RW\":true}]\n", i, i%volumes)
	}
	p.WriteString("]")
	return p.String(), v.String(), in.String()
}

// BenchmarkImagesWithUsers is the image list on a busy host: 500 images, 1000
// containers, and the containers that run each image.
func BenchmarkImagesWithUsers(b *testing.B) {
	var images, ps strings.Builder
	images.WriteString("[")
	ps.WriteString("[")
	for i := 0; i < 500; i++ {
		if i > 0 {
			images.WriteString(",")
		}
		fmt.Fprintf(&images, `{"Id":"%064x","RepoTags":["example/app%03d:latest"],"Size":%d,"Created":1700000000}`, i, i, 1000+i)
	}
	for i := 0; i < 1000; i++ {
		if i > 0 {
			ps.WriteString(",")
		}
		fmt.Fprintf(&ps, `{"Id":"id%06d","Names":["owned-c%06d"],"ImageID":"%064x","State":"running"}`, i, i, i%500)
	}
	images.WriteString("]")
	ps.WriteString("]")
	imageList, containerList := images.String(), ps.String()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		runner := &fakeRunner{responses: []response{{stdout: imageList}, {stdout: containerList}}}
		client := NewWithRunner(runner, "owned-")
		out, err := client.Images(context.Background())
		if err != nil || len(out) != 500 || len(out[0].Containers) != 2 {
			b.Fatalf("Images() = %d images, %v", len(out), err)
		}
	}
}

// BenchmarkVolumesWithMounts is the volume list on a busy host: 1000 containers
// and 200 volumes, with the mounts read in one podman call.
func BenchmarkVolumesWithMounts(b *testing.B) {
	ps, volumeList, inspect := benchmarkHostFixtures(1000, 200)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		runner := &fakeRunner{responses: []response{{stdout: volumeList}, {stdout: ps}, {stdout: inspect}}}
		client := NewWithRunner(runner, "owned-")
		volumes, err := client.Volumes(context.Background())
		if err != nil || len(volumes) != 200 || len(volumes[0].Mounts) == 0 {
			b.Fatalf("Volumes() = %d volumes, %v", len(volumes), err)
		}
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
