package stacks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRevisionReturnsEachSavedRevision(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	ctx := context.Background()
	if _, err := m.Save(ctx, "web", "services: {one: {}}\n", map[string]string{"TZ": "UTC"}, nil); err != nil {
		t.Fatal(err)
	}
	one := 1
	if _, err := m.Save(ctx, "web", "services: {two: {}}\n", map[string]string{"TZ": "Europe/Amsterdam"}, &one); err != nil {
		t.Fatal(err)
	}
	first, err := m.Revision(ctx, "web", 1)
	if err != nil || first.Version != 1 || first.ComposeYAML != "services: {one: {}}\n" || first.Environment["TZ"] != "UTC" {
		t.Fatalf("revision 1 = %#v, %v", first, err)
	}
	second, err := m.Revision(ctx, "web", 2)
	if err != nil || second.Version != 2 || second.ComposeYAML != "services: {two: {}}\n" || second.Environment["TZ"] != "Europe/Amsterdam" {
		t.Fatalf("revision 2 = %#v, %v", second, err)
	}
}

func TestRevisionRejectsUnknownNumbers(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if _, err := m.Save(context.Background(), "web", "services: {one: {}}\n", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{0, 2, -1} {
		if _, err := m.Revision(context.Background(), "web", version); !errors.Is(err, ErrRevisionNotFound) {
			t.Fatalf("revision %d error = %v, want not found", version, err)
		}
	}
	if _, err := m.Revision(context.Background(), "../web", 1); err == nil {
		t.Fatal("invalid stack name was accepted")
	}
}

func TestRevisionOfLegacyStackIsItsRegularComposeFile(t *testing.T) {
	m := New(t.TempDir(), "agent", &recordingExecutor{})
	if err := os.MkdirAll(m.dir("legacy"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.dir("legacy"), "compose.yaml"), []byte("services: {old: {}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	revision, err := m.Revision(context.Background(), "legacy", 1)
	if err != nil || revision.ComposeYAML != "services: {old: {}}\n" {
		t.Fatalf("legacy revision = %#v, %v", revision, err)
	}
}
