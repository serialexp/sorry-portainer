package main

import (
	"flag"
	"fmt"
	"github.com/serialexp/sorry-portainer/internal/provision"
	"log"
	"os"
)

func runInit(args []string) {
	f := flag.NewFlagSet("init", flag.ExitOnError)
	state := f.String("state-dir", "/var/lib/sorry-portainer", "state directory")
	password := f.String("admin-password", "", "initial web admin password")
	serverName := f.String("server-name", "control.local", "control certificate name")
	control := f.String("control-listen", ":9443", "mTLS control address")
	web := f.String("web-listen", ":8080", "plain HTTP web address")
	_ = f.Parse(args)
	if err := provision.Init(provision.InitOptions{StateDir: *state, AdminPassword: *password, ServerName: *serverName, ControlListen: *control, WebListen: *web}); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stdout, "initialized server state in %s\n", *state)
}
func runAgent(args []string) {
	f := flag.NewFlagSet("agent", flag.ExitOnError)
	create := f.String("create", "", "create a host agent (use: agent --create HOST_ID)")
	state := f.String("state-dir", "/var/lib/sorry-portainer", "server state directory")
	prefix := f.String("prefix", "", "container prefix")
	serverURL := f.String("server-url", "", "wss control URL")
	serverName := f.String("server-name", "control.local", "control server name")
	composeProvider := f.String("compose-provider", "/usr/bin/podman-compose", "absolute podman-compose provider path")
	_ = f.Parse(args)
	if *create == "" {
		log.Fatal("agent requires --create HOST_ID")
	}
	if err := provision.CreateAgent(provision.AgentOptions{StateDir: *state, HostID: *create, Prefix: *prefix, ServerURL: *serverURL, ServerName: *serverName, ComposeProvider: *composeProvider}); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stdout, "created agent bundle for %s\n", *create)
}
