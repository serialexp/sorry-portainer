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
	t.Setenv("SORRY_PORTAINER_AGENT_CERT", "cert")
	t.Setenv("SORRY_PORTAINER_AGENT_KEY", "key")
	t.Setenv("SORRY_PORTAINER_AGENT_CA", "ca")
	if _, err := AgentFromEnv(); err == nil {
		t.Fatal("expected missing state directory to fail")
	}
	t.Setenv("SORRY_PORTAINER_STATE_DIR", "/var/lib/sorry-portainer-agent")
	cfg, err := AgentFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ComposeProvider != "/usr/bin/podman-compose" {
		t.Fatalf("compose provider = %q", cfg.ComposeProvider)
	}
}

func TestServerFromEnvDefaults(t *testing.T) {
	t.Setenv("SORRY_PORTAINER_ADMIN_PASSWORD", "a sufficiently long password")
	t.Setenv("SORRY_PORTAINER_LISTEN", "")
	cfg, err := ServerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Fatalf("listen address = %q", cfg.ListenAddr)
	}
}
