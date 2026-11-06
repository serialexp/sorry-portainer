// Command sorry-portainer manages a sorry-portainer master through its admin
// socket: on the master itself, or from anywhere over SSH
// (--master ssh://root@HOST).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/serialexp/sorry-portainer/internal/admin"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

const defaultStateDir = "/var/lib/sorry-portainer"

const usage = `usage: sorry-portainer [--master ssh://[USER@]HOST[:PORT]] [--state-dir DIR] [--ssh-command CMD] [--json] COMMAND

  hosts list
  hosts token  --host HOST [--ttl 1h]
  hosts remove --host HOST
  stacks list    --host HOST
  stacks show    --host HOST NAME
  stacks create  --host HOST --file compose.yaml [--env-file FILE] NAME
  stacks update  --host HOST --file compose.yaml [--env-file FILE] [--expect-version N] NAME
  stacks up|down|restart --host HOST NAME

--master falls back to $SORRY_PORTAINER_MASTER; without either, the CLI uses
the local admin socket in --state-dir. --ssh-command (or $SORRY_PORTAINER_SSH)
replaces "ssh". --env-file takes KEY=VALUE lines or a JSON object; on update
it replaces the stack's environment, and without it the environment is kept.
A save that changes the environment of a running stack brings it up again.
`

// Exit codes.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

// client is what the commands need of admin.Client.
type client interface {
	List(ctx context.Context) (admin.ListResponse, error)
	Token(ctx context.Context, hostID string, ttl time.Duration) (admin.TokenResponse, error)
	Remove(ctx context.Context, hostID string) error
	Stacks(ctx context.Context, hostID string) ([]protocol.Stack, error)
	Stack(ctx context.Context, hostID, name string) (protocol.Stack, error)
	SaveStack(ctx context.Context, hostID string, save protocol.StackSave) (protocol.StackSaved, error)
	StackOperation(ctx context.Context, hostID, name, operation string) (protocol.StackOperation, error)
}

// connection is where the commands reach the master.
type connection struct {
	master     string
	stateDir   string
	sshCommand []string
}

func newClient(c connection) (client, error) {
	if c.master == "" {
		return admin.NewClient(c.stateDir)
	}
	target, err := admin.ParseSSHURL(c.master)
	if err != nil {
		return nil, err
	}
	return admin.NewSSHClient(target, c.stateDir, c.sshCommand)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr, newClient))
}

// cli carries one invocation's settings and output.
type cli struct {
	ctx    context.Context
	out    io.Writer
	errOut io.Writer
	json   bool
	client client
}

func run(ctx context.Context, args []string, getenv func(string) string, out, errOut io.Writer, connect func(connection) (client, error)) int {
	global := flag.NewFlagSet("sorry-portainer", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	master := global.String("master", getenv("SORRY_PORTAINER_MASTER"), "")
	stateDir := global.String("state-dir", defaultStateDir, "")
	sshCommand := global.String("ssh-command", getenv("SORRY_PORTAINER_SSH"), "")
	asJSON := global.Bool("json", false, "")
	if err := global.Parse(args); err != nil {
		fmt.Fprintf(errOut, "%v\n\n%s", err, usage)
		return exitUsage
	}
	rest := global.Args()
	if len(rest) < 2 {
		fmt.Fprint(errOut, usage)
		return exitUsage
	}
	c, err := connect(connection{master: *master, stateDir: *stateDir, sshCommand: strings.Fields(*sshCommand)})
	if err != nil {
		fmt.Fprintln(errOut, "sorry-portainer:", err)
		return exitFailed
	}
	x := &cli{ctx: ctx, out: out, errOut: errOut, json: *asJSON, client: c}
	switch rest[0] {
	case "hosts":
		return x.hosts(rest[1], rest[2:])
	case "stacks":
		return x.stacks(rest[1], rest[2:])
	}
	fmt.Fprint(errOut, usage)
	return exitUsage
}

// command parses a subcommand's flags, which may come before or after its
// positional arguments, and returns those arguments.
type command struct {
	*flag.FlagSet
	name string
}

func newCommand(name string) command {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	return command{FlagSet: f, name: name}
}

func (c command) parse(args []string) ([]string, error) {
	var positional []string
	for {
		if err := c.Parse(args); err != nil {
			return nil, fmt.Errorf("%s: %w", c.name, err)
		}
		if c.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, c.Arg(0))
		args = c.Args()[1:]
	}
}

