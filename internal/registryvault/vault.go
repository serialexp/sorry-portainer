// Package registryvault is the agent's copy of the registry logins the
// master pushes: held in memory, and written as Podman's auth.json to a
// tmpfs directory so that no password reaches the host's disk. The same set
// signs in the image check (as an authn.Keychain) and every Podman command
// (through REGISTRY_AUTH_FILE). See docs/design/registry-logins.md.
package registryvault

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/authn"
	"golang.org/x/sys/unix"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/registries"
)

// maxFile bounds the auth file read back at start: the largest set is far
// smaller.
const maxFile = 1 << 20

// Options configures a Vault.
type Options struct {
	// AllowNonTmpfs skips the check that the directory is a tmpfs. Only
	// tests set it; an agent refuses to put passwords on a disk.
	AllowNonTmpfs bool
	// Logf logs problems (never passwords). Nil uses log.Printf.
	Logf func(format string, args ...any)
}

// Vault holds the agent's registry logins. It is safe for concurrent use.
type Vault struct {
	dir, file string
	options   Options

	// syncMu serializes syncs, so the file and memory change together.
	syncMu sync.Mutex

	mu       sync.RWMutex
	setID    string
	logins   map[string]protocol.RegistryLogin
	onCommit []func()
}

// DefaultDir is the agent's directory under the user's runtime directory,
// which systemd creates as a per-user tmpfs; the hook socket lives there too.
func DefaultDir() (string, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		return "", errors.New("XDG_RUNTIME_DIR is not set; registry logins need a tmpfs runtime directory")
	}
	return filepath.Join(runtime, "sorry-portainer"), nil
}

// Open prepares the vault in dir for hostID. The directory is created with
// mode 0700 if missing and must be a tmpfs owned by this user with mode 0700,
// or Open fails. A file left by an earlier run (the agent restarted without a
// reboot) is read back; one that is unreadable or invalid is replaced by an
// empty set. Afterwards the file always exists.
func Open(dir, hostID string, options Options) (*Vault, error) {
	if !protocol.ValidHostID(hostID) {
		return nil, fmt.Errorf("invalid host ID %q", hostID)
	}
	if options.Logf == nil {
		options.Logf = log.Printf
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	v := &Vault{dir: dir, file: hostID + "-auth.json", options: options, logins: map[string]protocol.RegistryLogin{}}
	dirFD, err := v.openDir()
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirFD)
	set, err := v.read(dirFD)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		v.options.Logf("registry logins: ignoring %s: %v", v.Path(), err)
	default:
		v.install(set)
		return v, nil
	}
	if err := v.write(dirFD, protocol.RegistrySync{}); err != nil {
		return nil, fmt.Errorf("write %s: %w", v.Path(), err)
	}
	return v, nil
}

// Path is the auth file Podman reads (REGISTRY_AUTH_FILE).
func (v *Vault) Path() string { return filepath.Join(v.dir, v.file) }

// OnCommit runs fn after every committed sync, outside the vault's locks.
func (v *Vault) OnCommit(fn func()) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.onCommit = append(v.onCommit, fn)
}

// SetID is the ID of the set held, empty for none.
func (v *Vault) SetID() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.setID
}

// Has reports whether a login for key is held.
func (v *Vault) Has(key string) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	_, ok := v.logins[key]
	return ok
}

// LoginFor is the key of the login Podman uses for the image ref, or "".
func (v *Vault) LoginFor(ref string) string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return registries.Match(ref, v.hasLocked)
}

func (v *Vault) hasLocked(key string) bool {
	_, ok := v.logins[key]
	return ok
}

// Resolve makes the vault an authn.Keychain for the image check. An image
// without a matching login is asked about anonymously.
func (v *Vault) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	key := registries.MatchName(resource.String(), v.hasLocked)
	if key == "" {
		return authn.Anonymous, nil
	}
	login := v.logins[key]
	return authn.FromConfig(authn.AuthConfig{Username: login.Username, Password: string(login.Password)}), nil
}

// Scrubber removes the held passwords from text.
func (v *Vault) Scrubber() *registries.Scrubber {
	v.mu.RLock()
	defer v.mu.RUnlock()
	logins := make([]protocol.RegistryLogin, 0, len(v.logins))
	for _, login := range v.logins {
		logins = append(logins, login)
	}
	return registries.NewScrubber(logins)
}

// Sync replaces the set. The file is written first; only when that worked
// does memory change, so the file, the image check and Podman never
// disagree. On failure the previous set stays, in memory and on file.
func (v *Vault) Sync(request protocol.RegistrySync) (protocol.RegistrySynced, error) {
	if request.SetID == "" {
		return protocol.RegistrySynced{}, errors.New("registry login set has no ID")
	}
	if err := registries.Check(request.Logins); err != nil {
		return protocol.RegistrySynced{}, err
	}
	v.syncMu.Lock()
	defer v.syncMu.Unlock()
	dirFD, err := v.openDir()
	if err != nil {
		return protocol.RegistrySynced{}, err
	}
	defer unix.Close(dirFD)
	if err := v.write(dirFD, request); err != nil {
		return protocol.RegistrySynced{}, fmt.Errorf("write %s: %w", v.Path(), err)
	}
	callbacks := v.install(request)
	for _, fn := range callbacks {
		fn()
	}
	return protocol.RegistrySynced{SetID: request.SetID}, nil
}

