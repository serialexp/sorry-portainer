package stacks

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/imagecheck"
	"github.com/serialexp/sorry-portainer/internal/podman"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

func digest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

type fakeImages struct {
	local   []podman.LocalImage
	listErr error
	pulled  []string
	pullErr error
}

func (f *fakeImages) LocalImages(context.Context) ([]podman.LocalImage, error) {
	return f.local, f.listErr
}

func (f *fakeImages) Pull(_ context.Context, ref string) (string, error) {
	f.pulled = append(f.pulled, ref)
	return "0123456789ab", f.pullErr
}

type fakeChecker struct {
	remotes   map[string]imagecheck.Remote
	asked     []string
	forced    bool
	built     []string
	forgotten []string
}

func (f *fakeChecker) Latest(_ context.Context, refs []string, force bool) map[string]imagecheck.Remote {
	f.asked = append(f.asked, refs...)
	f.forced = force
	out := map[string]imagecheck.Remote{}
	for _, ref := range refs {
		out[ref] = f.remotes[ref]
	}
	return out
}

func (f *fakeChecker) Built(_ context.Context, ref, digest string) (time.Time, error) {
	f.built = append(f.built, ref+"@"+digest)
	return time.Unix(1_790_000_000, 0), nil
}

func (f *fakeChecker) Forget(ref string) { f.forgotten = append(f.forgotten, ref) }

const imageStack = `services:
  app:
    image: docker.io/aeolun/homes
  worker:
    image: docker.io/aeolun/homes:latest
  db:
    image: mysql:8
  cache:
    image: redis:7
  pinned:
    image: quay.io/libpod/busybox@sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `
  built:
    build: .
  templated:
    image: ${APP_IMAGE}
  mirror:
    image: quay.io/team/mirror:1
