package provision

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type InitOptions struct{ StateDir, AdminPassword, ServerName, ControlListen, WebListen string }
type AgentOptions struct{ StateDir, HostID, Prefix, ServerURL, ServerName, ComposeProvider string }

func Init(o InitOptions) error {
	if o.StateDir == "" || o.AdminPassword == "" {
		return errors.New("state directory and admin password are required")
	}
	if len(o.AdminPassword) < 12 {
		return errors.New("admin password must be at least 12 characters")
	}
	if o.ServerName == "" {
		o.ServerName = "control.local"
	}
	if o.ControlListen == "" {
		o.ControlListen = ":9443"
	}
	if o.WebListen == "" {
		o.WebListen = ":8080"
	}
	if o.ControlListen == ":80" || o.ControlListen == ":443" {
		return errors.New("control listener must not use port 80 or 443")
	}
	if err := os.MkdirAll(o.StateDir, 0700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(o.StateDir, "control-ca.key")); err == nil {
		return errors.New("server state already initialized")
	} else if !os.IsNotExist(err) {
		return err
	}
	caKey, caCert, err := newCA()
	if err != nil {
		return err
	}
	serverKey, serverCert, err := newLeaf(caKey, caCert, o.ServerName, false)
	if err != nil {
		return err
	}
	if err = writeKeyPair(o.StateDir, "control-ca", caKey, caCert); err != nil {
		return err
	}
	if err = writeKeyPair(o.StateDir, "control-server", serverKey, serverCert); err != nil {
		return err
	}
	cfg := map[string]any{"listen_addr": o.WebListen, "admin_password": o.AdminPassword, "session_ttl": "12h", "control": map[string]string{"listen_addr": o.ControlListen, "ca_file": filepath.Join(o.StateDir, "control-ca.crt"), "cert_file": filepath.Join(o.StateDir, "control-server.crt"), "key_file": filepath.Join(o.StateDir, "control-server.key")}}
	return writeJSON(filepath.Join(o.StateDir, "server.json"), cfg)
}
func CreateAgent(o AgentOptions) error {
	if o.StateDir == "" || o.HostID == "" {
		return errors.New("state directory and host ID are required")
	}
	if o.Prefix == "" {
		o.Prefix = o.HostID + "-"
	}
	if o.ServerName == "" {
		o.ServerName = "control.local"
	}
	if o.ServerURL == "" {
		o.ServerURL = "wss://" + o.ServerName + ":9443/control/agent"
	}
	if !strings.HasPrefix(o.ServerURL, "wss://") {
		return errors.New("server URL must use wss://")
	}
	caKey, caCert, err := readCA(o.StateDir)
	if err != nil {
		return err
	}
	key, cert, err := newLeaf(caKey, caCert, o.HostID, true)
	if err != nil {
		return err
	}
	dir := filepath.Join(o.StateDir, "agents", o.HostID)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err = writeKeyPair(dir, "agent", key, cert); err != nil {
		return err
	}
	if err = copyFile(filepath.Join(o.StateDir, "control-ca.crt"), filepath.Join(dir, "control-ca.crt"), 0600); err != nil {
		return err
	}
	if o.ComposeProvider == "" {
		o.ComposeProvider = "/usr/bin/podman-compose"
	}
	cfg := map[string]string{"server_url": o.ServerURL, "host_id": o.HostID, "host_prefix": o.Prefix, "agent_cert": filepath.Join(dir, "agent.crt"), "agent_key": filepath.Join(dir, "agent.key"), "agent_ca": filepath.Join(dir, "control-ca.crt"), "server_name": o.ServerName, "state_dir": dir, "compose_provider": o.ComposeProvider}
	return writeJSON(filepath.Join(dir, "agent.json"), cfg)
}
func newCA() (*ecdsa.PrivateKey, *x509.Certificate, error) {
	k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return nil, nil, e
	}
	c, e := makeCertificate(pkix.Name{CommonName: "sorry-portainer control CA"}, k, k, nil, true, "", nil)
	return k, c, e
}
func newLeaf(caKey *ecdsa.PrivateKey, ca *x509.Certificate, name string, agent bool) (*ecdsa.PrivateKey, *x509.Certificate, error) {
	k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return nil, nil, e
	}
	var uri *url.URL
	if agent {
		uri, _ = url.Parse("sorry-host://" + name)
	}
	c, e := makeCertificate(pkix.Name{CommonName: name}, k, caKey, ca, false, name, uri)
	return k, c, e
}
func makeCertificate(subject pkix.Name, key, issuerKey *ecdsa.PrivateKey, issuer *x509.Certificate, ca bool, dns string, uri *url.URL) (*x509.Certificate, error) {
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if e != nil {
		return nil, e
	}
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: subject, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().AddDate(10, 0, 0), BasicConstraintsValid: true, IsCA: ca, KeyUsage: x509.KeyUsageDigitalSignature}
	if ca {
		tmpl.KeyUsage |= x509.KeyUsageCertSign
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}
		tmpl.DNSNames = []string{dns}
		if uri != nil {
			tmpl.URIs = []*url.URL{uri}
		}
	}
	parent := tmpl
	if issuer != nil {
		parent = issuer
	}
	signer := key
	if issuerKey != nil {
		signer = issuerKey
	}
	der, e := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	if e != nil {
		return nil, e
	}
	return x509.ParseCertificate(der)
}
func writeKeyPair(dir, name string, key *ecdsa.PrivateKey, cert *x509.Certificate) error {
	kb, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		return e
	}
	if e = writeFile(filepath.Join(dir, name+".key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0600); e != nil {
		return e
	}
	return writeFile(filepath.Join(dir, name+".crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600)
}
func writeFile(path string, b []byte, mode os.FileMode) error { return os.WriteFile(path, b, mode) }
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return writeFile(path, append(b, '\n'), 0600)
}
func copyFile(src, dst string, mode os.FileMode) error {
	b, e := os.ReadFile(src)
	if e != nil {
		return e
	}
	return writeFile(dst, b, mode)
}
func readCA(dir string) (*ecdsa.PrivateKey, *x509.Certificate, error) {
	kb, e := os.ReadFile(filepath.Join(dir, "control-ca.key"))
	if e != nil {
		return nil, nil, e
	}
	block, _ := pem.Decode(kb)
	if block == nil {
		return nil, nil, errors.New("invalid CA key PEM")
	}
	raw, e := x509.ParsePKCS8PrivateKey(block.Bytes)
	if e != nil {
		return nil, nil, e
	}
	key, ok := raw.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, errors.New("CA key is not ECDSA")
	}
	cb, e := os.ReadFile(filepath.Join(dir, "control-ca.crt"))
	if e != nil {
		return nil, nil, e
	}
	certBlock, _ := pem.Decode(cb)
	if certBlock == nil {
		return nil, nil, errors.New("invalid CA certificate PEM")
	}
	cert, e := x509.ParseCertificate(certBlock.Bytes)
	return key, cert, e
}
