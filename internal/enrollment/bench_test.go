package enrollment

import (
	"crypto/rand"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/pki"
)

// benchHosts is the fleet size the performance budget is written for.
const benchHosts = 1000

// populate adds n synthetic hosts in one write. Their certificates are not
// real, which the registry does not need: it stores fingerprints and expiry.
// Every tenth host has a pending renewal, whose PEM is what makes the file
// large.
func populate(b *testing.B, f *fixture, n int) {
	b.Helper()
	_, sample := f.enroll("sample")
	pending := refOf(sample, true)
	err := f.svc.registry.update(func(hosts map[string]Host) error {
		for i := range n {
			var fp pki.Fingerprint
			_, _ = rand.Read(fp[:])
			h := Host{EnrolledAt: f.clock.now(), Prefix: fmt.Sprintf("bench-%04d-", i),
				Current: CertRef{SHA256: fp.String(), NotAfter: f.clock.now().Add(pki.LeafLifetime)}}
			if i%10 == 0 {
				h.Pending = pending
			}
			hosts[fmt.Sprintf("bench-%04d", i)] = h
		}
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
}

// BenchmarkRegistryWrite measures one durable registry write (rename and
// fsync included) at 1,000 hosts. Budget: under 50 ms on an SSD.
func BenchmarkRegistryWrite(b *testing.B) {
	f := newFixture(b)
	populate(b, f, benchHosts)
	st, err := os.Stat(f.path)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; b.Loop(); i++ {
		err := f.svc.registry.update(func(hosts map[string]Host) error {
			id := fmt.Sprintf("bench-%04d", i%benchHosts)
			h := hosts[id]
			h.EnrolledAt = h.EnrolledAt.Add(time.Second)
			hosts[id] = h
			return nil
		})
		if err != nil {
			b.Fatal(err)
		}
	}
	// After the loop: resetting the timer drops reported metrics.
	b.ReportMetric(float64(st.Size()), "file-bytes")
}

// BenchmarkCheckDuringWrites measures the admission lookup at 1,000 hosts
// while the registry is rewritten continuously. Budget: never blocked by a
// write, no allocation.
func BenchmarkCheckDuringWrites(b *testing.B) {
	f := newFixture(b)
	_, cert := f.enroll("agent")
	populate(b, f, benchHosts)
	stop := make(chan struct{})
	var writes atomic.Int64
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = f.svc.registry.update(func(hosts map[string]Host) error {
				h := hosts["bench-0000"]
				h.EnrolledAt = h.EnrolledAt.Add(time.Second)
				hosts["bench-0000"] = h
				return nil
			})
			writes.Add(1)
		}
	})
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if s, err := f.svc.Check("agent", cert); err != nil || s != StandingCurrent {
				b.Errorf("check: %v %v", s, err)
				return
			}
		}
	})
	b.StopTimer()
	close(stop)
	wg.Wait()
	b.ReportMetric(float64(writes.Load()), "writes")
}
