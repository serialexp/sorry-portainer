package stacks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/serialexp/sorry-portainer/internal/podman"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/registryvault"
)

const (
	canaryUser     = "canary-user"
	canaryPassword = "Canary-9f3e1b77-pass"
)

// privateRegistry is an in-process registry on 127.0.0.1 that requires
// Basic auth, behind TLS with a CA of its own.
type privateRegistry struct {
	host   string
	client *http.Client
}

func startPrivateRegistry(t *testing.T) *privateRegistry {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "sorry-portainer test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(2),
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage: x509.KeyUsageDigitalSignature}, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	inner := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != canaryUser || password != canaryPassword {
			w.Header().Set("WWW-Authenticate", `Basic realm="sorry-portainer test"`)
			http.Error(w, `{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`, http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &privateRegistry{
		host:   strings.TrimPrefix(server.URL, "https://"),
		client: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}},
	}
}

// push copies a local Podman image into the registry under ref, with label
// value so each push is a different image.
func (r *privateRegistry) push(t *testing.T, ctx context.Context, local, ref, label string) {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "image.tar")
	if out, err := exec.CommandContext(ctx, "podman", "save", "--format", "docker-archive", "-o", archive, local).CombinedOutput(); err != nil {
		t.Fatalf("podman save: %v: %s", err, out)
	}
	tag, _ := name.NewTag(local)
	image, err := tarball.ImageFromPath(archive, &tag)
	if err != nil {
		t.Fatal(err)
	}
	config, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	labelled := config.Config.DeepCopy()
	if labelled.Labels == nil {
		labelled.Labels = map[string]string{}
	}
	labelled.Labels["sorry-portainer-test"] = label
	image, err = mutate.Config(image, *labelled)
	if err != nil {
		t.Fatal(err)
	}
	target, err := name.NewTag(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(target, image, remote.WithContext(ctx), remote.WithTransport(r.client.Transport),
		remote.WithAuth(&authn.Basic{Username: canaryUser, Password: canaryPassword})); err != nil {
		t.Fatal(err)
	}
}