// install swaps the in-memory set and returns the commit callbacks.
func (v *Vault) install(set protocol.RegistrySync) []func() {
	logins := make(map[string]protocol.RegistryLogin, len(set.Logins))
	for _, login := range set.Logins {
		logins[login.Key] = login
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, old := range v.logins {
		clear(old.Password)
	}
	v.logins, v.setID = logins, set.SetID
	return append([]func(){}, v.onCommit...)
}

// openDir opens the vault's directory without following a symlink and checks,
// on that descriptor, that it is this user's, mode 0700, and a tmpfs.
func (v *Vault) openDir() (int, error) {
	fd, err := unix.Open(v.dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", v.dir, err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return -1, err
	}
	if stat.Uid != uint32(os.Getuid()) || stat.Mode&0o777 != 0o700 {
		unix.Close(fd)
		return -1, fmt.Errorf("%s must be owned by uid %d with mode 0700 (it is uid %d, mode %04o)", v.dir, os.Getuid(), stat.Uid, stat.Mode&0o777)
	}
	if !v.options.AllowNonTmpfs {
		var fs unix.Statfs_t
		if err := unix.Fstatfs(fd, &fs); err != nil {
			unix.Close(fd)
			return -1, err
		}
		if fs.Type != unix.TMPFS_MAGIC {
			unix.Close(fd)
			return -1, fmt.Errorf("%s is not on a tmpfs; registry passwords must not be written to disk", v.dir)
		}
	}
	return fd, nil
}

// authFile is Podman's containers-auth.json(5), plus the set ID in a member
// Podman ignores.
type authFile struct {
	Auths map[string]authEntry `json:"auths"`
	Meta  *authMeta            `json:"sorry-portainer,omitempty"`
}

type authEntry struct {
	Auth string `json:"auth"`
}

type authMeta struct {
	SetID string `json:"set_id"`
}

func encode(set protocol.RegistrySync) ([]byte, error) {
	file := authFile{Auths: make(map[string]authEntry, len(set.Logins))}
	if set.SetID != "" {
		file.Meta = &authMeta{SetID: set.SetID}
	}
	for _, login := range set.Logins {
		file.Auths[login.Key] = authEntry{Auth: registries.BasicAuth(login.Username, login.Password)}
	}
	return json.Marshal(file)
}

func decode(data []byte) (protocol.RegistrySync, error) {
	var file authFile
	if err := json.Unmarshal(data, &file); err != nil {
		return protocol.RegistrySync{}, err
	}
	set := protocol.RegistrySync{}
	if file.Meta != nil {
		set.SetID = file.Meta.SetID
	}
	keys := make([]string, 0, len(file.Auths))
	for key := range file.Auths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		plain, err := base64.StdEncoding.DecodeString(file.Auths[key].Auth)
		if err != nil {
			return protocol.RegistrySync{}, fmt.Errorf("login %q: %w", key, err)
		}
		username, password, ok := strings.Cut(string(plain), ":")
		clear(plain)
		if !ok {
			return protocol.RegistrySync{}, fmt.Errorf("login %q has no username", key)
		}
		set.Logins = append(set.Logins, protocol.RegistryLogin{Key: key, Username: username, Password: []byte(password)})
	}
	if len(set.Logins) > 0 && set.SetID == "" {
		return protocol.RegistrySync{}, errors.New("logins without a set ID")
	}
	if err := registries.Check(set.Logins); err != nil {
		return protocol.RegistrySync{}, err
	}
	return set, nil
}

func (v *Vault) read(dirFD int) (protocol.RegistrySync, error) {
	fd, err := unix.Openat(dirFD, v.file, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return protocol.RegistrySync{}, os.ErrNotExist
	}
	if err != nil {
		return protocol.RegistrySync{}, err
	}
	file := os.NewFile(uintptr(fd), v.Path())
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return protocol.RegistrySync{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFile {
		return protocol.RegistrySync{}, fmt.Errorf("not a regular file of at most %d bytes", maxFile)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFile+1))
	if err != nil {
		return protocol.RegistrySync{}, err
	}
	defer clear(data)
	return decode(data)
}

// write replaces the file atomically: a new 0600 file created next to it,
// synced, then renamed over it, all relative to the checked directory.
func (v *Vault) write(dirFD int, set protocol.RegistrySync) error {
	data, err := encode(set)
	if err != nil {
		return err
	}
	defer clear(data)
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temp := "." + v.file + "." + hex.EncodeToString(suffix) + ".tmp"
	fd, err := unix.Openat(dirFD, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temp)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = unix.Renameat(dirFD, temp, dirFD, v.file)
	}
	if err != nil {
		_ = unix.Unlinkat(dirFD, temp, 0)
		return err
	}
	return unix.Fsync(dirFD)
}
