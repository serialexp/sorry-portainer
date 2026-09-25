package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoginAndRequire(t *testing.T) {
	s := NewSessions("a sufficiently long password", time.Hour)
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"password":"a sufficiently long password"}`))
	w := httptest.NewRecorder()
	s.Login(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("login status %d", w.Code)
	}
	c := w.Result().Cookies()[0]
	next := s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(c)
	w = httptest.NewRecorder()
	next.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("authenticated status %d", w.Code)
	}
}
func TestRequireRejectsMissing(t *testing.T) {
	s := NewSessions("a sufficiently long password", time.Hour)
	w := httptest.NewRecorder()
	s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
}
