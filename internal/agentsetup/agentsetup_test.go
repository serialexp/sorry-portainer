package agentsetup

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestBuildPlanRejectsUnsupportedPlatforms(t *testing.T) {
	for _, state := range []State{
		{OSID: "debian", OSVersion: "24.04"},
		{OSID: "ubuntu", OSVersion: "22.04"},
		{OSID: "ubuntu", OSVersion: "26.10"},
	} {
		if _, err := BuildPlan(state, ""); err == nil {
			t.Fatalf("BuildPlan(%+v) succeeded", state)
		}
	}
}

func TestPlatformFromOSReleaseReportsDerivativeMode(t *testing.T) {
	platform, err := PlatformFromOSRelease([]byte("NAME=Pop!_OS\nID=pop\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=26.04\n"))
	if err != nil {
		t.Fatal(err)
	}
	if platform.DisplayName != "Pop!_OS 26.04" || platform.Mode != "ubuntu" {
		t.Fatalf("platform = %+v", platform)
	}
}

func TestPodmanVersionBoundary(t *testing.T) {
	for _, tc := range []struct {
		major, minor int
		want         bool
	}{
		{4, 6, false}, {4, 7, true}, {4, 9, true}, {5, 0, true},
	} {
		if got := PodmanSupported(tc.major, tc.minor); got != tc.want {
			t.Errorf("PodmanSupported(%d, %d) = %v, want %v", tc.major, tc.minor, got, tc.want)
		}
	}
}

func TestBuildPlanCompleteStateIsIdempotent(t *testing.T) {
	state := State{OSID: "ubuntu", OSVersion: "24.04", PodmanInstalled: true, PodmanComposeInstalled: true, PodmanMajor: 4, PodmanMinor: 7, UserExists: true, LingerEnabled: true, SocketEnabled: true, SocketActive: true}
	plan, err := BuildPlan(state, DefaultUser)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 0 {
		t.Fatalf("actions = %#v", plan.Actions)
	}
}

func TestBuildPlanUsesDirectArgumentVectors(t *testing.T) {
	plan, err := BuildPlan(State{OSID: "ubuntu", OSVersion: "26.04"}, "agent_user")
	if err != nil {
		t.Fatal(err)
	}
	want := []Command{
		{"apt-get", []string{"update"}},
		{"apt-get", []string{"install", "-y", "podman", "podman-compose"}},
		{"useradd", []string{"--create-home", "--user-group", "--shell", "/usr/sbin/nologin", "agent_user"}},
		{"loginctl", []string{"enable-linger", "agent_user"}},
		{"systemctl", []string{"--user", "--machine", "agent_user@", "enable", "podman.socket"}},
		{"systemctl", []string{"--user", "--machine", "agent_user@", "start", "podman.socket"}},
	}
	var got []Command
	for _, action := range plan.Actions {
		got = append(got, action.Command)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands:\n got %#v\nwant %#v", got, want)
	}
}

func TestInvalidUserCannotBecomeAnArgument(t *testing.T) {
	if _, err := BuildPlan(State{OSID: "ubuntu", OSVersion: "24.04"}, "bad;user"); err == nil {
		t.Fatal("expected invalid user error")
	}
}

func TestDetectAcceptsUbuntuDerivative(t *testing.T) {
	probe := &fakeProbe{responses: []fakeResponse{{}, {}, {exit: 1}}}
	state, err := Detect(context.Background(), probe, []byte("ID=pop\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=24.04\n"), DefaultUser)
	if err != nil {
		t.Fatal(err)
	}
	if state.OSID != "ubuntu" || state.OSVersion != "24.04" {
		t.Fatalf("state = %+v", state)
	}
}

