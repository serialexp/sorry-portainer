// Package registries holds the rules for registry logins that the master and
// the agents share: what a login key may be, which login an image uses, the
// auth.json Podman reads, and scrubbing passwords out of output.
//
// Matching follows containers/image (pkg/docker/config), so the image check
// and Podman always pick the same login. See docs/design/registry-logins.md.
package registries

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// Limits of one login and of the set. With passwords sent as base64, the
// largest set encodes to well under protocol.MaxMessageSize.
const (
	MaxKey      = 255
	MaxUsername = 255
	MaxPassword = 4096
	MaxLogins   = 64
)

// DockerHub is how a Docker Hub key is stored and written to auth.json.
const DockerHub = "docker.io"

var (
	ErrKeyEmpty     = errors.New("enter a registry, like ghcr.io, or a namespace, like docker.io/myorg")
	ErrKeyLong      = fmt.Errorf("a registry or namespace is at most %d characters", MaxKey)
	ErrKeyScheme    = errors.New("leave out http:// or https:// and write the registry name only, like ghcr.io/myorg")
	ErrKeySpace     = errors.New("a registry or namespace has no spaces")
	ErrKeyRef       = errors.New("a login is for a registry or namespace, not one image: leave out the tag or digest")
	ErrKeyPath      = errors.New("use a registry host, optionally followed by a path, like registry.example.net/team")
	ErrUsername     = fmt.Errorf("the username must be 1 to %d characters, without ':' or control characters", MaxUsername)
	ErrPassword     = fmt.Errorf("the password or token must be 1 to %d bytes, without control characters", MaxPassword)
	ErrTooManyLogin = fmt.Errorf("at most %d registry logins can be saved", MaxLogins)
)

// pathComponent is one repository path component, as the distribution spec
// allows it.
var pathComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)

// dockerHubAliases are the names Podman treats as Docker Hub for a
// registry-wide login (containers/image normalizeRegistry).
var dockerHubAliases = map[string]bool{"docker.io": true, "index.docker.io": true, "registry-1.docker.io": true}

// NormalizeKey checks a login key and returns it in its stored spelling. It
// rejects rather than rewrites anything but the registry's case and Docker
// Hub's aliases, so a mistyped key is reported instead of silently changed.
func NormalizeKey(raw string) (string, error) {
	switch {
	case raw == "":
		return "", ErrKeyEmpty
	case len(raw) > MaxKey:
		return "", ErrKeyLong
	case strings.Contains(raw, "://"):
		return "", ErrKeyScheme
	case strings.IndexFunc(raw, unicode.IsSpace) >= 0:
		return "", ErrKeySpace
	case strings.ContainsAny(raw, "@"):
		return "", ErrKeyRef
	}
	registry, path, hasPath := strings.Cut(raw, "/")
	registry = strings.ToLower(registry)
	if registry == "" || !validRegistry(registry) {
		return "", ErrKeyPath
	}
	if dockerHubAliases[registry] {
		registry = DockerHub
	}
	if !hasPath {
		return registry, nil
	}
	for _, segment := range strings.Split(path, "/") {
		if strings.Contains(segment, ":") {
			return "", ErrKeyRef
		}
		if !pathComponent.MatchString(segment) {
			return "", ErrKeyPath
		}
	}
	return registry + "/" + path, nil
}

func validRegistry(registry string) bool {
	if strings.ContainsAny(registry, "[]") {
		return false
	}
	_, err := name.NewRegistry(registry, name.StrictValidation)
	return err == nil
}

// Username is how a login's username is stored and sent. Docker Hub IDs are
// lowercase only, but its sign-in accepts any capitalisation, while its token
// service answers a capitalised name with 400 "malformed HTTP Authorization
// header" (a login saved as "Aeolun" passed `podman login` and then failed
// every pull). So a Docker Hub username is lowercased; others are kept as
// typed, since other registries may tell case apart.
func Username(key, username string) string {
	if RegistryOf(key) == DockerHub {
		return strings.ToLower(username)
	}
	return username
}

// Describe sums a login up for a log line without its password: the key, the
// username, the password's length, and whether it has whitespace around it
// (a pasted token often does).
func Describe(login protocol.RegistryLogin) string {
	note := ""
	if trimmed := strings.TrimSpace(string(login.Password)); len(trimmed) != len(login.Password) {
		note = ", with whitespace around it"
	}
	return fmt.Sprintf("%s (user %q, password %d bytes%s)", login.Key, login.Username, len(login.Password), note)
}

// RegistryOf is the registry part of a stored key.
func RegistryOf(key string) string {
	registry, _, _ := strings.Cut(key, "/")
	return registry
}

