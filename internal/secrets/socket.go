package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// hookRequest is what the OCI hook asks the agent over its Unix socket.
type hookRequest struct {
	HostID      string   `json:"host_id"`
	ContainerID string   `json:"container_id"`
	Stack       string   `json:"stack"`
	Names       []string `json:"names"`
}

type hookResponse struct {
	Values map[string][]byte `json:"values,omitempty"`
	Error  string            `json:"error,omitempty"`
}

const (
	socketTimeout      = 10 * time.Second
	maxHookRequest     = 64 << 10
	maxConcurrentHooks = 64
)

// maxHookResponse bounds what the hook reads: one stack's values in base64
// plus JSON framing.
const maxHookResponse = protocol.MaxStackSecretsTotal*4/3 + 64<<10

// DefaultSocketPath is the agent's hook socket under the user's runtime
// directory, which systemd creates as a per-user tmpfs with mode 0700.
func DefaultSocketPath(hostID string) (string, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		return "", errors.New("XDG_RUNTIME_DIR is not set; set secret_socket in the agent config")
	}
	return filepath.Join(runtime, "sorry-portainer", hostID+".sock"), nil
}

// SocketServer answers hook requests from the vault.
type SocketServer struct {
	HostID string
	Vault  *Vault
	// Logf logs deliveries (names only, never values). Nil uses log.Printf.
	Logf func(format string, args ...any)
}

// Listen creates the socket at path, replacing a stale socket but never any
// other kind of file. The directory is created with mode 0700.
func Listen(path string) (*net.UnixListener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secret socket directory %s must not be group/world accessible (mode %04o)", filepath.Dir(path), info.Mode().Perm())
	}
	if existing, err := os.Lstat(path); err == nil {
		if existing.Mode().Type() != fs.ModeSocket {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

// Serve answers connections until ctx ends. Each connection carries one
// request. Only processes running as the agent's own user are answered; the
// hook runs as root of the agent user's rootless user namespace, which the
// kernel reports as the agent's uid.
func (s *SocketServer) Serve(ctx context.Context, listener *net.UnixListener) error {
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	slots := make(chan struct{}, maxConcurrentHooks)
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		go func() {
			defer func() { <-slots }()
			s.handle(conn)
		}()
	}
}

func (s *SocketServer) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (s *SocketServer) handle(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(socketTimeout))
	response := s.answer(conn)
	_ = json.NewEncoder(conn).Encode(response)
	for _, value := range response.Values {
		clear(value)
	}
}

func (s *SocketServer) answer(conn *net.UnixConn) hookResponse {
	uid, err := peerUID(conn)
	if err != nil {
		return hookResponse{Error: err.Error()}
	}
	if uid != os.Getuid() {
		s.logf("secret socket: refused peer uid %d", uid)
		return hookResponse{Error: "peer is not the agent user"}
	}
	var request hookRequest
	if err := json.NewDecoder(io.LimitReader(conn, maxHookRequest)).Decode(&request); err != nil {
		return hookResponse{Error: "invalid request"}
	}
	if request.HostID != s.HostID {
		return hookResponse{Error: fmt.Sprintf("container belongs to agent %q, not %q", request.HostID, s.HostID)}
	}
	if !protocol.ValidStackName(request.Stack) || len(request.Names) == 0 || len(request.Names) > protocol.MaxSecretsPerStack {
		return hookResponse{Error: "invalid request"}
	}
	for _, name := range request.Names {
		if !protocol.ValidSecretName(name) {
			return hookResponse{Error: "invalid request"}
		}
	}
	values, err := s.Vault.Copy(request.Stack, request.Names)
	if err != nil {
		s.logf("secret socket: container %.12s of stack %s: %v", request.ContainerID, request.Stack, err)
		return hookResponse{Error: err.Error()}
	}
	s.logf("secret socket: delivered %d secret(s) of stack %s to container %.12s", len(values), request.Stack, request.ContainerID)
	return hookResponse{Values: values}
}

func peerUID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return int(cred.Uid), nil
}

// request asks the agent at path for values. It is the hook's client side.
func request(ctx context.Context, path string, req hookRequest) (map[string][]byte, error) {
	dialer := net.Dialer{Timeout: socketTimeout}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("reach sorry-portainer agent at %s: %w", path, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(socketTimeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	var response hookResponse
	if err := json.NewDecoder(io.LimitReader(conn, maxHookResponse)).Decode(&response); err != nil {
		return nil, fmt.Errorf("read agent answer: %w", err)
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}
	for _, name := range req.Names {
		if _, ok := response.Values[name]; !ok {
			return nil, fmt.Errorf("agent did not return secret %s", name)
		}
	}
	return response.Values, nil
}