func TestParseInputs(t *testing.T) {
	values, err := ParseOSRelease([]byte("NAME=Ubuntu\nID=ubuntu\nVERSION_ID=\"24.04\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if values["ID"] != "ubuntu" || values["VERSION_ID"] != "24.04" {
		t.Fatalf("values = %#v", values)
	}
	major, minor, err := ParsePodmanVersion("podman version 4.7.2\n")
	if err != nil || major != 4 || minor != 7 {
		t.Fatalf("version = %d.%d, %v", major, minor, err)
	}
	if _, _, err = ParsePodmanVersion("not a version"); err == nil {
		t.Fatal("expected parse error")
	}
}

type recordedCall struct {
	name string
	args []string
}
type fakeProbe struct {
	calls     []recordedCall
	responses []fakeResponse
}
type fakeResponse struct {
	output string
	exit   int
	err    error
}

func (f *fakeProbe) Run(_ context.Context, name string, args ...string) ([]byte, int, error) {
	f.calls = append(f.calls, recordedCall{name, append([]string(nil), args...)})
	response := f.responses[len(f.calls)-1]
	return []byte(response.output), response.exit, response.err
}

func TestDetectRejectsUnsupportedHostBeforeProbing(t *testing.T) {
	probe := &fakeProbe{}
	_, err := Detect(context.Background(), probe, []byte("ID=debian\nVERSION_ID=24.04\n"), DefaultUser)
	if err == nil || !strings.Contains(err.Error(), "unsupported operating system") {
		t.Fatalf("error = %v", err)
	}
	if len(probe.calls) != 0 {
		t.Fatalf("probed unsupported host: %#v", probe.calls)
	}
}

func TestDetectAcceptsUbuntuCompatibleDerivative(t *testing.T) {
	probe := &fakeProbe{responses: []fakeResponse{{exit: 1}, {exit: 1}, {exit: 1}}}
	state, err := Detect(context.Background(), probe, []byte("ID=pop\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=24.04\n"), DefaultUser)
	if err != nil {
		t.Fatal(err)
	}
	if state.OSID != "ubuntu" {
		t.Fatalf("normalized OS ID = %q", state.OSID)
	}
}

func TestDetectCompleteHost(t *testing.T) {
	probe := &fakeProbe{responses: []fakeResponse{
		{output: "ii  ", exit: 0}, {output: "ii  ", exit: 0},
		{output: "podman version 5.4.0"}, {}, {output: "yes\n"}, {}, {},
	}}
	state, err := Detect(context.Background(), probe, []byte("ID=ubuntu\nVERSION_ID=26.04\n"), DefaultUser)
	if err != nil {
		t.Fatal(err)
	}
	if !state.PodmanInstalled || !state.PodmanComposeInstalled || state.PodmanMajor != 5 || !state.UserExists || !state.LingerEnabled || !state.SocketEnabled || !state.SocketActive {
		t.Fatalf("state = %+v", state)
	}
	for _, call := range probe.calls {
		if call.name == "sh" || call.name == "bash" {
			t.Fatalf("shell invoked: %#v", call)
		}
	}
}

func TestInstallerRequiresRootBeforeExecuting(t *testing.T) {
	probe := &fakeProbe{}
	err := (Installer{Probe: probe, EUID: func() int { return 1000 }}).Apply(context.Background(), Plan{Actions: []Action{{Progress: "test", Command: Command{Name: "apt-get"}}}})
	if err == nil || !strings.Contains(err.Error(), "requires root") {
		t.Fatalf("error = %v", err)
	}
	if len(probe.calls) != 0 {
		t.Fatalf("executed %#v", probe.calls)
	}
}

func TestInstallerStopsOnCommandFailure(t *testing.T) {
	probe := &fakeProbe{responses: []fakeResponse{{output: "locked", exit: 100}}}
	err := (Installer{Probe: probe, EUID: func() int { return 0 }}).Apply(context.Background(), Plan{Actions: []Action{{Progress: "refresh", Command: Command{Name: "apt-get", Args: []string{"update"}}}, {Progress: "install", Command: Command{Name: "apt-get"}}}})
	if err == nil || !strings.Contains(err.Error(), "status 100") {
		t.Fatalf("error = %v", err)
	}
	if len(probe.calls) != 1 {
		t.Fatalf("calls = %#v", probe.calls)
	}
}

func TestInstallerVerifiesMinimumVersionAfterActions(t *testing.T) {
	probe := &fakeProbe{responses: []fakeResponse{{}, {output: "podman version 4.6.2"}}}
	plan := Plan{Actions: []Action{{Progress: "install", Command: Command{Name: "apt-get"}}}}
	err := (Installer{Probe: probe, EUID: func() int { return 0 }}).Apply(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "4.7 or later") {
		t.Fatalf("error = %v", err)
	}
	if got := probe.calls[1]; got.name != "podman" || !reflect.DeepEqual(got.args, []string{"--version"}) {
		t.Fatalf("verification call = %#v", got)
	}
}
