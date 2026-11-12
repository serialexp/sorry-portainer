package secretstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/registries"
)

var t0 = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

func TestLoginLifecycle(t *testing.T) {
	s, dir := unlocked(t)
	if id, err := s.RegistrySetID(); err != nil || id != "" {
		t.Fatalf("set ID of an empty store = %q, %v", id, err)
	}
	login, err := s.AddLogin("Index.Docker.IO/myorg", "bart", []byte("hunter2"), t0)
	if err != nil || login.Key != "docker.io/myorg" || login.Username != "bart" || !login.UpdatedAt.Equal(t0) {
		t.Fatalf("add = %+v, %v", login, err)
	}
	first, _ := s.RegistrySetID()
	if len(first) != 32 {
		t.Fatalf("set ID %q", first)
	}
	if _, err := s.AddLogin("docker.io/myorg", "other", []byte("x"), t0); !errors.Is(err, ErrLoginExists) {
		t.Fatalf("second add = %v", err)
	}
	if _, err := s.AddLogin("ghcr.io", "bot", []byte("token"), t0); err != nil {
		t.Fatal(err)
	}
	second, _ := s.RegistrySetID()
	if second == first {
		t.Fatal("the set ID did not change on add")
	}

	set, err := s.LoginSet()
	if err != nil || set.SetID != second || len(set.Logins) != 2 || set.Logins[0].Key != "docker.io/myorg" || string(set.Logins[0].Password) != "hunter2" || set.Logins[1].Key != "ghcr.io" {
		t.Fatalf("set = %+v, %v", set, err)
	}
	ClearLoginSet(set)
	if !bytes.Equal(set.Logins[0].Password, make([]byte, 7)) {
		t.Fatal("ClearLoginSet left the password")
	}

	// Replace with an empty password keeps the stored one.
	replaced, err := s.ReplaceLogin("docker.io/myorg", "bart2", nil, t0.Add(time.Minute))
	if err != nil || replaced.Username != "bart2" {
		t.Fatalf("replace = %+v, %v", replaced, err)
	}
	got, version, err := s.LoginForTest("docker.io/myorg")
	if err != nil || got.Username != "bart2" || string(got.Password) != "hunter2" || !version.Equal(t0.Add(time.Minute)) {
		t.Fatalf("after replace = %+v %v, %v", got, version, err)
	}
	if _, err := s.ReplaceLogin("quay.io", "x", []byte("y"), t0); !errors.Is(err, ErrLoginNotFound) {
		t.Fatalf("replace missing = %v", err)
	}
	third, _ := s.RegistrySetID()
	if third == second {
		t.Fatal("the set ID did not change on replace")
	}

	// Listing and the set ID work while locked; the set does not.
	s.Lock()
	logins, err := s.Logins()
	if err != nil || len(logins) != 2 || logins[0].Username != "bart2" || logins[1].Username != "bot" {
		t.Fatalf("locked list = %+v, %v", logins, err)
	}
	if _, err := s.LoginSet(); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked set = %v", err)
	}
	if _, err := s.AddLogin("quay.io", "x", []byte("y"), t0); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked add = %v", err)
	}
	if _, err := s.RevealLogin("ghcr.io", passphrase); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked reveal = %v", err)
	}
	if err := s.Unlock(passphrase); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteLogin("ghcr.io"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteLogin("ghcr.io"); !errors.Is(err, ErrLoginNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	fourth, _ := s.RegistrySetID()
	if fourth == third {
		t.Fatal("the set ID did not change on delete")
	}
	// The ID is cached; a store opened afresh reads the same one from disk.
	reopened, err := OpenWithKDF(dir, testKDF)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := reopened.RegistrySetID(); err != nil || id != fourth {
		t.Fatalf("reopened set ID = %q, %v, want %q", id, err, fourth)
	}

	// Nothing on disk holds the password in the clear.
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(path); bytes.Contains(data, []byte("hunter2")) {
				t.Errorf("%s holds the password", path)
			}
		}
		return nil
	})
}

