package registries

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

func TestNormalizeKey(t *testing.T) {
	good := map[string]string{
		"ghcr.io":                    "ghcr.io",
		"GHCR.io/myorg":              "ghcr.io/myorg",
		"docker.io":                  "docker.io",
		"index.docker.io":            "docker.io",
		"registry-1.docker.io/myorg": "docker.io/myorg",
		"docker.io/library":          "docker.io/library",
		"registry.example.com:5000":  "registry.example.com:5000",
		"10.0.0.2:5000/team/app":     "10.0.0.2:5000/team/app",
		"gitlab.example.com/a_b/c-d": "gitlab.example.com/a_b/c-d",
	}
	for raw, want := range good {
		got, err := NormalizeKey(raw)
		if err != nil || got != want {
			t.Errorf("NormalizeKey(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	bad := map[string]error{
		"":                              ErrKeyEmpty,
		strings.Repeat("a", MaxKey+1):   ErrKeyLong,
		"https://ghcr.io":               ErrKeyScheme,
		"http://ghcr.io":                ErrKeyScheme,
		"ghcr.io/ my":                   ErrKeySpace,
		" ghcr.io":                      ErrKeySpace,
		"ghcr.io/myorg/app:1.0":         ErrKeyRef,
		"ghcr.io/myorg/app@sha256:abcd": ErrKeyRef,
		"ghcr.io/":                      ErrKeyPath,
		"ghcr.io//myorg":                ErrKeyPath,
		"ghcr.io/MyOrg":                 ErrKeyPath,
		"/myorg":                        ErrKeyPath,
		"ghcr io":                       ErrKeySpace,
		"[::1]:5000":                    ErrKeyPath,
		"ghcr.io/my..org":               ErrKeyPath,
	}
	for raw, want := range bad {
		if _, err := NormalizeKey(raw); !errors.Is(err, want) {
			t.Errorf("NormalizeKey(%q) error = %v, want %v", raw, err, want)
		}
	}
}

func TestValidCredentials(t *testing.T) {
	if err := ValidCredentials("ci-bot", []byte("dckr_pat_x")); err != nil {
		t.Fatal(err)
	}
	if err := ValidCredentials("bot", []byte(strings.Repeat("p", MaxPassword))); err != nil {
		t.Fatal(err)
	}
	for _, username := range []string{"", "a:b", "line\nbreak", strings.Repeat("u", MaxUsername+1), "\xff"} {
		if !errors.Is(ValidCredentials(username, []byte("p")), ErrUsername) {
			t.Errorf("username %q accepted", username)
		}
	}
	for _, password := range []string{"", "tab\tbed", strings.Repeat("p", MaxPassword+1), "nul\x00"} {
		if !errors.Is(ValidCredentials("u", []byte(password)), ErrPassword) {
			t.Errorf("password %q accepted", password)
		}
	}
	if err := ValidCredentials("u", []byte("with:colon and spaces")); err != nil {
		t.Fatalf("a password may hold ':' and spaces: %v", err)
	}
}

// TestMatchFollowsPodman checks the lookup against containers/image's
// pkg/docker/config: literal prefixes of the normalized repository, then a
// registry-wide Docker Hub login for any of Docker Hub's names.
func TestMatchFollowsPodman(t *testing.T) {
	keys := []string{"docker.io", "docker.io/myorg", "ghcr.io/myorg", "ghcr.io/myorg/special", "registry.example.com:5000"}
	cases := map[string]string{
		"alpine":                                        "docker.io",
		"alpine:3.19":                                   "docker.io",
		"docker.io/library/alpine":                      "docker.io",
		"index.docker.io/myorg/app:1":                   "docker.io/myorg",
		"myorg/app":                                     "docker.io/myorg",
		"myorganisation/app":                            "docker.io",
		"registry-1.docker.io/myorg/app":                "docker.io",
		"ghcr.io/myorg/app:main":                        "ghcr.io/myorg",
		"ghcr.io/myorg/special:1":                       "ghcr.io/myorg/special",
		"ghcr.io/myorg/special/sub:1":                   "ghcr.io/myorg/special",
		"ghcr.io/myorgx/app":                            "",
		"ghcr.io/other/app":                             "",
		"registry.example.com:5000/team/app:v1":         "registry.example.com:5000",
		"registry.example.com/team/app":                 "",
		"quay.io/app@sha256:" + strings.Repeat("a", 64): "",
		"not a reference":                               "",
	}
	for ref, want := range cases {
		if got := MatchIn(ref, keys); got != want {
			t.Errorf("MatchIn(%q) = %q, want %q", ref, got, want)
		}
	}
	if got := MatchIn("alpine", []string{"docker.io/myorg"}); got != "" {
		t.Errorf("a namespace login matched an official image: %q", got)
	}
	if got := MatchIn("alpine", []string{"docker.io/library"}); got != "docker.io/library" {
		t.Errorf("docker.io/library did not match alpine: %q", got)
	}
}

func TestMatchNameTakesGoContainerregistrySpelling(t *testing.T) {
	has := func(key string) bool { return key == "docker.io/myorg" || key == "docker.io" }
	if got := MatchName("index.docker.io/myorg/app", has); got != "docker.io/myorg" {
		t.Fatalf("got %q", got)
	}
	if got := MatchName("index.docker.io", has); got != "docker.io" {
		t.Fatalf("registry resource: got %q", got)
	}
}

func TestCheck(t *testing.T) {
	ok := []protocol.RegistryLogin{{Key: "ghcr.io", Username: "u", Password: []byte("p")}}
	if err := Check(ok); err != nil {
		t.Fatal(err)
	}
	cases := [][]protocol.RegistryLogin{
		{{Key: "GHCR.io", Username: "u", Password: []byte("p")}},
		{{Key: "ghcr.io", Username: "u", Password: []byte("p")}, {Key: "ghcr.io", Username: "v", Password: []byte("q")}},
		{{Key: "ghcr.io", Username: "a:b", Password: []byte("p")}},
		{{Key: "https://ghcr.io", Username: "u", Password: []byte("p")}},
	}
	for _, logins := range cases {
		if Check(logins) == nil {
			t.Errorf("Check accepted %+v", logins)
		}
	}
	many := make([]protocol.RegistryLogin, MaxLogins+1)
	if !errors.Is(Check(many), ErrTooManyLogin) {
		t.Error("Check accepted too many logins")
	}
}

func TestScrubberRemovesPasswordsAndTheirBasicForm(t *testing.T) {
	logins := []protocol.RegistryLogin{{Key: "ghcr.io", Username: "bot", Password: []byte("s3cret-token")}}
	basic := BasicAuth("bot", []byte("s3cret-token"))
	text := "Error: denied: token s3cret-token rejected; header Basic " + basic
	got := NewScrubber(logins).Scrub(text)
	if strings.Contains(got, "s3cret-token") || strings.Contains(got, basic) {
		t.Fatalf("scrubbed text still holds a secret: %q", got)
	}
	if !strings.Contains(got, Mask) {
		t.Fatalf("no mask in %q", got)
	}
	var nilScrubber *Scrubber
	if nilScrubber.Scrub("x") != "x" {
		t.Fatal("nil scrubber changed text")
	}
}

// TestLargestSetFitsOneMessage checks the limits against the relay's
// message size, with JSON's escapes at their worst.
func TestLargestSetFitsOneMessage(t *testing.T) {
	logins := make([]protocol.RegistryLogin, MaxLogins)
	for i := range logins {
		logins[i] = protocol.RegistryLogin{Key: strings.Repeat("k", MaxKey), Username: strings.Repeat("<", MaxUsername), Password: []byte(strings.Repeat("<", MaxPassword))}
	}
	payload, err := json.Marshal(protocol.RegistrySync{SetID: strings.Repeat("f", 32), Logins: logins})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > protocol.MaxMessageSize/2 {
		t.Fatalf("largest set encodes to %d bytes, more than half of %d", len(payload), protocol.MaxMessageSize)
	}
}

func TestRefused(t *testing.T) {
	cases := map[string]bool{
		"Error: initializing source docker://x: reading manifest 1 in docker.io/a/b: requested access to the resource is denied": true,
		"Error: reading manifest main in ghcr.io/a/b: denied":                                                                    true,
		"denied: denied":                        true,
		"unauthorized: authentication required": true,
		"invalid username/password: unauthorized: incorrect username or password":             true,
		"received unexpected HTTP status: 403 Forbidden":                                      true,
		"Error: requesting bearer token: invalid status code from registry 400 (Bad Request)": true,
		"Error: open /home/u/.local/share/containers: permission denied":                      false,
		"mkdir /run/user/1000/libpod: permission denied: x":                                   false,
		"dial tcp 10.0.0.1:443: i/o timeout":                                                  false,
		"x509: certificate signed by unknown authority":                                       false,
	}
	for output, want := range cases {
		if got := Refused(output); got != want {
			t.Errorf("Refused(%q) = %v, want %v", output, got, want)
		}
	}
}

func TestUsernameLowercasesOnlyDockerHub(t *testing.T) {
	for _, c := range []struct{ key, username, want string }{
		{"docker.io", "Aeolun", "aeolun"},
		{"docker.io/myorg", "MyBot", "mybot"},
		{"ghcr.io/myorg", "MyBot", "MyBot"},
		{"registry.example.com:5000", "Admin", "Admin"},
	} {
		if got := Username(c.key, c.username); got != c.want {
			t.Errorf("Username(%q, %q) = %q, want %q", c.key, c.username, got, c.want)
		}
	}
}

func TestDescribeLeavesThePasswordOut(t *testing.T) {
	got := Describe(protocol.RegistryLogin{Key: "docker.io", Username: "aeolun", Password: []byte("dckr_pat_secret")})
	if got != `docker.io (user "aeolun", password 15 bytes)` {
		t.Fatalf("Describe = %q", got)
	}
	padded := Describe(protocol.RegistryLogin{Key: "ghcr.io", Username: "bot", Password: []byte(" token\n")})
	if !strings.Contains(padded, "7 bytes, with whitespace around it") || strings.Contains(padded, "token") {
		t.Fatalf("Describe = %q", padded)
	}
}
