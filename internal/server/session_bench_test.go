package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// BenchmarkLimiterFullTable: every client slot is held by a client still
// being limited, and new clients keep arriving. The table is scanned at
// most once per pruneInterval, so this stays a map lookup.
func BenchmarkLimiterFullTable(b *testing.B) {
	l := newLoginLimiter(maxLoginClients)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for i := range maxLoginClients {
		client := netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), 32)
		for range loginBurst {
			l.take(client, now)
		}
	}
	stranger := netip.MustParsePrefix("192.0.2.1/32")
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		// Time moves slowly enough that every scan finds nothing to drop.
		if _, ok := l.take(stranger, now.Add(time.Duration(i))); ok {
			b.Fatal("stranger admitted into a full table")
		}
	}
}

// BenchmarkLoginAtSessionCap: every login past maxSessions scans the table
// for the session closest to expiry.
func BenchmarkLoginAtSessionCap(b *testing.B) {
	s := NewSessions(SessionOptions{Password: testPassword, TTL: time.Hour})
	for i := range maxSessions {
		s.values[fmt.Sprint(i)] = time.Now().Add(time.Hour + time.Duration(i)*time.Millisecond)
	}
	body := fmt.Sprintf(`{"password":%q}`, testPassword)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r := httptest.NewRequest(http.MethodPost, "/api/session/login", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.Login(w, r)
		if w.Code != http.StatusNoContent {
			b.Fatalf("login %d", w.Code)
		}
	}
	b.StopTimer()
	if len(s.values) != maxSessions {
		b.Fatalf("%d sessions", len(s.values))
	}
}
