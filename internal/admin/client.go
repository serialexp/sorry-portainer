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

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/unixsock"
)

// clientMargin is how much longer than the server's deadline a client waits:
// the time to reach the server, an SSH handshake included.
const clientMargin = 5 * time.Second

func socketPath(stateDir string) string { return filepath.Join(stateDir, SocketFile) }

// Client talks to the admin socket of a server, directly or through SSH
// (NewSSHClient). Every call is one exchange on a fresh connection.
type Client struct {
	// dial opens the connection for one exchange.
	dial func(ctx context.Context) (io.ReadWriteCloser, error)
	// socket and allowServer are for a direct connection. allowServer decides
	// which server uids are trusted: this process's own, root's, and that of
	// the state directory's owner, which is the server's account. Anyone else
	// holding the socket path is not the server.
	socket      string
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
	c := &Client{socket: socketPath(stateDir), allowServer: func(uid int) bool { return serverAllowed(uid, self, owner) }}
	c.dial = func(ctx context.Context) (io.ReadWriteCloser, error) { return c.dialSocket(ctx) }
	return c, nil
}

// serverAllowed: the client trusts a server running as itself, as root, or as
// the owner of the state directory.
func serverAllowed(uid, self, owner int) bool { return uid == self || uid == 0 || uid == owner }

// dialSocket connects to the socket and checks who holds it.
func (c *Client) dialSocket(ctx context.Context) (*net.UnixConn, error) {
	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, fmt.Errorf("reach the server at %s (is it running?): %w", c.socket, err)
	}
	conn := raw.(*net.UnixConn)
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	uid, err := unixsock.PeerUID(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !c.allowServer(uid) {
		_ = conn.Close()
		return nil, fmt.Errorf("%s is held by uid %d, which is not the server's account", c.socket, uid)
	}
	return conn, nil
}

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

// Stacks lists a host's stacks.
func (c *Client) Stacks(ctx context.Context, hostID string) ([]protocol.Stack, error) {
	var out []protocol.Stack
	return out, c.call(ctx, Request{Action: ActionStackList, HostID: hostID}, &out)
}

// Stack returns one stack, with its Compose file.
func (c *Client) Stack(ctx context.Context, hostID, name string) (protocol.Stack, error) {
	var out protocol.Stack
	return out, c.call(ctx, Request{Action: ActionStackInspect, HostID: hostID, Stack: name}, &out)
}

// SaveStack creates a stack (save.ExpectedVersion nil, environment allowed)
// or updates one from the version it expects.
func (c *Client) SaveStack(ctx context.Context, hostID string, save protocol.StackSave) (protocol.Stack, error) {
	var out protocol.Stack
	return out, c.call(ctx, Request{Action: ActionStackSave, HostID: hostID, Save: &save}, &out)
}

// StackOperation runs "up", "down" or "restart". A Compose run that failed
// is a result with Success false and the output, not an error.
func (c *Client) StackOperation(ctx context.Context, hostID, name, operation string) (protocol.StackOperation, error) {
	var out protocol.StackOperation
	return out, c.call(ctx, Request{Action: "stacks." + operation, HostID: hostID, Stack: name}, &out)
}

func (c *Client) call(ctx context.Context, request Request, out any) error {
	ctx, cancel := context.WithTimeout(ctx, ActionTimeout(request.Action)+clientMargin)
	defer cancel()
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Closing the connection is what ends a blocked write or read once ctx
	// is done; a direct connection also has its deadline.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return answerError(ctx, "send the request", err)
	}
	// Read exactly one value: the server may close before reading all of a
	// request it refused, and the reset that follows the answer is not an
	// error. The connection stays open until then, which tells the server
	// the client is still waiting.
	var data json.RawMessage
	if err := json.NewDecoder(io.LimitReader(conn, MaxResponse)).Decode(&data); err != nil {
		return answerError(ctx, "read the server's answer", err)
	}
	var failure errorBody
	if err := json.Unmarshal(data, &failure); err == nil && failure.Error != "" {
		return &Error{Code: failure.Code, Message: failure.Error}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode the server's answer: %w", err)
	}
	return nil
}

// answerError says what failed, and that time ran out when it did.
func answerError(ctx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: no answer in time: %w", what, ctx.Err())
	}
	return fmt.Errorf("%s: %w", what, err)
}
