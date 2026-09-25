package agentsetup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHooksDropIn(t *testing.T) {
	got, err := HooksDropIn("/home/agent/.local/share/sorry-portainer/oci-hooks")
	if err != nil {
		t.Fatal(err)
	}
	want := "# Written by sorry-portainer-agent setup. The agent's OCI hook injects\n" +
		"# stack secrets into containers' /run/secrets tmpfs.\n" +
		"[engine]\nhooks_dir = [\"/usr/share/containers/oci/hooks.d\", \"/etc/containers/oci/hooks.d\", \"/home/agent/.local/share/sorry-portainer/oci-hooks\"]\n"
	if got != want {
		t.Fatalf("drop-in:\n%s", got)
	}
	for _, bad := range []string{"relative", "/a/../b", "/a\"b", "/a\\b", "/a\nb"} {
		if _, err := HooksDropIn(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestWriteHooksDropIn(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("refuses to run as root")
	}
	home := t.TempDir()
	hooks := filepath.Join(home, ".local", "share", "sorry-portainer", "oci-hooks")
	path, err := WriteHooksDropIn(home, hooks)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, ".config", "containers", "containers.conf.d", HooksDropInName) {
		t.Fatalf("path %s", path)
	}
	want, _ := HooksDropIn(hooks)
	if got, _ := os.ReadFile(path); string(got) != want {
		t.Fatalf("content %q", got)
	}
	if info, err := os.Stat(hooks); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("hooks dir %v %v", info, err)
	}
	// Rewriting is idempotent.
	if _, err := WriteHooksDropIn(home, hooks); err != nil {
		t.Fatal(err)
	}
}
