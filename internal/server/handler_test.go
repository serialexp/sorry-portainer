package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDashboardGetsEveryPathOutsideTheAPI(t *testing.T) {
	s := New("a sufficiently long password", time.Hour, &stackRelay{}, nil)
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := s.Handler(ui)
	cases := []struct {
		path string
		want int
	}{
		{"/", http.StatusTeapot},
		{"/hosts/h1/stacks/web", http.StatusTeapot},
		{"/assets/index-3f.js", http.StatusTeapot},
		{"/healthz", http.StatusNoContent},
		{"/api/hosts", http.StatusUnauthorized},
		{"/api/unknown", http.StatusNotFound},
		{"/api/", http.StatusNotFound},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, c.path, nil))
		if w.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.path, w.Code, c.want)
		}
	}
}

func TestWithoutADashboardOnlyTheAPIAnswers(t *testing.T) {
	s := New("a sufficiently long password", time.Hour, &stackRelay{}, nil)
	w := httptest.NewRecorder()
	s.Handler(nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", w.Code)
	}
}
