package server

import (
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	// A client may fail loginBurst sign-ins in a row, then one more every
	// loginInterval.
	loginBurst    = 10
	loginInterval = 3 * time.Second
	// maxLoginClients bounds the limiter's table.
	maxLoginClients = 4096
	// pruneInterval spaces out scans of a full table.
	pruneInterval = time.Second
)

// loginLimiter limits sign-in attempts per client with a generic cell rate
// algorithm: each client has one timestamp, the time its allowance would be
// full again, so an entry at or before now is the same as no entry.
type loginLimiter struct {
	mu        sync.Mutex
	full      map[netip.Prefix]time.Time
	max       int
	lastPrune time.Time
}

func newLoginLimiter(capacity int) *loginLimiter {
	return &loginLimiter{full: make(map[netip.Prefix]time.Time), max: capacity}
}

// take spends one attempt for client. When the client has none left, or the
// table is full of clients still being limited, it returns how long to wait.
func (l *loginLimiter) take(client netip.Prefix, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	tat, known := l.full[client]
	if !known && len(l.full) >= l.max {
		l.pruneLocked(now)
		if len(l.full) >= l.max {
			// Refusing an unknown client is the bounded choice: evicting a
			// limited one would hand its attempts back.
			return loginInterval, false
		}
	}
	if tat.Before(now) {
		tat = now
	}
	next := tat.Add(loginInterval)
	if over := next.Sub(now) - loginBurst*loginInterval; over > 0 {
		return over, false
	}
	l.full[client] = next
	return 0, true
}

// refund returns the attempt a successful sign-in took.
func (l *loginLimiter) refund(client netip.Prefix, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	tat, ok := l.full[client]
	if !ok {
		return
	}
	if tat = tat.Add(-loginInterval); tat.After(now) {
		l.full[client] = tat
	} else {
		delete(l.full, client)
	}
}

func (l *loginLimiter) pruneLocked(now time.Time) {
	if now.Sub(l.lastPrune) < pruneInterval {
		return
	}
	l.lastPrune = now
	for client, tat := range l.full {
		if !tat.After(now) {
			delete(l.full, client)
		}
	}
}

// clientKey identifies the client a request comes from: its IPv4 address, or
// its IPv6 /64, since one host usually holds a whole /64. When the peer is a
// trusted proxy, the client is the rightmost X-Forwarded-For entry that is
// not itself a trusted proxy.
func clientKey(r *http.Request, trusted []netip.Prefix) netip.Prefix {
	addr := peerAddr(r)
	if isTrusted(addr, trusted) {
		hops := forwardedFor(r)
		for i := len(hops) - 1; i >= 0; i-- {
			hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				// A garbled entry: everything left of it is unverifiable,
				// so the last trusted hop stands for the client.
				break
			}
			addr = hop.Unmap()
			if !isTrusted(addr, trusted) {
				break
			}
		}
	}
	return clientPrefix(addr)
}

func peerAddr(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

func forwardedFor(r *http.Request) []string {
	var hops []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(line, ",")...)
	}
	return hops
}

func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func clientPrefix(addr netip.Addr) netip.Prefix {
	bits := 32
	if addr.Is6() {
		bits = 64
	}
	if !addr.IsValid() {
		return netip.Prefix{}
	}
	p, _ := addr.Prefix(bits)
	return p
}
