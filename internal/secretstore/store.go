// Package secretstore is the master's encrypted store of stack secrets.
//
// Values are sealed with AES-256-GCM under a key derived with Argon2id from a
// passphrase typed when the server starts. The passphrase and key are never
// written to disk: until someone unlocks the store, values can be listed by
// name but not read or written. Each value is its own file, so changing one
// secret never rewrites the others.
package secretstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// State is the store's lock state.
type State string

const (
	Uninitialized State = "uninitialized"
	Locked        State = "locked"
	Unlocked      State = "unlocked"
)

var (
	ErrLocked           = errors.New("secret store is locked")
	ErrUninitialized    = errors.New("secret store has no passphrase yet")
	ErrInitialized      = errors.New("secret store is already initialized")
	ErrWrongPassphrase  = errors.New("wrong passphrase")
	ErrWeakPassphrase   = errors.New("passphrase must be at least 12 characters")
	ErrNotFound         = errors.New("secret not found")
	ErrInvalidName      = errors.New("invalid host, stack, or secret name")
	ErrValueSize        = fmt.Errorf("secret values must be 1 to %d bytes", protocol.MaxSecretSize)
	ErrStackLimit       = fmt.Errorf("a stack holds at most %d secrets totalling %d bytes", protocol.MaxSecretsPerStack, protocol.MaxStackSecretsTotal)
	ErrCorruptVaultFile = errors.New("secret store vault file is corrupt")
)

const (
	vaultFileName = "vault.json"
	valuesDir     = "values"
	sealedSuffix  = ".sealed"
	sealVersion   = 1
	keySize       = 32
	minPassphrase = 12
	checkText     = "sorry-portainer secret store key check v1"
)

// KDF holds Argon2id parameters. They are stored with the vault so they can be
// raised later without breaking existing stores.
type KDF struct {
	Algorithm string `json:"algorithm"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
}

// DefaultKDF costs roughly 100 ms and 64 MiB per unlock attempt, which also
// rate-limits passphrase guessing through the unlock endpoint.
var DefaultKDF = KDF{Algorithm: "argon2id", Time: 3, MemoryKiB: 64 << 10, Threads: 4}

type vaultFile struct {
	Version int    `json:"version"`
	KDF     KDF    `json:"kdf"`
	Salt    []byte `json:"salt"`
	// Check is a sealed known text; opening it proves the passphrase is right.
	Check []byte `json:"check"`
}

// Entry describes a stored secret without its value.
type Entry struct {
	Name      string    `json:"name"`
	Size      int       `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store is safe for concurrent use.
type Store struct {
	dir string
	kdf KDF

	// unlockMu serializes key derivation, so parallel guesses queue up.
	unlockMu sync.Mutex

	mu    sync.RWMutex
	key   []byte // nil while locked
	aead  cipher.AEAD
	state State

	// writeMu serializes mutations so per-stack limits hold under concurrency.
	writeMu sync.Mutex
}

// Open opens or prepares the store in dir, which is created with mode 0700.
func Open(dir string) (*Store, error) {
	return OpenWithKDF(dir, DefaultKDF)
}

// OpenWithKDF is Open with explicit KDF parameters for new vaults (tests use
// cheap ones). Existing vaults keep the parameters they were created with.
func OpenWithKDF(dir string, kdf KDF) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secret store %s must not be group/world accessible (mode %04o)", dir, info.Mode().Perm())
	}
	s := &Store{dir: dir, kdf: kdf, state: Uninitialized}
	if _, err := os.Stat(filepath.Join(dir, vaultFileName)); err == nil {
		s.state = Locked
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func (s *Store) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Initialize sets the passphrase of a new store and leaves it unlocked.
func (s *Store) Initialize(passphrase string) error {
	if len(passphrase) < minPassphrase {
		return ErrWeakPassphrase
	}
	s.unlockMu.Lock()
	defer s.unlockMu.Unlock()
	if s.State() != Uninitialized {
		return ErrInitialized
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key := deriveKey(passphrase, salt, s.kdf)
	aead, err := newAEAD(key)
	if err != nil {
		return err
	}
	check, err := seal(aead, []byte(checkText), []byte("check"))
	if err != nil {
		return err
	}
	data, err := json.Marshal(vaultFile{Version: 1, KDF: s.kdf, Salt: salt, Check: check})
	if err != nil {
		return err
	}
	// O_EXCL: two initializations racing cannot both win.
	if err := writeFileExclusive(filepath.Join(s.dir, vaultFileName), data); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrInitialized
		}
		return err
	}
	s.setUnlocked(key, aead)
	return nil
}

// Unlock derives the key from passphrase and keeps it in memory.
func (s *Store) Unlock(passphrase string) error {
	s.unlockMu.Lock()
	defer s.unlockMu.Unlock()
	switch s.State() {
	case Uninitialized:
		return ErrUninitialized
	case Unlocked:
		// Still verify, so an unlocked store is not an oracle that accepts anything.
	}
	data, err := os.ReadFile(filepath.Join(s.dir, vaultFileName))
	if err != nil {
		return err
	}
	var vault vaultFile
	if err := json.Unmarshal(data, &vault); err != nil || vault.Version != 1 || vault.KDF.Algorithm != "argon2id" || len(vault.Salt) < 16 {
		return ErrCorruptVaultFile
	}
	key := deriveKey(passphrase, vault.Salt, vault.KDF)
	aead, err := newAEAD(key)
	if err != nil {
		return err
	}
	text, err := open(aead, vault.Check, []byte("check"))
	if err != nil || subtle.ConstantTimeCompare(text, []byte(checkText)) != 1 {
		clear(key)
		return ErrWrongPassphrase
	}
	s.setUnlocked(key, aead)
	return nil
}

