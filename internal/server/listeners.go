package server

import (
	"net/http"
	"time"
)

const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 2 * time.Minute
	maxHeaderBytes    = 16 << 10
	// controlRequestTimeout bounds reading and answering enroll and renew
	// requests. The agent route hijacks its connection first and then
	// clears the deadlines.
	controlRequestTimeout = 30 * time.Second
)

// NewWebServer is the web listener's server. Stack operations can keep a
// response open for minutes, so only the header read and idle time are
// bounded, not the whole response.
func NewWebServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// NewControlServer is the control listener's server. It takes requests
// without a client certificate (enrollment), so every request is bounded.
func NewControlServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       controlRequestTimeout,
		WriteTimeout:      controlRequestTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}
