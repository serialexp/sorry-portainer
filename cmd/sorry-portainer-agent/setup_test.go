package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/serialexp/sorry-portainer/internal/agentsetup"
)

type setupProbe struct {
	responses []setupResponse
	calls     []string
}
type setupResponse struct {
	output string
	exit   int
	err    error
}

func (p *setupProbe) Run(_ context.Context, name string, args ...string) ([]byte, int, error) {
	p.calls = append(p.calls, name+" "+strings.Join(args, " "))
	if len(p.responses) == 0 {
		return nil, 0, nil
	}
	response := p.responses[0]
	p.responses = p.responses[1:]
	return []byte(response.output), response.exit, response.err
}

func testSetupEnvironment(probe agentsetup.Probe) setupEnvironment {
	return setupEnvironment{
		readOSRelease: func() ([]byte, error) {
			return []byte("NAME=Pop!_OS\nID=pop\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=24.04\n"), nil
		},
		executable: func() (string, error) { return "/home/bart/.local/bin/sorry-portainer-agent", nil },
		euid:       func() int { return 1000 },
		probe:      probe,
		installFiles: func(string, string) error {
			return nil
		},
		configureContainers: func(context.Context, string, agentsetup.ContainersConf) error { return nil },
		containersConfCurrent: func(context.Context, string, string, agentsetup.ContainersConf) (bool, error) {
			return false, errors.New("unexpected drop-in check")
		},
		lookupHome: func(user string) (string, error) { return "/home/" + user, nil },
	}
}

// completeHost answers Detect's probes for a host with every package,
// Podman at version, and the user's services set up.
func completeHost(version string) []setupResponse {
	responses := make([]setupResponse, 0, len(agentsetup.RequiredPackages)+5)
	for range agentsetup.RequiredPackages {
		responses = append(responses, setupResponse{output: "ii "})
	}
	return append(responses,
		setupResponse{output: "podman version " + version},
		setupResponse{}, setupResponse{output: "yes\n"}, setupResponse{}, setupResponse{})
}

// bareHost answers Detect's probes for a host with no packages and no user.
func bareHost() []setupResponse {
	responses := make([]setupResponse, 0, len(agentsetup.RequiredPackages)+1)
	for range agentsetup.RequiredPackages {
		responses = append(responses, setupResponse{exit: 1})
	}
	return append(responses, setupResponse{exit: 1})
}

func TestSetupRejectsUnexpectedArgumentsBeforeHostInspection(t *testing.T) {
	var out strings.Builder
	err := runSetupWithEnvironment(context.Background(), []string{"unexpected"}, &out, testSetupEnvironment(&setupProbe{}))
	if err == nil || !strings.Contains(err.Error(), "unexpected arguments") {
		t.Fatalf("error = %v", err)
	}
}

func TestSetupAutomaticallyRelaunchesWithSudo(t *testing.T) {
	var out strings.Builder
	environment := testSetupEnvironment(&setupProbe{})
	var executable string
	var arguments []string
	environment.runSudo = func(_ context.Context, path string, args []string, _ io.Writer) error {
		executable = path
		arguments = append([]string(nil), args...)
		return nil
	}
	if err := runSetupWithEnvironment(context.Background(), nil, &out, environment); err != nil {
		t.Fatal(err)
	}
	// The hooks directory defaults under the home, which only root can look up.
	if executable == "" || strings.Join(arguments, " ") != "--user sorry-portainer-agent" {
		t.Fatalf("sudo relaunch = %q %#v", executable, arguments)
	}
	// The host loopback address goes along, canonical.
	if err := runSetupWithEnvironment(context.Background(), []string{"--map-host-loopback", "fd00::0:3"}, &out, environment); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(arguments, " "); !strings.HasSuffix(got, " --map-host-loopback fd00::3") {
		t.Fatalf("sudo relaunch = %q", got)
	}
	if got := out.String(); !strings.Contains(got, "Detected OS: Pop!_OS 24.04 (ubuntu mode)") || !strings.Contains(got, "automatically relaunching with sudo") {
		t.Fatalf("output = %q", got)
	}
}

