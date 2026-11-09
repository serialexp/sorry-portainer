package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// maxEventLine bounds one line of `podman events --format json`. A line is one
// event with the container's labels, normally well under 4 KiB.
const maxEventLine = 256 << 10

// activity is what Podman's events say happened to one container since the
// start of the window.
type activity struct {
	restarts      int
	lastRestart   time.Time
	lastUnhealthy time.Time
}

// eventJSON is one event line. Podman 4 writes "Time" as an RFC 3339 string;
// Podman 5 and later write "time" in seconds and "timeNano". encoding/json
// matches keys without regard to case, so Time takes either.
type eventJSON struct {
	ID       string          `json:"ID"`
	Status   string          `json:"Status"`
	Time     json.RawMessage `json:"time"`
	TimeNano int64           `json:"timeNano"`
	Health   string          `json:"health_status"`
}

func (e *eventJSON) at() (time.Time, error) {
	if e.TimeNano > 0 {
		return time.Unix(0, e.TimeNano), nil
	}
	raw := bytes.TrimSpace(e.Time)
	if len(raw) == 0 {
		return time.Time{}, errors.New("event has no time")
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return time.Time{}, err
		}
		return time.Parse(time.RFC3339Nano, text)
	}
	seconds, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(seconds, 0), nil
}

// eventSink folds `podman events --format json` output into per-container
// activity as it arrives, so memory stays proportional to the containers with
// events, not to the number of events.
type eventSink struct {
	mu      sync.Mutex
	partial []byte
	byID    map[string]*activity
	err     error
	cancel  context.CancelFunc
}

func newEventSink(cancel context.CancelFunc) *eventSink {
	return &eventSink{byID: make(map[string]*activity), cancel: cancel}
}

func (s *eventSink) Write(p []byte) (int, error) {
	written := len(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return written, nil
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if len(s.partial)+len(p) > maxEventLine {
				s.fail(fmt.Errorf("podman events: a line exceeds %d bytes", maxEventLine))
				return written, nil
			}
			s.partial = append(s.partial, p...)
			break
		}
		line := p[:i]
		if len(s.partial) != 0 {
			if len(s.partial)+len(line) > maxEventLine {
				s.fail(fmt.Errorf("podman events: a line exceeds %d bytes", maxEventLine))
				return written, nil
			}
			line = append(s.partial, line...)
		}
		if err := s.line(line); err != nil {
			s.fail(err)
			return written, nil
		}
		s.partial = s.partial[:0]
		p = p[i+1:]
	}
	return written, nil
}

// fail records the first error and stops Podman; the caller must hold mu.
func (s *eventSink) fail(err error) {
	s.err = err
	s.cancel()
}

func (s *eventSink) line(line []byte) error {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	var e eventJSON
	if err := json.Unmarshal(line, &e); err != nil {
		return fmt.Errorf("decode podman events JSON: %w", err)
	}
	if e.ID == "" || e.Status != "restart" && e.Status != "health_status" {
		return nil
	}
	at, err := e.at()
	if err != nil {
		return fmt.Errorf("decode podman events JSON: %s event of %s: %w", e.Status, e.ID, err)
	}
	a := s.byID[e.ID]
	if a == nil {
		a = &activity{}
		s.byID[e.ID] = a
	}
	switch {
	case e.Status == "restart":
		a.restarts++
		if at.After(a.lastRestart) {
			a.lastRestart = at
		}
	case e.Health == "unhealthy":
		if at.After(a.lastUnhealthy) {
			a.lastUnhealthy = at
		}
	}
	return nil
}

// finish decodes a last line without a newline and returns the activity.
func (s *eventSink) finish() (map[string]*activity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil && len(s.partial) != 0 {
		if err := s.line(s.partial); err != nil {
			s.err = err
		}
	}
	return s.byID, s.err
}

func (s *eventSink) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// recentActivity reads the container restarts and health results of the last
// protocol.RecentWindow from Podman's events, in one call.
func (c *Client) recentActivity(ctx context.Context, now time.Time) (map[string]*activity, error) {
	since := now.Add(-protocol.RecentWindow).Format(time.RFC3339Nano)
	var sink *eventSink
	err := c.runWith(ctx, func(cancel context.CancelFunc) outputSink {
		sink = newEventSink(cancel)
		return sink
	}, "events", "--stream=false", "--since", since,
		"--filter", "event=restart", "--filter", "event=health_status", "--format", "json")
	if err != nil {
		return nil, err
	}
	return sink.finish()
}

// recent is a's protocol form at now, given the container's restart-policy
// restart count. Podman resets that count when a user starts the container,
// so restart events beyond it were restarts by a user.
func (a *activity) recent(now time.Time, restarts int) protocol.Recent {
	var r protocol.Recent
	if a == nil {
		return r
	}
	r.RecentRestarts = min(a.restarts, restarts)
	if r.RecentRestarts > 0 {
		r.LastRestartAgo = secondsAgo(now, a.lastRestart)
	}
	if !a.lastUnhealthy.IsZero() {
		r.UnhealthyAgo = secondsAgo(now, a.lastUnhealthy)
	}
	return r
}

// secondsAgo is the whole seconds from t to now, rounded up and at least 1.
func secondsAgo(now, t time.Time) int64 {
	d := now.Sub(t)
	if d <= time.Second {
		return 1
	}
	return int64((d + time.Second - 1) / time.Second)
}
