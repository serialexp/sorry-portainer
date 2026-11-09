package imagecheck

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// remoteRegistry asks real registries, signed in with the credentials Podman
// (or Docker) has stored for the agent's user.
type remoteRegistry struct {
	keychain authn.Keychain
	platform v1.Platform
	options  []name.Option
}

// NewRemoteRegistry returns the Registry that agents use. Credentials come
// from authn.DefaultKeychain: ~/.docker/config.json, $REGISTRY_AUTH_FILE, or
// Podman's containers/auth.json under $XDG_RUNTIME_DIR or ~/.config. The
// build time is read for the agent's own platform.
func NewRemoteRegistry() Registry {
	return remoteRegistry{keychain: authn.DefaultKeychain, platform: v1.Platform{OS: "linux", Architecture: runtime.GOARCH}}
}

func (r remoteRegistry) Head(ctx context.Context, ref string) (string, error) {
	parsed, err := name.ParseReference(ref, r.options...)
	if err != nil {
		return "", err
	}
	descriptor, err := remote.Head(parsed, remote.WithContext(ctx), remote.WithAuthFromKeychain(r.keychain))
	if err != nil {
		return "", err
	}
	return descriptor.Digest.String(), nil
}

func (r remoteRegistry) Created(ctx context.Context, ref, digest string) (time.Time, error) {
	parsed, err := name.ParseReference(ref, r.options...)
	if err != nil {
		return time.Time{}, err
	}
	pinned := parsed.Context().Digest(digest)
	image, err := remote.Image(pinned, remote.WithContext(ctx), remote.WithAuthFromKeychain(r.keychain), remote.WithPlatform(r.platform))
	if err != nil {
		return time.Time{}, err
	}
	config, err := image.ConfigFile()
	if err != nil {
		return time.Time{}, err
	}
	if config.Created.IsZero() {
		return time.Time{}, fmt.Errorf("image %s has no build time", pinned)
	}
	return config.Created.Time, nil
}
