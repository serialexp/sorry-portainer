package secretstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/registries"
)

// Registry logins live next to the stack secrets: one file per login, named
// by the SHA-256 of its key, holding the key, username, update time and last
// test in the clear and the password sealed. set-id names the current set.
// See docs/design/registry-logins.md.
const (
	registriesDir = "registries"
	setIDFile     = "set-id"
	loginSuffix   = ".json"
	// maxLoginFile bounds one login file: the clear fields at their limits
	// and a sealed 4096-byte password, base64 in JSON.
	maxLoginFile = 64 << 10
)

var (
	ErrLoginExists   = errors.New("a login for this registry or namespace already exists; use Replace on its row instead")
	ErrLoginNotFound = errors.New("no login for this registry or namespace")
)

// Kinds of a login test's result.
const (
	LoginTestOK     = "ok"
	LoginTestFailed = "failed"
)

// LoginTest is the outcome of the last sign-in test of a login. It describes
// the login, not its secret, so it is kept in the clear.
type LoginTest struct {
	// State is LoginTestOK or LoginTestFailed.
	State string `json:"state"`
	// Failure is a protocol.RegistryTest* kind when State is failed.
	Failure string    `json:"failure,omitempty"`
	Message string    `json:"message,omitempty"`
	Host    string    `json:"host"`
	At      time.Time `json:"at"`
}

// Login describes a stored registry login without its password.
type Login struct {
	Key       string     `json:"key"`
	Username  string     `json:"username"`
	UpdatedAt time.Time  `json:"updated_at"`
	LastTest  *LoginTest `json:"last_test,omitempty"`
}

type loginFile struct {
	Login
	PasswordSealed []byte `json:"password_sealed"`
}

// loginAAD binds a sealed password to its key and username, so a file
// edited to show another key or username no longer opens.
func loginAAD(key, username string) []byte {
	return []byte("sorry-portainer/registry/v1\x00" + key + "\x00" + username)
}

func (s *Store) registriesPath() string { return filepath.Join(s.dir, registriesDir) }

func (s *Store) loginPath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.registriesPath(), hex.EncodeToString(sum[:])+loginSuffix)
}

// readLogin reads one login file; ErrLoginNotFound when there is none.
func (s *Store) readLogin(key string) (loginFile, error) {
	return readLoginFile(s.loginPath(key), key)
}

func readLoginFile(path, key string) (loginFile, error) {
	data, err := readLimited(path, maxLoginFile)
	if errors.Is(err, fs.ErrNotExist) {
		return loginFile{}, ErrLoginNotFound
	}
	if err != nil {
		return loginFile{}, err
	}
	var file loginFile
	if err := json.Unmarshal(data, &file); err != nil {
		return loginFile{}, fmt.Errorf("registry login file %s is corrupt: %w", filepath.Base(path), err)
	}
	if want, err := registries.NormalizeKey(file.Key); err != nil || want != file.Key || (key != "" && file.Key != key) {
		return loginFile{}, fmt.Errorf("registry login file %s names the wrong key", filepath.Base(path))
	}
	return file, nil
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("%s is not a regular file of at most %d bytes", filepath.Base(path), limit)
	}
	data := make([]byte, info.Size())
	if _, err := f.ReadAt(data, 0); err != nil && info.Size() > 0 {
		return nil, err
	}
	return data, nil
}

// writeLoginLocked writes one login file and a new set ID. The caller
// holds writeMu.
func (s *Store) writeLoginLocked(file loginFile) error {
	data, err := json.Marshal(file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.registriesPath(), 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(s.loginPath(file.Key), data); err != nil {
		return err
	}
	return s.newSetIDLocked()
}

// newSetIDLocked replaces the set ID. The caller holds writeMu. The cached
// ID is replaced under setIDMu together with the file, so a reader never
// caches an ID older than the file; a failed write drops the cache, and the
// next read goes back to the file.
func (s *Store) newSetIDLocked() error {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	setID := hex.EncodeToString(id)
	s.setIDMu.Lock()
	defer s.setIDMu.Unlock()
	if err := writeFileAtomic(filepath.Join(s.registriesPath(), setIDFile), []byte(setID)); err != nil {
		s.setIDLoaded = false
		return err
	}
	s.setID, s.setIDLoaded = setID, true
	return nil
}

// RegistrySetID is the ID of the current login set, empty while no login was
// ever saved. It works while the store is locked. The file is read once:
// only this store changes it, and the master is its only process.
func (s *Store) RegistrySetID() (string, error) {
	s.setIDMu.Lock()
	defer s.setIDMu.Unlock()
	if s.setIDLoaded {
		return s.setID, nil
	}
	data, err := readLimited(filepath.Join(s.registriesPath(), setIDFile), 64)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.setID = ""
	case err != nil:
		return "", err
	default:
		s.setID = strings.TrimSpace(string(data))
	}
	s.setIDLoaded = true
	return s.setID, nil
}

