package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/serialexp/sorry-portainer/internal/admin"
)

// runAdmin is the far end of the CLI's SSH transport:
// `sorry-portainer-server admin dial-stdio` joins its stdin and stdout to the
// admin socket for one exchange.
func runAdmin(args []string) {
	usage := "usage: sorry-portainer-server admin dial-stdio [--state-dir DIR]"
	if len(args) == 0 || args[0] != "dial-stdio" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	f := flag.NewFlagSet("admin dial-stdio", flag.ExitOnError)
	state := f.String("state-dir", defaultStateDir, "server state directory")
	_ = f.Parse(args[1:])
	if f.NArg() != 0 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	// SSH closing the session sends SIGHUP; stdin ending says the same.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := admin.DialStdio(ctx, *state, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sorry-portainer-server admin dial-stdio:", err)
		os.Exit(1)
	}
}