// TestRealPodmanPrivateRegistry: with real Podman and podman-compose, a
// stack whose image is private waits for a login, a sign-in test tells a
// wrong password from a right one, the push of the login starts the stack,
// Update pulls a new version, podman-compose's own pull reads the agent's
// logins, and the password never lands outside tmpfs or in any output.
func TestRealPodmanPrivateRegistry(t *testing.T) {
	if os.Getenv("SORRY_PORTAINER_TEST_PODMAN") != "1" {
		t.Skip("set SORRY_PORTAINER_TEST_PODMAN=1 to run the rootless Podman integration test")
	}
	const base = "quay.io/libpod/busybox:latest"
	if output, err := exec.Command("podman", "image", "exists", base).CombinedOutput(); err != nil {
		t.Fatalf("image %s not present: %v: %s", base, err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	reg := startPrivateRegistry(t)

	// Rootless Podman reads certs.d from $HOME/.config only, and moving HOME
	// would move the image storage, so a private registries.conf marks just
	// the test registry as one whose certificate is not verified. The user's
	// own configuration is left alone; every reference here is qualified.
	config := t.TempDir()
	registriesConf := filepath.Join(config, "registries.conf")
	if err := os.WriteFile(registriesConf, []byte("[[registry]]\nlocation = \""+reg.host+"\"\ninsecure = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTAINERS_REGISTRIES_CONF", registriesConf)

	stamp := strings.ToLower(time.Now().Format("150405.000"))
	stamp = strings.ReplaceAll(stamp, ".", "")
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		t.Fatal("XDG_RUNTIME_DIR is not set")
	}
	vaultDir := filepath.Join(runtime, "sorry-portainer-test-"+stamp)
	t.Cleanup(func() { _ = os.RemoveAll(vaultDir) })
	var logMu sync.Mutex
	var logs strings.Builder
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logs.WriteString(strings.TrimSpace(fmt.Sprintf(format, args...)) + "\n")
	}
	vault, err := registryvault.Open(vaultDir, "local-test", registryvault.Options{Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("REGISTRY_AUTH_FILE", vault.Path())

	app := reg.host + "/private/app"
	reg.push(t, ctx, base, app+":1", "v1")
	prefix := "sorry-podman-registry-" + stamp + "-"
	manager := New(t.TempDir(), prefix, NewComposeExecutor("/usr/bin/podman-compose"))
	manager.Logf = logf
	client, err := podman.NewWithPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetBootID("boot-" + stamp)
	manager.EnableRuntimeStatus(client)
	manager.EnableImageChecks(client, &fakeChecker{})
	manager.EnableRegistryLogins(vault)
	vault.OnCommit(func() { manager.LoginsChanged(ctx) })
	removeImages := func() {
		for _, tag := range []string{app + ":1", app + ":2"} {
			_ = exec.Command("podman", "rmi", "--force", tag).Run()
		}
	}
	removeImages()
	t.Cleanup(removeImages)

	compose := "services:\n  app:\n    image: " + app + ":1\n    command: [\"sleep\", \"300\"]\n"
	if _, err := manager.Save(ctx, "priv", compose, nil, nil); err != nil {
		t.Fatal(err)
	}
	project := manager.project("priv")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if result, err := manager.Down(cleanupCtx, "priv"); err != nil {
			t.Errorf("clean up %s: %v: %s", project, err, result.Output)
		}
	})
	var outputs []string

	// 1. No login: the pull is refused and the stack waits for one.
	result, err := manager.Up(ctx, "priv")
	outputs = append(outputs, result.Output)
	if _, ok := err.(*missingLoginError); !ok {
		t.Fatalf("up without a login: %v\n%s", err, result.Output)
	}
	stack, err := manager.Inspect(ctx, "priv")
	if err != nil || stack.WaitingLogin != reg.host+"/private/app" || !strings.Contains(stack.Waiting, "waiting for registry login for "+reg.host) {
		t.Fatalf("stack waiting %q for %q, %v", stack.Waiting, stack.WaitingLogin, err)
	}

	// 2. The sign-in test, through real `podman login`.
	login := protocol.RegistryLogin{Key: reg.host + "/private", Username: canaryUser, Password: []byte("wrong-" + canaryPassword)}
	denied, err := vault.Test(ctx, login, nil)
	if err != nil || denied.OK || denied.Failure != protocol.RegistryTestDenied {
		t.Fatalf("test with a wrong password = %+v, %v", denied, err)
	}
	login.Password = []byte(canaryPassword)
	works, err := vault.Test(ctx, login, nil)
	if err != nil || !works.OK {
		t.Fatalf("test with the right password = %+v, %v", works, err)
	}
	outputs = append(outputs, denied.Message, works.Message)

	// 3. The login arrives: the stack starts in the background.
	if _, err := vault.Sync(protocol.RegistrySync{SetID: "set-1", Logins: []protocol.RegistryLogin{login}}); err != nil {
		t.Fatal(err)
	}
	running := func() bool {
		out, err := exec.CommandContext(ctx, "podman", "container", "inspect", "--format", "{{.State.Status}}", project+"_app_1").Output()
		return err == nil && strings.TrimSpace(string(out)) == "running"
	}
	deadline := time.Now().Add(2 * time.Minute)
	for !running() {
		if time.Now().After(deadline) {
			t.Fatalf("the stack did not start after its login arrived\n%s", logs.String())
		}
		time.Sleep(500 * time.Millisecond)
	}
	if stack, _ := manager.Inspect(ctx, "priv"); stack.Waiting != "" {
		t.Fatalf("still waiting: %q", stack.Waiting)
	}

	// 4. Update pulls the new version of the private tag.
	reg.push(t, ctx, base, app+":1", "v2")
	update, err := manager.Update(ctx, "priv", "app", nil)
	outputs = append(outputs, update.Output)
	if err != nil || !update.Success {
		t.Fatalf("update: %v\n%s", err, update.Output)
	}
	label, err := exec.CommandContext(ctx, "podman", "container", "inspect", "--format", `{{index .Config.Labels "sorry-portainer-test"}}`, project+"_app_1").Output()
	if err != nil || strings.TrimSpace(string(label)) != "v2" {
		t.Fatalf("after update the container runs %q, %v", label, err)
	}

	// 5. podman-compose passes REGISTRY_AUTH_FILE on: its own pull of an
	// image that is not local yet works.
	reg.push(t, ctx, base, app+":2", "v3")
	pullDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pullDir, "compose.yaml"), []byte("services:\n  app:\n    image: "+app+":2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := NewComposeExecutor("/usr/bin/podman-compose").Run(ctx, []string{"-p", prefix + "pull", "-f", "compose.yaml", "pull"}, pullDir, nil)
	outputs = append(outputs, out)
	if exists, existsErr := client.ImageExists(ctx, app+":2"); err != nil || existsErr != nil || !exists {
		t.Fatalf("podman-compose pull did not use the agent's logins: %v, %v\n%s", err, existsErr, out)
	}

	// 6. The password, and its base64 form, appear nowhere: not in output,
	// logs, the stack's files or the private config home. Only the tmpfs
	// auth file holds it.
	canaries := []string{canaryPassword, base64.StdEncoding.EncodeToString([]byte(canaryUser + ":" + canaryPassword))}
	outputs = append(outputs, logs.String())
	for _, text := range outputs {
		for _, canary := range canaries {
			if strings.Contains(text, canary) {
				t.Fatalf("the password is in output:\n%s", text)
			}
		}
	}
	for _, dir := range []string{manager.root, config, pullDir} {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			data, _ := os.ReadFile(path)
			for _, canary := range canaries {
				if strings.Contains(string(data), canary) {
					t.Errorf("%s holds the password", path)
				}
			}
			return nil
		})
	}
	authFile, _ := os.ReadFile(vault.Path())
	if !strings.Contains(string(authFile), canaries[1]) {
		t.Fatal("the tmpfs auth file does not hold the login")
	}
}
