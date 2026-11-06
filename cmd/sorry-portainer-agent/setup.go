package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/serialexp/sorry-portainer/internal/agentsetup"
)

const (
	systemAgentBinary = agentsetup.SystemAgentBinary
	systemAgentUnit   = agentsetup.SystemAgentUnit
	systemAgentDir    = agentsetup.SystemAgentDir
)

type setupEnvironment struct {
	readOSRelease func() ([]byte, error)
	executable    func() (string, error)
	euid          func() int
	runSudo       func(context.Context, string, []string, io.Writer) error
	probe         agentsetup.Probe
	installFiles  func(string, string) error
	// configureContainers writes the agent user's containers.conf drop-in,
	// running the installed agent as that user.
	configureContainers func(ctx context.Context, user string, conf agentsetup.ContainersConf) error
	// containersConfCurrent reports, for a dry run as root, whether the
	// user's drop-in already matches; it runs executable as that user.
	containersConfCurrent func(ctx context.Context, executable, user string, conf agentsetup.ContainersConf) (bool, error)
	// lookupHome is the user's home directory, from the account database.
	lookupHome func(user string) (string, error)
}

func lookupHome(user string) (string, error) {
	account, err := osuser.Lookup(user)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(account.HomeDir) {
		return "", fmt.Errorf("%s's home directory %q is not absolute", user, account.HomeDir)
	}
	return account.HomeDir, nil
}

func defaultSetupEnvironment() setupEnvironment {
	return setupEnvironment{
		readOSRelease: func() ([]byte, error) { return os.ReadFile("/etc/os-release") },
		executable:    os.Executable,
		euid:          os.Geteuid,
		probe:         agentsetup.ExecProbe{},
		runSudo: func(ctx context.Context, executable string, args []string, stdout io.Writer) error {
			command := exec.CommandContext(ctx, "sudo", append([]string{executable, "setup", "--elevated"}, args...)...)
			command.Stdin = os.Stdin
			command.Stdout = stdout
			command.Stderr = os.Stderr
			return command.Run()
		},
		installFiles:          installAgentService,
		configureContainers:   configureContainersAsUser,
		containersConfCurrent: containersConfCurrentAsUser,
		lookupHome:            lookupHome,
	}
}

// containersConfArgs are the setup-containers-conf flags for conf.
func containersConfArgs(conf agentsetup.ContainersConf) []string {
	args := []string{"--oci-hooks-dir", conf.HooksDir}
	if conf.Forwarder != "" {
		args = append(args, "--port-forwarder", conf.Forwarder)
	}
	if conf.HostLoopback != "" {
		args = append(args, "--map-host-loopback", conf.HostLoopback)
	}
	return args
}

// asUser runs executable's setup-containers-conf as user. Running as the
// user means files the user controls cannot redirect a root write
// elsewhere. runuser sets no runtime directory or session bus, and the
// subcommand asks the user's Podman whether containers run, so both are
// passed explicitly.
func asUser(ctx context.Context, executable, user string, args []string) ([]byte, error) {
	account, err := osuser.Lookup(user)
	if err != nil {
		return nil, fmt.Errorf("look up agent user: %w", err)
	}
	command := exec.CommandContext(ctx, "runuser", append([]string{"-u", user, "--", executable, "setup-containers-conf"}, args...)...)
	command.Dir = "/"
	runtime := "/run/user/" + account.Uid
	command.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "XDG_RUNTIME_DIR=" + runtime, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtime + "/bus"}
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

// configureContainersAsUser has the installed agent write the user's
// drop-in.
func configureContainersAsUser(ctx context.Context, user string, conf agentsetup.ContainersConf) error {
	if _, err := asUser(ctx, systemAgentBinary, user, containersConfArgs(conf)); err != nil {
		return fmt.Errorf("configure Podman for %s: %w", user, err)
	}
	return nil
}

func containersConfCurrentAsUser(ctx context.Context, executable, user string, conf agentsetup.ContainersConf) (bool, error) {
	output, err := asUser(ctx, executable, user, append(containersConfArgs(conf), "--check"))
	if err != nil {
		return false, fmt.Errorf("check %s's Podman configuration: %w", user, err)
	}
	switch strings.TrimSpace(string(output)) {
	case "current":
		return true, nil
	case "stale":
		return false, nil
	}
	return false, fmt.Errorf("check %s's Podman configuration: unexpected output %q", user, output)
}

