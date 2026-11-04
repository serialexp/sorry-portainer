// Package pki issues and checks the control channel's certificates: the
// control CA, the server's certificate, and agent certificates. All keys are
// ECDSA P-256.
package pki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"slices"
	"time"

	"github.com/serialexp/sorry-portainer/internal/atomicfile"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

const (
	CALifetime = 10 * 365 * 24 * time.Hour
	// LeafLifetime applies to server and agent certificates, which renew
	// themselves well before they expire.
	LeafLifetime = 90 * 24 * time.Hour
	// Backdate tolerates clocks that run behind the master's.
	Backdate = 15 * time.Minute
	// HostURIScheme carries an agent's host ID: sorry-host://HOST_ID.
	HostURIScheme = "sorry-host"
)

// Fingerprint is the SHA-256 of a certificate's DER encoding.
type Fingerprint [sha256.Size]byte

func FingerprintOf(cert *x509.Certificate) Fingerprint { return sha256.Sum256(cert.Raw) }

// String is unpadded base64url, the form used in tokens and hosts.json.
func (f Fingerprint) String() string { return base64.RawURLEncoding.EncodeToString(f[:]) }

// fingerprintTextLen is base64.RawURLEncoding.EncodedLen(sha256.Size).
const fingerprintTextLen = 43

// Matches reports whether s is f's string form. It does not allocate, so the
// admission path can call it on every connection.
func (f Fingerprint) Matches(s string) bool {
	var buf [fingerprintTextLen]byte
	base64.RawURLEncoding.Encode(buf[:], f[:])
	return string(buf[:]) == s
}

func ParseFingerprint(s string) (Fingerprint, error) {
	var f Fingerprint
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != len(f) {
		return f, errors.New("invalid certificate fingerprint")
	}
	copy(f[:], b)
	return f, nil
}

// CA is the control CA: the trust root for both directions of the control
// channel.
type CA struct {
	Key  *ecdsa.PrivateKey
	Cert *x509.Certificate
}

func NewKey() (*ecdsa.PrivateKey, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }

func NewCA(now time.Time) (*CA, error) {
	key, err := NewKey()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		Subject:               pkix.Name{CommonName: "sorry-portainer control CA"},
		NotBefore:             now.Add(-Backdate),
		NotAfter:              now.Add(CALifetime),
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	cert, err := sign(tmpl, &key.PublicKey, tmpl, key)
	if err != nil {
		return nil, err
	}
	return &CA{Key: key, Cert: cert}, nil
}

// LoadCA reads the CA certificate and its private key.
func LoadCA(certFile, keyFile string) (*CA, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	cert, err := ParseCertificatePEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", certFile, err)
	}
	key, err := ReadKey(keyFile)
	if err != nil {
		return nil, err
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, errors.New("control CA key does not match its certificate")
	}
	if !cert.IsCA {
		return nil, errors.New("control CA certificate is not a CA")
	}
	return &CA{Key: key, Cert: cert}, nil
}

// WriteFiles stores the certificate and the key (0600 both).
func (ca *CA) WriteFiles(certFile, keyFile string) error {
	if err := WriteKey(keyFile, ca.Key); err != nil {
		return err
	}
	return atomicfile.Write(certFile, EncodeCertificate(ca.Cert), 0o600)
}

func (ca *CA) Fingerprint() Fingerprint { return FingerprintOf(ca.Cert) }

// IssueAgent signs a client certificate for hostID. Only the public key comes
// from the requester.
func (ca *CA) IssueAgent(pub *ecdsa.PublicKey, hostID string, now time.Time) (*x509.Certificate, error) {
	if !protocol.ValidHostID(hostID) {
		return nil, fmt.Errorf("invalid host ID %q", hostID)
	}
	tmpl := ca.leaf(pkix.Name{CommonName: hostID}, now)
	tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	tmpl.URIs = []*url.URL{HostURI(hostID)}
	return sign(tmpl, pub, ca.Cert, ca.Key)
}

// IssueServer signs the control server's certificate for names: DNS names
// and IP addresses.
func (ca *CA) IssueServer(pub *ecdsa.PublicKey, names []string, now time.Time) (*x509.Certificate, error) {
	if len(names) == 0 {
		return nil, errors.New("server certificate needs at least one name")
	}
	tmpl := ca.leaf(pkix.Name{CommonName: names[0]}, now)
	tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	for _, name := range names {
		if ip := net.ParseIP(name); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, name)
		}
	}
	return sign(tmpl, pub, ca.Cert, ca.Key)
}

