// Package agentcred is the agent side of enrollment: the credential the
// agent connects with, enrolling with a token, and renewing before expiry.
package agentcred

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"sync/atomic"
	"time"

	"github.com/serialexp/sorry-portainer/internal/pki"
)

// Holder is the credential the agent currently connects with. Every new
// connection asks it, so a renewed certificate is used from the next dial
// on; an established connection keeps the identity it was made with.
type Holder struct {
	current atomic.Pointer[tls.Certificate]
}

// LoadHolder reads the credential bundle at path.
func LoadHolder(path string) (*Holder, error) {
	cert, err := pki.ReadBundle(path)
	if err != nil {
		return nil, err
	}
	h := &Holder{}
	h.current.Store(cert)
	return h, nil
}

func (h *Holder) Current() *tls.Certificate { return h.current.Load() }

func (h *Holder) Set(cert *tls.Certificate) { h.current.Store(cert) }

// ClientTLS is the agent's configuration for the relay: the master is
// verified against the control CA for serverName, and the client certificate
// comes from h at each handshake.
func ClientTLS(ca *x509.Certificate, serverName string, h *Holder, now func() time.Time) *tls.Config {
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    roots,
		ServerName: serverName,
		Time:       now,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return h.Current(), nil
		},
	}
}

// ReadCA reads the control CA certificate the agent pinned at enrollment.
func ReadCA(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cert, err := pki.ParseCertificatePEM(data)
	if err != nil {
		return nil, err
	}
	if !cert.IsCA {
		return nil, errors.New("control CA file does not hold a CA certificate")
	}
	return cert, nil
}
