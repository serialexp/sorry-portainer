// Package controlcert keeps the control listener's own certificate current.
// The server holds the CA key, so it re-issues its certificate before it
// expires and serves the new one without a restart.
package controlcert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/serialexp/sorry-portainer/internal/pki"
)

const (
	// RenewBefore is how long before expiry the certificate is replaced.
	RenewBefore = 30 * 24 * time.Hour
	// CheckInterval is how often a running server checks.
	CheckInterval = 24 * time.Hour
	// CAWarnBefore is when the server starts warning that the CA is ending.
	CAWarnBefore = 365 * 24 * time.Hour
)

// Manager holds the server's credential: key, leaf and CA, the CA included
// so agents enrolling with a pinned CA fingerprint find it in the chain.
type Manager struct {
	ca    *pki.CA
	path  string
	names []string
	now   func() time.Time
	logf  func(string, ...any)

	mu      sync.Mutex // serializes re-issuing
	current atomic.Pointer[tls.Certificate]
}

// New loads or issues the credential at path. now and logf may be nil.
func New(ca *pki.CA, path string, names []string, now func() time.Time, logf func(string, ...any)) (*Manager, error) {
	if now == nil {
		now = time.Now
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	m := &Manager{ca: ca, path: path, names: names, now: now, logf: logf}
	if err := m.Check(); err != nil {
		return nil, err
	}
	return m, nil
}

// Check re-issues the certificate if the file is missing or unusable, does
// not cover the configured names, is not server-auth only, was not issued by
// this CA, or expires within RenewBefore. It loads the file first, so a
// certificate issued by an earlier run is kept.
func (m *Manager) Check() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if m.ca.Cert.NotAfter.Sub(now) < CAWarnBefore {
		m.logf("control CA expires %s; after that every agent must be enrolled again (sorry-portainer-server pki reset)", m.ca.Cert.NotAfter.UTC().Format(time.RFC3339))
	}
	if cert := m.current.Load(); cert != nil && m.usable(cert.Leaf, now) {
		return nil
	}
	if cert, err := pki.ReadBundle(m.path); err == nil && m.usable(cert.Leaf, now) {
		m.current.Store(cert)
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		m.logf("control certificate %s is unusable (%v); issuing a new one", m.path, err)
	}
	key, err := pki.NewKey()
	if err != nil {
		return err
	}
	leaf, err := m.ca.IssueServer(&key.PublicKey, m.names, now)
	if err != nil {
		return err
	}
	if err := pki.WriteBundle(m.path, key, leaf, m.ca.Cert); err != nil {
		return fmt.Errorf("write control certificate: %w", err)
	}
	m.current.Store(pki.TLSCertificate(key, leaf, m.ca.Cert))
	m.logf("issued control certificate for %v, valid until %s", m.names, leaf.NotAfter.UTC().Format(time.RFC3339))
	return nil
}

func (m *Manager) usable(leaf *x509.Certificate, now time.Time) bool {
	return leaf != nil &&
		leaf.CheckSignatureFrom(m.ca.Cert) == nil &&
		pki.ServerCovers(leaf, m.names) &&
		!now.Before(leaf.NotBefore) &&
		leaf.NotAfter.Sub(now) > RenewBefore
}

// GetCertificate is the TLS config's only certificate source, so clients
// with and without SNI get the current certificate.
func (m *Manager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return m.current.Load(), nil
}

// Run checks every CheckInterval until ctx ends. A failed check is logged
// and retried at the next interval; the current certificate stays in use.
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(CheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.Check(); err != nil {
				m.logf("control certificate check failed: %v", err)
			}
		}
	}
}

// TLSConfig is the control listener's configuration. A client certificate
// is optional so that enrollment works without one, but any certificate
// presented is verified against the CA; routes that need one check it
// themselves. Session tickets are off, so every connection is verified in
// full.
func (m *Manager) TLSConfig() *tls.Config {
	roots := x509.NewCertPool()
	roots.AddCert(m.ca.Cert)
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		GetCertificate:         m.GetCertificate,
		ClientCAs:              roots,
		ClientAuth:             tls.VerifyClientCertIfGiven,
		SessionTicketsDisabled: true,
		Time:                   m.now,
	}
}
