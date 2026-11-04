package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/serialexp/sorry-portainer/internal/admin"
	"github.com/serialexp/sorry-portainer/internal/atomicfile"
	"github.com/serialexp/sorry-portainer/internal/config"
	"github.com/serialexp/sorry-portainer/internal/controlcert"
	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/pki"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/provision"
	"github.com/serialexp/sorry-portainer/internal/relay"
	"github.com/serialexp/sorry-portainer/internal/secretstore"
	"github.com/serialexp/sorry-portainer/internal/server"
	"github.com/serialexp/sorry-portainer/internal/webui"
)

// shutdownGrace is how long in-flight web and control requests get after
// SIGTERM.
const shutdownGrace = 10 * time.Second

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			runInit(os.Args[2:])
			return
		case "pki":
			runPKI(os.Args[2:])
			return
		case "hosts":
			runHosts(os.Args[2:])
			return
		}
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
	if err := serve(cfg, control); err != nil {
		log.Fatal(err)
	}
}

func serve(cfg config.Server, control config.ControlTLS) error {
	// One server per state directory: the registry and the admin socket are
	// not shared.
	lock, err := atomicfile.LockDir(cfg.StateDir, provision.LockFile)
	if err != nil {
		return err
	}
	defer lock.Release()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ca, err := pki.LoadCA(control.CAFile, control.CAKeyFile)
	if err != nil {
		return err
	}
	cert, err := controlcert.New(ca, control.CredentialFile, control.ServerNames, time.Now, log.Printf)
	if err != nil {
		return err
	}
	go cert.Run(ctx)
	registry, err := enrollment.OpenRegistry(filepath.Join(cfg.StateDir, provision.RegistryFile))
	if err != nil {
		return err
	}
	log.Printf("%d enrolled host(s)", len(registry.List()))
	store, err := secretstore.Open(filepath.Join(cfg.StateDir, "secrets"))
	if err != nil {
		return err
	}
	log.Printf("secret store is %s; unlock it in the web UI to deliver stack secrets", store.State())

	// api is assigned before any listener starts, so OnConnect never sees nil.
	var api *server.Server
	remote := relay.NewRemote(relay.RemoteOptions{OnConnect: func(info protocol.HostInfo) { api.HostConnected(info) }})
	api = server.New(cfg.AdminPassword, cfg.SessionTTL, remote, store)
	svc := enrollment.NewService(ca, registry, time.Now)
	svc.SetDisconnect(func(host string) { remote.Disconnect(host, errors.New("host removed or re-enrolled")) })

	adminListener, err := admin.Listen(cfg.StateDir)
	if err != nil {
		return err
	}
	adminServer := admin.NewServer(svc, remote.Hosts, log.Printf)
	go func() {
		if err := adminServer.Serve(ctx, adminListener); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("admin socket stopped: %v", err)
		}
	}()

	dashboard, err := webui.Handler()
	if err != nil {
		return err
	}
	web := server.NewWebServer(cfg.ListenAddr, api.Handler(dashboard))
	controlListener, err := tls.Listen("tcp", control.ListenAddr, cert.TLSConfig())
	if err != nil {
		return err
	}
	controlServer := server.NewControlServer(&server.Control{Remote: remote, Enrollment: svc, Logf: log.Printf})

	failed := make(chan error, 2)
	go func() {
		log.Printf("sorry-portainer dashboard and web API listening on %s", cfg.ListenAddr)
		failed <- web.ListenAndServe()
	}()
	go func() {
		log.Printf("sorry-portainer control API listening on %s for %v", control.ListenAddr, control.ServerNames)
		failed <- controlServer.Serve(controlListener)
	}()

	var result error
	select {
	case <-ctx.Done():
		log.Print("shutting down")
	case result = <-failed:
		stop()
	}
	// Shutdown does not wait for hijacked connections, so agent sessions are
	// closed with the process; agents reconnect to the next server.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	for _, s := range []*http.Server{web, controlServer} {
		if err := s.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}
	return result
}