func TestElevatedSetupReportsConciseProgressAndInstallsService(t *testing.T) {
	responses := append(bareHost(),
		setupResponse{}, setupResponse{}, setupResponse{}, setupResponse{}, setupResponse{}, setupResponse{}, // setup actions
		setupResponse{output: "podman version 6.1.4"}, // verification
	)
	probe := &setupProbe{responses: responses}
	environment := testSetupEnvironment(probe)
	environment.euid = func() int { return 0 }
	installed := false
	environment.installFiles = func(source, user string) error {
		installed = source != "" && user == "sorry-portainer-agent"
		return nil
	}
	var configured *agentsetup.ContainersConf
	configuredUser := ""
	environment.configureContainers = func(_ context.Context, user string, conf agentsetup.ContainersConf) error {
		if !installed {
			t.Error("Podman configured before the agent binary was installed")
		}
		configured, configuredUser = &conf, user
		return nil
	}
	var out strings.Builder
	if err := runSetupWithEnvironment(context.Background(), []string{"--elevated", "--oci-hooks-dir", "/srv/hooks", "--map-host-loopback", "169.254.1.3"}, &out, environment); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, wanted := range []string{
		"Detected OS: Pop!_OS 24.04 (ubuntu mode)",
		"Updating apt",
		"Installing podman, podman-compose, crun, netavark, aardvark-dns, passt",
		"Installing system service for spa",
		"Configuring Podman for sorry-portainer-agent: OCI hooks for stack secrets (/srv/hooks), pasta port forwarding, host loopback at 169.254.1.3",
		"Done!",
		"enroll --server-url wss://MASTER:9443/control/agent --host-id HOST_ID --dir /var/lib/sorry-portainer-agent --token-file -",
		"Happy using!",
	} {
		if !strings.Contains(got, wanted) {
			t.Fatalf("output missing %q: %q", wanted, got)
		}
	}
	if strings.Contains(got, "apt-get") || strings.Contains(got, "rootlessport") || !installed {
		t.Fatalf("raw commands leaked, forwarder note on Podman 6, or service not installed: output=%q installed=%v", got, installed)
	}
	want := agentsetup.ContainersConf{HooksDir: "/srv/hooks", Forwarder: agentsetup.ForwarderPasta, HostLoopback: "169.254.1.3"}
	if configuredUser != "sorry-portainer-agent" || configured == nil || *configured != want {
		t.Fatalf("configured %s with %+v", configuredUser, configured)
	}
}

func TestSetupOnOlderPodman(t *testing.T) {
	run := func(version string, args ...string) (string, *agentsetup.ContainersConf, error) {
		environment := testSetupEnvironment(&setupProbe{responses: append(completeHost(version), setupResponse{output: "podman version " + version})})
		environment.euid = func() int { return 0 }
		var configured *agentsetup.ContainersConf
		environment.configureContainers = func(_ context.Context, _ string, conf agentsetup.ContainersConf) error {
			configured = &conf
			return nil
		}
		var out strings.Builder
		err := runSetupWithEnvironment(context.Background(), append([]string{"--elevated"}, args...), &out, environment)
		return out.String(), configured, err
	}
	// Podman 5 keeps rootlessport and says what that costs; host loopback works.
	out, conf, err := run("5.7.0", "--map-host-loopback", "169.254.1.3")
	if err != nil {
		t.Fatal(err)
	}
	if conf == nil || conf.Forwarder != agentsetup.ForwarderRootlessport || conf.HostLoopback != "169.254.1.3" || !strings.Contains(out, "Note: Podman 5.7 forwards published ports through rootlessport") {
		t.Fatalf("conf %+v, output %q", conf, out)
	}
	// Podman 4 cannot map host loopback, and nothing is configured.
	if _, conf, err = run("4.9.3", "--map-host-loopback", "169.254.1.3"); err == nil || !strings.Contains(err.Error(), "needs Podman 5") || conf != nil {
		t.Fatalf("Podman 4: %v, configured %+v", err, conf)
	}
	if _, conf, err = run("4.9.3"); err != nil || conf == nil || conf.Forwarder != agentsetup.ForwarderRootlessport {
		t.Fatalf("Podman 4 without loopback: %v, %+v", err, conf)
	}
}

