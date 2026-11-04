package config

import "testing"

func TestServerFromEnvRequiresStrongAdminPassword(t *testing.T) {
	t.Setenv("SORRY_PORTAINER_ADMIN_PASSWORD", "short")
	if _, err := ServerFromEnv(); err == nil {
		t.Fatal("expected weak password to be rejected")
	}
}

func TestAgentFromEnvRequiresStateDir(t *testing.T) {
	t.Setenv("SORRY_PORTAINER_SERVER_URL", "wss://control.example/agent")
	t.Setenv("SORRY_PORTAINER_HOST_ID", "host-a")
	t.Setenv("SORRY_PORTAINER_HOST_PREFIX", "host-a-")
	t.Setenv("SORRY_PORTAINER_AGENT_CREDENTIAL", "agent.pem")
	t.Setenv("SORRY_PORTAINER_AGENT_CA", "ca")
	if _, err := AgentFromEnv(); err == nil {
		t.Fatal("expected missing state directory to fail")
	}
	t.Setenv("SORRY_PORTAINER_STATE_DIR", "/var/lib/sorry-portainer-agent")
	cfg, err := AgentFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ComposeProvider != "/usr/bin/podman-compose" || cfg.ServerName != "control.example" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestControlTLSFromEnvRequiresServerNames(t *testing.T) {
	t.Setenv("SORRY_PORTAINER_CONTROL_CA", "ca.crt")
	t.Setenv("SORRY_PORTAINER_CONTROL_CA_KEY", "ca.key")
	t.Setenv("SORRY_PORTAINER_CONTROL_CREDENTIAL", "server.pem")
	if _, err := ControlTLSFromEnv(); err == nil {
		t.Fatal("expected missing server names to fail")
	}
	t.Setenv("SORRY_PORTAINER_CONTROL_SERVER_NAMES", "jp1.example,192.0.2.1")
	c, err := ControlTLSFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ServerNames) != 2 || c.ListenAddr != ":9443" {
		t.Fatalf("control = %+v", c)
	}
	t.Setenv("SORRY_PORTAINER_CONTROL_SERVER_NAMES", "jp1.example:9443")
	if _, err := ControlTLSFromEnv(); err == nil {
		t.Fatal("accepted host:port as a server name")
	}
}

func TestAgentURL(t *testing.T) {
	for listen, want := range map[string]string{
		":9443":          "wss://jp1.example:9443/control/agent",
		"0.0.0.0:7443":   "wss://jp1.example:7443/control/agent",
		"127.0.0.1:6243": "wss://jp1.example:6243/control/agent",
	} {
		if got := (ControlTLS{ListenAddr: listen, ServerNames: []string{"jp1.example", "10.0.0.1"}}).AgentURL(); got != want {
			t.Errorf("%s: %s, want %s", listen, got, want)
		}
	}
	if got := (ControlTLS{ListenAddr: ":9443", ServerNames: []string{"2001:db8::1"}}).AgentURL(); got != "wss://[2001:db8::1]:9443/control/agent" {
		t.Errorf("IPv6 name: %s", got)
	}
}

func TestValidServerName(t *testing.T) {
	for _, ok := range []string{"control.example.com", "localhost", "127.0.0.1", "::1", "control.local"} {
		if !ValidServerName(ok) {
			t.Errorf("refused %q", ok)
		}
	}
	for _, bad := range []string{"", "jp1:9443", "-bad.example", "a..b", "with space", "[::1]"} {
		if ValidServerName(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestServerFromEnvDefaults(t *testing.T) {
	t.Setenv("SORRY_PORTAINER_ADMIN_PASSWORD", "a sufficiently long password")
	t.Setenv("SORRY_PORTAINER_LISTEN", "")
	t.Setenv("SORRY_PORTAINER_STATE_DIR", "")
	if _, err := ServerFromEnv(); err == nil {
		t.Fatal("expected missing state directory to fail")
	}
	t.Setenv("SORRY_PORTAINER_STATE_DIR", "/var/lib/sorry-portainer")
	cfg, err := ServerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":8080" || cfg.StateDir != "/var/lib/sorry-portainer" {
		t.Fatalf("config = %+v", cfg)
	}
}
