package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/serialexp/sorry-portainer/internal/agentsetup"
)

type Server struct {
	ListenAddr    string
	AdminPassword string
	SessionTTL    time.Duration
	// StateDir holds the master's durable state, including the encrypted
	// secret store under StateDir/secrets.
	StateDir string
	// InsecureCookie drops Secure from the session cookie (plain-HTTP
	// development only).
	InsecureCookie bool
	// TrustedProxies are the reverse proxies whose X-Forwarded-For names the
	// client for the sign-in limit.
	TrustedProxies []netip.Prefix
}

// ParseTrustedProxies reads IP addresses and CIDR prefixes. An address
// stands for itself alone.
func ParseTrustedProxies(values []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if strings.Contains(v, "/") {
			p, err := netip.ParsePrefix(v)
			if err != nil {
				return nil, err
			}
			if p.Addr().Is4In6() {
				return nil, fmt.Errorf("%s: write IPv4 prefixes in IPv4 form", v)
			}
			if p != p.Masked() {
				return nil, fmt.Errorf("%s has host bits set; did you mean %s?", v, p.Masked())
			}
			out = append(out, p)
			continue
		}
		a, err := netip.ParseAddr(v)
		if err != nil {
			return nil, err
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func ServerFromEnv() (Server, error) {
	cfg := Server{ListenAddr: getenv("SORRY_PORTAINER_LISTEN", ":8080"), SessionTTL: 12 * time.Hour, StateDir: os.Getenv("SORRY_PORTAINER_STATE_DIR"),
		AdminPassword: os.Getenv("SORRY_PORTAINER_ADMIN_PASSWORD"), InsecureCookie: os.Getenv("SORRY_PORTAINER_INSECURE_COOKIE") == "1"}
	proxies, err := ParseTrustedProxies(strings.Split(os.Getenv("SORRY_PORTAINER_TRUSTED_PROXIES"), ","))
	if err != nil {
		return Server{}, fmt.Errorf("SORRY_PORTAINER_TRUSTED_PROXIES: %w", err)
	}
	cfg.TrustedProxies = proxies
	if v := os.Getenv("SORRY_PORTAINER_SESSION_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Server{}, fmt.Errorf("SORRY_PORTAINER_SESSION_TTL: %w", err)
		}
		cfg.SessionTTL = d
	}
	if strings.TrimSpace(cfg.AdminPassword) == "" {
		return Server{}, errors.New("SORRY_PORTAINER_ADMIN_PASSWORD is required")
	}
	if len(cfg.AdminPassword) < 12 {
		return Server{}, errors.New("SORRY_PORTAINER_ADMIN_PASSWORD must be at least 12 characters")
	}
	if err := validateServer(cfg); err != nil {
		return Server{}, err
	}
	return cfg, nil
}

// defaultOCIHooksDir matches what `sorry-portainer-agent setup` puts in the
// agent user's containers.conf: $HOME/.local/share/sorry-portainer/oci-hooks.
// Without a home directory it falls back to the state directory.
func defaultOCIHooksDir(stateDir string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return agentsetup.DefaultOCIHooksDir(home)
	}
	return filepath.Join(stateDir, "oci-hooks")
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type Agent struct {
	ServerURL, HostID, Prefix, CAFile, ServerName string
	// CredentialFile is the agent's key and certificate in one PEM file,
	// which the agent replaces itself when it renews its certificate.
	CredentialFile            string
	StateDir, ComposeProvider string
	// SecretSocket is the Unix socket the OCI hook asks for secrets; empty
	// selects $XDG_RUNTIME_DIR/sorry-portainer/<host>.sock.
	SecretSocket string
	// OCIHooksDir receives the agent's hook JSON. It must be listed in the
	// agent user's containers.conf hooks_dir. Defaults to StateDir/oci-hooks.
	OCIHooksDir string
}

func AgentFromEnv() (Agent, error) {
	a := Agent{ServerURL: os.Getenv("SORRY_PORTAINER_SERVER_URL"), HostID: os.Getenv("SORRY_PORTAINER_HOST_ID"), Prefix: os.Getenv("SORRY_PORTAINER_HOST_PREFIX"), CredentialFile: os.Getenv("SORRY_PORTAINER_AGENT_CREDENTIAL"), CAFile: os.Getenv("SORRY_PORTAINER_AGENT_CA"), ServerName: os.Getenv("SORRY_PORTAINER_SERVER_NAME"), StateDir: os.Getenv("SORRY_PORTAINER_STATE_DIR"), ComposeProvider: os.Getenv("SORRY_PORTAINER_COMPOSE_PROVIDER"), SecretSocket: os.Getenv("SORRY_PORTAINER_SECRET_SOCKET"), OCIHooksDir: os.Getenv("SORRY_PORTAINER_OCI_HOOKS_DIR")}
	if a.OCIHooksDir == "" {
		a.OCIHooksDir = defaultOCIHooksDir(a.StateDir)
	}
	for k, v := range map[string]string{"SORRY_PORTAINER_SERVER_URL": a.ServerURL, "SORRY_PORTAINER_HOST_ID": a.HostID, "SORRY_PORTAINER_HOST_PREFIX": a.Prefix, "SORRY_PORTAINER_AGENT_CREDENTIAL": a.CredentialFile, "SORRY_PORTAINER_AGENT_CA": a.CAFile, "SORRY_PORTAINER_STATE_DIR": a.StateDir} {
		if v == "" {
			return Agent{}, fmt.Errorf("%s is required", k)
		}
	}
	if a.ComposeProvider == "" {
		a.ComposeProvider = "/usr/bin/podman-compose"
	}
	if a.ServerName == "" {
		a.ServerName = serverNameFromURL(a.ServerURL)
	}
	return a, nil
}

// serverNameFromURL is the name an agent verifies the master's certificate
// for when none is configured: the URL's host without its port.
func serverNameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
