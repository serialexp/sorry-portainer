package agentcred

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *tls.Config) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	return srv, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
}

func TestPostRefusesRedirects(t *testing.T) {
	followed := false
	srv, tlsConfig := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed = true
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	})
	err := post(context.Background(), tlsConfig, srv.URL+"/control/enroll", struct{}{}, &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "must not redirect") || followed {
		t.Fatalf("post = %v, followed %v", err, followed)
	}
}

func TestPostBoundsTheResponse(t *testing.T) {
	srv, tlsConfig := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"certificate":"` + strings.Repeat("A", maxResponseBody) + `"}`))
	})
	err := post(context.Background(), tlsConfig, srv.URL, struct{}{}, &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("post = %v", err)
	}
}

func TestPostReportsControlErrors(t *testing.T) {
	srv, tlsConfig := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"enrollment token is for another host","code":"wrong_host"}`))
	})
	err := post(context.Background(), tlsConfig, srv.URL, struct{}{}, &struct{}{})
	var ce *ControlError
	if !errors.As(err, &ce) || ce.Status != http.StatusForbidden || ce.Code != "wrong_host" {
		t.Fatalf("post = %#v", err)
	}
}
