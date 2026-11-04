package integration_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/testcontrol"
)

// controlClient talks to the control listener without a client certificate.
func controlClient(m *testcontrol.Master) *http.Client {
	roots := x509.NewCertPool()
	roots.AddCert(m.CA.Cert)
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "127.0.0.1", Time: m.Clock.Now},
	}}
}

func TestControlRoutesAndLimits(t *testing.T) {
	m := testcontrol.Start(t)
	client := controlClient(m)
	base := "https://" + m.Addr
	do := func(method, path, body string, header http.Header) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range header {
			req.Header[k] = v
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var failure struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&failure)
		return resp.StatusCode, failure.Code
	}
	cases := []struct {
		name, method, path, body string
		header                   http.Header
		status                   int
		code                     string
	}{
		{"unknown path", http.MethodGet, "/control/other", "", nil, http.StatusNotFound, ""},
		{"enroll by GET", http.MethodGet, "/control/enroll", "", nil, http.StatusMethodNotAllowed, "invalid_request"},
		{"renew by GET", http.MethodGet, "/control/renew", "", nil, http.StatusMethodNotAllowed, "invalid_request"},
		// No identity is 401; an identity that is not enrolled is 403.
		{"renew without certificate", http.MethodPost, "/control/renew", `{"csr":""}`, nil, http.StatusUnauthorized, "invalid_request"},
		{"agent without certificate", http.MethodGet, "/control/agent", "", nil, http.StatusUnauthorized, "invalid_request"},
		{"unknown field", http.MethodPost, "/control/enroll", `{"token":"x","admin":true}`, nil, http.StatusBadRequest, "invalid_request"},
		{"trailing data", http.MethodPost, "/control/enroll", `{"token":"x"} {}`, nil, http.StatusBadRequest, "invalid_request"},
		{"oversized body", http.MethodPost, "/control/enroll", `{"token":"` + strings.Repeat("a", 64<<10) + `"}`, nil, http.StatusRequestEntityTooLarge, "invalid_request"},
		{"oversized header", http.MethodPost, "/control/enroll", `{}`, http.Header{"X-Pad": {strings.Repeat("a", 20<<10)}}, http.StatusRequestHeaderFieldsTooLarge, ""},
	}
	for _, c := range cases {
		status, code := do(c.method, c.path, c.body, c.header)
		if status != c.status || code != c.code {
			t.Errorf("%s: %d %q, want %d %q", c.name, status, code, c.status, c.code)
		}
	}
}
