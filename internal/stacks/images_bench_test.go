package stacks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/imagecheck"
	"github.com/serialexp/sorry-portainer/internal/podman"
)

// BenchmarkImagesLargeStack measures the agent's own work in a check (the
// Compose parse, matching containers and local images, comparing digests)
// for 200 services on a host with 5000 images, with every registry answer
// cached. Run: go test -run '^$' -bench ImagesLargeStack ./internal/stacks
func BenchmarkImagesLargeStack(b *testing.B) {
	const services, extraImages = 200, 5000
	executor := &recordingExecutor{}
	m := New(b.TempDir(), "agent", executor)
	var compose strings.Builder
	compose.WriteString("services:\n")
	var containers []podman.ProjectContainer
	var local []podman.LocalImage
	remotes := map[string]imagecheck.Remote{}
	for i := range services {
		ref := fmt.Sprintf("docker.io/team/svc%d:latest", i)
		fmt.Fprintf(&compose, "  svc%d:\n    image: %s\n", i, ref)
		id := fmt.Sprintf("id%d", i)
		containers = append(containers, podman.ProjectContainer{Name: fmt.Sprintf("agent-big_svc%d_1", i), Service: fmt.Sprintf("svc%d", i), Image: ref, ImageID: id, State: "running"})
		d := "sha256:" + fmt.Sprintf("%064x", i)
		local = append(local, podman.LocalImage{ID: id, Names: []string{ref}, RepoDigests: []string{"docker.io/team/svc" + fmt.Sprint(i) + "@" + d}, Digest: d})
		remotes[ref] = imagecheck.Remote{Digest: d, CheckedAt: time.Unix(1_800_000_000, 0)}
	}
	for i := range extraImages {
		d := "sha256:" + fmt.Sprintf("%064x", services+i)
		local = append(local, podman.LocalImage{ID: fmt.Sprintf("other%d", i), Names: []string{fmt.Sprintf("quay.io/other/img%d:1", i)}, RepoDigests: []string{fmt.Sprintf("quay.io/other/img%d@%s", i, d)}, Digest: d})
	}
	if _, err := m.Save(context.Background(), "big", compose.String(), nil, nil); err != nil {
		b.Fatal(err)
	}
	m.EnableRuntimeStatus(&fakeLister{containers: map[string][]podman.ProjectContainer{m.project("big"): containers}})
	m.EnableImageChecks(&fakeImages{local: local}, &fakeChecker{remotes: remotes})
	b.ReportAllocs()
	for b.Loop() {
		got, err := m.Images(context.Background(), "big", false)
		if err != nil || len(got.Services) != services {
			b.Fatalf("%d services, %v", len(got.Services), err)
		}
	}
}