`

func imageManager(t *testing.T, containers []podman.ProjectContainer) (*Manager, *fakeImages, *fakeChecker, *recordingExecutor) {
	t.Helper()
	executor := &recordingExecutor{}
	m := New(t.TempDir(), "agent", executor)
	if _, err := m.Save(context.Background(), "homes", imageStack, nil, nil); err != nil {
		t.Fatal(err)
	}
	m.EnableRuntimeStatus(&fakeLister{containers: map[string][]podman.ProjectContainer{m.project("homes"): containers}})
	images := &fakeImages{local: []podman.LocalImage{
		{ID: "old", Names: []string{"docker.io/aeolun/homes:previous"}, RepoDigests: []string{"docker.io/aeolun/homes@" + digest("a")}, Digest: digest("a")},
		// Pulled through a multi-platform tag: the index's digest and the platform's.
		{ID: "new", Names: []string{"docker.io/aeolun/homes:latest"}, RepoDigests: []string{"docker.io/aeolun/homes@" + digest("c"), "docker.io/aeolun/homes@" + digest("b")}, Digest: digest("b")},
		{ID: "mysql", Names: []string{"docker.io/library/mysql:8"}, RepoDigests: []string{"docker.io/library/mysql@" + digest("d")}, Digest: digest("d")},
	}}
	checker := &fakeChecker{remotes: map[string]imagecheck.Remote{
		"docker.io/aeolun/homes:latest": {Digest: digest("b"), CheckedAt: time.Unix(1_800_000_000, 0)},
		"mysql:8":                       {Digest: digest("d"), CheckedAt: time.Unix(1_800_000_000, 0)},
		"redis:7":                       {Digest: digest("e"), CheckedAt: time.Unix(1_800_000_000, 0)},
		"quay.io/team/mirror:1":         {Digest: digest("f"), CheckedAt: time.Unix(1_700_000_000, 0), Err: "dial tcp: i/o timeout"},
	}}
	m.EnableImageChecks(images, checker)
	return m, images, checker, executor
}

func TestImagesComparesEachServiceWithItsTag(t *testing.T) {
	m, _, checker, _ := imageManager(t, []podman.ProjectContainer{
		// app runs the image the tag pointed to before the last push.
		{Name: "agent-homes_app_1", Service: "app", Image: "docker.io/aeolun/homes:latest", ImageID: "old", State: "running"},
		{Name: "agent-homes_worker_1", Service: "worker", Image: "docker.io/aeolun/homes:latest", ImageID: "new", State: "running"},
	})
	got, err := m.Images(context.Background(), "homes", true)
	if err != nil {
		t.Fatal(err)
	}
	checked := int64(1_800_000_000)
	want := []protocol.ServiceImage{
		{Service: "app", Image: "docker.io/aeolun/homes:latest", Status: protocol.ImageBehind, Registry: "docker.io",
			RunningDigest: digest("a"), LatestDigest: digest("b"), LatestCreated: 1_790_000_000, CheckedAt: checked},
		{Service: "worker", Image: "docker.io/aeolun/homes:latest", Status: protocol.ImageCurrent, Registry: "docker.io",
			RunningDigest: digest("b"), LatestDigest: digest("b"), CheckedAt: checked},
		// Without a container, the local image the Compose file's short name names.
		{Service: "db", Image: "mysql:8", Status: protocol.ImageCurrent, Registry: "docker.io",
			RunningDigest: digest("d"), LatestDigest: digest("d"), CheckedAt: checked},
		// Not on the host at all.
		{Service: "cache", Image: "redis:7", Status: protocol.ImageBehind, Registry: "docker.io",
			LatestDigest: digest("e"), LatestCreated: 1_790_000_000, CheckedAt: checked},
		{Service: "pinned", Image: "quay.io/libpod/busybox@" + digest("a"), Status: protocol.ImagePinned},
		{Service: "built", Status: protocol.ImageLocal, Error: "The service has no image; Compose builds it on the host."},
		{Service: "templated", Image: "${APP_IMAGE}", Status: protocol.ImageUnchecked,
			Error: "The image name uses variables, which Compose resolves when the stack starts."},
		// The registry did not answer: the last good digest and time stay.
		{Service: "mirror", Image: "quay.io/team/mirror:1", Status: protocol.ImageUnknown, Registry: "quay.io",
			LatestDigest: digest("f"), CheckedAt: 1_700_000_000, Error: "dial tcp: i/o timeout"},
	}
	if !reflect.DeepEqual(got.Services, want) {
		t.Fatalf("services:\n got %+v\nwant %+v", got.Services, want)
	}
	if !checker.forced {
		t.Fatal("force was not passed on")
	}
	wantAsked := []string{"docker.io/aeolun/homes:latest", "docker.io/aeolun/homes:latest", "mysql:8", "redis:7", "quay.io/team/mirror:1"}
	if !reflect.DeepEqual(checker.asked, wantAsked) {
		t.Fatalf("asked the registry for %v, want %v", checker.asked, wantAsked)
	}
	wantBuilt := []string{"docker.io/aeolun/homes:latest@" + digest("b"), "redis:7@" + digest("e")}
	if !reflect.DeepEqual(checker.built, wantBuilt) {
		t.Fatalf("build times asked for %v, want only the services behind: %v", checker.built, wantBuilt)
	}
}

// A service is behind when any of its containers runs another image, and
// shows that container's digest.
func TestImagesScaledServiceIsBehindWhenAnyContainerIs(t *testing.T) {
	m, _, _, _ := imageManager(t, []podman.ProjectContainer{
		{Name: "agent-homes_worker_1", Service: "worker", Image: "docker.io/aeolun/homes:latest", ImageID: "new", State: "running"},
		{Name: "agent-homes_worker_2", Service: "worker", Image: "docker.io/aeolun/homes:latest", ImageID: "old", State: "running"},
	})
	got, err := m.Images(context.Background(), "homes", false)
	if err != nil {
		t.Fatal(err)
	}
	worker := got.Services[slices.IndexFunc(got.Services, func(s protocol.ServiceImage) bool { return s.Service == "worker" })]
	if worker.Status != protocol.ImageBehind || worker.RunningDigest != digest("a") {
		t.Fatalf("worker = %+v", worker)
	}
}

func TestImagesWithoutChecksOrRuntime(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if _, err := m.Save(context.Background(), "homes", imageStack, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Images(context.Background(), "homes", false); !errors.Is(err, errImageChecksUnavailable) {
		t.Fatalf("without image checks: %v", err)
	}
	if _, err := m.Update(context.Background(), "homes", "app"); !errors.Is(err, errImageChecksUnavailable) {
		t.Fatalf("update without image checks: %v", err)
	}
	m.EnableImageChecks(&fakeImages{}, &fakeChecker{})
	if _, err := m.Images(context.Background(), "homes", false); !errors.Is(err, errImageChecksUnavailable) {
		t.Fatalf("without runtime status: %v", err)
	}
	if _, err := m.Images(context.Background(), "../etc", false); err == nil {
		t.Fatal("an invalid name was accepted")
	}
}

func TestImagesListFailureIsAnError(t *testing.T) {
	m, images, _, _ := imageManager(t, nil)
	images.listErr = errors.New("podman is gone")
	if _, err := m.Images(context.Background(), "homes", false); err == nil || !strings.Contains(err.Error(), "podman is gone") {
		t.Fatalf("err = %v", err)
	}
}

// recreatingExecutor stands in for podman-compose: when it runs, the
// service's container is replaced with one built from image in state.
type recreatingExecutor struct {
	recordingExecutor
	lister  *fakeLister
	project string
	image   string
	state   string
	remove  bool
}

func (e *recreatingExecutor) Run(ctx context.Context, args []string, dir string, env map[string]string) (string, error) {
	out, err := e.recordingExecutor.Run(ctx, args, dir, env)
	service := args[len(args)-1]
	var kept []podman.ProjectContainer
	for _, c := range e.lister.containers[e.project] {
		if c.Service != service {
			kept = append(kept, c)
		}
	}
	if !e.remove {
		kept = append(kept, podman.ProjectContainer{Name: "agent-homes_" + service + "_1", Service: service,
			Image: "docker.io/aeolun/homes:latest", ImageID: e.image, State: e.state})
	}
	e.lister.containers[e.project] = kept
	return out, err
}

// recreating swaps the manager's executor for one that recreates the
// service from image in state.
func recreating(m *Manager, image, state string) *recreatingExecutor {
	lister := m.runtime.(*fakeLister)
	e := &recreatingExecutor{lister: lister, project: m.project("homes"), image: image, state: state}
	m.executor = e
	return e
}

func TestUpdateRunningStackPullsAndRecreatesTheService(t *testing.T) {
	m, images, checker, _ := imageManager(t, []podman.ProjectContainer{
		{Name: "agent-homes_app_1", Service: "app", Image: "docker.io/aeolun/homes:latest", ImageID: "old", State: "running"},
	})
	executor := recreating(m, "0123456789ab", "running")
	result, err := m.Update(context.Background(), "homes", "app")
	if err != nil || !result.Success || result.Operation != "update" || result.Name != "homes" {
		t.Fatalf("update = %+v, %v", result, err)
	}
	if !reflect.DeepEqual(images.pulled, []string{"docker.io/aeolun/homes:latest"}) {
		t.Fatalf("pulled %v", images.pulled)
	}
	calls := executor.callsSnapshot()
	wantArgs := []string{"-p", m.project("homes"), "-f", runtimeComposeFile, "up", "-d", "--force-recreate", "app"}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].args, wantArgs) || calls[0].dir != m.dir("homes") {
		t.Fatalf("compose calls = %+v, want %v", calls, wantArgs)
	}
	if !reflect.DeepEqual(checker.forgotten, []string{"docker.io/aeolun/homes:latest"}) {
		t.Fatalf("forgotten = %v", checker.forgotten)
	}
	for _, line := range []string{"$ podman pull docker.io/aeolun/homes:latest", "0123456789ab", "$ podman-compose " + strings.Join(wantArgs, " "), "ok"} {
		if !strings.Contains(result.Output, line) {
			t.Fatalf("output %q lacks %q", result.Output, line)
		}
	}
	// The update is not an up: the desired state stays as it was.
	stack, err := m.Inspect(context.Background(), "homes")
	if err != nil || stack.Desired != "" {
		t.Fatalf("desired = %q, %v", stack.Desired, err)
	}
}

// podman-compose 1.0.6 exits 0 when `podman run` fails; the update checks
// that the service runs the pulled image.
func TestUpdateFailsWhenTheServiceDoesNotRunTheNewImage(t *testing.T) {
	for _, tc := range []struct {
		name, image, state string
		remove             bool
		want               string
	}{
		{name: "old image", image: "old", state: "running", want: "runs image old, not the pulled 0123456789ab"},
		{name: "not running", image: "0123456789ab", state: "exited", want: "agent-homes_app_1 is exited after the update"},
		{name: "no container", remove: true, want: "app has no container after the update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _, _ := imageManager(t, []podman.ProjectContainer{
				{Name: "agent-homes_app_1", Service: "app", Image: "docker.io/aeolun/homes:latest", ImageID: "old", State: "running"},
			})
			e := recreating(m, tc.image, tc.state)
			e.remove = tc.remove
			result, err := m.Update(context.Background(), "homes", "app")
			if err == nil || result.Success || !strings.Contains(result.Output, tc.want) {
				t.Fatalf("update = %+v, %v; want %q", result, err, tc.want)
			}
		})
	}
}

func TestUpdateStoppedStackOnlyPulls(t *testing.T) {
	m, images, _, executor := imageManager(t, []podman.ProjectContainer{
		{Name: "agent-homes_app_1", Service: "app", Image: "docker.io/aeolun/homes:latest", ImageID: "old", State: "exited"},
	})
	result, err := m.Update(context.Background(), "homes", "db")
	if err != nil || !result.Success {
		t.Fatalf("update = %+v, %v", result, err)
	}
	if !reflect.DeepEqual(images.pulled, []string{"mysql:8"}) || len(executor.callsSnapshot()) != 0 {
		t.Fatalf("pulled %v, compose calls %d", images.pulled, len(executor.callsSnapshot()))
	}
	if !strings.Contains(result.Output, "The stack is stopped; db uses the new image when the stack starts.") {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestUpdatePullFailureIsAFailedOperation(t *testing.T) {
	m, images, checker, executor := imageManager(t, []podman.ProjectContainer{
		{Name: "agent-homes_app_1", Service: "app", Image: "docker.io/aeolun/homes:latest", ImageID: "old", State: "running"},
	})
	images.pullErr = errors.New("podman pull: exit status 125: unauthorized")
	result, err := m.Update(context.Background(), "homes", "app")
	if err == nil || result.Success || result.Operation != "update" || !strings.Contains(result.Output, "unauthorized") {
		t.Fatalf("update = %+v, %v", result, err)
	}
	if len(executor.callsSnapshot()) != 0 {
		t.Fatal("compose ran after a failed pull")
	}
	if len(checker.forgotten) != 1 {
		t.Fatalf("forgotten = %v", checker.forgotten)
	}
}

func TestUpdateRefusesWhatCannotBePulled(t *testing.T) {
	m, images, _, _ := imageManager(t, nil)
	for service, want := range map[string]string{
		"nope":      `has no service "nope"`,
		"pinned":    "its image is pinned",
		"built":     "Compose builds it on the host",
		"templated": "uses variables",
	} {
		if _, err := m.Update(context.Background(), "homes", service); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("update %s: %v, want %q", service, err, want)
		}
	}
	if len(images.pulled) != 0 {
		t.Fatalf("pulled %v", images.pulled)
	}
}
