package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/serialexp/sorry-portainer/internal/config"
	"github.com/serialexp/sorry-portainer/internal/podman"
	"github.com/serialexp/sorry-portainer/internal/relay"
	"github.com/serialexp/sorry-portainer/internal/stacks"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		if err := runSetup(context.Background(), os.Args[2:], os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
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
	agent := &relay.Agent{HostID: cfg.HostID, Prefix: cfg.Prefix, Handler: client, Stacks: stacks.New(cfg.StateDir+"/stacks", cfg.Prefix, stacks.NewComposeExecutor(cfg.ComposeProvider))}
	// SIGTERM from systemd cancels in-flight operations and waits for them.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
