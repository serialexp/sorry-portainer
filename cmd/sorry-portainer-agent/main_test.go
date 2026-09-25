package main

import (
	"os/exec"
	"testing"
)

// TestDetachFromServiceUnit checks that processes the agent starts no longer
// see systemd's INVOCATION_ID, which would keep Podman's conmon in the agent
// unit's cgroup.
func TestDetachFromServiceUnit(t *testing.T) {
	t.Setenv("INVOCATION_ID", "0123456789abcdef")
	if err := detachFromServiceUnit(); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", `printf %s "${INVOCATION_ID-unset}"`).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "unset" {
		t.Fatalf("child saw INVOCATION_ID=%q", out)
	}
}

func TestReadBootIDIsStable(t *testing.T) {
	first, err := readBootID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := readBootID()
	if err != nil || second != first || len(first) != 36 {
		t.Fatalf("boot IDs %q %q (%v)", first, second, err)
	}
}