// ValidCredentials checks a username and password.
func ValidCredentials(username string, password []byte) error {
	if !validText(username, MaxUsername) || strings.Contains(username, ":") {
		return ErrUsername
	}
	if !validText(string(password), MaxPassword) {
		return ErrPassword
	}
	return nil
}

func validText(s string, max int) bool {
	if len(s) == 0 || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	return strings.IndexFunc(s, unicode.IsControl) < 0
}

// Check validates a whole set as an agent receives it.
func Check(logins []protocol.RegistryLogin) error {
	if len(logins) > MaxLogins {
		return ErrTooManyLogin
	}
	seen := make(map[string]bool, len(logins))
	for _, login := range logins {
		key, err := NormalizeKey(login.Key)
		if err != nil {
			return fmt.Errorf("login %q: %w", login.Key, err)
		}
		if key != login.Key {
			return fmt.Errorf("login %q is not in its stored spelling %q", login.Key, key)
		}
		if seen[key] {
			return fmt.Errorf("login %q appears twice", key)
		}
		seen[key] = true
		if err := ValidCredentials(login.Username, login.Password); err != nil {
			return fmt.Errorf("login %q: %w", key, err)
		}
	}
	return nil
}

// Repository is the name Podman looks logins up by for an image reference:
// the repository without tag or digest, Docker Hub spelled docker.io
// ("alpine" is "docker.io/library/alpine").
func Repository(ref string) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", err
	}
	repository := parsed.Context().Name()
	if rest, ok := strings.CutPrefix(repository, name.DefaultRegistry+"/"); ok {
		repository = DockerHub + "/" + rest
	}
	return repository, nil
}

// Match returns the key of the login Podman uses for the image ref, or "".
// has reports whether a key is saved.
func Match(ref string, has func(key string) bool) string {
	repository, err := Repository(ref)
	if err != nil {
		return ""
	}
	return MatchName(repository, has)
}

// MatchName is Match for a repository or registry name as
// go-containerregistry spells it ("index.docker.io/myorg/app", or just a
// registry). Like containers/image, it tries the name and each shorter path
// literally, then a registry-wide Docker Hub login for any of Docker Hub's
// names.
func MatchName(repository string, has func(key string) bool) string {
	if repository == name.DefaultRegistry {
		repository = DockerHub
	} else if rest, ok := strings.CutPrefix(repository, name.DefaultRegistry+"/"); ok {
		repository = DockerHub + "/" + rest
	}
	for candidate := repository; ; {
		if has(candidate) {
			return candidate
		}
		slash := strings.LastIndex(candidate, "/")
		if slash < 0 {
			break
		}
		candidate = candidate[:slash]
	}
	if dockerHubAliases[RegistryOf(repository)] && has(DockerHub) {
		return DockerHub
	}
	return ""
}

// MatchIn is Match against a list of keys.
func MatchIn(ref string, keys []string) string {
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return Match(ref, func(key string) bool { return set[key] })
}

// BasicAuth is the auth.json "auth" value: base64 of "user:password".
func BasicAuth(username string, password []byte) string {
	plain := make([]byte, 0, len(username)+1+len(password))
	plain = append(plain, username...)
	plain = append(plain, ':')
	plain = append(plain, password...)
	encoded := base64.StdEncoding.EncodeToString(plain)
	clear(plain)
	return encoded
}

// Scrubber replaces every password of a set, and its base64 user:password
// form, in text that may have come from a registry or Podman.
type Scrubber struct {
	secrets []string
}

// Mask is what a scrubbed password reads as.
const Mask = "[login]"

func NewScrubber(logins []protocol.RegistryLogin) *Scrubber {
	s := &Scrubber{}
	for _, login := range logins {
		if len(login.Password) > 0 {
			s.secrets = append(s.secrets, string(login.Password), BasicAuth(login.Username, login.Password))
		}
	}
	return s
}

func (s *Scrubber) Scrub(text string) string {
	if s == nil {
		return text
	}
	for _, secret := range s.secrets {
		text = strings.ReplaceAll(text, secret, Mask)
	}
	return text
}

// refusals are what Podman's errors say when a registry refuses a pull or a
// sign-in: no login, a wrong one, or one without access. "denied" is matched
// only as a registry error code (": denied", "denied: …"), never inside a
// local "permission denied".
var refusals = []string{"unauthorized", "invalid username/password", "incorrect username or password", "authentication required", " 401", " 403", "forbidden", ": denied", "denied:", "access to the resource is denied",
	// Docker Hub's token service answers a login it can't use with 400
	// ("Requesting bearer token: … 400 (Bad Request)").
	"400 (bad request)", "400 bad request"}

// Refused reports whether Podman's output says a registry refused the
// request for want of a login that works.
func Refused(output string) bool {
	text := strings.ReplaceAll(strings.ToLower(output), "permission denied", "")
	for _, needle := range refusals {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
