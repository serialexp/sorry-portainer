package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

const (
	sessionCookie = "sorry_portainer_session"
	// maxSessions bounds the session table; at the limit, expired sessions
	// are dropped first and then the one closest to expiry.
	maxSessions = 4096
)

// SessionOptions configures the dashboard's admin sessions.
type SessionOptions struct {
	Password string
	TTL      time.Duration
	// InsecureCookie leaves Secure off the session cookie, for a dashboard
	// served over plain HTTP during development. Behind a TLS-terminating
	// proxy the server cannot see that the browser used HTTPS, so the cookie
	// is Secure unless this is set.
	InsecureCookie bool
	// TrustedProxies are the reverse proxies whose X-Forwarded-For header
	// names the client, for limiting failed logins per client.
	TrustedProxies []netip.Prefix
	// Now defaults to time.Now.
	Now func() time.Time
}

type Sessions struct {
	mu      sync.Mutex
	values  map[string]time.Time
	opts    SessionOptions
	max     int
	limiter *loginLimiter
}

func NewSessions(o SessionOptions) *Sessions {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Sessions{values: make(map[string]time.Time), opts: o, max: maxSessions, limiter: newLoginLimiter(maxLoginClients)}
}

func (s *Sessions) cookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: sessionCookie, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: !s.opts.InsecureCookie, MaxAge: maxAge}
}

// Login checks the admin password and starts a session. Failed attempts are
// limited per client; a client over its limit is refused before its password
// is checked.
func (s *Sessions) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	client := clientKey(r, s.opts.TrustedProxies)
	now := s.opts.Now()
	if wait, ok := s.limiter.take(client, now); !ok {
		seconds := int(math.Ceil(wait.Seconds()))
		unit := "seconds"
		if seconds == 1 {
			unit = "second"
		}
		w.Header().Set("Retry-After", fmt.Sprint(seconds))
		http.Error(w, fmt.Sprintf("too many failed sign-ins; try again in %d %s", seconds, unit), http.StatusTooManyRequests)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := jsonDecode(r, &body); err != nil || subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.opts.Password)) != 1 {
		// The attempt taken above stays spent.
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	s.limiter.refund(client, now)
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	if len(s.values) >= s.max {
		s.makeRoomLocked(now)
	}
	s.values[token] = now.Add(s.opts.TTL)
	s.mu.Unlock()
	http.SetCookie(w, s.cookie(token, int(s.opts.TTL.Seconds())))
	w.WriteHeader(http.StatusNoContent)
}

// Logout ends the request's session, if any, and clears the cookie.
func (s *Sessions) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.values, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, s.cookie("", -1))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Sessions) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := r.Cookie(sessionCookie)
		if e != nil || !s.valid(c.Value) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Sessions) valid(v string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.values[v]
	if !ok || s.opts.Now().After(exp) {
		delete(s.values, v)
		return false
	}
	return true
}

// makeRoomLocked drops expired sessions, and if none had expired, the one
// closest to expiry, so the table never grows past max.
func (s *Sessions) makeRoomLocked(now time.Time) {
	oldest, oldestExpiry := "", time.Time{}
	for k, v := range s.values {
		if now.After(v) {
			delete(s.values, k)
			continue
		}
		if oldest == "" || v.Before(oldestExpiry) {
			oldest, oldestExpiry = k, v
		}
	}
	if len(s.values) >= s.max {
		delete(s.values, oldest)
	}
}