// podmanRunning reports whether any of the current user's containers run.
func podmanRunning() (bool, error) {
	output, err := exec.Command("podman", "ps", "--quiet").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, fmt.Errorf("podman ps: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return false, fmt.Errorf("podman ps: %w", err)
	}
	return strings.TrimSpace(string(output)) != "", nil
}

// runSetupContainersConf is the unprivileged half of setup: it writes the
// drop-in into the current user's home, or with --check reports whether
// it is "current" or "stale". running is asked before the port forwarder
// changes.
func runSetupContainersConf(args []string, stdout io.Writer, running func() (bool, error)) error {
	current, err := osuser.Current()
	if err != nil {
		return err
	}
	return setupContainersConfIn(current.HomeDir, args, stdout, running)
}

func setupContainersConfIn(home string, args []string, stdout io.Writer, running func() (bool, error)) error {
	flags := flag.NewFlagSet("setup-containers-conf", flag.ContinueOnError)
	hooksDir := flags.String("oci-hooks-dir", "", "directory the agent writes its OCI hook JSON to")
	forwarder := flags.String("port-forwarder", agentsetup.ForwarderRootlessport, "rootless port forwarder: rootlessport or pasta (Podman 6)")
	hostLoopback := flags.String("map-host-loopback", "", "address at which containers reach the host's 127.0.0.1")
	printOnly := flags.Bool("print", false, "print the drop-in instead of installing it (for CONTAINERS_CONF_OVERRIDE in development)")
	check := flags.Bool("check", false, `print "current" or "stale" instead of installing the drop-in`)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *printOnly && *check {
		return errors.New("--print and --check exclude each other")
	}
	if *hooksDir == "" {
		*hooksDir = agentsetup.DefaultOCIHooksDir(home)
	}
	conf := agentsetup.ContainersConf{HooksDir: *hooksDir, Forwarder: *forwarder, HostLoopback: *hostLoopback}
	if *printOnly {
		content, err := conf.Render()
		if err != nil {
			return err
		}
		_, err = io.WriteString(stdout, content)
		return err
	}
	if *check {
		matches, err := agentsetup.ContainersConfCurrent(home, conf)
		if err != nil {
			return err
		}
		if matches {
			fmt.Fprintln(stdout, "current")
		} else {
			fmt.Fprintln(stdout, "stale")
		}
		return nil
	}
	path, err := agentsetup.WriteContainersConf(home, conf, running)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s\n", path)
	return nil
}

// containersConfFor is the drop-in for a Podman version: pasta forwards
// ports on Podman 6, which keeps clients' addresses, and a host loopback
// address needs Podman 5's pasta networking.
func containersConfFor(version agentsetup.Version, hooksDir, hostLoopback string) (agentsetup.ContainersConf, error) {
	conf := agentsetup.ContainersConf{HooksDir: hooksDir, Forwarder: agentsetup.ForwarderRootlessport, HostLoopback: hostLoopback}
	if version.Major >= agentsetup.MinPastaForwarderMajor {
		conf.Forwarder = agentsetup.ForwarderPasta
	}
	if hostLoopback != "" && version.Major < agentsetup.MinHostLoopbackMajor {
		return agentsetup.ContainersConf{}, fmt.Errorf("--map-host-loopback needs Podman %d or later; this host has %s", agentsetup.MinHostLoopbackMajor, version)
	}
	return conf, nil
}

// describeContainersConf is setup's progress line for conf.
func describeContainersConf(user string, conf agentsetup.ContainersConf) string {
	parts := []string{"OCI hooks for stack secrets (" + conf.HooksDir + ")"}
	if conf.Forwarder == agentsetup.ForwarderPasta {
		parts = append(parts, "pasta port forwarding")
	}
	if conf.HostLoopback != "" {
		parts = append(parts, "host loopback at "+conf.HostLoopback)
	}
	return fmt.Sprintf("Configuring Podman for %s: %s", user, strings.Join(parts, ", "))
}

// runSetup performs setup by default. The private --elevated flag marks the
// sudo child and prevents recursive elevation.
func runSetup(ctx context.Context, args []string, stdout io.Writer) error {
	return runSetupWithEnvironment(ctx, args, stdout, defaultSetupEnvironment())
}

