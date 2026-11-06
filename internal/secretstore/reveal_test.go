package secretstore

import (
	"errors"
	"testing"
)

func TestRevealNeedsThePassphraseAgain(t *testing.T) {
	s, _ := unlocked(t)
	if err := s.Put("h1", "web", "db_password", []byte("hunter2\n")); err != nil {
		t.Fatal(err)
	}
	value, err := s.Reveal("h1", "web", "db_password", passphrase)
	if err != nil || string(value) != "hunter2\n" {
		t.Fatalf("Reveal = %q, %v; want the stored value", value, err)
	}
	if _, err := s.Reveal("h1", "web", "db_password", "not the passphrase"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v, want ErrWrongPassphrase", err)
	}
	if _, err := s.Reveal("h1", "web", "db_password", ""); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("empty passphrase: %v, want ErrWrongPassphrase", err)
	}
	// A wrong passphrase leaves the store unlocked and working.
	if s.State() != Unlocked {
		t.Fatalf("state = %s after a wrong reveal, want unlocked", s.State())
	}
}

func TestRevealOfAMissingOrInvalidName(t *testing.T) {
	s, _ := unlocked(t)
	if _, err := s.Reveal("h1", "web", "absent", passphrase); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing secret: %v, want ErrNotFound", err)
	}
	if _, err := s.Reveal("h1", "web", "../vault", passphrase); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("path name: %v, want ErrInvalidName", err)
	}
	if _, err := s.Reveal("h1", "../web", "x", passphrase); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("path stack: %v, want ErrInvalidName", err)
	}
}

func TestRevealNeedsAnUnlockedStore(t *testing.T) {
	fresh, _ := newStore(t)
	if _, err := fresh.Reveal("h1", "web", "x", passphrase); !errors.Is(err, ErrUninitialized) {
		t.Fatalf("uninitialized: %v, want ErrUninitialized", err)
	}
	s, _ := unlocked(t)
	if err := s.Put("h1", "web", "x", []byte("v")); err != nil {
		t.Fatal(err)
	}
	s.Lock()
	// The right passphrase is not a back door around the lock.
	if _, err := s.Reveal("h1", "web", "x", passphrase); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked: %v, want ErrLocked", err)
	}
	if s.State() != Locked {
		t.Fatalf("state = %s, Reveal must not unlock", s.State())
	}
}
