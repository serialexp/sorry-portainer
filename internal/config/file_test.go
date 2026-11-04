package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentFileRequiresPrivatePermissions(t *testing.T) {
	d := t.TempDir()
	t.Setenv("HOME", "/home/agent")
	p := filepath.Join(d, "agent.json")
	if err := os.WriteFile(p, []byte(`{"server_url":"wss://x","host_id":"a","host_prefix":"a-","agent_credential":"agent.pem","agent_ca":"ca"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAgentFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StateDir != d || cfg.ComposeProvider != "/usr/bin/podman-compose" || cfg.OCIHooksDir != "/home/agent/.local/share/sorry-portainer/oci-hooks" || cfg.SecretSocket != "" || cfg.ServerName != "x" {
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
	body := `{"server_url":"wss://x","host_id":"a","host_prefix":"a-","agent_credential":"agent.pem","agent_ca":"ca","unexpected":true}`
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
	body := `{"listen_addr":":8080","admin_password":"long-enough-admin-password","session_ttl":"1h","control":{"listen_addr":":9443","ca_file":"ca","ca_key_file":"ca.key","credential_file":"server.pem","server_names":["jp1.example"]}}`
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	s, c, err := LoadServerFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionTTL.Hours() != 1 || c.ListenAddr != ":9443" || s.StateDir != d || c.CAKeyFile != "ca.key" || c.ServerNames[0] != "jp1.example" {
		t.Fatalf("server=%+v control=%+v", s, c)
	}
	if s.InsecureCookie || len(s.TrustedProxies) != 0 {
		t.Fatalf("session defaults: insecure=%v proxies=%v", s.InsecureCookie, s.TrustedProxies)
	}
}

func TestLoadServerFileSessionSettings(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "server.json")
	const control = `"control":{"ca_file":"ca","ca_key_file":"ca.key","credential_file":"server.pem","server_names":["jp1.example"]}`
	write := func(extra string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(`{"admin_password":"long-enough-admin-password",`+extra+control+`}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`"insecure_cookie":true,"trusted_proxies":["172.18.0.0/16","10.0.0.7","fd00::/8"],`)
	s, _, err := LoadServerFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"172.18.0.0/16", "10.0.0.7/32", "fd00::/8"}
	if !s.InsecureCookie || len(s.TrustedProxies) != len(want) {
		t.Fatalf("server=%+v", s)
	}
	for i, p := range s.TrustedProxies {
		if p.String() != want[i] {
			t.Fatalf("proxy %d = %s, want %s", i, p, want[i])
		}
	}
	for _, bad := range []string{`"nonsense"`, `"172.18.0.1/16"`, `"::ffff:10.0.0.0/104"`, `"10.0.0.0/33"`} {
		write(`"trusted_proxies":[` + bad + `],`)
		if _, _, err := LoadServerFile(p); err == nil {
			t.Fatalf("trusted proxy %s accepted", bad)
		}
	}
}
