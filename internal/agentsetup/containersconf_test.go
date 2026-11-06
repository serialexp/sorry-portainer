package agentsetup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const hooksHeader = "# Written by sorry-portainer-agent setup. The agent's OCI hook injects\n" +
	"# stack secrets into containers' /run/secrets tmpfs.\n" +
	"[engine]\nhooks_dir = [\"/usr/share/containers/oci/hooks.d\", \"/etc/containers/oci/hooks.d\", \"/home/agent/.local/share/sorry-portainer/oci-hooks\"]\n"

func TestContainersConfHooksOnly(t *testing.T) {
	got, err := ContainersConf{HooksDir: "/home/agent/.local/share/sorry-portainer/oci-hooks"}.Render()
	if err != nil {
		t.Fatal(err)
	}
	if got != hooksHeader {
		t.Fatalf("drop-in:\n%s", got)
	}
	for _, bad := range []string{"", "relative", "/a/../b", "/a\"b", "/a\\b", "/a\nb"} {
		if _, err := (ContainersConf{HooksDir: bad}).Render(); err == nil {
			t.Errorf("accepted hooks dir %q", bad)
		}
	}
}

func TestContainersConfPastaAndHostLoopback(t *testing.T) {
	got, err := ContainersConf{
		HooksDir:     "/home/agent/.local/share/sorry-portainer/oci-hooks",
		Forwarder:    ForwarderPasta,
		HostLoopback: "169.254.1.3",
	}.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := hooksHeader + "\n[network]\n" +
		"# Published ports see clients' own addresses. Podman requires that no\n" +
		"# container runs while this changes.\n" +
		"rootless_port_forwarder = \"pasta\"\n" +
		"# Containers reach the host's 127.0.0.1 at 169.254.1.3.\n" +
		"pasta_options = [\"--map-host-loopback\", \"169.254.1.3\"]\n"
	if got != want {
		t.Fatalf("drop-in:\n%s", got)
	}
	// Each setting stands alone.
	loopbackOnly, err := ContainersConf{HooksDir: "/h", HostLoopback: "169.254.1.3"}.Render()
	if err != nil || strings.Contains(loopbackOnly, "rootless_port_forwarder") || !strings.Contains(loopbackOnly, "--map-host-loopback") {
		t.Fatalf("loopback only: %v\n%s", err, loopbackOnly)
	}
	if _, err := (ContainersConf{HooksDir: "/h", Forwarder: "slirp"}).Render(); err == nil {
		t.Fatal("accepted an unknown forwarder")
	}
	explicit, _ := ContainersConf{HooksDir: "/h", Forwarder: ForwarderRootlessport}.Render()
	if strings.Contains(explicit, "[network]") {
		t.Fatalf("rootlessport wrote a network table:\n%s", explicit)
	}
}

func TestParseHostLoopback(t *testing.T) {
	for in, want := range map[string]string{"169.254.1.3": "169.254.1.3", "fd00::0:3": "fd00::3"} {
		if got, err := ParseHostLoopback(in); err != nil || got != want {
			t.Errorf("ParseHostLoopback(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "host", "127.0.0.1", "::1", "0.0.0.0", "224.0.0.1", "169.254.1.2", "fe80::1%eth0", "169.254.1.3,x"} {
		if _, err := ParseHostLoopback(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	// Render takes canonical addresses only, so the file is stable.
	if _, err := (ContainersConf{HooksDir: "/h", HostLoopback: "fd00::0:3"}).Render(); err == nil {
		t.Fatal("rendered a non-canonical address")
	}
}

func never(t *testing.T) func() (bool, error) {
	return func() (bool, error) {
		t.Helper()
		t.Error("asked for running containers")
		return false, nil
	}
}

func TestWriteContainersConf(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("refuses to run as root")
	}
	home := t.TempDir()
	conf := ContainersConf{HooksDir: filepath.Join(home, ".local", "share", "sorry-portainer", "oci-hooks")}
	if current, err := ContainersConfCurrent(home, conf); err != nil || current {
		t.Fatalf("current before writing = %v, %v", current, err)
	}
	path, err := WriteContainersConf(home, conf, never(t))
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, ".config", "containers", "containers.conf.d", ContainersConfName) {
		t.Fatalf("path %s", path)
	}
	want, _ := conf.Render()
	if got, _ := os.ReadFile(path); string(got) != want {
		t.Fatalf("content %q", got)
	}
	if info, err := os.Stat(conf.HooksDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("hooks dir %v %v", info, err)
	}
	if current, err := ContainersConfCurrent(home, conf); err != nil || !current {
		t.Fatalf("current after writing = %v, %v", current, err)
	}
	// Rewriting is idempotent, and a loopback change alone never asks.
	if _, err := WriteContainersConf(home, conf, never(t)); err != nil {
		t.Fatal(err)
	}
	conf.HostLoopback = "169.254.1.3"
	if _, err := WriteContainersConf(home, conf, never(t)); err != nil {
		t.Fatal(err)
	}
}

func TestWriteContainersConfForwarderChange(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("refuses to run as root")
	}
	home := t.TempDir()
	conf := ContainersConf{HooksDir: filepath.Join(home, "hooks")}
	if _, err := WriteContainersConf(home, conf, never(t)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(ContainersConfPath(home))

	conf.Forwarder = ForwarderPasta
	busy := func() (bool, error) { return true, nil }
	if _, err := WriteContainersConf(home, conf, busy); err == nil || !strings.Contains(err.Error(), "stop this user's containers") {
		t.Fatalf("error = %v", err)
	}
	failing := func() (bool, error) { return false, errors.New("no podman") }
	if _, err := WriteContainersConf(home, conf, failing); err == nil || !strings.Contains(err.Error(), "no podman") {
		t.Fatalf("error = %v", err)
	}
	if after, _ := os.ReadFile(ContainersConfPath(home)); string(after) != string(before) {
		t.Fatalf("refused write changed the file:\n%s", after)
	}

	asked := 0
	idle := func() (bool, error) { asked++; return false, nil }
	if _, err := WriteContainersConf(home, conf, idle); err != nil || asked != 1 {
		t.Fatalf("idle switch: %v, asked %d", err, asked)
	}
	// Switching back asks again.
	conf.Forwarder = ForwarderRootlessport
	if _, err := WriteContainersConf(home, conf, busy); err == nil {
		t.Fatal("switched back under running containers")
	}
}
