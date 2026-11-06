package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/admin"
	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// fakeClient records calls and answers from its fields.
type fakeClient struct {
	calls     []string
	list      admin.ListResponse
	token     admin.TokenResponse
	stacks    []protocol.Stack
	stack     protocol.Stack
	saved     *protocol.StackSave
	applied   *protocol.StackOperation
	operation protocol.StackOperation
	err       error
}

func (f *fakeClient) List(context.Context) (admin.ListResponse, error) {
	f.calls = append(f.calls, "list")
	return f.list, f.err
}
func (f *fakeClient) Token(_ context.Context, host string, ttl time.Duration) (admin.TokenResponse, error) {
	f.calls = append(f.calls, "token "+host+" "+ttl.String())
	return f.token, f.err
}
func (f *fakeClient) Remove(_ context.Context, host string) error {
	f.calls = append(f.calls, "remove "+host)
	return f.err
}
func (f *fakeClient) Stacks(_ context.Context, host string) ([]protocol.Stack, error) {
	f.calls = append(f.calls, "stacks "+host)
	return f.stacks, f.err
}
func (f *fakeClient) Stack(_ context.Context, host, name string) (protocol.Stack, error) {
	f.calls = append(f.calls, "stack "+host+" "+name)
	return f.stack, f.err
}
func (f *fakeClient) SaveStack(_ context.Context, host string, save protocol.StackSave) (protocol.StackSaved, error) {
	f.calls = append(f.calls, "save "+host+" "+save.Name)
	f.saved = &save
	v := 1
	if save.ExpectedVersion != nil {
		v = *save.ExpectedVersion + 1
	}
	return protocol.StackSaved{Stack: protocol.Stack{Name: save.Name, Version: v}, Operation: f.applied}, f.err
}
func (f *fakeClient) StackOperation(_ context.Context, host, name, operation string) (protocol.StackOperation, error) {
	f.calls = append(f.calls, operation+" "+host+" "+name)
	return f.operation, f.err
}

type result struct {
	code       int
	out, err   string
	connection connection
}

func invoke(t *testing.T, f *fakeClient, env map[string]string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	var got connection
	code := run(context.Background(), args, func(k string) string { return env[k] }, &out, &errOut,
		func(c connection) (client, error) {
			got = c
			return f, nil
		})
	return result{code: code, out: out.String(), err: errOut.String(), connection: got}
}

func TestConnectionSettings(t *testing.T) {
	r := invoke(t, &fakeClient{}, nil, "hosts", "list")
	if r.code != exitOK || r.connection.master != "" || r.connection.stateDir != defaultStateDir {
		t.Fatalf("local default: %+v", r)
	}
	env := map[string]string{"SORRY_PORTAINER_MASTER": "ssh://root@hel1", "SORRY_PORTAINER_SSH": "ssh -F cfg"}
	r = invoke(t, &fakeClient{}, env, "hosts", "list")
	if r.connection.master != "ssh://root@hel1" || !reflect.DeepEqual(r.connection.sshCommand, []string{"ssh", "-F", "cfg"}) {
		t.Fatalf("environment: %+v", r.connection)
	}
	r = invoke(t, &fakeClient{}, env, "--master", "ssh://other", "--ssh-command", "myssh", "--state-dir", "/srv/sp", "hosts", "list")
	if r.connection.master != "ssh://other" || !reflect.DeepEqual(r.connection.sshCommand, []string{"myssh"}) || r.connection.stateDir != "/srv/sp" {
		t.Fatalf("flags override the environment: %+v", r.connection)
	}
}

func TestConnectFailureExitsOne(t *testing.T) {
	var errOut bytes.Buffer
	code := run(context.Background(), []string{"hosts", "list"}, func(string) string { return "" }, &bytes.Buffer{}, &errOut,
		func(connection) (client, error) { return nil, errors.New("no socket") })
	if code != exitFailed || !strings.Contains(errOut.String(), "no socket") {
		t.Fatalf("code %d, stderr %q", code, errOut.String())
	}
}

