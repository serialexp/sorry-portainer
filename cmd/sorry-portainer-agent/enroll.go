package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/serialexp/sorry-portainer/internal/agentcred"
)

// enrollTimeout bounds the whole exchange with the master.
const enrollTimeout = time.Minute

// runEnroll exchanges a one-time token for this host's certificate and
// writes the agent configuration. The token is read from a file or stdin,
// never from the command line, so it does not show up in a process list.
func runEnroll(args []string, stdin io.Reader, stdout io.Writer) error {
	flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
	serverURL := flags.String("server-url", "", "master control URL, wss://HOST:PORT/control/agent")
	hostID := flags.String("host-id", "", "this host's ID, as the token was issued for")
	dir := flags.String("dir", "", "agent directory for configuration, credentials and state (created 0700)")
	tokenFile := flags.String("token-file", "", "file holding the enrollment token, or - for stdin")
	prefix := flags.String("prefix", "", "container name prefix (default HOST_ID-, or the existing one with --replace)")
	serverName := flags.String("server-name", "", "name the master's certificate is checked for (default: the URL's host)")
	composeProvider := flags.String("compose-provider", "", "absolute podman-compose path (default /usr/bin/podman-compose)")
	replace := flags.Bool("replace", false, "enroll again over an existing agent.json, keeping its other settings")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	for name, value := range map[string]string{"--server-url": *serverURL, "--host-id": *hostID, "--dir": *dir, "--token-file": *tokenFile} {
		if value == "" {
			return fmt.Errorf("enroll requires %s", name)
		}
	}
	token, err := readToken(*tokenFile, stdin)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, enrollTimeout)
	defer cancel()
	configPath, err := agentcred.Enroll(ctx, agentcred.EnrollOptions{ServerURL: *serverURL, HostID: *hostID, Token: token, Dir: *dir,
		Prefix: *prefix, ServerName: *serverName, ComposeProvider: *composeProvider, Replace: *replace})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "enrolled %s; configuration in %s\n", *hostID, configPath)
	return nil
}

// maxTokenFile is far above a token's length; anything longer is not one.
const maxTokenFile = 4 << 10

func readToken(path string, stdin io.Reader) (string, error) {
	var source io.Reader = stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		source = f
	}
	data, err := io.ReadAll(io.LimitReader(source, maxTokenFile+1))
	if err != nil {
		return "", fmt.Errorf("read the token: %w", err)
	}
	if len(data) > maxTokenFile {
		return "", errors.New("the token file is too large to hold a token")
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("no token given")
	}
	return token, nil
}
