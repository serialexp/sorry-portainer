package imagecheck

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// fakeRegistry answers from a map and counts requests.
type fakeRegistry struct {
	mu      sync.Mutex
	digests map[string]string
	errs    map[string]error
	heads   map[string]int
	created int
	// gate, when set, holds every Head until it is closed.
	gate chan struct{}
	// running counts Heads in progress, peak the most at once.
	running, peak atomic.Int32
}

func newFake() *fakeRegistry {
	return &fakeRegistry{digests: map[string]string{}, errs: map[string]error{}, heads: map[string]int{}}
}

func (f *fakeRegistry) Head(ctx context.Context, ref string) (string, error) {
	n := f.running.Add(1)
	defer f.running.Add(-1)
	for {
		peak := f.peak.Load()
		if n <= peak || f.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads[ref]++
	if err := f.errs[ref]; err != nil {
		return "", err
	}
	return f.digests[ref], nil
}

func (f *fakeRegistry) Created(ctx context.Context, ref, digest string) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created++
	if err := f.errs[ref+"@"+digest]; err != nil {
		return time.Time{}, err
	}
	return time.Unix(1_700_000_000, 0), nil
}

func (f *fakeRegistry) headCount(ref string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.heads[ref]
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func TestLatestCachesForTheTTL(t *testing.T) {
	fake := newFake()
	fake.digests["docker.io/a/b:latest"] = "sha256:one"
	now := &clock{now: time.Unix(1000, 0)}
	checker := New(fake, Options{TTL: time.Minute, Now: now.Now})
	ctx := context.Background()

	got := checker.Latest(ctx, []string{"docker.io/a/b:latest"}, false)["docker.io/a/b:latest"]
	if got.Digest != "sha256:one" || !got.CheckedAt.Equal(now.now) || got.Err != "" {
		t.Fatalf("first check = %+v", got)
	}
	fake.digests["docker.io/a/b:latest"] = "sha256:two"
	now.now = now.now.Add(59 * time.Second)
	if got := checker.Latest(ctx, []string{"docker.io/a/b:latest"}, false)["docker.io/a/b:latest"]; got.Digest != "sha256:one" {
		t.Fatalf("within the TTL the cached digest is used, got %+v", got)
	}
	if n := fake.headCount("docker.io/a/b:latest"); n != 1 {
		t.Fatalf("heads within the TTL = %d, want 1", n)
	}
	now.now = now.now.Add(time.Second)
	if got := checker.Latest(ctx, []string{"docker.io/a/b:latest"}, false)["docker.io/a/b:latest"]; got.Digest != "sha256:two" {
		t.Fatalf("after the TTL the registry is asked again, got %+v", got)
	}
}

func TestLatestForceAndForgetAskAgain(t *testing.T) {
	fake := newFake()
	fake.digests["r:1"] = "sha256:one"
	checker := New(fake, Options{})
	ctx := context.Background()
	checker.Latest(ctx, []string{"r:1"}, false)
	checker.Latest(ctx, []string{"r:1"}, true)
	if n := fake.headCount("r:1"); n != 2 {
		t.Fatalf("force: heads = %d, want 2", n)
	}
	checker.Forget("r:1")
	checker.Latest(ctx, []string{"r:1"}, false)
	if n := fake.headCount("r:1"); n != 3 {
		t.Fatalf("forget: heads = %d, want 3", n)
	}
}

func TestLatestFailureKeepsTheLastGoodAnswer(t *testing.T) {
	fake := newFake()
	fake.digests["r:1"] = "sha256:one"
	now := &clock{now: time.Unix(1000, 0)}
	checker := New(fake, Options{TTL: time.Minute, Now: now.Now})
	ctx := context.Background()
	checker.Latest(ctx, []string{"r:1"}, false)
	good := now.now

	fake.errs["r:1"] = errors.New("dial tcp: no route to host")
	now.now = now.now.Add(2 * time.Hour)
	got := checker.Latest(ctx, []string{"r:1"}, false)["r:1"]
	if got.Digest != "sha256:one" || !got.CheckedAt.Equal(good) || !strings.Contains(got.Err, "no route") {
		t.Fatalf("failed check = %+v, want the last good digest, its time and the error", got)
	}
	// The failure is cached too: a down registry is not asked on every open.
	checker.Latest(ctx, []string{"r:1"}, false)
	if n := fake.headCount("r:1"); n != 2 {
		t.Fatalf("heads after a cached failure = %d, want 2", n)
	}
	// A reference never answered has no digest, only the error.
	fake.errs["r:2"] = errors.New("unauthorized")
	if got := checker.Latest(ctx, []string{"r:2"}, false)["r:2"]; got.Digest != "" || !got.CheckedAt.IsZero() || got.Err != "unauthorized" {
		t.Fatalf("never answered = %+v", got)
	}
	// Success clears the error.
	delete(fake.errs, "r:1")
	if got := checker.Latest(ctx, []string{"r:1"}, true)["r:1"]; got.Err != "" || !got.CheckedAt.Equal(now.now) {
		t.Fatalf("after recovery = %+v", got)
	}
}

func TestLatestAsksOncePerReferenceAndBoundsConcurrency(t *testing.T) {
	fake := newFake()
	fake.gate = make(chan struct{})
	refs := []string{"a:1", "b:1", "c:1", "d:1", "e:1", "f:1", "a:1", "b:1"}
	for _, ref := range refs {
		fake.digests[ref] = "sha256:" + ref[:1]
	}
	checker := New(fake, Options{Concurrency: 2})
	done := make(chan map[string]Remote)
	go func() { done <- checker.Latest(context.Background(), refs, false) }()
	// A second, concurrent check of the same references shares the requests
	// already running for them.
	second := make(chan map[string]Remote)
	go func() { second <- checker.Latest(context.Background(), []string{"a:1"}, true) }()
	deadline := time.Now().Add(5 * time.Second)
	for fake.running.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(fake.gate)
	got := <-done
	<-second
	if len(got) != 6 {
		t.Fatalf("answers = %d, want 6 distinct references", len(got))
	}
	for ref, remote := range got {
		if remote.Digest != "sha256:"+ref[:1] {
			t.Fatalf("%s = %+v", ref, remote)
		}
	}
	if peak := fake.peak.Load(); peak > 3 {
		// Two from the first check, at most one more from the second.
		t.Fatalf("peak concurrent heads = %d, want at most 3", peak)
	}
	for _, ref := range []string{"b:1", "c:1"} {
		if n := fake.headCount(ref); n != 1 {
			t.Fatalf("%s heads = %d, want 1", ref, n)
		}
	}
}

func TestLatestCancelledBeforeAsking(t *testing.T) {
	fake := newFake()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checker := New(fake, Options{Concurrency: 1})
	got := checker.Latest(ctx, []string{"a:1", "b:1"}, false)
	for ref, remote := range got {
		if remote.Err == "" {
			t.Fatalf("%s = %+v, want the cancellation", ref, remote)
		}
	}
}

func TestEntriesAreBounded(t *testing.T) {
	fake := newFake()
	now := &clock{now: time.Unix(1000, 0)}
	checker := New(fake, Options{MaxEntries: 3, Now: now.Now})
	for _, ref := range []string{"a:1", "b:1", "c:1", "d:1"} {
		now.now = now.now.Add(time.Second)
		checker.Latest(context.Background(), []string{ref}, false)
	}
	if len(checker.entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(checker.entries))
	}
	if _, kept := checker.entries["a:1"]; kept {
		t.Fatal("the oldest entry was kept")
	}
}