func TestNewClientRejectsABadMaster(t *testing.T) {
	if _, err := newClient(connection{master: "http://hel1", stateDir: defaultStateDir}); err == nil {
		t.Fatal("an http master was accepted")
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	for name, args := range map[string][]string{
		"nothing":               {},
		"group only":            {"hosts"},
		"unknown group":         {"volumes", "list"},
		"unknown hosts command": {"hosts", "frob"},
		"token without host":    {"hosts", "token"},
		"positional host":       {"hosts", "token", "jp1"},
		"list with host":        {"hosts", "list", "--host", "jp1"},
		"remove with ttl":       {"hosts", "remove", "--host", "jp1", "--ttl", "1h"},
		"stacks without host":   {"stacks", "list"},
		"show without name":     {"stacks", "show", "--host", "jp1"},
		"two names":             {"stacks", "up", "--host", "jp1", "a", "b"},
		"file on up":            {"stacks", "up", "--host", "jp1", "--file", "x", "a"},
		"create without file":   {"stacks", "create", "--host", "jp1", "a"},
		"expect on create":      {"stacks", "create", "--host", "jp1", "--file", "x", "--expect-version", "2", "a"},
		"env on up":             {"stacks", "up", "--host", "jp1", "--env-file", "y", "a"},
		"unknown flag":          {"stacks", "list", "--host", "jp1", "--bogus"},
		"bad global flag":       {"--nope", "hosts", "list"},
	} {
		f := &fakeClient{}
		r := invoke(t, f, nil, args...)
		if r.code != exitUsage {
			t.Errorf("%s: code %d, want %d (stderr %q)", name, r.code, exitUsage, r.err)
		}
		if len(f.calls) != 0 {
			t.Errorf("%s: called the master: %v", name, f.calls)
		}
	}
}

func TestFlagsAfterTheName(t *testing.T) {
	f := &fakeClient{stack: protocol.Stack{Name: "web", ComposeYAML: "services: {}"}}
	r := invoke(t, f, nil, "stacks", "show", "web", "--host", "jp1")
	if r.code != exitOK || !reflect.DeepEqual(f.calls, []string{"stack jp1 web"}) {
		t.Fatalf("code %d calls %v stderr %q", r.code, f.calls, r.err)
	}
	if !strings.HasSuffix(r.out, "services: {}\n") {
		t.Fatalf("output %q", r.out)
	}
}

func TestHostsCommands(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	f := &fakeClient{
		list: admin.ListResponse{
			Hosts:  []enrollment.HostStatus{{HostID: "jp1", Online: true, CurrentNotAfter: now, EnrolledAt: now}},
			Tokens: []enrollment.TokenInfo{{HostID: "jp2", ExpiresAt: now}},
		},
		token: admin.TokenResponse{Token: "tok-123", ExpiresAt: now},
	}
	r := invoke(t, f, nil, "hosts", "list")
	if r.code != exitOK || !strings.Contains(r.out, "jp1") || !strings.Contains(r.out, "TOKEN FOR") || !strings.Contains(r.out, "jp2") {
		t.Fatalf("list: %+v", r)
	}
	r = invoke(t, f, nil, "hosts", "token", "--host", "jp2", "--ttl", "30m")
	if r.code != exitOK || r.out != "tok-123\n" || !strings.Contains(r.err, "expires") {
		t.Fatalf("token: %+v", r)
	}
	if f.calls[len(f.calls)-1] != "token jp2 30m0s" {
		t.Fatalf("calls %v", f.calls)
	}
	r = invoke(t, f, nil, "hosts", "remove", "--host", "jp2")
	if r.code != exitOK || f.calls[len(f.calls)-1] != "remove jp2" {
		t.Fatalf("remove: %+v %v", r, f.calls)
	}
}

func TestJSONOutput(t *testing.T) {
	f := &fakeClient{stacks: []protocol.Stack{{Name: "web", Project: "jp1-web", Status: "running", Version: 3}}}
	r := invoke(t, f, nil, "--json", "stacks", "list", "--host", "jp1")
	if r.code != exitOK || !strings.Contains(r.out, `"project": "jp1-web"`) {
		t.Fatalf("%+v", r)
	}
	f = &fakeClient{}
	r = invoke(t, f, nil, "--json", "stacks", "list", "--host", "jp1")
	if strings.TrimSpace(r.out) != "[]" {
		t.Fatalf("empty list: %q", r.out)
	}
}

func TestServerErrorExitsOne(t *testing.T) {
	f := &fakeClient{err: &admin.Error{Code: "failed", Message: "host jp1 is offline"}}
	r := invoke(t, f, nil, "stacks", "list", "--host", "jp1")
	if r.code != exitFailed || !strings.Contains(r.err, "host jp1 is offline") {
		t.Fatalf("%+v", r)
	}
}

func TestStackOperation(t *testing.T) {
	f := &fakeClient{operation: protocol.StackOperation{Name: "web", Operation: "up", Output: "started\n", Success: true}}
	r := invoke(t, f, nil, "stacks", "up", "--host", "jp1", "web")
	if r.code != exitOK || r.out != "started\n" || f.calls[0] != "up jp1 web" {
		t.Fatalf("%+v %v", r, f.calls)
	}
	// A failed Compose run prints its output and exits 1.
	f = &fakeClient{operation: protocol.StackOperation{Name: "web", Operation: "down", Output: "Error: no such network", Success: false}}
	r = invoke(t, f, nil, "stacks", "down", "--host", "jp1", "web")
	if r.code != exitFailed || r.out != "Error: no such network\n" || !strings.Contains(r.err, "failed") {
		t.Fatalf("%+v", r)
	}
	r = invoke(t, f, nil, "--json", "stacks", "restart", "--host", "jp1", "web")
	if r.code != exitFailed || !strings.Contains(r.out, `"success": false`) {
		t.Fatalf("json failure: %+v", r)
	}
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCreateSendsComposeAndEnvironment(t *testing.T) {
	compose := writeFile(t, "compose.yaml", "services:\n  web:\n    image: docker.io/nginx\n")
	envFile := writeFile(t, "web.env", "# comment\n\nDB_PASSWORD=s3cr=t value\nEMPTY=\n")
	f := &fakeClient{}
	r := invoke(t, f, nil, "stacks", "create", "--host", "jp1", "--file", compose, "--env-file", envFile, "web")
	if r.code != exitOK {
		t.Fatalf("%+v", r)
	}
	want := map[string]string{"DB_PASSWORD": "s3cr=t value", "EMPTY": ""}
	if f.saved == nil || f.saved.ComposeYAML != "services:\n  web:\n    image: docker.io/nginx\n" || !reflect.DeepEqual(f.saved.Environment, want) || f.saved.ExpectedVersion != nil {
		t.Fatalf("saved %+v", f.saved)
	}
	if strings.Contains(r.out+r.err, "s3cr") {
		t.Fatal("an environment value was printed")
	}
}

func TestBadEnvFileNeverPrintsValues(t *testing.T) {
	compose := writeFile(t, "compose.yaml", "services: {}\n")
	for name, content := range map[string]string{
		"no equals":    "hunter2-secret\n",
		"bad key":      "1BAD=hunter2-secret\n",
		"twice":        "A=hunter2-secret\nA=hunter2-secret\n",
		"json number":  `{"A": 12345}`,
		"json bad key": `{"bad key": "hunter2-secret"}`,
		"json broken":  `{"A": "hunter2-secret"`,
	} {
		f := &fakeClient{}
		r := invoke(t, f, nil, "stacks", "create", "--host", "jp1", "--file", compose, "--env-file", writeFile(t, "e", content), "web")
		if r.code != exitFailed || f.saved != nil {
			t.Errorf("%s: code %d saved %v", name, r.code, f.saved)
		}
		if strings.Contains(r.out+r.err, "hunter2") || strings.Contains(r.out+r.err, "12345") {
			t.Errorf("%s: printed a value: %q", name, r.err)
		}
	}
}

func TestUpdateUsesTheCurrentVersion(t *testing.T) {
	compose := writeFile(t, "compose.yaml", "services: {}\n")
	f := &fakeClient{stack: protocol.Stack{Name: "web", Version: 4}}
	r := invoke(t, f, nil, "stacks", "update", "--host", "jp1", "--file", compose, "web")
	if r.code != exitOK || !reflect.DeepEqual(f.calls, []string{"stack jp1 web", "save jp1 web"}) || *f.saved.ExpectedVersion != 4 {
		t.Fatalf("%+v %v", r, f.calls)
	}
	if !strings.Contains(r.out, "version 5") {
		t.Fatalf("output %q", r.out)
	}
	f = &fakeClient{}
	r = invoke(t, f, nil, "stacks", "update", "--host", "jp1", "--file", compose, "--expect-version", "7", "web")
	if r.code != exitOK || !reflect.DeepEqual(f.calls, []string{"save jp1 web"}) || *f.saved.ExpectedVersion != 7 {
		t.Fatalf("%+v %v", r, f.calls)
	}
}

func TestParseEnvFile(t *testing.T) {
	env, err := parseEnvFile([]byte(`  {"A": "1", "B_2": "x=y"}`))
	if err != nil || !reflect.DeepEqual(env, map[string]string{"A": "1", "B_2": "x=y"}) {
		t.Fatalf("json: %v %v", env, err)
	}
	env, err = parseEnvFile([]byte("A=1\r\n  # indented comment\nB=\"quoted\"\n"))
	if err != nil || !reflect.DeepEqual(env, map[string]string{"A": "1", "B": `"quoted"`}) {
		t.Fatalf("lines: %v %v", env, err)
	}
	var many strings.Builder
	for i := 0; i <= maxEnvironment; i++ {
		many.WriteString("K")
		many.WriteString(strings.Repeat("A", i+1))
		many.WriteString("=v\n")
	}
	if _, err := parseEnvFile([]byte(many.String())); err == nil {
		t.Fatal("more than the agent's limit was accepted")
	}
	if env, err := parseEnvFile(nil); err != nil || len(env) != 0 {
		t.Fatalf("empty: %v %v", env, err)
	}
}
func TestUpdateReplacesOrKeepsTheEnvironment(t *testing.T) {
	compose := writeFile(t, "compose.yaml", "services: {}\n")
	envFile := writeFile(t, "web.env", "MODE=b\n")
	f := &fakeClient{stack: protocol.Stack{Name: "web", Version: 4}}
	r := invoke(t, f, nil, "stacks", "update", "--host", "jp1", "--file", compose, "--env-file", envFile, "web")
	if r.code != exitOK || !reflect.DeepEqual(f.saved.Environment, map[string]string{"MODE": "b"}) {
		t.Fatalf("%+v %+v", r, f.saved)
	}
	r = invoke(t, f, nil, "stacks", "update", "--host", "jp1", "--file", compose, "web")
	if r.code != exitOK || f.saved.Environment != nil {
		t.Fatalf("without --env-file the environment must be kept: %+v", f.saved)
	}
}

func TestSavePrintsTheUpItRan(t *testing.T) {
	compose := writeFile(t, "compose.yaml", "services: {}\n")
	envFile := writeFile(t, "web.env", "MODE=b\n")
	f := &fakeClient{stack: protocol.Stack{Name: "web", Version: 4}, applied: &protocol.StackOperation{Name: "web", Operation: "up", Success: true, Output: "recreated web_1"}}
	r := invoke(t, f, nil, "stacks", "update", "--host", "jp1", "--file", compose, "--env-file", envFile, "web")
	if r.code != exitOK || !strings.Contains(r.out, "version 5") || !strings.Contains(r.out, "recreated web_1") {
		t.Fatalf("%+v", r)
	}
	f.applied = &protocol.StackOperation{Name: "web", Operation: "up", Success: false, Output: "Error: pull failed"}
	r = invoke(t, f, nil, "stacks", "update", "--host", "jp1", "--file", compose, "--env-file", envFile, "web")
	if r.code != exitFailed || !strings.Contains(r.out, "pull failed") || !strings.Contains(r.err, "saved, but") {
		t.Fatalf("failed up: %+v", r)
	}
	r = invoke(t, f, nil, "--json", "stacks", "update", "--host", "jp1", "--file", compose, "--env-file", envFile, "web")
	if r.code != exitFailed || !strings.Contains(r.out, `"operation"`) {
		t.Fatalf("json: %+v", r)
	}
}

func TestShowPrintsTheEnvironment(t *testing.T) {
	f := &fakeClient{stack: protocol.Stack{Name: "web", ComposeYAML: "services: {}\n", Environment: map[string]string{"TZ": "UTC", "A": "x=y"}}}
	r := invoke(t, f, nil, "stacks", "show", "--host", "jp1", "web")
	if r.code != exitOK || !strings.HasSuffix(r.out, "\nenvironment:\nA=x=y\nTZ=UTC\n") {
		t.Fatalf("output %q", r.out)
	}
}
