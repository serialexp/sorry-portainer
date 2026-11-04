package integration_test

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/pki"
	"github.com/serialexp/sorry-portainer/internal/testcontrol"
)

// floodWorkers is far above the 8 unauthenticated slots, so most of the
// flood is refused as busy and the rest fails on its token.
const floodWorkers = 64

// BenchmarkAgentAdmission measures an enrolled agent's admission (TLS
// handshake with its certificate, registry checks, WebSocket upgrade), idle
// and while unauthenticated clients flood /control/enroll with new TLS
// connections. Budget: the flood costs agents no more than the handshakes
// it competes with for CPU; admission never waits on an enrollment slot.
func BenchmarkAgentAdmission(b *testing.B) {
	for _, flood := range []bool{false, true} {
		name := "idle"
		if flood {
			name = "flood"
		}
		b.Run(name, func(b *testing.B) {
			m := testcontrol.Start(b)
			cert := enrolledCertificate(b, m, "hel1")
			var sent atomic.Int64
			stop := make(chan struct{})
			var wg sync.WaitGroup
			if flood {
				for range floodWorkers {
					wg.Go(func() { floodEnroll(m, stop, &sent) })
				}
			}
			b.ResetTimer()
			for b.Loop() {
				if status, err := dialWith(b, m, cert); status != http.StatusSwitchingProtocols {
					b.Fatalf("admission: status %d, %v", status, err)
				}
			}
			b.StopTimer()
			close(stop)
			wg.Wait()
			if flood {
				b.ReportMetric(float64(sent.Load())/b.Elapsed().Seconds(), "flood-req/s")
			}
		})
	}
}

func enrolledCertificate(b *testing.B, m *testcontrol.Master, host string) *tls.Certificate {
	b.Helper()
	key, err := pki.NewKey()
	if err != nil {
		b.Fatal(err)
	}
	csr, err := pki.NewCSR(key, host)
	if err != nil {
		b.Fatal(err)
	}
	resp, _, err := m.Enrollment.Enroll(enrollment.EnrollRequest{Token: m.Token(b, host), HostID: host, Prefix: host + "-", CSR: string(csr)})
	if err != nil {
		b.Fatal(err)
	}
	leaf, err := pki.ParseCertificatePEM([]byte(resp.Certificate))
	if err != nil {
		b.Fatal(err)
	}
	return pki.TLSCertificate(key, leaf)
}

// floodEnroll posts enrollments with a wrong token, each on a new TLS
// connection, until stop closes.
func floodEnroll(m *testcontrol.Master, stop <-chan struct{}, sent *atomic.Int64) {
	roots := x509.NewCertPool()
	roots.AddCert(m.CA.Cert)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "127.0.0.1", Time: m.Clock.Now},
	}}
	url := "https://" + m.Addr + "/control/enroll"
	body := `{"token":"spt1.AAAAAAAAAAAAAAAAAAAAAA.` + strings.Repeat("A", 43) + `.` + strings.Repeat("A", 43) + `","host_id":"x","prefix":"x-","csr":"x"}`
	for {
		select {
		case <-stop:
			return
		default:
		}
		resp, err := client.Post(url, "application/json", bytes.NewBufferString(body))
		if err == nil {
			_ = resp.Body.Close()
		}
		sent.Add(1)
	}
}
