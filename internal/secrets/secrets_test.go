package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

func TestVaultReplaceReportsChangesAndZeroesOldValues(t *testing.T) {
	v := NewVault()
	first := map[string][]byte{"a": []byte("1"), "b": []byte("2")}
	oldA := first["a"]
	changed, removed, err := v.Replace("web", first)
	if err != nil || !reflect.DeepEqual(changed, []string{"a", "b"}) || removed != nil {
		t.Fatalf("first %v %v %v", changed, removed, err)
	}
	changed, removed, err = v.Replace("web", map[string][]byte{"a": []byte("9"), "c": []byte("3")})
	if err != nil || !reflect.DeepEqual(changed, []string{"a", "c"}) || !reflect.DeepEqual(removed, []string{"b"}) {
		t.Fatalf("second %v %v %v", changed, removed, err)
	}
	if !bytes.Equal(oldA, []byte{0}) {
		t.Fatalf("old value not zeroed: %q", oldA)
	}
	values, err := v.Copy("web", []string{"a", "c"})
	if err != nil || string(values["a"]) != "9" || string(values["c"]) != "3" {
		t.Fatalf("copy %q %v", values, err)
	}
	values["a"][0] = 'x'
	if again, _ := v.Copy("web", []string{"a"}); string(again["a"]) != "9" {
		t.Fatal("Copy returned the vault's own slice")
	}
	if _, err := v.Copy("web", []string{"a", "missing"}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("copy missing: %v", err)
	}
	if got := v.Missing("web", []string{"a", "b"}); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("missing %v", got)
	}
	if _, _, err := v.Replace("web", nil); err != nil || len(v.Names("web")) != 0 {
		t.Fatalf("forget: %v %v", err, v.Names("web"))
	}
}

