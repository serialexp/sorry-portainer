package imagecheck

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// remoteRegistry asks real registries, signed in with the agent's registry
// logins.
type remoteRegistry struct {
	keychain  authn.Keychain
	platform  v1.Platform
	options   []name.Option
	transport http.RoundTripper
}

// NewRemoteRegistry returns the Registry that agents use. keychain holds the
// logins the master pushed (the same ones Podman reads), so the check and a
// pull always sign in alike. Requests go over HTTPS only: go-containerregistry
// would otherwise also try plain HTTP for private-network, loopback and .local
// registries and could send a login unencrypted; Podman likewise requires TLS
// by default. The build time is read for the agent's own platform.
func NewRemoteRegistry(keychain authn.Keychain) Registry {
	return remoteRegistry{keychain: keychain, platform: v1.Platform{OS: "linux", Architecture: runtime.GOARCH}, transport: HTTPSOnly(remote.DefaultTransport)}
}

// HTTPSOnly wraps next so that any request that is not https:// fails
// before it is sent.
func HTTPSOnly(next http.RoundTripper) http.RoundTripper {
	return httpsOnly{next: next}
}

type httpsOnly struct{ next http.RoundTripper }

func (h httpsOnly) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" {
		return nil, fmt.Errorf("refusing a plain %s request to %s: registries are reached over HTTPS only", request.URL.Scheme, request.URL.Host)
	}
	return h.next.RoundTrip(request)
}

// remoteOptions are the go-containerregistry options of every request.
func (r remoteRegistry) remoteOptions(ctx context.Context) []remote.Option {
	options := []remote.Option{remote.WithContext(ctx), remote.WithAuthFromKeychain(r.keychain)}
	if r.transport != nil {
		options = append(options, remote.WithTransport(r.transport))
	}
	return options
}

func (r remoteRegistry) Head(ctx context.Context, ref string) (string, error) {
	parsed, err := name.ParseReference(ref, r.options...)
	if err != nil {
		return "", err
	}
	descriptor, err := remote.Head(parsed, r.remoteOptions(ctx)...)
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
	image, err := remote.Image(pinned, append(r.remoteOptions(ctx), remote.WithPlatform(r.platform))...)
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
