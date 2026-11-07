package podman

import (
	"context"
	"errors"
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

func TestRemoveImageRunsRmiOnHexIDsOnly(t *testing.T) {
	runner := &fakeRunner{responses: []response{{}}}
	client := NewWithRunner(runner)
	if err := client.RemoveImage(context.Background(), "abcdef012345"); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"rmi", "--", "abcdef012345"}}; !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
	for _, id := range []string{"", "../etc", "--force", "abc", "abcdef01234g"} {
		if err := client.RemoveImage(context.Background(), id); err == nil {
			t.Fatalf("RemoveImage(%q) succeeded", id)
		}
	}
	if len(runner.calls) != 1 {
		t.Fatalf("rejected IDs ran podman: %#v", runner.calls)
	}
}
