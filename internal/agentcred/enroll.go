package agentcred

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/serialexp/sorry-portainer/internal/atomicfile"
	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/pki"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// File names inside an agent directory.
const (
	ConfigFile     = "agent.json"
	CredentialFile = "agent.pem"
	CAFile         = "control-ca.crt"
	// StagedKeyFile holds the key of an enrollment or renewal in progress,
	// so a retry after a crash or a lost response uses the same key.
	StagedKeyFile = "agent-next.key"
)

// EnrollOptions configures Enroll.
type EnrollOptions struct {
	ServerURL string
	HostID    string
	Token     string
	// Dir holds the agent's config, credentials and state.
	Dir string
	// Prefix defaults to the existing config's prefix, then HostID+"-".
	Prefix string
	// ServerName defaults to the server URL's host.
	ServerName      string
	ComposeProvider string
	// Replace allows enrolling over an existing configuration, keeping its
	// other settings and the stack state.
	Replace bool
	Now     func() time.Time
}

// configFile is agent.json as enrollment writes it; it matches
// config.AgentFile, which reads it.
type configFile struct {
	ServerURL       string `json:"server_url"`
	HostID          string `json:"host_id"`
	Prefix          string `json:"host_prefix"`
	CredentialFile  string `json:"agent_credential"`
	CAFile          string `json:"agent_ca"`
	ServerName      string `json:"server_name"`
	StateDir        string `json:"state_dir"`
	ComposeProvider string `json:"compose_provider"`
	SecretSocket    string `json:"secret_socket,omitempty"`
	OCIHooksDir     string `json:"oci_hooks_dir,omitempty"`
}

// Enroll exchanges a token for a certificate and writes the agent's
// configuration. Nothing is committed until the master's answer has been
// checked; a failed enrollment can be retried with the same token.
func Enroll(ctx context.Context, o EnrollOptions) (string, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if !protocol.ValidHostID(o.HostID) {
		return "", fmt.Errorf("invalid host ID %q", o.HostID)
	}
	token, err := enrollment.ParseToken(o.Token)
	if err != nil {
		return "", err
	}
	enrollURL, err := controlURL(o.ServerURL, "/control/enroll")
	if err != nil {
		return "", err
	}
	if o.ServerName == "" {
		if o.ServerName, err = ServerName(o.ServerURL); err != nil {
			return "", err
		}
	}
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return "", err
	}
	if err := privateDir(dir); err != nil {
		return "", err
	}
	configPath := filepath.Join(dir, ConfigFile)
	cfg, err := existingConfig(configPath, o.Replace)
	if err != nil {
		return "", err
	}
	prefix := o.Prefix
	if prefix == "" {
		prefix = cfg.Prefix
	}
	if prefix == "" {
		prefix = o.HostID + "-"
	}
	if !protocol.ValidHostPrefix(prefix) {
		return "", fmt.Errorf("invalid host prefix %q", prefix)
	}

	key, err := stagedKey(filepath.Join(dir, StagedKeyFile))
	if err != nil {
		return "", err
	}
	csr, err := pki.NewCSR(key, o.HostID)
	if err != nil {
		return "", err
	}
	var resp enrollment.EnrollResponse
	tlsConfig := pki.PinnedClientConfig(token.CA, o.ServerName, o.Now)
	req := enrollment.EnrollRequest{Token: o.Token, HostID: o.HostID, Prefix: prefix, CSR: string(csr)}
	if err := post(ctx, tlsConfig, enrollURL, req, &resp); err != nil {
		return "", fmt.Errorf("enroll with %s: %w", enrollURL, err)
	}
	ca, err := pki.ParseCertificatePEM([]byte(resp.CA))
	if err != nil {
		return "", fmt.Errorf("master's CA: %w", err)
	}
	if pki.FingerprintOf(ca) != token.CA {
		return "", errors.New("master returned a CA other than the one named in the token")
	}
	cert, err := pki.ParseCertificatePEM([]byte(resp.Certificate))
	if err != nil {
		return "", fmt.Errorf("issued certificate: %w", err)
	}
	if err := pki.CheckIssuedAgent(cert, ca, o.HostID, &key.PublicKey, o.Now()); err != nil {
		return "", fmt.Errorf("issued certificate: %w", err)
	}

	cfg.ServerURL, cfg.HostID, cfg.Prefix, cfg.ServerName = o.ServerURL, o.HostID, prefix, o.ServerName
	cfg.CredentialFile = filepath.Join(dir, CredentialFile)
	cfg.CAFile = filepath.Join(dir, CAFile)
	if cfg.StateDir == "" {
		cfg.StateDir = dir
	}
	if o.ComposeProvider != "" {
		cfg.ComposeProvider = o.ComposeProvider
	}
	if cfg.ComposeProvider == "" {
		cfg.ComposeProvider = "/usr/bin/podman-compose"
	}
	// agent.json goes last: its presence means the rest is in place.
	if err := atomicfile.Write(cfg.CAFile, pki.EncodeCertificate(ca), 0o600); err != nil {
		return "", err
	}
	if err := pki.WriteBundle(cfg.CredentialFile, key, cert); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	if err := atomicfile.Write(configPath, append(data, '\n'), 0o600); err != nil {
		return "", err
	}
	if err := os.Remove(filepath.Join(dir, StagedKeyFile)); err != nil {
		return "", err
	}
	return configPath, nil
}

// privateDir creates dir if needed and checks it belongs to this user and is
// not group or world accessible.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must not be group/world accessible (mode %04o)", dir, st.Mode().Perm())
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to uid %d, not to this user (%d)", dir, sys.Uid, os.Getuid())
	}
	return nil
}

func existingConfig(path string, replace bool) (configFile, error) {
	var cfg configFile
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if !replace {
		return cfg, fmt.Errorf("%s exists; pass --replace to enroll again", path)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// stagedKey returns the staged key, creating and storing one if there is
// none.
func stagedKey(path string) (*ecdsa.PrivateKey, error) {
	key, err := pki.ReadKey(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if key, err = pki.NewKey(); err != nil {
		return nil, err
	}
	if err := pki.WriteKey(path, key); err != nil {
		return nil, err
	}
	return key, nil
}
