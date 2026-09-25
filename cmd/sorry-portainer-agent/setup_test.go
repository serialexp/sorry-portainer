package main

import (
	"context"
	"io"
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
	if executable == "" || strings.Join(arguments, " ") != "--user sorry-portainer" {
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
	var out strings.Builder
	if err := runSetupWithEnvironment(context.Background(), []string{"--elevated"}, &out, environment); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, wanted := range []string{
		"Detected OS: Pop!_OS 24.04 (ubuntu mode)",
		"Updating apt",
		"Installing podman and podman-compose",
		"Installing system service for spa",
		"Done!",
		"Happy using!",
	} {
		if !strings.Contains(got, wanted) {
			t.Fatalf("output missing %q: %q", wanted, got)
		}
	}
	if strings.Contains(got, "apt-get") || !installed {
		t.Fatalf("raw commands leaked or service not installed: output=%q installed=%v", got, installed)
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
