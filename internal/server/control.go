package server

import (
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/protocol"
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
		// Serve owns the hijacked connection and returns when it ends, so a
		// disconnected agent is removed promptly rather than on request cancel.
		if err := registry.Serve(conn, certificateHostID); err != nil {
			log.Printf("agent %s disconnected: %v", certificateHostID, err)
		}
	})
}

// certificateIdentity returns the host ID carried by an agent certificate. The
// ID names master-side directories, so it must pass protocol.ValidHostID.
func certificateIdentity(cert *x509.Certificate) (string, error) {
	identity := ""
	for _, uri := range cert.URIs {
		if uri.Scheme == "sorry-host" {
			identity = uri.Opaque
			if identity == "" {
				identity = uri.Host
			}
			break
		}
	}
	if identity == "" && len(cert.DNSNames) == 1 && strings.HasPrefix(cert.DNSNames[0], "host-") {
		identity = strings.TrimPrefix(cert.DNSNames[0], "host-")
	}
	if identity == "" {
		return "", errors.New("agent certificate has no host identity")
	}
	if !protocol.ValidHostID(identity) {
		return "", fmt.Errorf("agent certificate host identity %q is invalid", identity)
	}
	return identity, nil
}
