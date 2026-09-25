package provision

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestInitAndCreateAgent(t *testing.T) {
	d := t.TempDir()
	if err := Init(InitOptions{StateDir: d, AdminPassword: "long-enough-admin-password", ServerName: "control.example"}); err != nil {
		t.Fatal(err)
	}
	if err := CreateAgent(AgentOptions{StateDir: d, HostID: "local-b"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"control-ca.key", "control-ca.crt", "control-server.key", "control-server.crt", "server.json", "agents/local-b/agent.key", "agents/local-b/agent.crt", "agents/local-b/control-ca.crt", "agents/local-b/agent.json"} {
		st, err := os.Stat(filepath.Join(d, p))
		if err != nil {
			t.Fatal(p, err)
		}
		if st.Mode().Perm() != 0600 {
			t.Fatalf("%s mode %04o", p, st.Mode().Perm())
		}
	}
	caBlock, _ := pem.Decode(mustRead(t, filepath.Join(d, "control-ca.crt")))
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	leafBlock, _ := pem.Decode(mustRead(t, filepath.Join(d, "agents/local-b/agent.crt")))
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = leaf.CheckSignatureFrom(ca); err != nil {
		t.Fatal(err)
	}
	if len(leaf.URIs) != 1 || leaf.URIs[0].Host != "local-b" {
		t.Fatalf("URI SAN %#v", leaf.URIs)
	}
	var cfg map[string]string
	if err := json.Unmarshal(mustRead(t, filepath.Join(d, "agents/local-b/agent.json")), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["state_dir"] != filepath.Join(d, "agents", "local-b") || cfg["compose_provider"] != "/usr/bin/podman-compose" {
		t.Fatalf("unexpected agent config: %#v", cfg)
	}
}
func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
