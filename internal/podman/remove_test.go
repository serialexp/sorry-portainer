package podman

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRemoveVolumeRunsVolumeRmAndForgetsSize(t *testing.T) {
	runner := &fakeRunner{responses: []response{{}}}
	client := NewWithRunner(runner, "owned-")
	client.sizes.store("owned-data", volumeSize{bytes: 1})
	if err := client.RemoveVolume(context.Background(), "owned-data"); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"volume", "rm", "--", "owned-data"}}; !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
	if _, ok := client.sizes.get("owned-data"); ok {
		t.Fatal("a removed volume kept its size")
	}
}

func TestRemoveVolumeRefusesOutsidePrefixWithoutRunningPodman(t *testing.T) {
	runner := &fakeRunner{}
	client := NewWithRunner(runner, "owned-")
	for _, name := range []string{"other-data", ""} {
		if err := client.RemoveVolume(context.Background(), name); err == nil {
			t.Fatalf("RemoveVolume(%q) succeeded", name)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("refused removals ran podman: %#v", runner.calls)
	}
}

func TestRemoveVolumeReturnsPodmanRefusalAndKeepsSize(t *testing.T) {
	runner := &fakeRunner{responses: []response{{stderr: "volume owned-data is being used", err: errors.New("exit status 2")}}}
	client := NewWithRunner(runner, "owned-")
	client.sizes.store("owned-data", volumeSize{bytes: 1})
	err := client.RemoveVolume(context.Background(), "owned-data")
	if err == nil || !strings.Contains(err.Error(), "is being used") {
		t.Fatalf("RemoveVolume error = %v; want podman's refusal", err)
	}
	if _, ok := client.sizes.get("owned-data"); !ok {
		t.Fatal("a volume that was not removed lost its size")
	}
}

// imageInspectOutput is `podman image inspect` for one image with the given tags.
func imageInspectOutput(id string, tags ...string) string {
	quoted := make([]string, len(tags))
	for i, tag := range tags {
		quoted[i] = `"` + tag + `"`
	}
	return fmt.Sprintf(`[{"Id":%q,"RepoTags":[%s],"Size":1}]`, id, strings.Join(quoted, ","))
}

const (
	testImageID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	// elsewhereOutput is a ps listing with one container outside the prefix that runs the image.
	elsewhereOutput = `[{"Id":"c1","Names":["other-web"],"ImageID":"` + testImageID + `","State":"running"}]`
)

func TestRemoveImageRemovesSingleTagByID(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: imageInspectOutput(testImageID, "example/web:1")},
		{stdout: `[]`},
		{},
	}}
	client := NewWithRunner(runner, "owned-")
	if err := client.RemoveImage(context.Background(), "abcdef012345"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"image", "inspect", "--format", "json", "--", "abcdef012345"},
		{"ps", "--all", "--format", "json"},
		{"rmi", "--", testImageID},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestRemoveImageRemovesEveryTagByName(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: imageInspectOutput(testImageID, "example/web:1", "example/web:latest")},
		{stdout: `[]`},
		{},
		{},
	}}
	client := NewWithRunner(runner, "owned-")
	if err := client.RemoveImage(context.Background(), testImageID); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"image", "inspect", "--format", "json", "--", testImageID},
		{"ps", "--all", "--format", "json"},
		{"rmi", "--", "example/web:1"},
		{"rmi", "--", "example/web:latest"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestRemoveImageSkipsPlaceholderTag(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: imageInspectOutput(testImageID, "<none>:<none>")},
		{stdout: `[]`},
		{},
	}}
	client := NewWithRunner(runner, "owned-")
	if err := client.RemoveImage(context.Background(), testImageID); err != nil {
		t.Fatal(err)
	}
	if last := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(last, []string{"rmi", "--", testImageID}) {
		t.Fatalf("last call = %#v, want removal by ID", last)
	}
}

func TestRemoveImageRefusesImageInUseBeforeTouchingTags(t *testing.T) {
	cases := map[string]string{
		"this prefix": `[{"Id":"c1","Names":["owned-web"],"ImageID":"` + testImageID + `","State":"running"}]`,
		"elsewhere":   elsewhereOutput,
	}
	for name, ps := range cases {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{responses: []response{
				{stdout: imageInspectOutput(testImageID, "example/web:1", "example/web:latest")},
				{stdout: ps},
			}}
			client := NewWithRunner(runner, "owned-")
			err := client.RemoveImage(context.Background(), testImageID)
			if err == nil || !strings.Contains(err.Error(), "in use") {
				t.Fatalf("RemoveImage error = %v; want an in-use refusal", err)
			}
			for _, call := range runner.calls {
				if call[0] == "rmi" {
					t.Fatalf("an in-use image had a tag removed: %#v", runner.calls)
				}
			}
		})
	}
}

func TestRemoveImageReportsTagsRemovedBeforeAFailure(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: imageInspectOutput(testImageID, "example/web:1", "example/web:latest")},
		{stdout: `[]`},
		{},
		{stderr: "image is in use", err: errors.New("exit status 2")},
	}}
	client := NewWithRunner(runner, "owned-")
	err := client.RemoveImage(context.Background(), testImageID)
	if err == nil || !strings.Contains(err.Error(), "removed 1 of 2 tags") || !strings.Contains(err.Error(), "image is in use") {
		t.Fatalf("RemoveImage error = %v; want the count of removed tags and podman's message", err)
	}
}

func TestRemoveImageRunsNothingOnRejectedIDs(t *testing.T) {
	runner := &fakeRunner{}
	client := NewWithRunner(runner)
	for _, id := range []string{"", "../etc", "--force", "abc", "abcdef01234g"} {
		if err := client.RemoveImage(context.Background(), id); err == nil {
			t.Fatalf("RemoveImage(%q) succeeded", id)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("rejected IDs ran podman: %#v", runner.calls)
	}
}