func TestRevealLoginNeedsThePassphrase(t *testing.T) {
	s, _ := unlocked(t)
	if _, err := s.AddLogin("ghcr.io/myorg", "bot", []byte("token"), t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevealLogin("ghcr.io/myorg", "wrong passphrase!"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase = %v", err)
	}
	password, err := s.RevealLogin("GHCR.io/myorg", passphrase)
	if err != nil || string(password) != "token" {
		t.Fatalf("reveal = %q, %v", password, err)
	}
	if _, err := s.RevealLogin("ghcr.io/other", passphrase); !errors.Is(err, ErrLoginNotFound) {
		t.Fatalf("reveal missing = %v", err)
	}
}

// A login file edited to name another key or username no longer opens.
func TestTamperedLoginDoesNotOpen(t *testing.T) {
	for _, field := range []string{"username", "key"} {
		t.Run(field, func(t *testing.T) {
			s, _ := unlocked(t)
			if _, err := s.AddLogin("ghcr.io/myorg", "bot", []byte("token"), t0); err != nil {
				t.Fatal(err)
			}
			path := s.loginPath("ghcr.io/myorg")
			data, _ := os.ReadFile(path)
			var file map[string]any
			if err := json.Unmarshal(data, &file); err != nil {
				t.Fatal(err)
			}
			if field == "username" {
				file["username"] = "admin"
			} else {
				file["key"] = "ghcr.io/other"
			}
			edited, _ := json.Marshal(file)
			if err := os.WriteFile(path, edited, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LoginSet(); err == nil {
				t.Fatal("an edited login file opened")
			}
		})
	}
}

func TestLoginValidationAndLimit(t *testing.T) {
	s, _ := unlocked(t)
	cases := []struct {
		key, username, password string
		want                    error
	}{
		{"https://ghcr.io", "u", "p", registries.ErrKeyScheme},
		{"ghcr.io/app:1", "u", "p", registries.ErrKeyRef},
		{"ghcr.io", "a:b", "p", registries.ErrUsername},
		{"ghcr.io", "u", "p\nq", registries.ErrPassword},
		{"ghcr.io", "u", strings.Repeat("p", registries.MaxPassword+1), registries.ErrPassword},
	}
	for _, c := range cases {
		if _, err := s.AddLogin(c.key, c.username, []byte(c.password), t0); !errors.Is(err, c.want) {
			t.Errorf("AddLogin(%q, %q) = %v, want %v", c.key, c.username, err, c.want)
		}
	}
	for i := range registries.MaxLogins {
		if _, err := s.AddLogin("registry.example.com/team"+string(rune('a'+i/26))+string(rune('a'+i%26)), "u", []byte("p"), t0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddLogin("ghcr.io", "u", []byte("p"), t0); !errors.Is(err, registries.ErrTooManyLogin) {
		t.Fatalf("login %d = %v", registries.MaxLogins+1, err)
	}
}

func TestRecordLoginTestKeepsOnlyTheCurrentVersion(t *testing.T) {
	s, _ := unlocked(t)
	login, err := s.AddLogin("ghcr.io", "bot", []byte("token"), t0)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.RegistrySetID()
	test := LoginTest{State: LoginTestFailed, Failure: "denied", Message: "ghcr.io refused the login", Host: "jp1", At: t0.Add(time.Second)}
	if err := s.RecordLoginTest("ghcr.io", login.UpdatedAt, test); err != nil {
		t.Fatal(err)
	}
	logins, _ := s.Logins()
	if logins[0].LastTest == nil || *logins[0].LastTest != test {
		t.Fatalf("last test = %+v", logins[0].LastTest)
	}
	if after, _ := s.RegistrySetID(); after != before {
		t.Fatal("recording a test changed the set ID")
	}
	// A replace drops the old result, and a late result of the old version
	// is not kept.
	if _, err := s.ReplaceLogin("ghcr.io", "bot", []byte("new"), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLoginTest("ghcr.io", login.UpdatedAt, LoginTest{State: LoginTestOK, Host: "jp1", At: t0}); err != nil {
		t.Fatal(err)
	}
	if logins, _ := s.Logins(); logins[0].LastTest != nil {
		t.Fatalf("a stale test was kept: %+v", logins[0].LastTest)
	}
}
