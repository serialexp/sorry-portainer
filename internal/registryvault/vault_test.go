package registryvault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"golang.org/x/sys/unix"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/registries"
)

func testDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sorry-portainer")
	return dir
}

func open(t *testing.T, dir string) (*Vault, *[]string) {
	t.Helper()
	var logged []string
	v, err := Open(dir, "local-a", Options{AllowNonTmpfs: true, Logf: func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}})
	if err != nil {
		t.Fatal(err)
	}
	return v, &logged
}

func set(id string, logins ...protocol.RegistryLogin) protocol.RegistrySync {
	return protocol.RegistrySync{SetID: id, Logins: logins}
}

func login(key, username, password string) protocol.RegistryLogin {
	return protocol.RegistryLogin{Key: key, Username: username, Password: []byte(password)}
}

func readAuthFile(t *testing.T, path string) authFile {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s has mode %04o, want 0600", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file authFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestOpenCreatesThePrivateDirectoryAndAnEmptyFile(t *testing.T) {
	dir := testDir(t)
	v, _ := open(t, dir)
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory: %v, %v", info, err)
	}
	if v.Path() != filepath.Join(dir, "local-a-auth.json") {
		t.Fatalf("path %s", v.Path())
	}
	file := readAuthFile(t, v.Path())
	if len(file.Auths) != 0 || v.SetID() != "" {
		t.Fatalf("new vault is not empty: %+v, %q", file, v.SetID())
	}
}

func TestSyncWritesPodmansFormatAndSignsInTheCheck(t *testing.T) {
	v, _ := open(t, testDir(t))
	committed := 0
	v.OnCommit(func() { committed++ })
	answer, err := v.Sync(set("set-1", login("docker.io", "hub", "hub-token"), login("ghcr.io/myorg", "bot", "gh-token")))
	if err != nil || answer.SetID != "set-1" || committed != 1 {
		t.Fatalf("sync: %+v, %v, committed %d", answer, err, committed)
	}
	file := readAuthFile(t, v.Path())
	if file.Meta == nil || file.Meta.SetID != "set-1" {
		t.Fatalf("meta: %+v", file.Meta)
	}
	if got := file.Auths["ghcr.io/myorg"].Auth; got != registries.BasicAuth("bot", []byte("gh-token")) {
		t.Fatalf("ghcr auth %q", got)
	}
	if v.LoginFor("ghcr.io/myorg/app:1") != "ghcr.io/myorg" || v.LoginFor("alpine") != "docker.io" || v.LoginFor("quay.io/x/y") != "" {
		t.Fatal("LoginFor does not match like Podman")
	}

	resolve := func(ref string) authn.AuthConfig {
		t.Helper()
		parsed, err := name.ParseReference(ref)
		if err != nil {
			t.Fatal(err)
		}
		auth, err := v.Resolve(parsed.Context())
		if err != nil {
			t.Fatal(err)
		}
		config, err := auth.Authorization()
		if err != nil {
			t.Fatal(err)
		}
		return *config
	}
	if got := resolve("ghcr.io/myorg/app:1"); got.Username != "bot" || got.Password != "gh-token" {
		t.Fatalf("ghcr resolved to %+v", got)
	}
	if got := resolve("myorg/app"); got.Username != "hub" {
		t.Fatalf("docker hub resolved to %+v", got)
	}
	if got := resolve("quay.io/x/y"); got.Username != "" || got.Password != "" {
		t.Fatalf("unmatched image resolved to %+v", got)
	}
}

func TestARestartedAgentReadsItsFileBack(t *testing.T) {
	dir := testDir(t)
	first, _ := open(t, dir)
	if _, err := first.Sync(set("set-7", login("ghcr.io", "bot", "token"))); err != nil {
		t.Fatal(err)
	}
	second, logged := open(t, dir)
	if second.SetID() != "set-7" || !second.Has("ghcr.io") {
		t.Fatalf("read back %q, has %v", second.SetID(), second.Has("ghcr.io"))
	}
	if len(*logged) != 1 || !strings.HasPrefix((*logged)[0], "registry logins: read set set-7 from ") || strings.Contains((*logged)[0], "token\"") {
		t.Fatalf("logged %v, want only the set read back", *logged)
	}
}