func (ca *CA) leaf(subject pkix.Name, now time.Time) *x509.Certificate {
	notAfter := now.Add(LeafLifetime)
	// No leaf outlives the CA that vouches for it.
	if notAfter.After(ca.Cert.NotAfter) {
		notAfter = ca.Cert.NotAfter
	}
	return &x509.Certificate{
		Subject:               subject,
		NotBefore:             now.Add(-Backdate),
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
	}
}

func sign(tmpl *x509.Certificate, pub *ecdsa.PublicKey, parent *x509.Certificate, signer *ecdsa.PrivateKey) (*x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, err
	}
	tmpl.SerialNumber = serial
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

func HostURI(hostID string) *url.URL { return &url.URL{Scheme: HostURIScheme, Host: hostID} }

// AgentHostID returns the host ID an agent certificate carries. Only the
// sorry-host URI counts, and the certificate must be for client
// authentication only, so a server certificate is never taken for an agent.
func AgentHostID(cert *x509.Certificate) (string, error) {
	if !slices.Equal(cert.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) {
		return "", errors.New("agent certificate must be for client authentication only")
	}
	identity := ""
	for _, uri := range cert.URIs {
		if uri.Scheme != HostURIScheme {
			continue
		}
		if identity != "" {
			return "", errors.New("agent certificate has more than one host identity")
		}
		identity = uri.Host
		if identity == "" {
			identity = uri.Opaque
		}
		if identity == "" {
			return "", errors.New("agent certificate has an empty host identity")
		}
	}
	if identity == "" {
		return "", errors.New("agent certificate has no host identity")
	}
	if !protocol.ValidHostID(identity) {
		return "", fmt.Errorf("agent certificate host identity %q is invalid", identity)
	}
	return identity, nil
}

// CheckIssuedAgent checks a certificate the master returned: issued by ca,
// for hostID, for pub, client authentication only, valid at now.
func CheckIssuedAgent(cert *x509.Certificate, ca *x509.Certificate, hostID string, pub *ecdsa.PublicKey, now time.Time) error {
	if err := cert.CheckSignatureFrom(ca); err != nil {
		return fmt.Errorf("certificate is not signed by the control CA: %w", err)
	}
	got, err := AgentHostID(cert)
	if err != nil {
		return err
	}
	if got != hostID {
		return fmt.Errorf("certificate is for host %q, not %q", got, hostID)
	}
	if !pub.Equal(cert.PublicKey) {
		return errors.New("certificate is not for this key")
	}
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return fmt.Errorf("certificate is valid %s to %s, and this host's clock says %s", cert.NotBefore.UTC().Format(time.RFC3339), cert.NotAfter.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	return nil
}

// ServerCovers reports whether a server certificate is for exactly the
// usages and at least the names configured.
func ServerCovers(cert *x509.Certificate, names []string) bool {
	if !slices.Equal(cert.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) {
		return false
	}
	for _, name := range names {
		if cert.VerifyHostname(name) != nil {
			return false
		}
	}
	return true
}

// NewCSR creates a certificate request for hostID signed by key.
func NewCSR(key *ecdsa.PrivateKey, hostID string) ([]byte, error) {
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: hostID}}, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// ErrUnsupportedKey reports a CSR whose key is not ECDSA P-256.
var ErrUnsupportedKey = errors.New("certificate request key must be ECDSA P-256")

// ParseCSR decodes one PEM certificate request, checks its signature and
// returns its public key. Nothing else from the request is used.
func ParseCSR(data []byte) (*ecdsa.PublicKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("expected one PEM certificate request")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("certificate request signature: %w", err)
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, ErrUnsupportedKey
	}
	return pub, nil
}

func EncodeCertificate(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

// ParseCertificatePEM decodes exactly one PEM certificate.
func ParseCertificatePEM(data []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("expected one PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func encodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// WriteKey stores a private key alone (0600), durably.
func WriteKey(path string, key *ecdsa.PrivateKey) error {
	data, err := encodeKey(key)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o600)
}

// ReadKey reads a PEM PKCS#8 ECDSA P-256 private key.
func ReadKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("%s: expected a PEM private key", path)
	}
	raw, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	key, ok := raw.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("%s: key is not ECDSA P-256", path)
	}
	return key, nil
}
