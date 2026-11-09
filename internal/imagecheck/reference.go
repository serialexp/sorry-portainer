package imagecheck

import (
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

// Kind is how a check treats an image reference.
type Kind string

const (
	// Tagged references are checked against their registry.
	Tagged Kind = "tagged"
	// Pinned references name a digest; they cannot move.
	Pinned Kind = "pinned"
	// Local references are images built or kept on the host only.
	Local Kind = "local"
)

// Classify parses ref as Podman would name it. A reference without a tag
// means ":latest" and one without a registry means docker.io, as for
// Docker; Podman's ps output is always fully qualified, so only a Compose
// file's short names rely on that default.
func Classify(ref string) (Kind, error) {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("invalid image reference %q", ref)
	}
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", err
	}
	if parsed.Context().RegistryStr() == "localhost" || strings.HasPrefix(parsed.Context().RegistryStr(), "localhost:") {
		return Local, nil
	}
	if _, ok := parsed.(name.Digest); ok {
		return Pinned, nil
	}
	return Tagged, nil
}

// Repository is ref's repository in one spelling for every way of writing
// it ("mysql", "docker.io/library/mysql" and "index.docker.io/library/mysql"
// are one repository), so Podman's repo digests compare with a reference.
func Repository(ref string) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", err
	}
	return parsed.Context().Name(), nil
}

// Canonical is ref in one spelling ("mysql:8", "docker.io/library/mysql:8"
// and "index.docker.io/library/mysql:8" are one name), to find the local
// image a Compose file's reference names among Podman's fully qualified tags.
func Canonical(ref string) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", err
	}
	return parsed.Name(), nil
}

// RegistryName is the registry ref lives in, as people write it: docker.io
// rather than index.docker.io.
func RegistryName(ref string) string {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return ""
	}
	registry := parsed.Context().RegistryStr()
	if registry == name.DefaultRegistry {
		return "docker.io"
	}
	return registry
}

// DigestsFor returns the digests among Podman's repo digests (each
// "repository@sha256:…") that belong to ref's repository.
func DigestsFor(ref string, repoDigests []string) []string {
	repository, err := Repository(ref)
	if err != nil {
		return nil
	}
	var out []string
	for _, repoDigest := range repoDigests {
		parsed, err := name.NewDigest(repoDigest)
		if err != nil || parsed.Context().Name() != repository {
			continue
		}
		out = append(out, parsed.DigestStr())
	}
	return out
}