func (x *cli) usageError(format string, args ...any) int {
	fmt.Fprintf(x.errOut, "sorry-portainer: "+format+"\n\n%s", append(args, usage)...)
	return exitUsage
}

func (x *cli) failed(err error) int {
	fmt.Fprintln(x.errOut, "sorry-portainer:", err)
	return exitFailed
}

func (x *cli) printJSON(v any) int {
	encoder := json.NewEncoder(x.out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		return x.failed(err)
	}
	return exitOK
}

func (x *cli) hosts(sub string, args []string) int {
	cmd := newCommand("hosts " + sub)
	host := cmd.String("host", "", "")
	ttl := cmd.Duration("ttl", 0, "")
	positional, err := cmd.parse(args)
	if err != nil {
		return x.usageError("%v", err)
	}
	if len(positional) != 0 {
		return x.usageError("hosts %s takes no arguments; use --host", sub)
	}
	if *ttl != 0 && sub != "token" {
		return x.usageError("hosts %s takes no --ttl", sub)
	}
	switch sub {
	case "list":
		if *host != "" {
			return x.usageError("hosts list takes no --host")
		}
		list, err := x.client.List(x.ctx)
		if err != nil {
			return x.failed(err)
		}
		if x.json {
			return x.printJSON(list)
		}
		printHosts(x.out, list)
		return exitOK
	case "token":
		if *host == "" {
			return x.usageError("hosts token needs --host")
		}
		issued, err := x.client.Token(x.ctx, *host, *ttl)
		if err != nil {
			return x.failed(err)
		}
		if x.json {
			return x.printJSON(issued)
		}
		// Only the token goes to stdout, so a script can capture it.
		fmt.Fprintln(x.out, issued.Token)
		fmt.Fprintf(x.errOut, "token for %s expires %s\n", *host, issued.ExpiresAt.Local().Format(time.RFC3339))
		return exitOK
	case "remove":
		if *host == "" {
			return x.usageError("hosts remove needs --host")
		}
		if err := x.client.Remove(x.ctx, *host); err != nil {
			return x.failed(err)
		}
		fmt.Fprintf(x.out, "removed %s\n", *host)
		return exitOK
	}
	return x.usageError("unknown command hosts %s", sub)
}

func (x *cli) stacks(sub string, args []string) int {
	cmd := newCommand("stacks " + sub)
	host := cmd.String("host", "", "")
	file := cmd.String("file", "", "")
	envFile := cmd.String("env-file", "", "")
	expect := cmd.Int("expect-version", 0, "")
	positional, err := cmd.parse(args)
	if err != nil {
		return x.usageError("%v", err)
	}
	if *host == "" {
		return x.usageError("stacks %s needs --host", sub)
	}
	if sub == "list" {
		if len(positional) != 0 {
			return x.usageError("stacks list takes no arguments")
		}
		stacks, err := x.client.Stacks(x.ctx, *host)
		if err != nil {
			return x.failed(err)
		}
		if x.json {
			if stacks == nil {
				stacks = []protocol.Stack{}
			}
			return x.printJSON(stacks)
		}
		printStacks(x.out, stacks)
		return exitOK
	}
	if len(positional) != 1 {
		return x.usageError("stacks %s takes one stack name", sub)
	}
	name := positional[0]
	if (*file != "" || *envFile != "" || *expect != 0) && sub != "create" && sub != "update" {
		return x.usageError("stacks %s takes no --file, --env-file or --expect-version", sub)
	}
	switch sub {
	case "show":
		stack, err := x.client.Stack(x.ctx, *host, name)
		if err != nil {
			return x.failed(err)
		}
		if x.json {
			return x.printJSON(stack)
		}
		printStack(x.out, stack)
		return exitOK
	case "create", "update":
		return x.save(sub, *host, name, *file, *envFile, *expect)
	case "up", "down", "restart":
		result, err := x.client.StackOperation(x.ctx, *host, name, sub)
		if err != nil {
			return x.failed(err)
		}
		if x.json {
			if code := x.printJSON(result); code != exitOK {
				return code
			}
		} else if result.Output != "" {
			fmt.Fprint(x.out, strings.TrimSuffix(result.Output, "\n")+"\n")
		}
		if !result.Success {
			fmt.Fprintf(x.errOut, "sorry-portainer: stacks %s %s failed\n", sub, name)
			return exitFailed
		}
		return exitOK
	}
	return x.usageError("unknown command stacks %s", sub)
}

