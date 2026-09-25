package secretstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// testKDF keeps tests fast; the default is exercised by the benchmark.
var testKDF = KDF{Algorithm: "argon2id", Time: 1, MemoryKiB: 64, Threads: 1}

const passphrase = "correct horse battery"

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "secrets")
	s, err := OpenWithKDF(dir, testKDF)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func unlocked(t *testing.T) (*Store, string) {
	t.Helper()
	s, dir := newStore(t)
	if err := s.Initialize(passphrase); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestLifecycle(t *testing.T) {
	s, dir := newStore(t)
	if s.State() != Uninitialized {
		t.Fatalf("state %s", s.State())
	}
	if err := s.Put("h1", "web", "db", []byte("x")); !errors.Is(err, ErrUninitialized) {
		t.Fatalf("put before init: %v", err)
	}
	if err := s.Unlock(passphrase); !errors.Is(err, ErrUninitialized) {
		t.Fatalf("unlock before init: %v", err)
	}
	if err := s.Initialize("short"); !errors.Is(err, ErrWeakPassphrase) {
		t.Fatalf("weak: %v", err)
	}
	if err := s.Initialize(passphrase); err != nil {
		t.Fatal(err)
	}
	if err := s.Initialize(passphrase); !errors.Is(err, ErrInitialized) {
		t.Fatalf("second init: %v", err)
	}
	if err := s.Put("h1", "web", "db_password", []byte("hunter2")); err != nil {
		t.Fatal(err)
	}
	s.Lock()
	if s.State() != Locked {
		t.Fatalf("state %s", s.State())
	}
	if _, err := s.Stack("h1", "web"); !errors.Is(err, ErrLocked) {
		t.Fatalf("read while locked: %v", err)
	}
	if err := s.Put("h1", "web", "other", []byte("x")); !errors.Is(err, ErrLocked) {
		t.Fatalf("put while locked: %v", err)
	}
	// Names stay visible while locked.
	entries, err := s.List("h1", "web")
	if err != nil || len(entries) != 1 || entries[0].Name != "db_password" || entries[0].Size != 7 {
		t.Fatalf("list while locked: %+v %v", entries, err)
	}
	if err := s.Unlock("wrong passphrase!"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if s.State() != Locked {
		t.Fatal("wrong passphrase unlocked the store")
	}

	// A fresh process sees a locked store and the same values after unlock.
	again, err := OpenWithKDF(dir, DefaultKDF)
	if err != nil {
		t.Fatal(err)
	}
	if again.State() != Locked {
		t.Fatalf("reopened state %s", again.State())
	}
	if err := again.Unlock(passphrase); err != nil {
		t.Fatal(err)
	}
	values, err := again.Stack("h1", "web")
	if err != nil || string(values["db_password"]) != "hunter2" || len(values) != 1 {
		t.Fatalf("values %q %v", values, err)
	}
}

func TestNothingPlaintextOnDisk(t *testing.T) {
	s, dir := unlocked(t)
	secret := []byte("very-distinctive-plaintext-value")
	if err := s.Put("h1", "web", "token", secret); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			if info.Mode().Perm() != 0o700 {
				t.Errorf("%s mode %04o", path, info.Mode().Perm())
			}
			return nil
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %04o", path, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, secret) || bytes.Contains(data, []byte(passphrase)) {
			t.Errorf("%s contains plaintext", path)
		}
		if strings.HasPrefix(filepath.Base(path), ".tmp-") {
			t.Errorf("leftover temp file %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSealedValueIsBoundToItsLocation(t *testing.T) {
	s, dir := unlocked(t)
	if err := s.Put("h1", "web", "a", []byte("value-a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("h1", "web", "b", []byte("value-b")); err != nil {
		t.Fatal(err)
	}
	// Someone with disk access swaps files between names.
	values := filepath.Join(dir, "values", "h1", "web")
	a, _ := os.ReadFile(filepath.Join(values, "a.sealed"))
	if err := os.WriteFile(filepath.Join(values, "b.sealed"), a, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stack("h1", "web"); err == nil || !strings.Contains(err.Error(), "does not decrypt") {
		t.Fatalf("swapped value accepted: %v", err)
	}
	// And copies to another host.
	other := filepath.Join(dir, "values", "h2", "web")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "a.sealed"), a, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stack("h2", "web"); err == nil {
		t.Fatal("value copied to another host decrypted")
	}
	// Tampered bytes fail too.
	a[len(a)-1] ^= 1
	if err := os.WriteFile(filepath.Join(values, "a.sealed"), a, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("h1", "web", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stack("h1", "web"); err == nil {
		t.Fatal("tampered value decrypted")
	}
}

func TestValidationAndLimits(t *testing.T) {
	s, _ := unlocked(t)
	for _, c := range [][3]string{{"../h", "web", "a"}, {"h1", "../web", "a"}, {"h1", "web", "../a"}, {"h1", "web", ""}, {"h1", "Web", "a"}, {"h1", "web", "a/b"}} {
		if err := s.Put(c[0], c[1], c[2], []byte("x")); !errors.Is(err, ErrInvalidName) {
			t.Errorf("%q: %v", c, err)
		}
	}
	if err := s.Put("h1", "web", "empty", nil); !errors.Is(err, ErrValueSize) {
		t.Fatalf("empty: %v", err)
	}
	if err := s.Put("h1", "web", "big", make([]byte, protocol.MaxSecretSize+1)); !errors.Is(err, ErrValueSize) {
		t.Fatalf("big: %v", err)
	}
	if err := s.Put("h1", "web", "max", make([]byte, protocol.MaxSecretSize)); err != nil {
		t.Fatalf("max size: %v", err)
	}
	// Count limit: max already holds one secret.
	for i := 1; i < protocol.MaxSecretsPerStack; i++ {
		if err := s.Put("h1", "web", "n"+string(rune('a'+i/26))+string(rune('a'+i%26)), []byte("x")); err != nil {
			t.Fatalf("secret %d: %v", i, err)
		}
	}
	if err := s.Put("h1", "web", "one-too-many", []byte("x")); !errors.Is(err, ErrStackLimit) {
		t.Fatalf("count limit: %v", err)
	}
	// Replacing an existing name is not an extra secret.
	if err := s.Put("h1", "web", "max", []byte("smaller")); err != nil {
		t.Fatalf("replace at limit: %v", err)
	}
	// Total size limit on another stack.
	full := make([]byte, protocol.MaxSecretSize)
	for i := range protocol.MaxStackSecretsTotal / protocol.MaxSecretSize {
		if err := s.Put("h1", "big", "s"+string(rune('a'+i)), full); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	if err := s.Put("h1", "big", "extra", []byte("x")); !errors.Is(err, ErrStackLimit) {
		t.Fatalf("size limit: %v", err)
	}
	if err := s.Delete("h1", "big", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
	stacks, err := s.Stacks("h1")
	if err != nil || strings.Join(stacks, ",") != "big,web" {
		t.Fatalf("stacks %v %v", stacks, err)
	}
	if stacks, err := s.Stacks("nobody"); err != nil || len(stacks) != 0 {
		t.Fatalf("empty host %v %v", stacks, err)
	}
}

func TestConcurrentPutsRespectLimit(t *testing.T) {
	s, _ := unlocked(t)
	var wg sync.WaitGroup
	errs := make(chan error, protocol.MaxSecretsPerStack*2)
	for i := range protocol.MaxSecretsPerStack * 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- s.Put("h1", "web", "s"+string(rune('a'+i/26))+string(rune('a'+i%26)), []byte("x"))
		}(i)
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrStackLimit) {
			t.Fatal(err)
		}
	}
	entries, _ := s.List("h1", "web")
	if ok != protocol.MaxSecretsPerStack || len(entries) != protocol.MaxSecretsPerStack {
		t.Fatalf("ok=%d stored=%d", ok, len(entries))
	}
}

func TestConcurrentInitializeHasOneWinner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	var wg sync.WaitGroup
	wins := make(chan string, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Separate Store values, like two server processes on one dir.
			s, err := OpenWithKDF(dir, testKDF)
			if err != nil {
				t.Error(err)
				return
			}
			pass := passphrase + string(rune('a'+i))
			if err := s.Initialize(pass); err == nil {
				wins <- pass
			} else if !errors.Is(err, ErrInitialized) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	close(wins)
	var winners []string
	for w := range wins {
		winners = append(winners, w)
	}
	if len(winners) != 1 {
		t.Fatalf("winners %v", winners)
	}
	s, _ := OpenWithKDF(dir, testKDF)
	if err := s.Unlock(winners[0]); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsLooseDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("accepted world-readable store directory")
	}
}

func TestCorruptVault(t *testing.T) {
	_, dir := unlocked(t)
	if err := os.WriteFile(filepath.Join(dir, "vault.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenWithKDF(dir, testKDF)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock(passphrase); !errors.Is(err, ErrCorruptVaultFile) {
		t.Fatalf("corrupt vault: %v", err)
	}
}

// BenchmarkUnlock measures the default KDF, which bounds how fast the unlock
// endpoint can be used to guess passphrases. Budget: 30–500 ms per attempt
// (about 46 ms on a Ryzen 9 9900X; slower hosts take longer, which is fine).
func BenchmarkUnlock(b *testing.B) {
	dir := filepath.Join(b.TempDir(), "secrets")
	s, err := Open(dir)
	if err != nil {
		b.Fatal(err)
	}
	if err := s.Initialize(passphrase); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if err := s.Unlock(passphrase); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStackFull decrypts a stack at the limits (64 secrets, 1 MiB).
// Budget: well under 50 ms, since it runs on every agent connect.
func BenchmarkStackFull(b *testing.B) {
	dir := filepath.Join(b.TempDir(), "secrets")
	s, _ := OpenWithKDF(dir, testKDF)
	if err := s.Initialize(passphrase); err != nil {
		b.Fatal(err)
	}
	value := make([]byte, protocol.MaxStackSecretsTotal/protocol.MaxSecretsPerStack)
	for i := range protocol.MaxSecretsPerStack {
		if err := s.Put("h1", "web", "s"+string(rune('a'+i/26))+string(rune('a'+i%26)), value); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for b.Loop() {
		values, err := s.Stack("h1", "web")
		if err != nil || len(values) != protocol.MaxSecretsPerStack {
			b.Fatal(len(values), err)
		}
	}
}
