package provision

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/atomicfile"
	"github.com/serialexp/sorry-portainer/internal/config"
	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/pki"
)

const password = "long-enough-admin-password"

func initialized(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "state")
	if err := Init(InitOptions{StateDir: d, AdminPassword: password, ServerNames: []string{"control.example", "10.0.0.1"}, ControlListen: ":7443"}); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestInit(t *testing.T) {
	d := initialized(t)
	st, err := os.Stat(d)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("state dir %v %v", st.Mode(), err)
	}
	for _, name := range []string{CAFile, CAKeyFile, RegistryFile, ConfigFile} {
		st, err := os.Stat(filepath.Join(d, name))
		if err != nil {
			t.Fatal(name, err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %04o", name, st.Mode().Perm())
		}
	}
	cfg, control, err := config.LoadServerFile(filepath.Join(d, ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StateDir != d || cfg.AdminPassword != password || cfg.ListenAddr != ":8080" || cfg.InsecureCookie {
		t.Fatalf("server config %+v", cfg)
	}
	want := config.ControlTLS{ListenAddr: ":7443", CAFile: filepath.Join(d, CAFile), CAKeyFile: filepath.Join(d, CAKeyFile),
		CredentialFile: filepath.Join(d, CredentialFile), ServerNames: []string{"control.example", "10.0.0.1"}}
	if !reflect.DeepEqual(control, want) {
		t.Fatalf("control %+v, want %+v", control, want)
	}
	if _, err := pki.LoadCA(control.CAFile, control.CAKeyFile); err != nil {
		t.Fatal(err)
	}
	registry, err := enrollment.OpenRegistry(filepath.Join(d, RegistryFile))
	if err != nil || len(registry.List()) != 0 {
		t.Fatalf("registry %v", err)
	}
}

func TestInitInsecureCookie(t *testing.T) {
	d := filepath.Join(t.TempDir(), "state")
	if err := Init(InitOptions{StateDir: d, AdminPassword: password, ServerNames: []string{"127.0.0.1"}, InsecureCookie: true}); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.LoadServerFile(filepath.Join(d, ConfigFile))
	if err != nil || !cfg.InsecureCookie {
		t.Fatalf("insecure cookie not configured: %+v %v", cfg, err)
	}
}

func TestInitRefusesBadInput(t *testing.T) {
	base := t.TempDir()
	cases := map[string]InitOptions{
		"short password":  {AdminPassword: "short", ServerNames: []string{"a.example"}},
		"no server name":  {AdminPassword: password},
		"bad server name": {AdminPassword: password, ServerNames: []string{"not a name"}},
		"port 443":        {AdminPassword: password, ServerNames: []string{"a.example"}, ControlListen: ":443"},
	}
	for name, o := range cases {
		o.StateDir = filepath.Join(base, strings.ReplaceAll(name, " ", "-"))
		if err := Init(o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestInitRefusesExistingOrOpenState(t *testing.T) {
	d := initialized(t)
	if err := Init(InitOptions{StateDir: d, AdminPassword: password, ServerNames: []string{"a.example"}}); err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("second init = %v", err)
	}
	open := t.TempDir()
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Init(InitOptions{StateDir: open, AdminPassword: password, ServerNames: []string{"a.example"}}); err == nil {
		t.Fatal("initialized a world-readable directory")
	}
}

func TestStateLockIsExclusive(t *testing.T) {
	d := initialized(t)
	lock, err := atomicfile.LockDir(d, LockFile)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := ResetPKI(ResetOptions{StateDir: d}); !errors.Is(err, atomicfile.ErrLocked) {
		t.Fatalf("reset of a running server's state = %v", err)
	}
}

func TestResetPKIReplacesCAAndKeepsSettings(t *testing.T) {
	d := initialized(t)
	oldCA, err := pki.LoadCA(filepath.Join(d, CAFile), filepath.Join(d, CAKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, CredentialFile), []byte("old credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 10, 30, 0, 0, time.UTC)
	retired, err := ResetPKI(ResetOptions{StateDir: d, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if retired != filepath.Join(d, "pki-retired-20261105T103000Z") {
		t.Fatalf("retired into %s", retired)
	}
	for _, name := range []string{CAFile, CAKeyFile, RegistryFile, CredentialFile} {
		if _, err := os.Stat(filepath.Join(retired, name)); err != nil {
			t.Fatalf("not retired: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(d, CredentialFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old server credential left in place")
	}
	newCA, err := pki.LoadCA(filepath.Join(d, CAFile), filepath.Join(d, CAKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if newCA.Fingerprint() == oldCA.Fingerprint() {
		t.Fatal("CA not replaced")
	}
	cfg, control, err := config.LoadServerFile(filepath.Join(d, ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminPassword != password || control.ListenAddr != ":7443" || !reflect.DeepEqual(control.ServerNames, []string{"control.example", "10.0.0.1"}) {
		t.Fatalf("settings lost: %+v %+v", cfg, control)
	}
	// A second reset in the same second cannot reuse the directory.
	if _, err := ResetPKI(ResetOptions{StateDir: d, Now: func() time.Time { return now }}); err == nil {
		t.Fatal("reset reused a retired directory")
	}
}

// TestResetPKIMigratesPreEnrollmentState: the development state from before
// enrollment has separate certificate files and no server names.
func TestResetPKIMigratesPreEnrollmentState(t *testing.T) {
	d := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{"listen_addr": "127.0.0.1:6200", "admin_password": password, "session_ttl": "12h",
		"control": map[string]string{"listen_addr": "127.0.0.1:6243", "ca_file": filepath.Join(d, CAFile),
			"cert_file": filepath.Join(d, "control-server.crt"), "key_file": filepath.Join(d, "control-server.key")}}
	data, _ := json.Marshal(legacy)
	for name, content := range map[string]string{ConfigFile: string(data), CAFile: "x", CAKeyFile: "x", "control-server.crt": "x", "control-server.key": "x"} {
		if err := os.WriteFile(filepath.Join(d, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ResetPKI(ResetOptions{StateDir: d}); err == nil || !strings.Contains(err.Error(), "--server-name") {
		t.Fatalf("reset without names = %v", err)
	}
	retired, err := ResetPKI(ResetOptions{StateDir: d, ServerNames: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range legacyFiles {
		if _, err := os.Stat(filepath.Join(retired, name)); err != nil {
			t.Fatalf("legacy file not retired: %v", err)
		}
	}
	cfg, control, err := config.LoadServerFile(filepath.Join(d, ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:6200" || control.ListenAddr != "127.0.0.1:6243" || !reflect.DeepEqual(control.ServerNames, []string{"127.0.0.1"}) {
		t.Fatalf("migrated %+v %+v", cfg, control)
	}
}
