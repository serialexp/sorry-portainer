package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/serialexp/sorry-portainer/internal/agentsetup"
)

const systemAgentBinary = "/usr/local/bin/sorry-portainer-agent"
const systemAgentUnit = "/etc/systemd/system/sorry-portainer-agent.service"

type setupEnvironment struct {
	readOSRelease func() ([]byte, error)
	executable    func() (string, error)
	euid          func() int
	runSudo       func(context.Context, string, []string, io.Writer) error
	probe         agentsetup.Probe
	installFiles  func(string, string) error
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
		installFiles: installAgentService,
	}
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
	if err := flags.Parse(args); err != nil {
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
		forwarded := []string{"--user", *user}
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
	fmt.Fprintln(stdout, "Done!")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Happy using!")
	return nil
}

func installAgentService(sourceBinary, user string) error {
	binary, err := os.ReadFile(sourceBinary)
	if err != nil {
		return fmt.Errorf("read agent binary: %w", err)
	}
	if err := writeAtomic(systemAgentBinary, binary, 0755); err != nil {
		return fmt.Errorf("install agent binary: %w", err)
	}
	home := filepath.Join("/home", user)
	unit := fmt.Sprintf(`[Unit]
Description=Sorry Portainer Podman agent
After=network-online.target
Wants=network-online.target
ConditionPathExists=/etc/sorry-portainer/agent.json

[Service]
Type=simple
User=%s
Group=%s
Environment=HOME=%s
Environment=XDG_RUNTIME_DIR=/run/user/%%U
ExecStart=%s --config /etc/sorry-portainer/agent.json
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
`, user, user, home, systemAgentBinary)
	if err := writeAtomic(systemAgentUnit, []byte(unit), 0644); err != nil {
		return fmt.Errorf("install agent systemd service: %w", err)
	}
	command := exec.Command("systemctl", "daemon-reload")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("reload systemd after installing agent service: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
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
