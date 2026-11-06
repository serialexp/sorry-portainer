package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RemoteServerCommand is what the SSH transport runs on the master; it is on
// the PATH there (/usr/local/bin).
const RemoteServerCommand = "sorry-portainer-server"

// maxSSHStderr is how much of ssh's error output a failure quotes.
const maxSSHStderr = 4 << 10

// SSHTarget is the master an ssh:// URL names.
type SSHTarget struct {
	User string
	Host string
	// Port is 0 for ssh's own default (or its configuration's).
	Port int
}

// safeWord matches what may reach the remote shell unquoted: ssh joins the
// remote command into one string that the remote shell splits again.
var safeWord = regexp.MustCompile(`^[A-Za-z0-9/._-]+$`)

// ParseSSHURL reads ssh://[USER@]HOST[:PORT].
func ParseSSHURL(raw string) (SSHTarget, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return SSHTarget{}, fmt.Errorf("master %q: %w", raw, err)
	}
	if u.Scheme != "ssh" {
		return SSHTarget{}, fmt.Errorf("master %q: only ssh://[USER@]HOST[:PORT] is supported", raw)
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return SSHTarget{}, fmt.Errorf("master %q: only ssh://[USER@]HOST[:PORT], with no path or query", raw)
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		return SSHTarget{}, fmt.Errorf("master %q: a password does not belong in the URL; use an SSH key", raw)
	}
	t := SSHTarget{User: u.User.Username(), Host: u.Hostname()}
	// A leading "-" would make ssh read the host or user as an option.
	if t.Host == "" || strings.HasPrefix(t.Host, "-") || strings.HasPrefix(t.User, "-") {
		return SSHTarget{}, fmt.Errorf("master %q: needs a host", raw)
	}
	if port := u.Port(); port != "" {
		if t.Port, err = strconv.Atoi(port); err != nil || t.Port < 1 || t.Port > 65535 {
			return SSHTarget{}, fmt.Errorf("master %q: port %q is not valid", raw, port)
		}
	}
	return t, nil
}

// sshArgs builds the ssh command line after the program itself.
func sshArgs(t SSHTarget, stateDir string, leading []string) []string {
	args := append([]string{}, leading...)
	args = append(args, "-T", "-o", "BatchMode=yes")
	if t.Port != 0 {
		args = append(args, "-p", strconv.Itoa(t.Port))
	}
	dest := t.Host
	if t.User != "" {
		dest = t.User + "@" + t.Host
	}
	return append(args, dest, "--", RemoteServerCommand, "admin", "dial-stdio", "--state-dir", stateDir)
}

// NewSSHClient reaches the admin socket on the master through SSH: each call
// runs `ssh … -- sorry-portainer-server admin dial-stdio --state-dir DIR`
// there. sshCommand is the ssh program and any leading arguments; empty
// means "ssh". The SSH user must be root or the server's account.
func NewSSHClient(t SSHTarget, stateDir string, sshCommand []string) (*Client, error) {
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir || !safeWord.MatchString(stateDir) {
		return nil, fmt.Errorf("state directory %q: needs a clean absolute path of letters, digits and / . _ -", stateDir)
	}
	if len(sshCommand) == 0 {
		sshCommand = []string{"ssh"}
	}
	program, args := sshCommand[0], sshArgs(t, stateDir, sshCommand[1:])
	return &Client{dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
		return startSSH(ctx, program, args)
	}}, nil
}

// sshConn is one ssh process carrying one exchange on its stdin and stdout.
type sshConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	stderr *tailBuffer
	once   sync.Once
	err    error
}

func startSSH(ctx context.Context, program string, args []string) (*sshConn, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c := &sshConn{cmd: cmd, stdin: stdin, stdout: stdout, stderr: &tailBuffer{limit: maxSSHStderr}}
	cmd.Stderr = c.stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("run %s: %w", program, err)
	}
	return c, nil
}

func (c *sshConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

// Read ends with ssh's own failure when the process failed: an
// authentication or host key problem closes stdout before any answer.
func (c *sshConn) Read(p []byte) (int, error) {
	n, err := c.stdout.Read(p)
	if errors.Is(err, io.EOF) {
		if werr := c.wait(); werr != nil {
			return n, werr
		}
	}
	return n, err
}

// Close ends the exchange: with stdin closed, the remote end closes the
// socket and exits.
func (c *sshConn) Close() error {
	_ = c.stdin.Close()
	_ = c.wait()
	return nil
}

func (c *sshConn) wait() error {
	c.once.Do(func() {
		if err := c.cmd.Wait(); err != nil {
			if msg := strings.TrimSpace(c.stderr.String()); msg != "" {
				c.err = fmt.Errorf("ssh: %w: %s", err, msg)
			} else {
				c.err = fmt.Errorf("ssh: %w", err)
			}
		}
	})
	return c.err
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if over := len(b.data) - b.limit; over > 0 {
		b.data = append(b.data[:0], b.data[over:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

// DialStdio joins in and out to the admin socket in stateDir for one
// exchange: what `sorry-portainer-server admin dial-stdio` runs at the far
// end of the SSH transport. It trusts the socket as NewClient does. The
// client keeps its end open until it has the answer, so in ending means it
// went away; the connection then closes, which the server takes as
// cancellation.
func DialStdio(ctx context.Context, stateDir string, in io.Reader, out io.Writer) error {
	c, err := NewClient(stateDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, StackOperationTimeout+clientMargin)
	defer cancel()
	conn, err := c.dialSocket(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	go func() {
		_, _ = io.Copy(conn, in)
		_ = conn.Close()
	}()
	if _, err := io.Copy(out, conn); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}
