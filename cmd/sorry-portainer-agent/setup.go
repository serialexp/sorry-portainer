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

const systemAgentBinary = "/usr/local/bin/sorry-portainer-agent"
const systemAgentUnit = "/etc/systemd/system/sorry-portainer-agent.service"

// systemAgentDir holds the agent's configuration, credentials and state. The
// agent user owns it, because the agent replaces its own certificate.
const systemAgentDir = "/var/lib/sorry-portainer-agent"

type setupEnvironment struct {
	readOSRelease func() ([]byte, error)
	executable    func() (string, error)
	euid          func() int
	runSudo       func(context.Context, string, []string, io.Writer) error
	probe         agentsetup.Probe
	installFiles  func(string, string) error
	// configureHooks writes the agent user's containers.conf hooks drop-in,
	// running as that user.
	configureHooks func(ctx context.Context, user, hooksDir string) error
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
		installFiles:   installAgentService,
		configureHooks: configureHooksAsUser,
	}
}

// configureHooksAsUser runs the installed agent as the service user to write
// its containers.conf drop-in. Doing the write as the user means files the
// user controls cannot redirect a root write elsewhere.
func configureHooksAsUser(ctx context.Context, user, hooksDir string) error {
	command := exec.CommandContext(ctx, "runuser", "-u", user, "--", systemAgentBinary, "setup-hooks-conf", "--oci-hooks-dir", hooksDir)
	command.Dir = "/"
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("configure Podman OCI hooks for %s: %w: %s", user, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// runSetupHooksConf is the unprivileged half of setup: it writes the drop-in
// into the current user's home.
func runSetupHooksConf(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("setup-hooks-conf", flag.ContinueOnError)
	hooksDir := flags.String("oci-hooks-dir", "", "directory the agent writes its OCI hook JSON to")
	printOnly := flags.Bool("print", false, "print the drop-in instead of installing it (for CONTAINERS_CONF_OVERRIDE in development)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	current, err := osuser.Current()
	if err != nil {
		return err
	}
	if *hooksDir == "" {
		*hooksDir = agentsetup.DefaultOCIHooksDir(current.HomeDir)
	}
	if *printOnly {
		content, err := agentsetup.HooksDropIn(*hooksDir)
		if err != nil {
			return err
		}
		_, err = io.WriteString(stdout, content)
		return err
	}
	path, err := agentsetup.WriteHooksDropIn(current.HomeDir, *hooksDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s\n", path)
	return nil
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
	hooksDir := flags.String("oci-hooks-dir", "", "OCI hook directory for stack secrets (default /home/USER/.local/share/sorry-portainer/oci-hooks; must match the agent's oci_hooks_dir)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *hooksDir == "" {
		*hooksDir = agentsetup.DefaultOCIHooksDir(filepath.Join("/home", *user))
	}
	if _, err := agentsetup.HooksDropIn(*hooksDir); err != nil {
		return err
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
		forwarded := []string{"--user", *user, "--oci-hooks-dir", *hooksDir}
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
	if *dryRun {
		if len(plan.Actions) == 0 {
			fmt.Fprintln(stdout, "No package or rootless Podman changes are required.")
			return nil
		}
		lastProgress := ""
		for _, action := range plan.Actions {
			if action.Progress != lastProgress {
				fmt.Fprintln(stdout, action.Progress)
				lastProgress = action.Progress
			}
		}
		fmt.Fprintln(stdout, "Installing system service for spa")
		fmt.Fprintf(stdout, "Configuring Podman OCI hooks for stack secrets (%s)\n", *hooksDir)
		return nil
	}

	installer := agentsetup.Installer{
		Probe: environment.probe,
		EUID:  environment.euid,
		Progress: func(message string) {
			fmt.Fprintln(stdout, message)
		},
	}
	if err := installer.Apply(ctx, plan); err != nil {
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
	fmt.Fprintf(stdout, "Configuring Podman OCI hooks for stack secrets (%s)\n", *hooksDir)
	if err := environment.configureHooks(ctx, *user, *hooksDir); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Done!")
	fmt.Fprintln(stdout)
	fmt.Fprint(stdout, enrollInstructions(*user))
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Happy using!")
	return nil
}

// enrollInstructions is the next step after setup: the agent starts once it
// has been enrolled.
func enrollInstructions(user string) string {
	return fmt.Sprintf(`Next, enroll this host. On the master, as the server's account:
  sorry-portainer-server hosts token --host HOST_ID
Then here, giving the token on stdin:
  sudo runuser -u %s -- %s enroll --server-url wss://MASTER:9443/control/agent --host-id HOST_ID --dir %s --token-file -
  sudo systemctl enable --now sorry-portainer-agent
`, user, systemAgentBinary, systemAgentDir)
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
	unit := agentUnit(user, filepath.Join("/home", user), systemAgentBinary, systemAgentDir)
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
// agent.json it does not start.
func agentUnit(user, home, binary, dir string) string {
	config := filepath.Join(dir, "agent.json")
	return fmt.Sprintf(`[Unit]
Description=Sorry Portainer Podman agent
After=network-online.target
Wants=network-online.target
ConditionPathExists=%s

[Service]
Type=simple
User=%s
Group=%s
Environment=HOME=%s
Environment=XDG_RUNTIME_DIR=/run/user/%%U
ExecStart=%s --config %s
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
`, config, user, user, home, binary, config)
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