func TestSetupDefaultsTheHooksDirUnderTheAccountsHome(t *testing.T) {
	environment := testSetupEnvironment(&setupProbe{responses: append(completeHost("6.1.4"), setupResponse{output: "podman version 6.1.4"})})
	environment.euid = func() int { return 0 }
	environment.lookupHome = func(user string) (string, error) { return "/srv/" + user, nil }
	var configured agentsetup.ContainersConf
	environment.configureContainers = func(_ context.Context, _ string, conf agentsetup.ContainersConf) error {
		configured = conf
		return nil
	}
	var out strings.Builder
	if err := runSetupWithEnvironment(context.Background(), []string{"--elevated"}, &out, environment); err != nil {
		t.Fatal(err)
	}
	if configured.HooksDir != "/srv/sorry-portainer-agent/.local/share/sorry-portainer/oci-hooks" {
		t.Fatalf("hooks dir %q", configured.HooksDir)
	}
	environment = testSetupEnvironment(&setupProbe{responses: append(completeHost("6.1.4"), setupResponse{output: "podman version 6.1.4"})})
	environment.euid = func() int { return 0 }
	environment.lookupHome = func(string) (string, error) { return "", errors.New("no such user") }
	if err := runSetupWithEnvironment(context.Background(), []string{"--elevated"}, &out, environment); err == nil || !strings.Contains(err.Error(), "no such user") {
		t.Fatalf("error = %v", err)
	}
}

func TestSetupRejectsBadHostLoopback(t *testing.T) {
	var out strings.Builder
	for _, bad := range []string{"localhost", "127.0.0.1", "169.254.1.2"} {
		if err := runSetupWithEnvironment(context.Background(), []string{"--map-host-loopback", bad}, &out, testSetupEnvironment(&setupProbe{})); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSetupRejectsRelativeHooksDir(t *testing.T) {
	var out strings.Builder
	err := runSetupWithEnvironment(context.Background(), []string{"--oci-hooks-dir", "hooks"}, &out, testSetupEnvironment(&setupProbe{}))
	if err == nil {
		t.Fatal("accepted a relative hooks dir")
	}
}

func noContainers() (bool, error) { return false, nil }

func TestSetupContainersConfPrint(t *testing.T) {
	var out strings.Builder
	if err := runSetupContainersConf([]string{"--print", "--oci-hooks-dir", "/srv/hooks", "--port-forwarder", "pasta", "--map-host-loopback", "169.254.1.3"}, &out, noContainers); err != nil {
		t.Fatal(err)
	}
	want, _ := agentsetup.ContainersConf{HooksDir: "/srv/hooks", Forwarder: agentsetup.ForwarderPasta, HostLoopback: "169.254.1.3"}.Render()
	if out.String() != want {
		t.Fatalf("printed %q", out.String())
	}
	for _, bad := range [][]string{
		{"--print", "extra"},
		{"--print", "--oci-hooks-dir", "relative"},
		{"--print", "--port-forwarder", "slirp"},
		{"--print", "--map-host-loopback", "127.0.0.1"},
		{"--print", "--check"},
	} {
		if err := runSetupContainersConf(bad, &out, noContainers); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSetupContainersConfCheck(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("refuses to run as root")
	}
	home := t.TempDir()
	args := []string{"--port-forwarder", "pasta"}
	check := func() string {
		var out strings.Builder
		if err := setupContainersConfIn(home, append(args, "--check"), &out, noContainers); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := check(); got != "stale\n" {
		t.Fatalf("before writing: %q", got)
	}
	var out strings.Builder
	if err := setupContainersConfIn(home, args, &out, noContainers); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agentsetup.DefaultOCIHooksDir(home)); err != nil {
		t.Fatalf("default hooks dir under home: %v", err)
	}
	if got := check(); got != "current\n" {
		t.Fatalf("after writing: %q", got)
	}
}

func TestSetupDryRunUsesHumanProgressOnly(t *testing.T) {
	probe := &setupProbe{responses: bareHost()}
	environment := testSetupEnvironment(probe)
	var out strings.Builder
	if err := runSetupWithEnvironment(context.Background(), []string{"--dry-run"}, &out, environment); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "apt-get") || !strings.Contains(out.String(), "Updating apt") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestSetupDryRunChecksTheDropInAsRoot(t *testing.T) {
	for _, tc := range []struct {
		current bool
		want    string
	}{
		{true, noChanges},
		{false, "Configuring Podman for sorry-portainer-agent: OCI hooks for stack secrets (/home/sorry-portainer-agent/.local/share/sorry-portainer/oci-hooks), pasta port forwarding, host loopback at 169.254.1.3"},
	} {
		environment := testSetupEnvironment(&setupProbe{responses: completeHost("6.1.4")})
		environment.euid = func() int { return 0 }
		var checked agentsetup.ContainersConf
		environment.containersConfCurrent = func(_ context.Context, executable, user string, conf agentsetup.ContainersConf) (bool, error) {
			if executable != "/home/bart/.local/bin/sorry-portainer-agent" || user != "sorry-portainer-agent" {
				t.Errorf("checked with %s as %s", executable, user)
			}
			checked = conf
			return tc.current, nil
		}
		var out strings.Builder
		if err := runSetupWithEnvironment(context.Background(), []string{"--dry-run", "--map-host-loopback", "169.254.1.3"}, &out, environment); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), tc.want) || tc.current && strings.Contains(out.String(), "Configuring") {
			t.Fatalf("current=%v: output %q", tc.current, out.String())
		}
		if checked.Forwarder != agentsetup.ForwarderPasta || checked.HostLoopback != "169.254.1.3" {
			t.Fatalf("checked %+v", checked)
		}
	}
}

func TestSetupDryRunWithoutRootAssumesTheDropInChanges(t *testing.T) {
	environment := testSetupEnvironment(&setupProbe{responses: completeHost("6.1.4")})
	var out strings.Builder
	if err := runSetupWithEnvironment(context.Background(), []string{"--dry-run"}, &out, environment); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), noChanges) || !strings.Contains(out.String(), "Configuring Podman for sorry-portainer-agent") {
		t.Fatalf("output %q", out.String())
	}
}

