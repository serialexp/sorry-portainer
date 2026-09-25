package main

import (
	"crypto/tls"
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/serialexp/sorry-portainer/internal/config"
	"github.com/serialexp/sorry-portainer/internal/relay"
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
	registry := relay.NewRemote()
	api := server.New(cfg.AdminPassword, cfg.SessionTTL, registry)
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
