package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type Server struct {
	ListenAddr          string
	AdminPassword       string
	SessionTTL          time.Duration
	AgentCAFile         string
	AgentServerCertFile string
	AgentServerKeyFile  string
}

func ServerFromEnv() (Server, error) {
	cfg := Server{ListenAddr: getenv("SORRY_PORTAINER_LISTEN", ":8080"), SessionTTL: 12 * time.Hour,
		AdminPassword: os.Getenv("SORRY_PORTAINER_ADMIN_PASSWORD"), AgentCAFile: os.Getenv("SORRY_PORTAINER_AGENT_CA"),
		AgentServerCertFile: os.Getenv("SORRY_PORTAINER_AGENT_CERT"), AgentServerKeyFile: os.Getenv("SORRY_PORTAINER_AGENT_KEY")}
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
func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type Agent struct {
	ServerURL, HostID, Prefix, CertFile, KeyFile, CAFile, ServerName string
	StateDir, ComposeProvider                                        string
}

func AgentFromEnv() (Agent, error) {
	a := Agent{ServerURL: os.Getenv("SORRY_PORTAINER_SERVER_URL"), HostID: os.Getenv("SORRY_PORTAINER_HOST_ID"), Prefix: os.Getenv("SORRY_PORTAINER_HOST_PREFIX"), CertFile: os.Getenv("SORRY_PORTAINER_AGENT_CERT"), KeyFile: os.Getenv("SORRY_PORTAINER_AGENT_KEY"), CAFile: os.Getenv("SORRY_PORTAINER_AGENT_CA"), ServerName: os.Getenv("SORRY_PORTAINER_SERVER_NAME"), StateDir: os.Getenv("SORRY_PORTAINER_STATE_DIR"), ComposeProvider: os.Getenv("SORRY_PORTAINER_COMPOSE_PROVIDER")}
	for k, v := range map[string]string{"SORRY_PORTAINER_SERVER_URL": a.ServerURL, "SORRY_PORTAINER_HOST_ID": a.HostID, "SORRY_PORTAINER_HOST_PREFIX": a.Prefix, "SORRY_PORTAINER_AGENT_CERT": a.CertFile, "SORRY_PORTAINER_AGENT_KEY": a.KeyFile, "SORRY_PORTAINER_AGENT_CA": a.CAFile, "SORRY_PORTAINER_STATE_DIR": a.StateDir} {
		if v == "" {
			return Agent{}, fmt.Errorf("%s is required", k)
		}
	}
	if a.ComposeProvider == "" {
		a.ComposeProvider = "/usr/bin/podman-compose"
	}
	return a, nil
}
func LoadClientTLS(a Agent) (*tls.Config, error) {
	cert, e := tls.LoadX509KeyPair(a.CertFile, a.KeyFile)
	if e != nil {
		return nil, e
	}
	b, e := os.ReadFile(a.CAFile)
	if e != nil {
		return nil, e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(b) {
		return nil, errors.New("agent CA contains no certificates")
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: roots, ServerName: a.ServerName, MinVersion: tls.VersionTLS13}, nil
}