func (x *cli) save(sub, host, name, file, envFile string, expect int) int {
	switch {
	case file == "":
		return x.usageError("stacks %s needs --file", sub)
	case sub == "create" && expect != 0:
		return x.usageError("stacks create takes no --expect-version")
	}
	compose, err := os.ReadFile(file)
	if err != nil {
		return x.failed(err)
	}
	save := protocol.StackSave{Name: name, ComposeYAML: string(compose)}
	if envFile != "" {
		data, err := os.ReadFile(envFile)
		if err != nil {
			return x.failed(err)
		}
		if save.Environment, err = parseEnvFile(data); err != nil {
			return x.failed(fmt.Errorf("%s: %w", envFile, err))
		}
	}
	if sub == "update" {
		if expect == 0 {
			current, err := x.client.Stack(x.ctx, host, name)
			if err != nil {
				return x.failed(err)
			}
			expect = current.Version
		}
		save.ExpectedVersion = &expect
	}
	saved, err := x.client.SaveStack(x.ctx, host, save)
	if err != nil {
		return x.failed(err)
	}
	if x.json {
		if code := x.printJSON(saved); code != exitOK {
			return code
		}
	} else {
		fmt.Fprintf(x.out, "saved %s on %s as version %d\n", saved.Name, host, saved.Version)
	}
	if op := saved.Operation; op != nil {
		if !x.json && op.Output != "" {
			fmt.Fprint(x.out, strings.TrimSuffix(op.Output, "\n")+"\n")
		}
		if !op.Success {
			fmt.Fprintf(x.errOut, "sorry-portainer: saved, but bringing %s up with the new environment failed\n", name)
			return exitFailed
		}
	}
	return exitOK
}

func printHosts(w io.Writer, list admin.ListResponse) {
	t := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(t, "HOST\tPREFIX\tONLINE\tCERTIFICATE EXPIRES\tENROLLED")
	for _, h := range list.Hosts {
		expires := h.CurrentNotAfter.Local().Format(time.DateOnly)
		if h.Pending {
			expires += " (renewal pending)"
		}
		fmt.Fprintf(t, "%s\t%s\t%t\t%s\t%s\n", h.HostID, h.Prefix, h.Online, expires, h.EnrolledAt.Local().Format(time.DateOnly))
	}
	_ = t.Flush()
	if len(list.Tokens) == 0 {
		return
	}
	fmt.Fprintln(w)
	t = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(t, "TOKEN FOR\tEXPIRES")
	for _, token := range list.Tokens {
		fmt.Fprintf(t, "%s\t%s\n", token.HostID, token.ExpiresAt.Local().Format(time.RFC3339))
	}
	_ = t.Flush()
}

func printStacks(w io.Writer, stacks []protocol.Stack) {
	t := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(t, "STACK\tPROJECT\tSTATUS\tVERSION\tAFTER REBOOT")
	for _, s := range stacks {
		after := s.Desired
		if s.Waiting != "" {
			after += " (waiting: " + s.Waiting + ")"
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%d\t%s\n", s.Name, s.Project, s.Status, s.Version, after)
	}
	_ = t.Flush()
}

func printStack(w io.Writer, s protocol.Stack) {
	fmt.Fprintf(w, "stack %s (project %s), version %d, %s\n", s.Name, s.Project, s.Version, s.Status)
	if len(s.Secrets) > 0 {
		fmt.Fprintf(w, "secrets: %s\n", strings.Join(s.Secrets, ", "))
	}
	if s.Waiting != "" {
		fmt.Fprintf(w, "waiting: %s\n", s.Waiting)
	}
	fmt.Fprintf(w, "\n%s", s.ComposeYAML)
	if !strings.HasSuffix(s.ComposeYAML, "\n") {
		fmt.Fprintln(w)
	}
	if len(s.Environment) == 0 {
		return
	}
	fmt.Fprintln(w, "\nenvironment:")
	keys := make([]string, 0, len(s.Environment))
	for k := range s.Environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%s=%s\n", k, s.Environment[k])
	}
}