func TestVaultRejectsInvalidSets(t *testing.T) {
	v := NewVault()
	big := map[string][]byte{}
	for i := range protocol.MaxSecretsPerStack + 1 {
		big["s"+strconv.Itoa(i)] = []byte("x")
	}
	for name, set := range map[string]map[string][]byte{
		"bad name":   {"../x": []byte("x")},
		"empty":      {"a": {}},
		"too big":    {"a": make([]byte, protocol.MaxSecretSize+1)},
		"too many":   big,
		"total size": totalTooBig(),
	} {
		if _, _, err := v.Replace("web", set); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, _, err := v.Replace("../web", map[string][]byte{"a": []byte("x")}); err == nil {
		t.Error("bad stack accepted")
	}
}

func totalTooBig() map[string][]byte {
	out := map[string][]byte{}
	for i := range protocol.MaxStackSecretsTotal/protocol.MaxSecretSize + 1 {
		out["s"+strconv.Itoa(i)] = make([]byte, protocol.MaxSecretSize)
	}
	return out
}

func TestMountsRoundTripAndValidation(t *testing.T) {
	mounts := []Mount{{Source: "db", Target: "db", Mode: 0o444}, {Source: "db", Target: "db2", UID: 5, GID: 6, Mode: 0o400}}
	encoded, err := EncodeMounts(mounts)
	if err != nil {
		t.Fatal(err)
	}
	if encoded != "db:db:0:0:0444;db:db2:5:6:0400" {
		t.Fatalf("encoded %q", encoded)
	}
	// Podman parses --annotation values as CSV.
	if strings.ContainsAny(encoded, `,"`) {
		t.Fatalf("encoding is not CSV-safe: %q", encoded)
	}
	decoded, err := DecodeMounts(encoded)
	if err != nil || !reflect.DeepEqual(decoded, mounts) {
		t.Fatalf("%+v %v", decoded, err)
	}
	if got := Sources(mounts); !reflect.DeepEqual(got, []string{"db"}) {
		t.Fatalf("sources %v", got)
	}
	for _, bad := range []string{
		``,
		`a:../x:0:0:0444`,
		`a:a:0:0:10000`,
		`a:a:-1:0:0444`,
		`a:a:+1:0:0444`,
		`a:a:0:0:0448`,
		`a:a:2147483648:0:0444`,
		`a:a:0:0`,
		`a:a:0:0:0444:x`,
		`a:a:0:0:0444;b:a:0:0:0444`,
		`a:a:0:0:0444;`,
		`[{"source":"a","target":"a","mode":292}]`,
	} {
		if _, err := DecodeMounts(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestSocketServerAnswersOwnUser(t *testing.T) {
	dir, err := os.MkdirTemp(os.Getenv("XDG_RUNTIME_DIR"), "sp-test-")
	if err != nil {
		dir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	path := filepath.Join(dir, "s", "h1.sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %04o", info.Mode().Perm())
	}
	vault := NewVault()
	if _, _, err := vault.Replace("web", map[string][]byte{"db": []byte("pw")}); err != nil {
		t.Fatal(err)
	}
	var logs []string
	var mu sync.Mutex
	server := &SocketServer{HostID: "h1", Vault: vault, Logf: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, format)
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()

	values, err := request(ctx, path, hookRequest{HostID: "h1", ContainerID: "c", Stack: "web", Names: []string{"db"}})
	if err != nil || string(values["db"]) != "pw" {
		t.Fatalf("request %q %v", values, err)
	}
	for name, req := range map[string]hookRequest{
		"other host":  {HostID: "h2", Stack: "web", Names: []string{"db"}},
		"missing":     {HostID: "h1", Stack: "web", Names: []string{"nope"}},
		"bad stack":   {HostID: "h1", Stack: "../web", Names: []string{"db"}},
		"no names":    {HostID: "h1", Stack: "web"},
		"bad name":    {HostID: "h1", Stack: "web", Names: []string{"../db"}},
		"other stack": {HostID: "h1", Stack: "api", Names: []string{"db"}},
	} {
		if _, err := request(ctx, path, req); err == nil {
			t.Errorf("%s: answered", name)
		}
	}
	// Garbage does not crash the server.
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("not json\n"))
	_ = conn.Close()
	if _, err := request(ctx, path, hookRequest{HostID: "h1", ContainerID: "c", Stack: "web", Names: []string{"db"}}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, line := range logs {
		if strings.Contains(line, "pw") {
			t.Fatal("log contains a value")
		}
	}
	// A regular file where the socket goes is never removed.
	if err := os.WriteFile(filepath.Join(dir, "s", "file.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(filepath.Join(dir, "s", "file.sock")); err == nil {
		t.Fatal("replaced a regular file")
	}
}

func TestWriteHookConfig(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteHookConfig(dir, "/opt/sp/sorry-portainer-agent", "host.1", "/run/user/1/sp/host.1.sock")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "sorry-portainer-host.1.json" {
		t.Fatalf("path %s", path)
	}
	var config hookFile
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Hook.Path != "/opt/sp/sorry-portainer-agent" || config.Stages[0] != "createRuntime" || config.Hook.Timeout != HookTimeoutSeconds {
		t.Fatalf("config %+v", config)
	}
	if !reflect.DeepEqual(config.Hook.Args, []string{"sorry-portainer-agent", "oci-hook", "--host-id", "host.1", "--socket", "/run/user/1/sp/host.1.sock"}) {
		t.Fatalf("args %v", config.Hook.Args)
	}
	for key, value := range config.When.Annotations {
		if !regexp.MustCompile(key).MatchString(AnnotationAgent) || !regexp.MustCompile(value).MatchString("host.1") {
			t.Fatal("when does not match own annotation")
		}
		if regexp.MustCompile(value).MatchString("hostx1") || regexp.MustCompile(value).MatchString("host.10") {
			t.Fatal("when matches another host")
		}
	}
	if _, err := WriteHookConfig(dir, "relative", "h", "/s"); err == nil {
		t.Fatal("accepted relative executable")
	}
}

func TestSwapActive(t *testing.T) {
	header := "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n"
	if active, err := swapActive(strings.NewReader(header)); err != nil || active {
		t.Fatalf("header only: %v %v", active, err)
	}
	if active, err := swapActive(strings.NewReader(header + "/dev/dm-1  partition\t8388604\t0\t-2\n")); err != nil || !active {
		t.Fatalf("with swap: %v %v", active, err)
	}
}

func TestMapID(t *testing.T) {
	ranges := []idRange{{inside: 0, outside: 1000, length: 1}, {inside: 1, outside: 100000, length: 65536}}
	for id, want := range map[int]int{0: 1000, 1: 100000, 1000: 100999} {
		if got, err := mapID(ranges, id); err != nil || got != want {
			t.Errorf("%d: %d %v", id, got, err)
		}
	}
	if _, err := mapID(ranges, 70000); err == nil {
		t.Error("unmapped id accepted")
	}
}

// fakeProc builds a stand-in for /proc: <pid>/root points at root, and
// self/root at the real root, so the container looks already pivoted.
func fakeProc(t *testing.T, root string, pid int) string {
	t.Helper()
	proc := t.TempDir()
	for _, dir := range []string{filepath.Join(proc, strconv.Itoa(pid), "ns"), filepath.Join(proc, "self", "ns")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	userns := filepath.Join(proc, "userns")
	if err := os.WriteFile(userns, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		filepath.Join(proc, strconv.Itoa(pid), "root"):       root,
		filepath.Join(proc, "self", "root"):                  "/",
		filepath.Join(proc, strconv.Itoa(pid), "ns", "user"): userns,
		filepath.Join(proc, "self", "ns", "user"):            userns,
	}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	return proc
}

func TestOpenSecretsDirRefusesUnsafeTargets(t *testing.T) {
	// Not a tmpfs: the directory is on the test's temp filesystem.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "run", "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	var st unix.Statfs_t
	if err := unix.Statfs(root, &st); err == nil && st.Type == tmpfsMagic {
		t.Skip("temp dir is on tmpfs; the disk case cannot be shown here")
	}
	proc := fakeProc(t, root, 7)
	if _, err := openSecretsDir(proc, 7, "", unix.O_PATH); err == nil || !strings.Contains(err.Error(), "not a tmpfs") {
		t.Fatalf("disk dir: %v", err)
	}
	// A symlink in the container is never followed.
	linked := t.TempDir()
	if err := os.Mkdir(filepath.Join(linked, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(os.Getenv("XDG_RUNTIME_DIR"), filepath.Join(linked, "run", "secrets")); err != nil {
		t.Fatal(err)
	}
	proc = fakeProc(t, linked, 8)
	if _, err := openSecretsDir(proc, 8, "", unix.O_PATH); err == nil || !errors.Is(err, unix.ELOOP) {
		t.Fatalf("symlink: %v", err)
	}
	// The process root being our own root means "not pivoted": refused.
	proc = fakeProc(t, "/", 9)
	if _, err := openSecretsDir(proc, 9, "", unix.O_PATH); err == nil || !strings.Contains(err.Error(), "host's /run/secrets") {
		t.Fatalf("own root: %v", err)
	}
}

const usernsChildEnv = "SORRY_PORTAINER_USERNS_CHILD"

// inUserNamespace re-runs the calling test as root of a new user and mount
// namespace, where it may mount tmpfs. It returns true in the child.
func inUserNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getenv(usernsChildEnv) == t.Name() {
		return true
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare not available")
	}
	probe := exec.Command(unshare, "-rm", "true")
	if err := probe.Run(); err != nil {
		t.Skipf("unprivileged user namespaces unavailable: %v", err)
	}
	cmd := exec.Command(unshare, "-rm", os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), usernsChildEnv+"="+t.Name())
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	return false
}

func mountTmpfs(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", path, "tmpfs", 0, "size=2m,mode=0755"); err != nil {
		t.Fatal(err)
	}
	// Runs before the TempDir cleanup, which cannot remove a mountpoint.
	t.Cleanup(func() { _ = unix.Unmount(path, unix.MNT_DETACH) })
}

// TestHookInjectsIntoContainerTmpfs runs the whole hook: OCI state on stdin,
// bundle config, socket request to a live SocketServer, and the write.
func TestHookInjectsIntoContainerTmpfs(t *testing.T) {
	if !inUserNamespace(t) {
		return
	}
	base := t.TempDir()
	// Before pivot: the container's mount namespace root is the host root and
	// the rootfs is a path under it. Here the fake process root is "host"
	// and the rootfs "host/rootfs".
	host := filepath.Join(base, "host")
	rootfs := filepath.Join(host, "var", "lib", "rootfs")
	mountTmpfs(t, filepath.Join(rootfs, "run", "secrets"))
	proc := fakeProc(t, host, 42)
	bundle := filepath.Join(base, "bundle")
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	mounts := []Mount{{Source: "db", Target: "db", Mode: 0o444}, {Source: "db", Target: "copy", Mode: 0o400}, {Source: "token", Target: "token", Mode: 0o440}}
	encoded, _ := EncodeMounts(mounts)
	config := map[string]any{
		"root":        map[string]string{"path": "/var/lib/rootfs"},
		"annotations": map[string]string{AnnotationAgent: "h1", AnnotationStack: "web", AnnotationSecrets: encoded},
	}
	data, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	socket := filepath.Join(base, "sock", "h1.sock")
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	vault := NewVault()
	if _, _, err := vault.Replace("web", map[string][]byte{"db": []byte("pw"), "token": []byte("tk")}); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	go func() {
		_ = (&SocketServer{HostID: "h1", Vault: vault, Logf: func(string, ...any) {}}).Serve(ctx, listener)
	}()

	state := `{"ociVersion":"1.0.2","id":"abc","status":"creating","pid":42,"bundle":"` + bundle + `"}`
	hookCtx, hookCancel := context.WithTimeout(ctx, 5*time.Second)
	defer hookCancel()
	if err := RunHook(hookCtx, strings.NewReader(state), HookOptions{HostID: "h1", Socket: socket, ProcRoot: proc}); err != nil {
		t.Fatal(err)
	}
	secretsDir := filepath.Join(rootfs, "run", "secrets")
	for target, want := range map[string]struct {
		value string
		mode  os.FileMode
	}{"db": {"pw", 0o444}, "copy": {"pw", 0o400}, "token": {"tk", 0o440}} {
		info, err := os.Stat(filepath.Join(secretsDir, target))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(secretsDir, target))
		if string(got) != want.value || info.Mode().Perm() != want.mode {
			t.Errorf("%s: %q %04o", target, got, info.Mode().Perm())
		}
	}

	// The agent's post-start check sees the files once the container has
	// pivoted, i.e. its process root is the rootfs.
	pivoted := fakeProc(t, rootfs, 43)
	if err := Verify(pivoted, 43, mounts); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := Verify(pivoted, 43, append(mounts, Mount{Source: "x", Target: "absent", Mode: 0o444})); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("verify missing: %v", err)
	}

	// A second run would overwrite nothing: files are created exclusively.
	if err := RunHook(hookCtx, strings.NewReader(state), HookOptions{HostID: "h1", Socket: socket, ProcRoot: proc}); err == nil {
		t.Fatal("hook overwrote existing files")
	}

	// A hook for another agent's container fails.
	if err := RunHook(hookCtx, strings.NewReader(state), HookOptions{HostID: "h2", Socket: socket, ProcRoot: proc}); err == nil {
		t.Fatal("hook served another agent's container")
	}

	// Missing values: fail closed, nothing written.
	emptyRootfs := filepath.Join(host, "var", "lib", "rootfs2")
	mountTmpfs(t, filepath.Join(emptyRootfs, "run", "secrets"))
	config["root"] = map[string]string{"path": "/var/lib/rootfs2"}
	config["annotations"].(map[string]string)[AnnotationStack] = "api"
	data, _ = json.Marshal(config)
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunHook(hookCtx, strings.NewReader(state), HookOptions{HostID: "h1", Socket: socket, ProcRoot: proc}); err == nil {
		t.Fatal("hook succeeded without values")
	}
	if entries, _ := os.ReadDir(filepath.Join(emptyRootfs, "run", "secrets")); len(entries) != 0 {
		t.Fatalf("wrote %d files without values", len(entries))
	}
}

// TestHookRefusesTmpfsThatIsNotItsOwnMount covers /run being a tmpfs with
// run/secrets a plain directory in it: still memory, but not the mount the
// rewrite asked for, so something else is wrong.
func TestHookRefusesTmpfsThatIsNotItsOwnMount(t *testing.T) {
	if !inUserNamespace(t) {
		return
	}
	root := t.TempDir()
	mountTmpfs(t, filepath.Join(root, "run"))
	if err := os.Mkdir(filepath.Join(root, "run", "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	proc := fakeProc(t, root, 5)
	if _, err := openSecretsDir(proc, 5, "", unix.O_PATH); err == nil || !strings.Contains(err.Error(), "not its own mount") {
		t.Fatalf("got %v", err)
	}
}
