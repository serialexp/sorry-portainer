package main

import (
	"crypto/tls"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/serialexp/sorry-portainer/internal/config"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/relay"
	"github.com/serialexp/sorry-portainer/internal/secretstore"
	"github.com/serialexp/sorry-portainer/internal/server"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "init" {
		runInit(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "agent" {
		runAgent(os.Args[2:])
		return
	}
	configPath := flag.String("config", "", "JSON server configuration file")
	flag.Parse()
	var cfg config.Server
	var control config.ControlTLS
	var err error
	if *configPath != "" {
		cfg, control, err = config.LoadServerFile(*configPath)
	} else {
		cfg, err = config.ServerFromEnv()
		if err == nil {
			control, err = config.ControlTLSFromEnv()
		}
	}
	if err != nil {
		log.Fatal(err)
	}
	tlsConfig, err := config.LoadServerTLS(control)
	if err != nil {
		log.Fatal(err)
	}
	store, err := secretstore.Open(filepath.Join(cfg.StateDir, "secrets"))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("secret store is %s; unlock it in the web UI to deliver stack secrets", store.State())
	// api is assigned before any listener starts, so OnConnect never sees nil.
	var api *server.Server
	registry := relay.NewRemote(relay.RemoteOptions{OnConnect: func(info protocol.HostInfo) { api.HostConnected(info) }})
	api = server.New(cfg.AdminPassword, cfg.SessionTTL, registry, store)
	go func() {
		log.Printf("sorry-portainer web API listening on %s", cfg.ListenAddr)
		if err := http.ListenAndServe(cfg.ListenAddr, api.Handler()); err != nil {
			log.Print(err)
		}
	}()
	listener, err := tls.Listen("tcp", control.ListenAddr, tlsConfig)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("sorry-portainer control API listening on %s", control.ListenAddr)
	if err := http.Serve(listener, server.ControlHandler(registry)); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
