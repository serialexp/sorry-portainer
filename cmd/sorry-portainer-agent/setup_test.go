package main

import (
	"context"
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
		configureHooks: func(context.Context, string, string) error { return nil },
	}
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
	if executable == "" || strings.Join(arguments, " ") != "--user sorry-portainer --oci-hooks-dir /home/sorry-portainer/.local/share/sorry-portainer/oci-hooks" {
		t.Fatalf("sudo relaunch = %q %#v", executable, arguments)
	}
	if got := out.String(); !strings.Contains(got, "Detected OS: Pop!_OS 24.04 (ubuntu mode)") || !strings.Contains(got, "automatically relaunching with sudo") {
		t.Fatalf("output = %q", got)
	}
}

func TestElevatedSetupReportsConciseProgressAndInstallsService(t *testing.T) {
	probe := &setupProbe{responses: []setupResponse{
		{exit: 1}, {exit: 1}, {exit: 1}, // package and user probes
		{}, {}, {}, {}, {}, {}, // setup actions
		{output: "podman version 4.9.3"}, // verification
	}}
	environment := testSetupEnvironment(probe)
	environment.euid = func() int { return 0 }
	installed := false
	environment.installFiles = func(source, user string) error {
		installed = source != "" && user == "sorry-portainer"
		return nil
	}
	hooks := ""
	environment.configureHooks = func(_ context.Context, user, dir string) error {
		if !installed {
			t.Error("hooks configured before the agent binary was installed")
		}
		hooks = user + " " + dir
		return nil
	}
	var out strings.Builder
	if err := runSetupWithEnvironment(context.Background(), []string{"--elevated", "--oci-hooks-dir", "/srv/hooks"}, &out, environment); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, wanted := range []string{
		"Detected OS: Pop!_OS 24.04 (ubuntu mode)",
		"Updating apt",
		"Installing podman and podman-compose",
		"Installing system service for spa",
		"Configuring Podman OCI hooks for stack secrets (/srv/hooks)",
		"Done!",
		"enroll --server-url wss://MASTER:9443/control/agent --host-id HOST_ID --dir /var/lib/sorry-portainer-agent --token-file -",
		"Happy using!",
	} {
		if !strings.Contains(got, wanted) {
			t.Fatalf("output missing %q: %q", wanted, got)
		}
	}
	if strings.Contains(got, "apt-get") || !installed {
		t.Fatalf("raw commands leaked or service not installed: output=%q installed=%v", got, installed)
	}
	if hooks != "sorry-portainer /srv/hooks" {
		t.Fatalf("hooks configured as %q", hooks)
	}
}

func TestSetupRejectsRelativeHooksDir(t *testing.T) {
	var out strings.Builder
	err := runSetupWithEnvironment(context.Background(), []string{"--oci-hooks-dir", "hooks"}, &out, testSetupEnvironment(&setupProbe{}))
	if err == nil {
		t.Fatal("accepted a relative hooks dir")
	}
}

func TestSetupHooksConfPrint(t *testing.T) {
	var out strings.Builder
	if err := runSetupHooksConf([]string{"--print", "--oci-hooks-dir", "/srv/hooks"}, &out); err != nil {
		t.Fatal(err)
	}
	want, _ := agentsetup.HooksDropIn("/srv/hooks")
	if out.String() != want {
		t.Fatalf("printed %q", out.String())
	}
	if err := runSetupHooksConf([]string{"--print", "extra"}, &out); err == nil {
		t.Fatal("accepted extra arguments")
	}
	if err := runSetupHooksConf([]string{"--print", "--oci-hooks-dir", "relative"}, &out); err == nil {
		t.Fatal("accepted a relative hooks dir")
	}
}

func TestSetupDryRunUsesHumanProgressOnly(t *testing.T) {
	probe := &setupProbe{responses: []setupResponse{{exit: 1}, {exit: 1}, {exit: 1}}}
	environment := testSetupEnvironment(probe)
	var out strings.Builder
	if err := runSetupWithEnvironment(context.Background(), []string{"--dry-run"}, &out, environment); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "apt-get") || !strings.Contains(out.String(), "Updating apt") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestAgentUnitRunsFromTheAgentDirectory(t *testing.T) {
	unit := agentUnit("spa", "/home/spa", "/usr/local/bin/sorry-portainer-agent", "/var/lib/sorry-portainer-agent")
	for _, wanted := range []string{
		"ConditionPathExists=/var/lib/sorry-portainer-agent/agent.json\n",
		"ExecStart=/usr/local/bin/sorry-portainer-agent --config /var/lib/sorry-portainer-agent/agent.json\n",
		"User=spa\n",
		"Environment=HOME=/home/spa\n",
		"Environment=XDG_RUNTIME_DIR=/run/user/%U\n",
	} {
		if !strings.Contains(unit, wanted) {
			t.Fatalf("unit missing %q:\n%s", wanted, unit)
		}
	}
	if strings.Contains(unit, "/etc/sorry-portainer") {
		t.Fatalf("unit still uses /etc/sorry-portainer:\n%s", unit)
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
