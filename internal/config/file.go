package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ServerFile is the on-disk configuration consumed by the control server.
// Private keys and passwords are referenced or stored here, so files must be
// owned by the service account and have mode 0600.
type ServerFile struct {
	ListenAddr    string         `json:"listen_addr"`
	AdminPassword string         `json:"admin_password"`
	SessionTTL    string         `json:"session_ttl"`
	Control       ControlTLSFile `json:"control"`
}
type ControlTLSFile struct {
	ListenAddr string `json:"listen_addr"`
	CAFile     string `json:"ca_file"`
	CertFile   string `json:"cert_file"`
	KeyFile    string `json:"key_file"`
}

func LoadServerFile(path string) (Server, ControlTLS, error) {
	var f ServerFile
	if err := loadPrivate(path, &f); err != nil {
		return Server{}, ControlTLS{}, err
	}
	ttl := 12 * time.Hour
	if f.SessionTTL != "" {
		var err error
		ttl, err = time.ParseDuration(f.SessionTTL)
		if err != nil || ttl <= 0 {
			return Server{}, ControlTLS{}, fmt.Errorf("session_ttl: %w", err)
		}
	}
	cfg := Server{ListenAddr: f.ListenAddr, AdminPassword: f.AdminPassword, SessionTTL: ttl}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	control := ControlTLS{ListenAddr: f.Control.ListenAddr, CAFile: f.Control.CAFile, CertFile: f.Control.CertFile, KeyFile: f.Control.KeyFile}
	if control.ListenAddr == "" {
		control.ListenAddr = ":9443"
	}
	if err := validateServer(cfg); err != nil {
		return Server{}, ControlTLS{}, err
	}
	if err := validateControl(control); err != nil {
		return Server{}, ControlTLS{}, err
	}
	return cfg, control, nil
}

type AgentFile struct {
	ServerURL       string `json:"server_url"`
	HostID          string `json:"host_id"`
	Prefix          string `json:"host_prefix"`
	CertFile        string `json:"agent_cert"`
	KeyFile         string `json:"agent_key"`
	CAFile          string `json:"agent_ca"`
	ServerName      string `json:"server_name"`
	StateDir        string `json:"state_dir"`
	ComposeProvider string `json:"compose_provider"`
}

func LoadAgentFile(path string) (Agent, error) {
	var f AgentFile
	if err := loadPrivate(path, &f); err != nil {
		return Agent{}, err
	}
	a := Agent{ServerURL: f.ServerURL, HostID: f.HostID, Prefix: f.Prefix, CertFile: f.CertFile, KeyFile: f.KeyFile, CAFile: f.CAFile, ServerName: f.ServerName, StateDir: f.StateDir, ComposeProvider: f.ComposeProvider}
	if a.StateDir == "" {
		a.StateDir = filepath.Dir(path)
	}
	if a.ComposeProvider == "" {
		a.ComposeProvider = "/usr/bin/podman-compose"
	}
	if err := validateAgent(a); err != nil {
		return Agent{}, err
	}
	return a, nil
}

func loadPrivate(path string, dst any) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("config %s must not be group/world accessible (mode %04o)", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(dst); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("parse config %s: multiple JSON values", path)
		}
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	return nil
}
func validateServer(cfg Server) error {
	if strings.TrimSpace(cfg.AdminPassword) == "" {
		return errors.New("SORRY_PORTAINER_ADMIN_PASSWORD is required")
	}
	if len(cfg.AdminPassword) < 12 {
		return errors.New("admin password must be at least 12 characters")
	}
	return nil
}
func validateControl(c ControlTLS) error {
	if c.ListenAddr == ":80" || c.ListenAddr == ":443" {
		return errors.New("control listener must not use port 80 or 443")
	}
	for n, v := range map[string]string{"control CA": c.CAFile, "control certificate": c.CertFile, "control key": c.KeyFile} {
		if v == "" {
			return fmt.Errorf("%s is required", n)
		}
	}
	return nil
}
func validateAgent(a Agent) error {
	u, err := url.Parse(a.ServerURL)
	if err != nil || u.Scheme != "wss" || u.Host == "" {
		return errors.New("server_url must be a wss:// URL")
	}
	if a.HostID == "" || strings.ContainsAny(a.HostID, " /\\") {
		return errors.New("host_id contains invalid characters")
	}
	for n, v := range map[string]string{"server_url": a.ServerURL, "host_id": a.HostID, "host_prefix": a.Prefix, "agent_cert": a.CertFile, "agent_key": a.KeyFile, "agent_ca": a.CAFile, "state_dir": a.StateDir, "compose_provider": a.ComposeProvider} {
		if v == "" {
			return fmt.Errorf("%s is required", n)
		}
	}
	return nil
}
