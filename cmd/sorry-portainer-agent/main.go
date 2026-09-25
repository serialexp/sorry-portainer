package main

import (
	"context"
	"flag"
	"log"
	"os"

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
	conn, err := relay.DialAgent(cfg.ServerURL, tlsConfig)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	agent := &relay.Agent{Conn: conn, HostID: cfg.HostID, Prefix: cfg.Prefix, Handler: client, Stacks: stacks.New(cfg.StateDir+"/stacks", cfg.Prefix, stacks.NewComposeExecutor(cfg.ComposeProvider))}
	if err := agent.Serve(context.Background()); err != nil {
		log.Fatal(err)
	}
}
