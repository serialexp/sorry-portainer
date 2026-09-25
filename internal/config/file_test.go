package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentFileRequiresPrivatePermissions(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "agent.json")
	if err := os.WriteFile(p, []byte(`{"server_url":"wss://x","host_id":"a","host_prefix":"a-","agent_cert":"c","agent_key":"k","agent_ca":"ca"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAgentFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StateDir != d || cfg.ComposeProvider != "/usr/bin/podman-compose" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgentFile(p); err == nil {
		t.Fatal("expected permission rejection")
	}
}
func TestLoadAgentFileRejectsUnknownFields(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "agent.json")
	body := `{"server_url":"wss://x","host_id":"a","host_prefix":"a-","agent_cert":"c","agent_key":"k","agent_ca":"ca","unexpected":true}`
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgentFile(p); err == nil {
		t.Fatal("expected unknown field rejection")
	}
}

func TestLoadServerFile(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "server.json")
	body := `{"listen_addr":":8080","admin_password":"long-enough-admin-password","session_ttl":"1h","control":{"listen_addr":":9443","ca_file":"ca","cert_file":"cert","key_file":"key"}}`
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	s, c, err := LoadServerFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionTTL.Hours() != 1 || c.ListenAddr != ":9443" {
		t.Fatalf("server=%+v control=%+v", s, c)
	}
}
