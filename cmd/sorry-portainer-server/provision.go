package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/serialexp/sorry-portainer/internal/admin"
	"github.com/serialexp/sorry-portainer/internal/provision"
)

const defaultStateDir = "/var/lib/sorry-portainer"

// names collects --server-name, given more than once or comma-separated.
type names []string

func (n *names) String() string { return strings.Join(*n, ",") }

func (n *names) Set(v string) error {
	for _, name := range strings.Split(v, ",") {
		if name = strings.TrimSpace(name); name != "" {
			*n = append(*n, name)
		}
	}
	return nil
}

func runInit(args []string) {
	f := flag.NewFlagSet("init", flag.ExitOnError)
	state := f.String("state-dir", defaultStateDir, "state directory")
	password := f.String("admin-password", "", "initial web admin password")
	var serverNames names
	f.Var(&serverNames, "server-name", "DNS name or IP address agents reach the control listener by (repeat or comma-separate)")
	control := f.String("control-listen", ":9443", "mTLS control address")
	web := f.String("web-listen", ":8080", "plain HTTP web address")
	_ = f.Parse(args)
	if err := provision.Init(provision.InitOptions{StateDir: *state, AdminPassword: *password, ServerNames: serverNames, ControlListen: *control, WebListen: *web}); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stdout, "initialized server state in %s\n", *state)
}

func runPKI(args []string) {
	if len(args) == 0 || args[0] != "reset" {
		log.Fatal("usage: sorry-portainer-server pki reset --state-dir DIR [--server-name NAME]")
	}
	f := flag.NewFlagSet("pki reset", flag.ExitOnError)
	state := f.String("state-dir", defaultStateDir, "server state directory (the server must be stopped)")
	var serverNames names
	f.Var(&serverNames, "server-name", "replace the control server names (repeat or comma-separate)")
	_ = f.Parse(args[1:])
	retired, err := provision.ResetPKI(provision.ResetOptions{StateDir: *state, ServerNames: serverNames})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stdout, "new control CA and empty host registry in %s; old files in %s\nevery agent must enroll again\n", *state, retired)
}

func runHosts(args []string) {
	usage := "usage: sorry-portainer-server hosts token|list|remove --state-dir DIR [--host HOST_ID] [--ttl 1h] [--json]"
	if len(args) == 0 {
		log.Fatal(usage)
	}
	f := flag.NewFlagSet("hosts "+args[0], flag.ExitOnError)
	state := f.String("state-dir", defaultStateDir, "server state directory")
	host := f.String("host", "", "host ID")
	ttl := f.Duration("ttl", 0, "token lifetime (default 1h, at most 24h)")
	asJSON := f.Bool("json", false, "list as JSON")
	_ = f.Parse(args[1:])
	client, err := admin.NewClient(*state)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	switch args[0] {
	case "token":
		if *host == "" {
			log.Fatal("hosts token requires --host")
		}
		issued, err := client.Token(ctx, *host, *ttl)
		if err != nil {
			log.Fatal(err)
		}
		// Only the token goes to stdout, so a script can capture it.
		fmt.Fprintln(os.Stdout, issued.Token)
		fmt.Fprintf(os.Stderr, "token for %s expires %s\n", *host, issued.ExpiresAt.Local().Format(time.RFC3339))
	case "list":
		list, err := client.List(ctx)
		if err != nil {
			log.Fatal(err)
		}
		if *asJSON {
			if err := json.NewEncoder(os.Stdout).Encode(list); err != nil {
				log.Fatal(err)
			}
			return
		}
		printList(list)
	case "remove":
		if *host == "" {
			log.Fatal("hosts remove requires --host")
		}
		if err := client.Remove(ctx, *host); err != nil {
			log.Fatal(err)
		}
		fmt.Fprintf(os.Stdout, "removed %s\n", *host)
	default:
		log.Fatal(usage)
	}
}

func printList(list admin.ListResponse) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "HOST\tPREFIX\tONLINE\tCERTIFICATE EXPIRES\tENROLLED")
	for _, h := range list.Hosts {
		expires := h.CurrentNotAfter.Local().Format(time.DateOnly)
		if h.Pending {
			expires += " (renewal pending)"
		}
		fmt.Fprintf(w, "%s\t%s\t%t\t%s\t%s\n", h.HostID, h.Prefix, h.Online, expires, h.EnrolledAt.Local().Format(time.DateOnly))
	}
	_ = w.Flush()
	if len(list.Tokens) == 0 {
		return
	}
	fmt.Fprintln(os.Stdout)
	w = tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TOKEN FOR\tEXPIRES")
	for _, t := range list.Tokens {
		fmt.Fprintf(w, "%s\t%s\n", t.HostID, t.ExpiresAt.Local().Format(time.RFC3339))
	}
	_ = w.Flush()
}
