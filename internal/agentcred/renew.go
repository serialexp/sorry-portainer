package agentcred

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/serialexp/sorry-portainer/internal/pki"
)

const (
	// RenewBefore is how long before expiry an agent renews, less a jitter
	// of up to MaxJitter chosen per certificate.
	RenewBefore = 30 * 24 * time.Hour
	MaxJitter   = 7 * 24 * time.Hour
	// CheckInterval is the longest the renewer sleeps between checks.
	CheckInterval = 12 * time.Hour
	minRetry      = time.Minute
	maxRetry      = time.Hour
)

// Renewer replaces the agent's certificate before it expires.
type Renewer struct {
	HostID         string
	ServerURL      string
	ServerName     string
	CA             *x509.Certificate
	Dir            string // holds the staged key
	CredentialFile string
	Holder         *Holder
	// OnRenewed runs after a new credential is installed; the agent uses it
	// to reconnect with the new certificate so the master promotes it.
	OnRenewed func()
	Now       func() time.Time
	Logf      func(string, ...any)
	// sleep waits; tests replace it.
	sleep func(context.Context, time.Duration) error
}

// RenewAt is when the renewer replaces cert: RenewBefore before expiry,
// minus a jitter derived from the certificate, so it is stable across
// restarts and differs between hosts enrolled together.
func RenewAt(cert *x509.Certificate) time.Time {
	sum := sha256.Sum256(cert.Raw)
	jitter := time.Duration(binary.BigEndian.Uint64(sum[:8]) % uint64(MaxJitter))
	return cert.NotAfter.Add(-RenewBefore - jitter)
}

func (r *Renewer) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *Renewer) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Run renews whenever the certificate is due, until ctx ends. Renewals run
// one at a time; failures are retried with backoff from a minute to an hour.
func (r *Renewer) Run(ctx context.Context) error {
	if r.sleep == nil {
		r.sleep = sleepContext
	}
	retry := minRetry
	for {
		leaf := r.Holder.Current().Leaf
		now := r.now()
		wait := RenewAt(leaf).Sub(now)
		switch {
		case !now.Before(leaf.NotAfter):
			r.logf("agent certificate expired %s; it cannot be renewed; enroll this host again", leaf.NotAfter.UTC().Format(time.RFC3339))
			wait = CheckInterval
		case wait <= 0:
			if err := r.RenewOnce(ctx); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				r.logf("certificate renewal failed (retrying in %s): %v", retry, err)
				wait, retry = retry, min(retry*2, maxRetry)
			} else {
				retry = minRetry
				continue
			}
		default:
			wait = min(wait, CheckInterval)
		}
		if err := r.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// RenewOnce asks the master for a new certificate with a new (staged) key,
// installs it, and calls OnRenewed.
func (r *Renewer) RenewOnce(ctx context.Context) error {
	renewURL, err := controlURL(r.ServerURL, "/control/renew")
	if err != nil {
		return err
	}
	stagedPath := filepath.Join(r.Dir, StagedKeyFile)
	key, err := stagedKey(stagedPath)
	if err != nil {
		return err
	}
	csr, err := pki.NewCSR(key, r.HostID)
	if err != nil {
		return err
	}
	current := r.Holder.Current()
	roots := x509.NewCertPool()
	roots.AddCert(r.CA)
	// This connection authenticates with the current certificate only.
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: r.ServerName, Certificates: []tls.Certificate{*current}, Time: r.now}
	var resp struct {
		Certificate string `json:"certificate"`
	}
	if err := post(ctx, tlsConfig, renewURL, struct {
		CSR string `json:"csr"`
	}{string(csr)}, &resp); err != nil {
		return err
	}
	cert, err := pki.ParseCertificatePEM([]byte(resp.Certificate))
	if err != nil {
		return fmt.Errorf("renewed certificate: %w", err)
	}
	if err := pki.CheckIssuedAgent(cert, r.CA, r.HostID, &key.PublicKey, r.now()); err != nil {
		return fmt.Errorf("renewed certificate: %w", err)
	}
	if err := pki.WriteBundle(r.CredentialFile, key, cert); err != nil {
		return err
	}
	r.Holder.Set(pki.TLSCertificate(key, cert))
	if err := os.Remove(stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	r.logf("renewed agent certificate until %s; reconnecting with it", cert.NotAfter.UTC().Format(time.RFC3339))
	if r.OnRenewed != nil {
		r.OnRenewed()
	}
	return nil
}
