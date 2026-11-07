package podman

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSizedFile creates path with size bytes, along with its parent directories.
func writeSizedFile(t testing.TB, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVolumeSizeScanMeasuresPrefixedVolumesAndListsThem(t *testing.T) {
	owned := t.TempDir()
	writeSizedFile(t, filepath.Join(owned, "data"), 30)
	other := t.TempDir()
	writeSizedFile(t, filepath.Join(other, "data"), 1000)
	listing := fmt.Sprintf(`[{"Name":"owned-data","Driver":"local","Mountpoint":%q},{"Name":"other-data","Driver":"local","Mountpoint":%q}]`, owned, other)
	runner := &fakeRunner{responses: []response{
		{stdout: listing}, // the scan's volume ls
		{stdout: listing}, // the listing's volume ls
		{stdout: `[]`},    // the listing's ps: no containers
	}}
	client := NewWithRunner(runner, "owned-")
	client.measureVolumeSizes(context.Background(), nil)
	if len(runner.calls) != 1 {
		t.Fatalf("scan issued %d podman calls, want 1 (the volume listing)", len(runner.calls))
	}
	volumes, err := client.Volumes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 1 || volumes[0].SizeBytes == nil || *volumes[0].SizeBytes != 30 || volumes[0].SizePartial || volumes[0].SizeMeasuredAt == "" {
		t.Fatalf("volumes = %+v; want owned-data at 30 bytes, complete, with a measured time", volumes)
	}
}

func TestVolumeSizeScanKeepsSizeOnFailureAndDropsRemovedVolumes(t *testing.T) {
	dir := t.TempDir()
	writeSizedFile(t, filepath.Join(dir, "data"), 10)
	listing := fmt.Sprintf(`[{"Name":"owned-data","Driver":"local","Mountpoint":%q}]`, dir)
	runner := &fakeRunner{responses: []response{
		{stdout: listing}, // first scan measures the volume
		{stdout: listing}, // second scan finds the directory gone
		{stdout: `[]`},    // third scan no longer lists the volume
	}}
	client := NewWithRunner(runner, "owned-")
	ctx := context.Background()
	client.measureVolumeSizes(ctx, nil)
	if size, ok := client.sizes.get("owned-data"); !ok || size.bytes != 10 {
		t.Fatalf("first scan stored %+v, %t; want 10 bytes", size, ok)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	var logged []string
	client.measureVolumeSizes(ctx, func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	if len(logged) != 1 || !strings.Contains(logged[0], "owned-data") {
		t.Fatalf("failed measurement logged %q; want one line naming the volume", logged)
	}
	if size, ok := client.sizes.get("owned-data"); !ok || size.bytes != 10 {
		t.Fatalf("failed scan changed the stored size to %+v, %t; want the previous 10 bytes", size, ok)
	}

	client.measureVolumeSizes(ctx, nil)
	if _, ok := client.sizes.get("owned-data"); ok {
		t.Fatal("a volume that is no longer listed kept its size")
	}
}

// staticRunner answers every Podman call with the same output.
type staticRunner struct{ stdout string }

func (r staticRunner) Run(_ context.Context, stdout, _ io.Writer, _ ...string) error {
	_, err := io.WriteString(stdout, r.stdout)
	return err
}

// BenchmarkVolumeSizeScan measures one full scan of a host with 20 volumes of
// 2,500 one-byte files each: 50,000 files. The budget is that such a scan
// finishes well inside DefaultVolumeSizeInterval (30 minutes), so the agent is
// never still walking when the next scan is due. Run it on one machine and
// compare runs over time:
//
//	go test -run '^$' -bench VolumeSizeScan -benchmem ./internal/podman/
func BenchmarkVolumeSizeScan(b *testing.B) {
	const volumes, filesPerVolume = 20, 2500
	root := b.TempDir()
	var listing strings.Builder
	listing.WriteString("[")
	for v := 0; v < volumes; v++ {
		mount := filepath.Join(root, fmt.Sprintf("v%02d", v))
		for f := 0; f < filesPerVolume; f++ {
			// Spread the files over directories of 100, as a real volume would.
			writeSizedFile(b, filepath.Join(mount, fmt.Sprintf("d%02d", f/100), fmt.Sprintf("f%04d", f)), 1)
		}
		if v > 0 {
			listing.WriteString(",")
		}
		fmt.Fprintf(&listing, `{"Name":"owned-v%02d","Driver":"local","Mountpoint":%q}`, v, mount)
	}
	listing.WriteString("]")
	client := NewWithRunner(staticRunner{stdout: listing.String()}, "owned-")
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client.measureVolumeSizes(ctx, nil)
	}
	b.StopTimer()
	// Each file is one byte, so the stored sizes count every file in the scan.
	for v := 0; v < volumes; v++ {
		size, ok := client.sizes.get(fmt.Sprintf("owned-v%02d", v))
		if !ok || size.bytes != filesPerVolume || size.partial {
			b.Fatalf("volume %d measured %+v, %t; want %d bytes, complete", v, size, ok, filesPerVolume)
		}
	}
}
