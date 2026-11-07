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

// RemoveImage removes one image by its ID. The removal is not forced: Podman
// refuses an image that a container uses, and an image with several tags, and
// those refusals are returned unchanged.
func (c *Client) RemoveImage(ctx context.Context, id string) error {
	if !imageIDPattern.MatchString(id) {
		return fmt.Errorf("image ID %q is not a hexadecimal image ID", id)
	}
	_, err := c.run(ctx, "rmi", "--", id)
	return err
}
