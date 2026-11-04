package agentcred

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/pki"
	"github.com/serialexp/sorry-portainer/internal/testcontrol"
)

func enrolled(t *testing.T, m *testcontrol.Master, host string) (*Renewer, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), host)
	token, _, err := m.Enrollment.CreateToken(host, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Enroll(context.Background(), EnrollOptions{ServerURL: m.ServerURL, HostID: host, Token: token, Dir: dir, Now: m.Clock.Now}); err != nil {
		t.Fatal(err)
	}
	holder, err := LoadHolder(filepath.Join(dir, CredentialFile))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := ReadCA(filepath.Join(dir, CAFile))
	if err != nil {
		t.Fatal(err)
	}
	return &Renewer{HostID: host, ServerURL: m.ServerURL, ServerName: "127.0.0.1", CA: ca, Dir: dir,
		CredentialFile: filepath.Join(dir, CredentialFile), Holder: holder, Now: m.Clock.Now, Logf: t.Logf}, dir
}

func TestRenewAtIsWithinTheJitterWindow(t *testing.T) {
	m := testcontrol.Start(t)
	r, _ := enrolled(t, m, "hel1")
	leaf := r.Holder.Current().Leaf
	at := RenewAt(leaf)
	latest, earliest := leaf.NotAfter.Add(-RenewBefore), leaf.NotAfter.Add(-RenewBefore-MaxJitter)
	if at.After(latest) || at.Before(earliest) {
		t.Fatalf("renew at %s, want between %s and %s", at, earliest, latest)
	}
	if !RenewAt(leaf).Equal(at) {
		t.Fatal("renewal time is not stable for a certificate")
	}
}

// sleeper records requested waits and ends Run after a number of them.
type sleeper struct {
	mu     sync.Mutex
	waits  []time.Duration
	cancel context.CancelFunc
	after  int
	onWait func(time.Duration)
}

func (s *sleeper) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.waits = append(s.waits, d)
	n := len(s.waits)
	s.mu.Unlock()
	if s.onWait != nil {
		s.onWait(d)
	}
	if n >= s.after {
		s.cancel()
		return context.Canceled
	}
	return nil
}

func TestRunSleepsUntilDueThenRenews(t *testing.T) {
	m := testcontrol.Start(t)
	r, _ := enrolled(t, m, "hel1")
	first := r.Holder.Current().Leaf
	renewed := 0
	r.OnRenewed = func() { renewed++ }
	ctx, cancel := context.WithCancel(context.Background())
	s := &sleeper{cancel: cancel, after: 3}
	// Each wait moves the clock forward by what was asked.
	s.onWait = func(d time.Duration) { m.Clock.Advance(d) }
	r.sleep = s.sleep
	if err := r.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	for _, wait := range s.waits {
		if wait > CheckInterval {
			t.Fatalf("slept %s, longer than the check interval", wait)
		}
	}
	if renewed == 0 || r.Holder.Current().Leaf.Equal(first) {
		// Three waits of at most 12 hours do not reach a 53-day renewal;
		// advance to it and run again.
		m.Clock.Advance(RenewAt(first).Sub(m.Clock.Now()))
		ctx, cancel = context.WithCancel(context.Background())
		s = &sleeper{cancel: cancel, after: 1}
		r.sleep = s.sleep
		_ = r.Run(ctx)
	}
	if renewed != 1 || r.Holder.Current().Leaf.Equal(first) {
		t.Fatalf("renewed %d times", renewed)
	}
	// After renewing, the next wait is for the new certificate.
	if len(s.waits) != 1 || s.waits[0] != CheckInterval {
		t.Fatalf("waits after renewal %v", s.waits)
	}
}

func TestRunBacksOffOnFailure(t *testing.T) {
	m := testcontrol.Start(t)
	r, _ := enrolled(t, m, "hel1")
	m.Clock.Advance(RenewAt(r.Holder.Current().Leaf).Sub(m.Clock.Now()))
	r.ServerURL = "wss://127.0.0.1:1/control/agent" // nothing listens
	ctx, cancel := context.WithCancel(context.Background())
	s := &sleeper{cancel: cancel, after: 8}
	r.sleep = s.sleep
	_ = r.Run(ctx)
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour}
	for i, wait := range s.waits {
		if wait != want[i] {
			t.Fatalf("waits %v, want %v", s.waits, want)
		}
	}
}

func TestRunReportsExpiredCertificate(t *testing.T) {
	m := testcontrol.Start(t)
	r, _ := enrolled(t, m, "hel1")
	m.Clock.Advance(r.Holder.Current().Leaf.NotAfter.Sub(m.Clock.Now()) + time.Second)
	var logged []string
	r.Logf = func(format string, args ...any) { logged = append(logged, format) }
	ctx, cancel := context.WithCancel(context.Background())
	r.sleep = (&sleeper{cancel: cancel, after: 1}).sleep
	_ = r.Run(ctx)
	if len(logged) != 1 || !strings.Contains(logged[0], "enroll this host again") {
		t.Fatalf("logged %v", logged)
	}
}

// TestRenewalSurvivesACrashBeforeInstalling: the master signed and recorded
// a pending certificate, but the agent died before writing it. The retry
// uses the staged key and gets the same certificate.
func TestRenewalSurvivesACrashBeforeInstalling(t *testing.T) {
	m := testcontrol.Start(t)
	r, dir := enrolled(t, m, "hel1")
	old := r.Holder.Current()
	r.CredentialFile = filepath.Join(dir, "missing", CredentialFile) // the write fails
	if err := r.RenewOnce(context.Background()); err == nil {
		t.Fatal("renewal into a missing directory succeeded")
	}
	host, _ := m.Enrollment.Registry().Get("hel1")
	if host.Pending == (enrollment.CertRef{}) {
		t.Fatal("master recorded no pending certificate")
	}
	if r.Holder.Current() != old {
		t.Fatal("holder changed although the credential was not written")
	}
	if _, err := os.Stat(filepath.Join(dir, StagedKeyFile)); err != nil {
		t.Fatalf("staged key gone: %v", err)
	}
	// The agent is still accepted with its old certificate meanwhile.
	if s, err := m.Enrollment.Check("hel1", old.Leaf); err != nil || s != enrollment.StandingCurrent {
		t.Fatalf("old certificate after a failed install: %v %v", s, err)
	}
	r.CredentialFile = filepath.Join(dir, CredentialFile)
	if err := r.RenewOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := pki.FingerprintOf(r.Holder.Current().Leaf).String(); got != host.Pending.SHA256 {
		t.Fatal("retry did not get the pending certificate back")
	}
}
