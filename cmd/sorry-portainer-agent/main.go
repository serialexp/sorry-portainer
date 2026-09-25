package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/config"
	"github.com/serialexp/sorry-portainer/internal/podman"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/relay"
	"github.com/serialexp/sorry-portainer/internal/secrets"
	"github.com/serialexp/sorry-portainer/internal/stacks"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		if err := runSetup(context.Background(), os.Args[2:], os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "setup-hooks-conf" {
		if err := runSetupHooksConf(os.Args[2:], os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "oci-hook" {
		os.Exit(runOCIHook(os.Args[2:]))
	}
	configPath := flag.String("config", "", "JSON agent configuration file")
	flag.Parse()
	var cfg config.Agent
	var err error
	if *configPath != "" {
		cfg, err = config.LoadAgentFile(*configPath)
	} else {
		cfg, err = config.AgentFromEnv()
	}
	if err != nil {
		log.Fatal(err)
	}
	client, err := podman.NewWithPrefix(cfg.Prefix)
	if err != nil {
		log.Fatal(err)
	}
	tlsConfig, err := config.LoadClientTLS(cfg)
	if err != nil {
		log.Fatal(err)
	}
	// SIGTERM from systemd cancels in-flight operations and waits for them.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	vault, err := startSecretSupport(ctx, cfg)
	if err != nil {
		log.Fatalf("stack secrets: %v", err)
	}
	manager := stacks.New(cfg.StateDir+"/stacks", cfg.Prefix, stacks.NewComposeExecutor(cfg.ComposeProvider))
	manager.EnableSecrets(stacks.SecretSupport{HostID: cfg.HostID, Vault: vault, Containers: client})
	agent := &relay.Agent{HostID: cfg.HostID, Prefix: cfg.Prefix, Handler: client, Stacks: manager, Describe: describeHost}
	err = relay.RunAgent(ctx, agent, relay.ReconnectOptions{
		Dial: func(ctx context.Context) (*websocket.Conn, error) {
			return relay.DialAgent(ctx, cfg.ServerURL, tlsConfig)
		},
		OnDisconnect: func(err error, wait time.Duration) {
			log.Printf("relay connection ended: %v; reconnecting in %s", err, wait.Round(time.Millisecond))
		},
	})
	if !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

// startSecretSupport opens the hook socket and writes the hook JSON. The
// vault lives for the whole process, across relay reconnects.
func startSecretSupport(ctx context.Context, cfg config.Agent) (*secrets.Vault, error) {
	socket := cfg.SecretSocket
	if socket == "" {
		var err error
		if socket, err = secrets.DefaultSocketPath(cfg.HostID); err != nil {
			return nil, err
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if executable, err = filepath.EvalSymlinks(executable); err != nil {
		return nil, err
	}
	listener, err := secrets.Listen(socket)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", socket, err)
	}
	hookFile, err := secrets.WriteHookConfig(cfg.OCIHooksDir, executable, cfg.HostID, socket)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("write OCI hook: %w", err)
	}
	vault := secrets.NewVault()
	server := &secrets.SocketServer{HostID: cfg.HostID, Vault: vault}
	go func() {
		if err := server.Serve(ctx, listener); err != nil && !errors.Is(err, context.Canceled) {
			log.Fatalf("secret socket failed: %v", err)
		}
	}()
	log.Printf("stack secrets: socket %s, hook %s (its directory must be in containers.conf hooks_dir)", socket, hookFile)
	return vault, nil
}

func describeHost(info *protocol.HostInfo) {
	// startSecretSupport is fatal on failure, so a connected agent is ready.
	info.SecretsReady = true
	swap, err := secrets.SwapActive()
	if err != nil {
		info.SecretsProblem = "cannot tell whether swap is active: " + err.Error()
		swap = true
	}
	info.SwapActive = swap
}

// runOCIHook is the OCI createRuntime hook. A non-zero exit makes the runtime
// refuse to start the container.
func runOCIHook(args []string) int {
	flags := flag.NewFlagSet("oci-hook", flag.ContinueOnError)
	hostID := flags.String("host-id", "", "agent host ID")
	socket := flags.String("socket", "", "agent secret socket")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *hostID == "" || *socket == "" {
		fmt.Fprintln(os.Stderr, "sorry-portainer oci-hook: --host-id and --socket are required")
		return 2
	}
	// Stay inside the hook's timeout so our error is what gets reported.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secrets.HookTimeoutSeconds-3)*time.Second)
	defer cancel()
	if err := secrets.RunHook(ctx, os.Stdin, secrets.HookOptions{HostID: *hostID, Socket: *socket}); err != nil {
		fmt.Fprintf(os.Stderr, "sorry-portainer oci-hook: %v\n", err)
		return 1
	}
	return 0
}
