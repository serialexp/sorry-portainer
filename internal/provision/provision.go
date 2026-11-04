// Package provision creates and resets a server's state directory.
package provision

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/serialexp/sorry-portainer/internal/atomicfile"
	"github.com/serialexp/sorry-portainer/internal/config"
	"github.com/serialexp/sorry-portainer/internal/enrollment"
	"github.com/serialexp/sorry-portainer/internal/pki"
)

// The state directory layout.
const (
	ConfigFile     = "server.json"
	LockFile       = "server.lock"
	RegistryFile   = "hosts.json"
	CAFile         = "control-ca.crt"
	CAKeyFile      = "control-ca.key"
	CredentialFile = "control-server.pem"
)

// legacyFiles are what provisioning before enrollment wrote; a reset retires
// them along with the current ones.
var legacyFiles = []string{"control-server.crt", "control-server.key"}

type InitOptions struct {
	StateDir, AdminPassword string
	// ServerNames are the DNS names and IP addresses agents reach the
	// control listener by; the server certificate covers all of them.
	ServerNames              []string
	ControlListen, WebListen string
	Now                      func() time.Time
}

// Init creates a new state directory: the control CA, an empty host
// registry and server.json. The server certificate is issued when the
// server starts.
func Init(o InitOptions) error {
	if o.StateDir == "" || o.AdminPassword == "" {
		return errors.New("state directory and admin password are required")
	}
	if len(o.AdminPassword) < 12 {
		return errors.New("admin password must be at least 12 characters")
	}
	if o.ControlListen == "" {
		o.ControlListen = ":9443"
	}
	if o.WebListen == "" {
		o.WebListen = ":8080"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	control := controlSection(o.StateDir, o.ControlListen, o.ServerNames)
	if err := config.ValidateControlFile(control); err != nil {
		return err
	}
	if err := os.MkdirAll(o.StateDir, 0o700); err != nil {
		return err
	}
	if err := checkPrivateDir(o.StateDir); err != nil {
		return err
	}
	lock, err := atomicfile.LockDir(o.StateDir, LockFile)
	if err != nil {
		return err
	}
	defer lock.Release()
	for _, name := range []string{ConfigFile, CAKeyFile, RegistryFile} {
		if _, err := os.Lstat(filepath.Join(o.StateDir, name)); err == nil {
			return fmt.Errorf("server state already initialized: %s exists", filepath.Join(o.StateDir, name))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := writePKI(o.StateDir, o.Now()); err != nil {
		return err
	}
	cfg := config.ServerFile{ListenAddr: o.WebListen, AdminPassword: o.AdminPassword, SessionTTL: "12h", Control: control}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(o.StateDir, ConfigFile), append(data, '\n'), 0o600)
}

type ResetOptions struct {
	StateDir string
	// ServerNames replace the configured names; they are required when
	// server.json has none, as before enrollment existed.
	ServerNames []string
	Now         func() time.Time
}

// ResetPKI replaces the control CA and the host registry with new, empty
// ones, moving the old files into pki-retired-<timestamp>/. The control
// section of server.json is rewritten; every other setting is kept. Every
// agent must enroll again afterwards. The server must be stopped: the reset
// takes the state lock.
func ResetPKI(o ResetOptions) (retired string, err error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if err := checkPrivateDir(o.StateDir); err != nil {
		return "", err
	}
	lock, err := atomicfile.LockDir(o.StateDir, LockFile)
	if err != nil {
		return "", err
	}
	defer lock.Release()
	configPath := filepath.Join(o.StateDir, ConfigFile)
	// Read loosely: the control section may be from an older version, and
	// everything else is carried over untouched.
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return "", fmt.Errorf("parse %s: %w", configPath, err)
	}
	var old struct {
		ListenAddr  string   `json:"listen_addr"`
		ServerNames []string `json:"server_names"`
	}
	if raw, ok := top["control"]; ok {
		if err := json.Unmarshal(raw, &old); err != nil {
			return "", fmt.Errorf("parse %s control section: %w", configPath, err)
		}
	}
	names := o.ServerNames
	if len(names) == 0 {
		names = old.ServerNames
	}
	if len(names) == 0 {
		return "", errors.New("server.json has no control server names; pass --server-name")
	}
	listen := old.ListenAddr
	if listen == "" {
		listen = ":9443"
	}
	control := controlSection(o.StateDir, listen, names)
	if err := config.ValidateControlFile(control); err != nil {
		return "", err
	}
	if top["control"], err = json.Marshal(control); err != nil {
		return "", err
	}
	updated, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return "", err
	}

	now := o.Now()
	retired = filepath.Join(o.StateDir, "pki-retired-"+now.UTC().Format("20060102T150405Z"))
	if err := os.Mkdir(retired, 0o700); err != nil {
		return "", err
	}
	for _, name := range append([]string{CAFile, CAKeyFile, CredentialFile, RegistryFile}, legacyFiles...) {
		err := os.Rename(filepath.Join(o.StateDir, name), filepath.Join(retired, name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	if err := atomicfile.SyncDir(retired); err != nil {
		return "", err
	}
	if err := atomicfile.SyncDir(o.StateDir); err != nil {
		return "", err
	}
	if err := writePKI(o.StateDir, now); err != nil {
		return "", err
	}
	if err := atomicfile.Write(configPath, append(updated, '\n'), 0o600); err != nil {
		return "", err
	}
	return retired, nil
}

func controlSection(stateDir, listen string, names []string) config.ControlTLSFile {
	return config.ControlTLSFile{
		ListenAddr:     listen,
		CAFile:         filepath.Join(stateDir, CAFile),
		CAKeyFile:      filepath.Join(stateDir, CAKeyFile),
		CredentialFile: filepath.Join(stateDir, CredentialFile),
		ServerNames:    names,
	}
}

func writePKI(stateDir string, now time.Time) error {
	ca, err := pki.NewCA(now)
	if err != nil {
		return err
	}
	if err := ca.WriteFiles(filepath.Join(stateDir, CAFile), filepath.Join(stateDir, CAKeyFile)); err != nil {
		return err
	}
	return enrollment.CreateRegistry(filepath.Join(stateDir, RegistryFile))
}

// checkPrivateDir refuses a state directory others can read or that belongs
// to another account: it holds the CA key.
func checkPrivateDir(dir string) error {
	st, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must not be group/world accessible (mode %04o)", dir, st.Mode().Perm())
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to uid %d, not to this account (uid %d)", dir, sys.Uid, os.Getuid())
	}
	return nil
}
