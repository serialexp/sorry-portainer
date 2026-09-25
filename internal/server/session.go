package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"sync"
	"time"
)

type Sessions struct {
	mu       sync.Mutex
	values   map[string]time.Time
	password string
	ttl      time.Duration
	max      int
}

func NewSessions(password string, ttl time.Duration) *Sessions {
	return &Sessions{values: make(map[string]time.Time), password: password, ttl: ttl, max: 4096}
}
func (s *Sessions) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if r.Body == nil {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	if err := jsonDecode(r, &body); err != nil || subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.password)) != 1 {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		http.Error(w, "internal error", 500)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	if len(s.values) >= s.max {
		s.pruneLocked(time.Now())
	}
	s.values[token] = time.Now().Add(s.ttl)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "sorry_portainer_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: int(s.ttl.Seconds())})
	w.WriteHeader(http.StatusNoContent)
}
func (s *Sessions) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := r.Cookie("sorry_portainer_session")
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
	if !ok || time.Now().After(exp) {
		delete(s.values, v)
		return false
	}
	return true
}
func (s *Sessions) pruneLocked(now time.Time) {
	for k, v := range s.values {
		if now.After(v) {
			delete(s.values, k)
		}
	}
}
