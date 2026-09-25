package server

import (
	"crypto/x509"
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/relay"
)

func ControlHandler(registry *relay.Remote) http.Handler {
	// The control listener is mTLS-only and is never exposed as a browser surface;
	// agent clients do not send browser Origin headers.
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/control/agent" {
			http.NotFound(w, r)
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "client certificate required", http.StatusUnauthorized)
			return
		}
		certificateHostID, err := certificateIdentity(r.TLS.PeerCertificates[0])
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		info, err := registry.AcceptAgentForCertificate(conn, certificateHostID)
		if err != nil {
			_ = conn.Close()
			return
		}
		defer registry.Remove(info.HostID, conn)
		<-r.Context().Done()
	})
}

func certificateIdentity(cert *x509.Certificate) (string, error) {
	for _, uri := range cert.URIs {
		if strings.HasPrefix(uri.Scheme, "sorry-host") {
			identity := uri.Opaque
			if identity == "" {
				identity = uri.Host
			}
			if identity != "" {
				return identity, nil
			}
		}
	}
	if len(cert.DNSNames) == 1 && strings.HasPrefix(cert.DNSNames[0], "host-") {
		return strings.TrimPrefix(cert.DNSNames[0], "host-"), nil
	}
	return "", errors.New("agent certificate has no host identity")
}