// AddLogin stores a new login. The key is normalized; a key already saved
// is ErrLoginExists.
func (s *Store) AddLogin(key, username string, password []byte, now time.Time) (Login, error) {
	key, err := registries.NormalizeKey(key)
	if err != nil {
		return Login{}, err
	}
	if err := registries.ValidCredentials(username, password); err != nil {
		return Login{}, err
	}
	aead, err := s.cipher()
	if err != nil {
		return Login{}, err
	}
	sealed, err := seal(aead, password, loginAAD(key, username))
	if err != nil {
		return Login{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.readLogin(key); err == nil {
		return Login{}, ErrLoginExists
	} else if !errors.Is(err, ErrLoginNotFound) {
		return Login{}, err
	}
	logins, err := s.Logins()
	if err != nil {
		return Login{}, err
	}
	if len(logins) >= registries.MaxLogins {
		return Login{}, registries.ErrTooManyLogin
	}
	file := loginFile{Login: Login{Key: key, Username: username, UpdatedAt: now.UTC()}, PasswordSealed: sealed}
	if err := s.writeLoginLocked(file); err != nil {
		return Login{}, err
	}
	return file.Login, nil
}

// ReplaceLogin changes the username and password of a saved login. An empty
// password keeps the stored one. The last test is dropped: it was of the old
// credentials.
func (s *Store) ReplaceLogin(key, username string, password []byte, now time.Time) (Login, error) {
	key, err := registries.NormalizeKey(key)
	if err != nil {
		return Login{}, err
	}
	aead, err := s.cipher()
	if err != nil {
		return Login{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	file, err := s.readLogin(key)
	if err != nil {
		return Login{}, err
	}
	if len(password) == 0 {
		kept, err := open(aead, file.PasswordSealed, loginAAD(file.Key, file.Username))
		if err != nil {
			return Login{}, fmt.Errorf("registry login %s does not decrypt: %w", key, err)
		}
		defer clear(kept)
		password = kept
	}
	if err := registries.ValidCredentials(username, password); err != nil {
		return Login{}, err
	}
	sealed, err := seal(aead, password, loginAAD(key, username))
	if err != nil {
		return Login{}, err
	}
	file = loginFile{Login: Login{Key: key, Username: username, UpdatedAt: now.UTC()}, PasswordSealed: sealed}
	if err := s.writeLoginLocked(file); err != nil {
		return Login{}, err
	}
	return file.Login, nil
}

// DeleteLogin removes a saved login.
func (s *Store) DeleteLogin(key string) error {
	key, err := registries.NormalizeKey(key)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := os.Remove(s.loginPath(key)); errors.Is(err, fs.ErrNotExist) {
		return ErrLoginNotFound
	} else if err != nil {
		return err
	}
	if err := syncDir(s.registriesPath()); err != nil {
		return err
	}
	return s.newSetIDLocked()
}

// RecordLoginTest keeps the result of a sign-in test with the login, if the
// login was not replaced since the test began (updatedAt is the version that
// was tested). It does not change the set ID.
func (s *Store) RecordLoginTest(key string, updatedAt time.Time, test LoginTest) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	file, err := s.readLogin(key)
	if err != nil {
		return err
	}
	if !file.UpdatedAt.Equal(updatedAt) {
		return nil // a newer version gets its own test
	}
	test.At = test.At.UTC()
	file.LastTest = &test
	data, err := json.Marshal(file)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.loginPath(key), data)
}

// Logins lists the saved logins by key, without passwords. It works while
// the store is locked.
func (s *Store) Logins() ([]Login, error) {
	files, err := s.loginFiles()
	if err != nil {
		return nil, err
	}
	out := make([]Login, len(files))
	for i := range files {
		out[i] = files[i].Login
	}
	return out, nil
}

// loginFiles reads every login file, sorted by key.
func (s *Store) loginFiles() ([]loginFile, error) {
	entries, err := os.ReadDir(s.registriesPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]loginFile, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasSuffix(name, loginSuffix) || len(name) != 64+len(loginSuffix) {
			continue
		}
		file, err := readLoginFile(filepath.Join(s.registriesPath(), name), "")
		if err != nil {
			return nil, err
		}
		if s.loginPath(file.Key) != filepath.Join(s.registriesPath(), name) {
			return nil, fmt.Errorf("registry login file %s names the wrong key", name)
		}
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// LoginSet decrypts every login, with the set ID they make up. Callers clear
// the passwords once sent (ClearLoginSet).
func (s *Store) LoginSet() (protocol.RegistrySync, error) {
	aead, err := s.cipher()
	if err != nil {
		return protocol.RegistrySync{}, err
	}
	// Under writeMu, so the set ID belongs to exactly these logins.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	setID, err := s.RegistrySetID()
	if err != nil {
		return protocol.RegistrySync{}, err
	}
	files, err := s.loginFiles()
	if err != nil {
		return protocol.RegistrySync{}, err
	}
	set := protocol.RegistrySync{SetID: setID, Logins: make([]protocol.RegistryLogin, 0, len(files))}
	for _, file := range files {
		password, err := open(aead, file.PasswordSealed, loginAAD(file.Key, file.Username))
		if err != nil {
			ClearLoginSet(set)
			return protocol.RegistrySync{}, fmt.Errorf("registry login %s does not decrypt: %w", file.Key, err)
		}
		set.Logins = append(set.Logins, protocol.RegistryLogin{Key: file.Key, Username: file.Username, Password: password})
	}
	return set, nil
}

// ClearLoginSet overwrites a decrypted set's passwords.
func ClearLoginSet(set protocol.RegistrySync) {
	for _, login := range set.Logins {
		clear(login.Password)
	}
}

// LoginForTest decrypts one login, with the version (UpdatedAt) it is.
func (s *Store) LoginForTest(key string) (protocol.RegistryLogin, time.Time, error) {
	aead, err := s.cipher()
	if err != nil {
		return protocol.RegistryLogin{}, time.Time{}, err
	}
	key, err = registries.NormalizeKey(key)
	if err != nil {
		return protocol.RegistryLogin{}, time.Time{}, err
	}
	file, err := s.readLogin(key)
	if err != nil {
		return protocol.RegistryLogin{}, time.Time{}, err
	}
	password, err := open(aead, file.PasswordSealed, loginAAD(file.Key, file.Username))
	if err != nil {
		return protocol.RegistryLogin{}, time.Time{}, fmt.Errorf("registry login %s does not decrypt: %w", key, err)
	}
	return protocol.RegistryLogin{Key: file.Key, Username: file.Username, Password: password}, file.UpdatedAt, nil
}

// RevealLogin decrypts one login's password for showing to the operator,
// after checking the passphrase again, as Reveal does for stack secrets.
func (s *Store) RevealLogin(key, passphrase string) ([]byte, error) {
	key, err := registries.NormalizeKey(key)
	if err != nil {
		return nil, err
	}
	if _, err := s.cipher(); err != nil {
		return nil, err
	}
	s.unlockMu.Lock()
	derived, aead, err := s.verify(passphrase)
	s.unlockMu.Unlock()
	if err != nil {
		return nil, err
	}
	clear(derived)
	if s.State() != Unlocked {
		return nil, ErrLocked
	}
	file, err := s.readLogin(key)
	if err != nil {
		return nil, err
	}
	password, err := open(aead, file.PasswordSealed, loginAAD(file.Key, file.Username))
	if err != nil {
		return nil, fmt.Errorf("registry login %s does not decrypt: %w", key, err)
	}
	return password, nil
}