func TestAgentUnitRunsFromTheAgentDirectory(t *testing.T) {
	unit := agentUnit("spa", 1234, "/home/spa", "/usr/local/bin/sorry-portainer-agent", "/var/lib/sorry-portainer-agent")
	for _, wanted := range []string{
		"After=network-online.target user@1234.service\n",
		"Wants=network-online.target user@1234.service\n",
		"ConditionPathExists=/var/lib/sorry-portainer-agent/agent.json\n",
		"ExecStart=/usr/local/bin/sorry-portainer-agent --config /var/lib/sorry-portainer-agent/agent.json\n",
		"User=spa\n",
		"Group=spa\n",
		"Environment=HOME=/home/spa\n",
		"Environment=XDG_RUNTIME_DIR=/run/user/1234\n",
	} {
		if !strings.Contains(unit, wanted) {
			t.Fatalf("unit missing %q:\n%s", wanted, unit)
		}
	}
	// A specifier would expand to the system manager's values (root's),
	// not the service user's.
	for _, unwanted := range []string{"/etc/sorry-portainer", "%"} {
		if strings.Contains(unit, unwanted) {
			t.Fatalf("unit contains %q:\n%s", unwanted, unit)
		}
	}
}

func TestPrepareAgentDir(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	dir := filepath.Join(t.TempDir(), "agent")
	if err := prepareAgentDir(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Fatalf("created %v %v", st.Mode(), err)
	}
	// An existing, too open directory is tightened; its content is kept.
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareAgentDir(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", st.Mode())
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.json")); err != nil {
		t.Fatal(err)
	}
	// Symlinks and files are refused.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := prepareAgentDir(link, uid, gid); err == nil {
		t.Fatal("followed a symlink")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareAgentDir(file, uid, gid); err == nil {
		t.Fatal("accepted a file")
	}
}
