package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/serialexp/sorry-portainer/internal/unixsock"
)

func socketPath(stateDir string) string { return filepath.Join(stateDir, SocketFile) }

// Client talks to the admin socket of the server owning a state directory.
type Client struct {
	socket string
	// allowServer decides which server uids are trusted: this process's own,
	// root's, and that of the state directory's owner, which is the server's
	// account. Anyone else holding the socket path is not the server.
	allowServer func(uid int) bool
}

// NewClient prepares a client for the server whose state is in stateDir.
func NewClient(stateDir string) (*Client, error) {
	st, err := os.Stat(stateDir)
	if err != nil {
		return nil, err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("cannot tell who owns %s", stateDir)
	}
	self, owner := os.Getuid(), int(sys.Uid)
	return &Client{socket: socketPath(stateDir), allowServer: func(uid int) bool { return serverAllowed(uid, self, owner) }}, nil
}

// serverAllowed: the client trusts a server running as itself, as root, or as
// the owner of the state directory.
func serverAllowed(uid, self, owner int) bool { return uid == self || uid == 0 || uid == owner }

// Token creates an enrollment token for hostID. A zero ttl is the server's
// default.
func (c *Client) Token(ctx context.Context, hostID string, ttl time.Duration) (TokenResponse, error) {
	request := Request{Action: ActionToken, HostID: hostID}
	if ttl != 0 {
		request.TTL = ttl.String()
	}
	var out TokenResponse
	return out, c.call(ctx, request, &out)
}

// List returns the enrolled hosts and the outstanding tokens.
func (c *Client) List(ctx context.Context) (ListResponse, error) {
	var out ListResponse
	return out, c.call(ctx, Request{Action: ActionList}, &out)
}

// Remove forgets a host, revokes its token and disconnects it.
func (c *Client) Remove(ctx context.Context, hostID string) error {
	return c.call(ctx, Request{Action: ActionRemove, HostID: hostID}, &RemoveResponse{})
}

func (c *Client) call(ctx context.Context, request Request, out any) error {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return fmt.Errorf("reach the server at %s (is it running?): %w", c.socket, err)
	}
	conn := raw.(*net.UnixConn)
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	uid, err := unixsock.PeerUID(conn)
	if err != nil {
		return err
	}
	if !c.allowServer(uid) {
		return fmt.Errorf("%s is held by uid %d, which is not the server's account", c.socket, uid)
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return err
	}
	// Read exactly one value: the server may close before reading all of a
	// request it refused, and the reset that follows the answer is not an
	// error.
	var data json.RawMessage
	if err := json.NewDecoder(io.LimitReader(conn, MaxResponse)).Decode(&data); err != nil {
		return fmt.Errorf("read the server's answer: %w", err)
	}
	var failure errorBody
	if err := json.Unmarshal(data, &failure); err != nil {
		return fmt.Errorf("decode the server's answer: %w", err)
	}
	if failure.Error != "" {
		return &Error{Code: failure.Code, Message: failure.Error}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode the server's answer: %w", err)
	}
	return nil
}
