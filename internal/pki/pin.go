package pki

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"time"
)

// PinnedClientConfig authenticates the master during enrollment, before the
// agent has a CA file: the presented chain must contain the CA whose
// fingerprint the token carries, and the leaf must verify against that CA
// alone, for serverName, for server authentication. No client certificate is
// sent, so an expired or foreign certificate on disk cannot get in the way.
func PinnedClientConfig(pin Fingerprint, serverName string, now func() time.Time) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: serverName,
		// Verification happens in VerifyConnection against the pinned CA;
		// the system roots must play no part.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			_, err := VerifyPinned(state.PeerCertificates, pin, serverName, now())
			return err
		},
	}
}

// VerifyPinned checks a presented chain against the pinned CA and returns
// the CA.
func VerifyPinned(chain []*x509.Certificate, pin Fingerprint, serverName string, now time.Time) (*x509.Certificate, error) {
	if len(chain) == 0 {
		return nil, errors.New("master presented no certificate")
	}
	var ca *x509.Certificate
	for _, cert := range chain[1:] {
		if FingerprintOf(cert) == pin {
			ca = cert
			break
		}
	}
	if ca == nil || !ca.IsCA {
		return nil, errors.New("master's certificate chain does not contain the control CA named in the token")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	_, err := chain[0].Verify(x509.VerifyOptions{
		DNSName:     serverName,
		Roots:       roots,
		CurrentTime: now,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		return nil, fmt.Errorf("master's certificate does not verify against the control CA named in the token: %w", err)
	}
	return ca, nil
}