func TestBuiltIsCachedPerDigestAndFailuresAreNot(t *testing.T) {
	fake := newFake()
	checker := New(fake, Options{MaxEntries: 2})
	ctx := context.Background()
	for range 3 {
		if _, err := checker.Built(ctx, "r:1", "sha256:one"); err != nil {
			t.Fatal(err)
		}
	}
	if fake.created != 1 {
		t.Fatalf("created requests = %d, want 1", fake.created)
	}
	fake.errs["r:1@sha256:bad"] = errors.New("manifest unknown")
	for range 2 {
		if _, err := checker.Built(ctx, "r:1", "sha256:bad"); err == nil {
			t.Fatal("want the error")
		}
	}
	if fake.created != 3 {
		t.Fatalf("created requests = %d, want failures retried", fake.created)
	}
	checker.Built(ctx, "r:1", "sha256:two")
	checker.Built(ctx, "r:1", "sha256:three")
	if len(checker.built) != 2 {
		t.Fatalf("built entries = %d, want 2", len(checker.built))
	}
}

func TestClassify(t *testing.T) {
	for ref, want := range map[string]Kind{
		"docker.io/aeolun/homes:latest": Tagged,
		"mysql:8":                       Tagged,
		"docker.io/aeolun/homes":        Tagged,
		"quay.io/libpod/busybox@sha256:" + strings.Repeat("a", 64): Pinned,
		"localhost/myapp:latest":                                   Local,
		"localhost:5000/myapp":                                     Local,
	} {
		got, err := Classify(ref)
		if err != nil || got != want {
			t.Errorf("Classify(%q) = %q, %v; want %q", ref, got, err, want)
		}
	}
	for _, ref := range []string{"", "-v", "UPPER/case", "${IMAGE}"} {
		if _, err := Classify(ref); err == nil {
			t.Errorf("Classify(%q) accepted", ref)
		}
	}
}