func TestACorruptFileIsReplacedByAnEmptySet(t *testing.T) {
	dir := testDir(t)
	first, _ := open(t, dir)
	for _, content := range []string{"not json", `{"auths":{"ghcr.io":{"auth":"!!"}},"sorry-portainer":{"set_id":"x"}}`, `{"auths":{"GHCR.io":{"auth":"dTpw"}},"sorry-portainer":{"set_id":"x"}}`, `{"auths":{"ghcr.io":{"auth":"dTpw"}}}`} {
		if err := os.WriteFile(first.Path(), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		v, logged := open(t, dir)
		if v.SetID() != "" || v.Has("ghcr.io") {
			t.Fatalf("%q: kept %q", content, v.SetID())
		}
		if len(*logged) != 1 || strings.Contains((*logged)[0], "dTpw") {
			t.Fatalf("%q: logged %v", content, *logged)
		}
		if file := readAuthFile(t, v.Path()); len(file.Auths) != 0 {
			t.Fatalf("%q: file not emptied: %+v", content, file)
		}
	}
}

func TestAFailedWriteKeepsThePreviousSet(t *testing.T) {
	dir := testDir(t)
	v, _ := open(t, dir)
	if _, err := v.Sync(set("set-1", login("ghcr.io", "bot", "old"))); err != nil {
		t.Fatal(err)
	}
	committed := 0
	v.OnCommit(func() { committed++ })
	// A directory that fails the checks stands in for any write failure.
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Sync(set("set-2", login("quay.io", "bot", "new"))); err == nil {
		t.Fatal("sync into a group-readable directory succeeded")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if v.SetID() != "set-1" || !v.Has("ghcr.io") || v.Has("quay.io") || committed != 0 {
		t.Fatalf("memory changed: %q, committed %d", v.SetID(), committed)
	}
	if file := readAuthFile(t, v.Path()); file.Meta.SetID != "set-1" {
		t.Fatalf("file changed: %+v", file.Meta)
	}
}

func TestSyncRefusesAnInvalidSet(t *testing.T) {
	v, _ := open(t, testDir(t))
	for _, bad := range []protocol.RegistrySync{
		set("", login("ghcr.io", "u", "p")),
		set("s", login("GHCR.io", "u", "p")),
		set("s", login("ghcr.io", "a:b", "p")),
	} {
		if _, err := v.Sync(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestOpenRefusesAnUnsafeDirectory(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link, "local-a", Options{AllowNonTmpfs: true}); err == nil {
		t.Fatal("opened a symlinked directory")
	}
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(open, "local-a", Options{AllowNonTmpfs: true}); err == nil {
		t.Fatal("opened a world-readable directory")
	}
	if _, err := Open(real, "../x", Options{AllowNonTmpfs: true}); err == nil {
		t.Fatal("accepted a host ID with a path in it")
	}
}

func TestOpenRefusesADirectoryOnDisk(t *testing.T) {
	base := t.TempDir()
	var fs unix.Statfs_t
	if err := unix.Statfs(base, &fs); err != nil {
		t.Fatal(err)
	}
	if fs.Type == unix.TMPFS_MAGIC {
		t.Skip("the test directory is on a tmpfs")
	}
	_, err := Open(filepath.Join(base, "sorry-portainer"), "local-a", Options{})
	if err == nil || !strings.Contains(err.Error(), "not on a tmpfs") {
		t.Fatalf("Open on disk: %v", err)
	}
}

func TestOpenAcceptsTheRuntimeTmpfs(t *testing.T) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	var fs unix.Statfs_t
	if runtime == "" || unix.Statfs(runtime, &fs) != nil || fs.Type != unix.TMPFS_MAGIC {
		t.Skip("no tmpfs runtime directory")
	}
	dir, err := os.MkdirTemp(runtime, "sp-vault-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if _, err := Open(filepath.Join(dir, "sorry-portainer"), "local-a", Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestSignInTestUsesAThrowawayFileAndOwnText(t *testing.T) {
	v, _ := open(t, testDir(t))
	var seenFile string
	run := func(output string, err error) LoginRunner {
		return func(ctx context.Context, authFile, key, username string, password []byte) (string, error) {
			seenFile = authFile
			if filepath.Dir(authFile) != filepath.Dir(v.Path()) {
				t.Errorf("auth file %s outside the vault", authFile)
			}
			if _, statErr := os.Stat(authFile); statErr != nil {
				t.Errorf("auth file missing during the run: %v", statErr)
			}
			if key != "ghcr.io/myorg" || username != "bot" || string(password) != "tok-secret" {
				t.Errorf("ran with %q %q %q", key, username, password)
			}
			return output, err
		}
	}
	target := login("ghcr.io/myorg", "bot", "tok-secret")
	failed := errors.New("exit status 125")
	cases := []struct {
		output  string
		err     error
		want    protocol.RegistryTestResult
		message string
	}{
		{"Login Succeeded!", nil, protocol.RegistryTestResult{OK: true}, ""},
		{"Error: logging into \"ghcr.io\": invalid username/password tok-secret", failed, protocol.RegistryTestResult{Failure: protocol.RegistryTestDenied}, "ghcr.io refused the login"},
		{"Error: x509: certificate signed by unknown authority", failed, protocol.RegistryTestResult{Failure: protocol.RegistryTestTLS}, "couldn’t verify the TLS certificate of ghcr.io"},
		{"Error: dial tcp: lookup ghcr.io: no such host", failed, protocol.RegistryTestResult{Failure: protocol.RegistryTestUnreachable}, "couldn’t reach ghcr.io"},
	}
	for _, c := range cases {
		got, err := v.Test(context.Background(), target, run(c.output, c.err))
		if err != nil {
			t.Fatal(err)
		}
		if got.OK != c.want.OK || got.Failure != c.want.Failure || got.Message != c.message {
			t.Errorf("%q: got %+v", c.output, got)
		}
		if strings.Contains(got.Message, "tok-secret") {
			t.Errorf("message holds the password: %q", got.Message)
		}
		if _, err := os.Stat(seenFile); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("throwaway auth file left behind: %v", err)
		}
	}
	if _, err := v.Test(context.Background(), login("GHCR.io", "bot", "x"), run("", nil)); err == nil {
		t.Error("tested an invalid login")
	}
}

func TestDockerHubUsernamesAreLowercasedForPodmanAndTheCheck(t *testing.T) {
	v, logged := open(t, testDir(t))
	request := set("set-1", login("docker.io", "Aeolun", "hub-token"), login("ghcr.io/myorg", "MyBot", "gh-token"))
	if _, err := v.Sync(request); err != nil {
		t.Fatal(err)
	}
	file := readAuthFile(t, v.Path())
	if got := file.Auths["docker.io"].Auth; got != registries.BasicAuth("aeolun", []byte("hub-token")) {
		t.Fatalf("docker hub auth %q, want the lowercase username", got)
	}
	if got := file.Auths["ghcr.io/myorg"].Auth; got != registries.BasicAuth("MyBot", []byte("gh-token")) {
		t.Fatalf("ghcr auth %q, want the username as given", got)
	}
	if got := v.Describe("docker.io"); got != `docker.io (user "aeolun", password 9 bytes)` {
		t.Fatalf("Describe = %q", got)
	}
	if got := v.Describe("quay.io"); got != "anonymous" {
		t.Fatalf("Describe of no login = %q", got)
	}
	last := (*logged)[len(*logged)-1]
	if !strings.Contains(last, "received set set-1") || !strings.Contains(last, `user "aeolun"`) || strings.Contains(last, "hub-token") {
		t.Fatalf("logged %q", last)
	}
}

// An agent that already holds a set with a capitalised Docker Hub username
// gets no new push (the set ID is unchanged), so it fixes its file itself.
func TestOpenRewritesAFileWithACapitalisedDockerHubUsername(t *testing.T) {
	dir := testDir(t)
	first, _ := open(t, dir)
	path := first.Path()
	legacy, err := json.Marshal(authFile{
		Auths: map[string]authEntry{"docker.io": {Auth: registries.BasicAuth("Aeolun", []byte("hub-token"))}},
		Meta:  &authMeta{SetID: "set-9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	second, logged := open(t, dir)
	if second.SetID() != "set-9" || second.Describe("docker.io") != `docker.io (user "aeolun", password 9 bytes)` {
		t.Fatalf("read back %q, %s", second.SetID(), second.Describe("docker.io"))
	}
	if got := readAuthFile(t, path).Auths["docker.io"].Auth; got != registries.BasicAuth("aeolun", []byte("hub-token")) {
		t.Fatalf("file not rewritten: %q", got)
	}
	if len(*logged) != 2 || !strings.Contains((*logged)[0], "rewrote") {
		t.Fatalf("logged %v", *logged)
	}
	// Once rewritten, a restart leaves it alone.
	_, again := open(t, dir)
	if len(*again) != 1 || strings.Contains((*again)[0], "rewrote") {
		t.Fatalf("second restart logged %v", *again)
	}
}

func TestSignInTestLowercasesDockerHubAndLogsWithoutThePassword(t *testing.T) {
	v, logged := open(t, testDir(t))
	var ranAs string
	refused := func(ctx context.Context, authFile, key, username string, password []byte) (string, error) {
		ranAs = username
		return "Error: logging into \"docker.io\": requesting bearer token: 400 (Bad Request) for " + string(password), errors.New("exit status 125")
	}
	got, err := v.Test(context.Background(), login("docker.io", "Aeolun", "hub-token"), refused)
	if err != nil {
		t.Fatal(err)
	}
	if ranAs != "aeolun" || got.Failure != protocol.RegistryTestDenied {
		t.Fatalf("ran as %q, got %+v", ranAs, got)
	}
	last := (*logged)[len(*logged)-1]
	if !strings.Contains(last, "failed (denied)") || !strings.Contains(last, "400 (Bad Request)") || !strings.Contains(last, registries.Mask) || strings.Contains(last, "hub-token") {
		t.Fatalf("logged %q", last)
	}
}
