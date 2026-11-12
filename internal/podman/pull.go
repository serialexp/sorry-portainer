package podman

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// LocalImage is an image on the host as `podman images` lists it.
type LocalImage struct {
	ID string
	// Names are the image's tags, fully qualified.
	Names []string
	// RepoDigests are "repository@sha256:…" for each manifest Podman knows
	// the image by: for an image pulled through a multi-platform tag, both
	// the index's digest and the platform manifest's.
	RepoDigests []string
	// Digest is the digest Podman pulled the image by.
	Digest string
}

type localImageJSON struct {
	ID          string   `json:"Id"`
	Names       []string `json:"Names"`
	RepoDigests []string `json:"RepoDigests"`
	Digest      string   `json:"Digest"`
}

// LocalImages lists the host's tagged and untagged top-level images in one
// `podman images` call.
func (c *Client) LocalImages(ctx context.Context) ([]LocalImage, error) {
	data, err := c.run(ctx, "images", "--format", "json")
	if err != nil {
		return nil, err
	}
	items, err := decodeJSON[[]localImageJSON]("images", data)
	if err != nil {
		return nil, err
	}
	out := make([]LocalImage, len(items))
	for i := range items {
		item := &items[i]
		out[i] = LocalImage{ID: item.ID, Names: item.Names, RepoDigests: item.RepoDigests, Digest: item.Digest}
	}
	return out, nil
}

// Pull pulls ref and returns the ID of the image it now names. Podman's
// progress goes to stderr and is dropped; a failure carries it.
func (c *Client) Pull(ctx context.Context, ref string) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("invalid image reference %q", ref)
	}
	data, err := c.run(ctx, "pull", "--quiet", "--", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// ImageExists reports whether Podman has an image ref names, resolving it
// as `podman run` would (short names included), without asking a registry.
func (c *Client) ImageExists(ctx context.Context, ref string) (bool, error) {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return false, fmt.Errorf("invalid image reference %q", ref)
	}
	_, err := c.run(ctx, "image", "exists", "--", ref)
	if err == nil {
		return true, nil
	}
	// `podman image exists` exits 1 for a missing image, 125 for a failure.
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}
