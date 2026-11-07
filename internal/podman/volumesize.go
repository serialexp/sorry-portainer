package podman

import (
	"context"
	"sync"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// DefaultVolumeSizeInterval is the time between two volume size scans. Volume
// sizes change slowly, so a scan every half hour is close enough for a listing.
const DefaultVolumeSizeInterval = 30 * time.Minute

// volumeSizes holds the latest measured size of each volume. Podman 4.9 cannot
// report a volume's size, so a background scan measures it and the listings
// read the stored values: a listing never waits for a walk of the files.
type volumeSizes struct {
	mu      sync.RWMutex
	entries map[string]volumeSize
}

type volumeSize struct {
	bytes      int64
	measuredAt time.Time
	partial    bool
}

func (s *volumeSizes) get(name string) (volumeSize, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.entries[name]
	return entry, ok
}

func (s *volumeSizes) store(name string, entry volumeSize) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]volumeSize)
	}
	s.entries[name] = entry
}

// forget drops the size of a volume that was removed.
func (s *volumeSizes) forget(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, name)
}

// keepOnly drops the sizes of volumes that are no longer listed.
func (s *volumeSizes) keepOnly(listed map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range s.entries {
		if _, ok := listed[name]; !ok {
			delete(s.entries, name)
		}
	}
}

// applySize copies the stored size of a volume into its listing entry.
func (c *Client) applySize(volume *protocol.Volume) {
	size, ok := c.sizes.get(volume.Name)
	if !ok {
		return
	}
	bytes := size.bytes
	volume.SizeBytes = &bytes
	volume.SizeMeasuredAt = size.measuredAt.UTC().Format(time.RFC3339)
	volume.SizePartial = size.partial
}

// StartVolumeSizeScan measures the volumes of this agent's prefix now, and then
// every interval, until ctx ends. A volume whose measurement fails keeps its
// previous size; logf receives the reason.
func (c *Client) StartVolumeSizeScan(ctx context.Context, interval time.Duration, logf func(format string, args ...any)) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			c.measureVolumeSizes(ctx, logf)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// measureVolumeSizes walks each volume of this agent's prefix once, one volume
// at a time, so the scan adds at most one walk's worth of disk reads at once.
func (c *Client) measureVolumeSizes(ctx context.Context, logf func(format string, args ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	volumes, err := c.listVolumes(ctx)
	if err != nil {
		logf("volume size scan: %v", err)
		return
	}
	listed := make(map[string]struct{}, len(volumes))
	for i := range volumes {
		listed[volumes[i].Name] = struct{}{}
	}
	for i := range volumes {
		volume := &volumes[i]
		if ctx.Err() != nil {
			return
		}
		if volume.Mountpoint == "" {
			continue
		}
		tree, err := directorySize(ctx, volume.Mountpoint)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logf("volume %s: size not measured: %v", volume.Name, err)
			continue
		}
		c.sizes.store(volume.Name, volumeSize{bytes: tree.bytes, measuredAt: time.Now(), partial: tree.partial})
	}
	c.sizes.keepOnly(listed)
}
