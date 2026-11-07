package podman

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// RemoveVolume removes one volume of this agent's prefix. Podman refuses a
// volume that a container uses, and that refusal is returned unchanged.
func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("volume name is required")
	}
	if c.prefix != "" && !strings.HasPrefix(name, c.prefix) {
		return fmt.Errorf("volume %q is outside agent prefix %q", name, c.prefix)
	}
	if _, err := c.run(ctx, "volume", "rm", "--", name); err != nil {
		return err
	}
	c.sizes.forget(name)
	return nil
}

// RemoveImage removes one image and every tag it has. Nothing is forced: an
// image that any container runs, in this prefix or outside it, is refused before
// a tag is touched, so a refusal never leaves the image half removed.
//
// Podman refuses to remove an image by ID while it has several tags, so those
// are removed one by one by name; the last one deletes the image. If a removal
// fails after some tags went, the error says how many, and the rest stay.
func (c *Client) RemoveImage(ctx context.Context, id string) error {
	if !imageIDPattern.MatchString(id) {
		return fmt.Errorf("image ID %q is not a hexadecimal image ID", id)
	}
	data, err := c.run(ctx, "image", "inspect", "--format", "json", "--", id)
	if err != nil {
		return err
	}
	items, err := decodeJSON[[]imageInspectJSON]("image inspect", data)
	if err != nil {
		return err
	}
	if len(items) != 1 || items[0].ID == "" {
		return fmt.Errorf("podman image inspect returned no unique image for %q", id)
	}
	image := items[0]
	users, err := c.imageUsers(ctx)
	if err != nil {
		return err
	}
	if usage := users[image.ID]; len(usage.containers) > 0 || usage.elsewhere {
		return fmt.Errorf("image %s is in use by a container", shortID(image.ID))
	}
	tags := namedTags(image.RepoTags)
	if len(tags) <= 1 {
		_, err := c.run(ctx, "rmi", "--", image.ID)
		return err
	}
	for removed, tag := range tags {
		if _, err := c.run(ctx, "rmi", "--", tag); err != nil {
			return fmt.Errorf("image %s: removed %d of %d tags before %q failed: %w", shortID(image.ID), removed, len(tags), tag, err)
		}
	}
	return nil
}

// shortID is the twelve-character form of an image ID, as Podman prints it.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// namedTags returns the tags of an image that have a name, without the
// placeholder Podman prints for an untagged image.
func namedTags(repoTags []string) []string {
	out := make([]string, 0, len(repoTags))
	for _, tag := range repoTags {
		if tag != "" && tag != "<none>:<none>" {
			out = append(out, tag)
		}
	}
	return out
}