func runSetupWithEnvironment(ctx context.Context, args []string, stdout io.Writer, environment setupEnvironment) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(stdout)
	user := flags.String("user", agentsetup.DefaultUser, "dedicated rootless service user")
	dryRun := flags.Bool("dry-run", false, "show the required setup steps without applying them")
	elevated := flags.Bool("elevated", false, "internal: setup is running with elevated privileges")
	hooksDir := flags.String("oci-hooks-dir", "", "OCI hook directory for stack secrets (default ~USER/.local/share/sorry-portainer/oci-hooks; must match the agent's oci_hooks_dir)")
	hostLoopback := flags.String("map-host-loopback", "", "address at which the agent user's containers reach the host's 127.0.0.1 (Podman 5 or later); every one of them can then reach the host's loopback services")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *hostLoopback != "" {
		canonical, err := agentsetup.ParseHostLoopback(*hostLoopback)
		if err != nil {
			return err
		}
		*hostLoopback = canonical
	}
	if *hooksDir != "" {
		if err := (agentsetup.ContainersConf{HooksDir: *hooksDir}).Validate(); err != nil {
			return err
		}
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("setup: unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	osRelease, err := environment.readOSRelease()
	if err != nil {
		return fmt.Errorf("read /etc/os-release: %w", err)
	}
	platform, err := agentsetup.PlatformFromOSRelease(osRelease)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Detected OS: %s (%s mode)\n", platform.DisplayName, platform.Mode)

	if environment.euid() != 0 && !*dryRun {
		if *elevated {
			return errors.New("setup was relaunched for elevation but is still not running as root")
		}
		executable, err := environment.executable()
		if err != nil {
			return fmt.Errorf("locate current executable: %w", err)
		}
		fmt.Fprintln(stdout, "Setup requires sudo; automatically relaunching with sudo.")
		forwarded := []string{"--user", *user}
		if *hooksDir != "" {
			forwarded = append(forwarded, "--oci-hooks-dir", *hooksDir)
		}
		if *hostLoopback != "" {
			forwarded = append(forwarded, "--map-host-loopback", *hostLoopback)
		}
		return environment.runSudo(ctx, executable, forwarded, stdout)
	}

	state, err := agentsetup.Detect(ctx, environment.probe, osRelease, *user)
	if err != nil {
		return fmt.Errorf("detect host setup: %w", err)
	}
	plan, err := agentsetup.BuildPlan(state, *user)
	if err != nil {
		return err
	}
	// The hooks directory defaults to one under the user's home, which
	// useradd puts in /home for a user setup creates.
	resolveHooksDir := func() (string, error) {
		if *hooksDir != "" {
			return *hooksDir, nil
		}
		if *dryRun && !state.UserExists {
			return agentsetup.DefaultOCIHooksDir(filepath.Join("/home", *user)), nil
		}
		home, err := environment.lookupHome(*user)
		if err != nil {
			return "", fmt.Errorf("look up agent user: %w", err)
		}
		return agentsetup.DefaultOCIHooksDir(home), nil
	}
	if *dryRun {
		dir, err := resolveHooksDir()
		if err != nil {
			return err
		}
		return reportDryRun(ctx, stdout, environment, state, plan, dir, *hostLoopback)
	}

	installer := agentsetup.Installer{
		Probe: environment.probe,
		EUID:  environment.euid,
		Progress: func(message string) {
			fmt.Fprintln(stdout, message)
		},
	}
	version, err := installer.Apply(ctx, plan)
	if err != nil {
		return err
	}
	dir, err := resolveHooksDir()
	if err != nil {
		return err
	}
	conf, err := containersConfFor(version, dir, *hostLoopback)
	if err != nil {
		return err
	}
	executable, err := environment.executable()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}
	fmt.Fprintln(stdout, "Installing system service for spa")
	if err := environment.installFiles(executable, *user); err != nil {
		return err
	}
	fmt.Fprintln(stdout, describeContainersConf(*user, conf))
	if err := environment.configureContainers(ctx, *user, conf); err != nil {
		return err
	}
	if conf.Forwarder != agentsetup.ForwarderPasta {
		fmt.Fprintf(stdout, "Note: Podman %s forwards published ports through rootlessport, so containers see an internal source address instead of the client's; Podman %d keeps it.\n", version, agentsetup.MinPastaForwarderMajor)
	}
	fmt.Fprintln(stdout, "Done!")
	fmt.Fprintln(stdout)
	fmt.Fprint(stdout, enrollInstructions(*user))
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Happy using!")
	return nil
}

// noChanges is the dry run's report for a host setup would leave alone.
// The rsansible role looks for it.
const noChanges = "No package or rootless Podman changes are required."