func TestDigestsForMatchesOneRepositoryInAnySpelling(t *testing.T) {
	a := "sha256:" + strings.Repeat("a", 64)
	b := "sha256:" + strings.Repeat("b", 64)
	c := "sha256:" + strings.Repeat("c", 64)
	repoDigests := []string{"docker.io/library/mysql@" + a, "docker.io/library/mysql@" + b, "quay.io/other/mysql@" + c, "garbage"}
	got := DigestsFor("mysql:8", repoDigests)
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("DigestsFor = %v", got)
	}
	if got := DigestsFor("index.docker.io/library/mysql:8", repoDigests); len(got) != 2 {
		t.Fatalf("index.docker.io spelling = %v", got)
	}
	if name := RegistryName("mysql:8"); name != "docker.io" {
		t.Fatalf("RegistryName = %q", name)
	}
	if name := RegistryName("quay.io/a/b:1"); name != "quay.io" {
		t.Fatalf("RegistryName = %q", name)
	}
}

// TestRemoteRegistryAgainstARegistry checks the go-containerregistry side
// against an in-process registry: HEAD sees a moved tag, Created reads the
// platform image's build time through a multi-platform index.
func TestRemoteRegistryAgainstARegistry(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	ref := host + "/team/app:latest"
	reg := remoteRegistry{keychain: authn.NewMultiKeychain(), platform: v1.Platform{OS: "linux", Architecture: "amd64"}, options: []name.Option{name.Insecure}}

	built := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	push := func(created time.Time) v1.Hash {
		t.Helper()
		amd64 := imageAt(t, created)
		arm64 := imageAt(t, created.Add(time.Hour))
		index := mutate.AppendManifests(empty.Index,
			mutate.IndexAddendum{Add: amd64, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}},
			mutate.IndexAddendum{Add: arm64, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}},
		)
		tag, err := name.NewTag(ref, name.Insecure)
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.WriteIndex(tag, index); err != nil {
			t.Fatal(err)
		}
		digest, err := index.Digest()
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	first := push(built)
	ctx := context.Background()
	got, err := reg.Head(ctx, ref)
	if err != nil || got != first.String() {
		t.Fatalf("Head = %q, %v; want %s", got, err, first)
	}
	second := push(built.Add(24 * time.Hour))
	if got, _ := reg.Head(ctx, ref); got != second.String() || second == first {
		t.Fatalf("Head after a push = %q, want %s", got, second)
	}
	created, err := reg.Created(ctx, ref, second.String())
	if err != nil || !created.Equal(built.Add(24*time.Hour)) {
		t.Fatalf("Created = %v, %v; want the amd64 image's time", created, err)
	}
	if _, err := reg.Head(ctx, host+"/team/missing:latest"); err == nil {
		t.Fatal("Head of a missing tag succeeded")
	}
}

func imageAt(t *testing.T, created time.Time) v1.Image {
	t.Helper()
	image, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	image, err = mutate.CreatedAt(image, v1.Time{Time: created})
	if err != nil {
		t.Fatal(err)
	}
	return image
}
