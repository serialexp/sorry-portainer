package pki

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"github.com/serialexp/sorry-portainer/internal/atomicfile"
)

// A credential bundle is one PEM file with the private key followed by the
// certificate chain, leaf first. Key and certificate are replaced together by
// a single rename, so no crash leaves a mismatched pair.

// WriteBundle stores key and chain at path (0600), durably.
func WriteBundle(path string, key *ecdsa.PrivateKey, chain ...*x509.Certificate) error {
	if len(chain) == 0 {
		return errors.New("credential bundle needs a certificate")
	}
	if !key.PublicKey.Equal(chain[0].PublicKey) {
		return errors.New("credential key does not match its certificate")
	}
	data, err := encodeKey(key)
	if err != nil {
		return err
	}
	for _, cert := range chain {
		data = append(data, EncodeCertificate(cert)...)
	}
	return atomicfile.Write(path, data, 0o600)
}

// ReadBundle loads a credential bundle. The key must match the leaf, and the
// file must not be group or world accessible.
func ReadBundle(path string) (*tls.Certificate, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s must not be group/world accessible (mode %04o)", path, st.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// X509KeyPair takes the certificate blocks from its first argument and
	// the key block from its second, and checks that they match.
	pair, err := tls.X509KeyPair(data, data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if pair.Leaf == nil {
		if pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if _, ok := pair.PrivateKey.(*ecdsa.PrivateKey); !ok {
		return nil, fmt.Errorf("%s: key is not ECDSA", path)
	}
	return &pair, nil
}

// TLSCertificate builds an in-memory credential from key and chain.
func TLSCertificate(key *ecdsa.PrivateKey, chain ...*x509.Certificate) *tls.Certificate {
	out := &tls.Certificate{PrivateKey: key, Leaf: chain[0]}
	for _, cert := range chain {
		out.Certificate = append(out.Certificate, cert.Raw)
	}
	return out
}