// reportDryRun prints what setup would do. Only root can see the agent
// user's drop-in, and only when no package changes first, so otherwise the
// configuration step is assumed.
func reportDryRun(ctx context.Context, stdout io.Writer, environment setupEnvironment, state agentsetup.State, plan agentsetup.Plan, hooksDir, hostLoopback string) error {
	configure := true
	var conf agentsetup.ContainersConf
	if len(plan.Actions) == 0 {
		var err error
		conf, err = containersConfFor(agentsetup.Version{Major: state.PodmanMajor, Minor: state.PodmanMinor}, hooksDir, hostLoopback)
		if err != nil {
			return err
		}
		if environment.euid() == 0 {
			executable, err := environment.executable()
			if err != nil {
				return fmt.Errorf("locate current executable: %w", err)
			}
			current, err := environment.containersConfCurrent(ctx, executable, plan.User, conf)
			if err != nil {
				return err
			}
			configure = !current
		}
		if !configure {
			fmt.Fprintln(stdout, noChanges)
			return nil
		}
	}
	lastProgress := ""
	for _, action := range plan.Actions {
		if action.Progress != lastProgress {
			fmt.Fprintln(stdout, action.Progress)
			lastProgress = action.Progress
		}
	}
	fmt.Fprintln(stdout, "Installing system service for spa")
	if len(plan.Actions) == 0 {
		fmt.Fprintln(stdout, describeContainersConf(plan.User, conf))
	} else {
		fmt.Fprintf(stdout, "Configuring Podman for %s (OCI hooks for stack secrets (%s), and the settings the installed Podman supports)\n", plan.User, hooksDir)
	}
	return nil
}

// enrollInstructions is the next step after setup: the agent starts once it
// has been enrolled.
func enrollInstructions(user string) string {
	return fmt.Sprintf(`Next, enroll this host. Create a token in the dashboard's Enrollment page,
or on the master as the server's account:
  sorry-portainer-server hosts token --host HOST_ID
Then run here, and paste the token when asked:
  %s
  sudo systemctl enable --now sorry-portainer-agent
`, agentsetup.EnrollCommand(user, "wss://MASTER:9443/control/agent", "HOST_ID", false))
}

func installAgentService(sourceBinary, user string) error {
	binary, err := os.ReadFile(sourceBinary)
	if err != nil {
		return fmt.Errorf("read agent binary: %w", err)
	}
	if err := writeAtomic(systemAgentBinary, binary, 0755); err != nil {
		return fmt.Errorf("install agent binary: %w", err)
	}
	account, err := osuser.Lookup(user)
	if err != nil {
		return fmt.Errorf("look up agent user: %w", err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return fmt.Errorf("agent user %s has uid %q: %w", user, account.Uid, err)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return fmt.Errorf("agent user %s has gid %q: %w", user, account.Gid, err)
	}
	if err := prepareAgentDir(systemAgentDir, uid, gid); err != nil {
		return fmt.Errorf("prepare %s: %w", systemAgentDir, err)
	}
	if !filepath.IsAbs(account.HomeDir) {
		return fmt.Errorf("agent user %s's home directory %q is not absolute", user, account.HomeDir)
	}
	unit := agentUnit(user, uid, account.HomeDir, systemAgentBinary, systemAgentDir)
	if err := writeAtomic(systemAgentUnit, []byte(unit), 0644); err != nil {
		return fmt.Errorf("install agent systemd service: %w", err)
	}
	command := exec.Command("systemctl", "daemon-reload")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("reload systemd after installing agent service: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// agentUnit is the system service. It waits for enrollment: without
// agent.json it does not start. The runtime directory is written out
// because systemd's %U in a system unit is the manager's UID (0), not
// User='s; the user's manager owns that directory and the Podman socket,
// so the agent starts after it.
func agentUnit(user string, uid int, home, binary, dir string) string {
	config := filepath.Join(dir, "agent.json")
	return fmt.Sprintf(`[Unit]
Description=Sorry Portainer Podman agent
After=network-online.target user@%[2]d.service
Wants=network-online.target user@%[2]d.service
ConditionPathExists=%[3]s

[Service]
Type=simple
User=%[1]s
Group=%[1]s
Environment=HOME=%[4]s
Environment=XDG_RUNTIME_DIR=/run/user/%[2]d
ExecStart=%[5]s --config %[3]s
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
`, user, uid, config, home, binary)
}

// prepareAgentDir creates the agent directory, or brings an existing one to
// owner uid:gid and mode 0700. A symlink or a non-directory is refused, so
// nothing is redirected through it.
func prepareAgentDir(path string, uid, gid int) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s exists and is not a directory", path)
	}
	if err := os.Lchown(path, uid, gid); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".sorry-portainer-install-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
