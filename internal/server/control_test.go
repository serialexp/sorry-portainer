package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/url"
	"testing"
	"time"
)

func TestCertificateIdentityUsesHostURI(t *testing.T) {
	uri, err := url.Parse("sorry-host://local-b")
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{URIs: []*url.URL{uri}}
	got, err := certificateIdentity(cert)
	if err != nil || got != "local-b" {
		t.Fatalf("identity=%q err=%v", got, err)
	}
}
func TestCertificateIdentityRejectsMissingIdentity(t *testing.T) {
	if _, err := certificateIdentity(&x509.Certificate{}); err == nil {
		t.Fatal("expected missing identity error")
	}
}
func TestCertificateIdentityCanUseHostDNSName(t *testing.T) {
	got, err := certificateIdentity(&x509.Certificate{DNSNames: []string{"host-local-c"}})
	if err != nil || got != "local-c" {
		t.Fatalf("identity=%q err=%v", got, err)
	}
}
func TestCertificateIdentityOnGeneratedCertificate(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	uri, _ := url.Parse("sorry-host://local-a")
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "local-a"}, URIs: []*url.URL{uri}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}, &x509.Certificate{SerialNumber: serial, NotAfter: time.Now().Add(time.Hour)}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := certificateIdentity(cert); err != nil || got != "local-a" {
		t.Fatalf("identity=%q err=%v", got, err)
	}
}
