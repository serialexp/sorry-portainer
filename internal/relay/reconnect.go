package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	handshakeTimeout = 15 * time.Second
	// DefaultMinBackoff and DefaultMaxBackoff bound the wait between reconnects.
	DefaultMinBackoff = time.Second
	DefaultMaxBackoff = 30 * time.Second
	// DefaultStableAfter is how long a session must last before the backoff resets,
	// so a server that accepts and then immediately drops the agent is not hammered.
	DefaultStableAfter = time.Minute
)

// DialAgent opens the agent's mTLS WebSocket to the server.
func DialAgent(ctx context.Context, url string, tlsConfig *tls.Config) (*websocket.Conn, error) {
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		TLSClientConfig:  tlsConfig,
		HandshakeTimeout: handshakeTimeout,
	}
	conn, _, err := dialer.DialContext(ctx, url, nil)
	return conn, err
}

// ReconnectOptions configures RunAgent. Zero values select the defaults.
type ReconnectOptions struct {
	MinBackoff, MaxBackoff, StableAfter time.Duration
	// Dial opens one connection. It is required.
	Dial func(context.Context) (*websocket.Conn, error)
	// OnDisconnect is told why each attempt ended and how long RunAgent will wait.
	OnDisconnect func(err error, wait time.Duration)
}

// RunAgent keeps the agent connected until ctx ends, redialling with jittered
// exponential backoff. It returns only ctx's error.
func RunAgent(ctx context.Context, agent *Agent, options ReconnectOptions) error {
	if options.Dial == nil {
		return errors.New("reconnect options need a Dial function")
	}
	if options.MinBackoff <= 0 {
		options.MinBackoff = DefaultMinBackoff
	}
	if options.MaxBackoff < options.MinBackoff {
		options.MaxBackoff = max(DefaultMaxBackoff, options.MinBackoff)
	}
	if options.StableAfter <= 0 {
		options.StableAfter = DefaultStableAfter
	}
	backoff := options.MinBackoff
	for {
		started := time.Now()
		err := connectOnce(ctx, agent, options.Dial)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(started) >= options.StableAfter {
			backoff = options.MinBackoff
		}
		// Jitter in [backoff/2, backoff] spreads a fleet reconnecting after a
		// server restart.
		wait := backoff/2 + rand.N(backoff/2+1)
		if options.OnDisconnect != nil {
			options.OnDisconnect(err, wait)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(backoff*2, options.MaxBackoff)
	}
}

func connectOnce(ctx context.Context, agent *Agent, dial func(context.Context) (*websocket.Conn, error)) error {
	conn, err := dial(ctx)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	return agent.Serve(ctx, conn)
}