func (s *Store) setUnlocked(key []byte, aead cipher.AEAD) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil {
		clear(s.key)
	}
	s.key, s.aead, s.state = key, aead, Unlocked
}

// Lock forgets the key. Values stay on disk, sealed.
func (s *Store) Lock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != Unlocked {
		return
	}
	clear(s.key)
	s.key, s.aead, s.state = nil, nil, Locked
}

func deriveKey(passphrase string, salt []byte, kdf KDF) []byte {
	return argon2.IDKey([]byte(passphrase), salt, kdf.Time, kdf.MemoryKiB, kdf.Threads, keySize)
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// seal returns version || nonce || ciphertext.
func seal(aead cipher.AEAD, plaintext, aad []byte) ([]byte, error) {
	out := make([]byte, 1+aead.NonceSize(), 1+aead.NonceSize()+len(plaintext)+aead.Overhead())
	out[0] = sealVersion
	if _, err := rand.Read(out[1:]); err != nil {
		return nil, err
	}
	return aead.Seal(out, out[1:], plaintext, aad), nil
}

func open(aead cipher.AEAD, sealed, aad []byte) ([]byte, error) {
	if len(sealed) < 1+aead.NonceSize()+aead.Overhead() || sealed[0] != sealVersion {
		return nil, errors.New("malformed sealed value")
	}
	nonce := sealed[1 : 1+aead.NonceSize()]
	return aead.Open(nil, nonce, sealed[1+aead.NonceSize():], aad)
}

// valueAAD binds a sealed value to its location, so a file copied to another
// host, stack, or name does not open.
func valueAAD(host, stack, name string) []byte {
	return []byte("sorry-portainer/secret/v1\x00" + host + "\x00" + stack + "\x00" + name)
}

func validate(host, stack string, names ...string) error {
	if !protocol.ValidHostID(host) || !protocol.ValidStackName(stack) {
		return ErrInvalidName
	}
	for _, name := range names {
		if !protocol.ValidSecretName(name) {
			return ErrInvalidName
		}
	}
	return nil
}

func (s *Store) stackDir(host, stack string) string {
	return filepath.Join(s.dir, valuesDir, host, stack)
}

func (s *Store) cipher() (cipher.AEAD, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch s.state {
	case Uninitialized:
		return nil, ErrUninitialized
	case Locked:
		return nil, ErrLocked
	}
	return s.aead, nil
}

// Put stores or replaces one value.
func (s *Store) Put(host, stack, name string, value []byte) error {
	if err := validate(host, stack, name); err != nil {
		return err
	}
	if len(value) == 0 || len(value) > protocol.MaxSecretSize {
		return ErrValueSize
	}
	aead, err := s.cipher()
	if err != nil {
		return err
	}
	sealed, err := seal(aead, value, valueAAD(host, stack, name))
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	entries, err := s.List(host, stack)
	if err != nil {
		return err
	}
	count, total := 1, len(value)
	for _, entry := range entries {
		if entry.Name != name {
			count++
			total += entry.Size
		}
	}
	if count > protocol.MaxSecretsPerStack || total > protocol.MaxStackSecretsTotal {
		return ErrStackLimit
	}
	dir := s.stackDir(host, stack)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, name+sealedSuffix), sealed)
}

// Delete removes one value.
func (s *Store) Delete(host, stack, name string) error {
	if err := validate(host, stack, name); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	err := os.Remove(filepath.Join(s.stackDir(host, stack), name+sealedSuffix))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return syncDir(s.stackDir(host, stack))
}

// List returns a stack's secret names, sizes, and update times, sorted by name.
// It works while the store is locked. Sizes are plaintext sizes.
func (s *Store) List(host, stack string) ([]Entry, error) {
	if err := validate(host, stack); err != nil {
		return nil, err
	}
	dirEntries, err := os.ReadDir(s.stackDir(host, stack))
	if errors.Is(err, fs.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(dirEntries))
	for _, d := range dirEntries {
		name, ok := strings.CutSuffix(d.Name(), sealedSuffix)
		if !ok || !d.Type().IsRegular() || !protocol.ValidSecretName(name) {
			continue
		}
		info, err := d.Info()
		if err != nil {
			return nil, err
		}
		size := int(info.Size()) - 1 - 12 - 16 // version, GCM nonce, GCM tag
		out = append(out, Entry{Name: name, Size: max(size, 0), UpdatedAt: info.ModTime().UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Stack decrypts every value of one stack. Callers should clear the returned
// slices once they have been sent.
func (s *Store) Stack(host, stack string) (map[string][]byte, error) {
	aead, err := s.cipher()
	if err != nil {
		return nil, err
	}
	entries, err := s.List(host, stack)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		sealed, err := os.ReadFile(filepath.Join(s.stackDir(host, stack), entry.Name+sealedSuffix))
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted since List
		}
		if err != nil {
			return nil, err
		}
		value, err := open(aead, sealed, valueAAD(host, stack, entry.Name))
		if err != nil {
			return nil, fmt.Errorf("secret %s/%s/%s does not decrypt: %w", host, stack, entry.Name, err)
		}
		out[entry.Name] = value
	}
	return out, nil
}

// Stacks lists the stacks of host that have at least one stored secret.
func (s *Store) Stacks(host string) ([]string, error) {
	if !protocol.ValidHostID(host) {
		return nil, ErrInvalidName
	}
	dirEntries, err := os.ReadDir(filepath.Join(s.dir, valuesDir, host))
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(dirEntries))
	for _, d := range dirEntries {
		if !d.IsDir() || !protocol.ValidStackName(d.Name()) {
			continue
		}
		entries, err := s.List(host, d.Name())
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			out = append(out, d.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func writeFileExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// writeFileAtomic replaces path durably: temp file, fsync, rename, fsync dir.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
