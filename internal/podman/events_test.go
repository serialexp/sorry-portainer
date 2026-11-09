package podman

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/serialexp/sorry-portainer/internal/protocol"
)

var eventsNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// podman4Event is a line as Podman 4 writes it: "Time" is an RFC 3339 string.
func podman4Event(id, status, health string, at time.Time) string {
	line := `{"ID":"` + id + `","Image":"example/web:1","Name":"x","Status":"` + status + `","Time":"` + at.Format(time.RFC3339Nano) + `","Type":"container"`
	if health != "" {
		line += `,"health_status":"` + health + `"`
	}
	return line + `,"Attributes":{"com.docker.compose.project":"owned-app"}}` + "\n"
}

// podman6Event is a line as Podman 5 and later write it: "time" in seconds and
// "timeNano".
func podman6Event(id, status, health string, at time.Time) string {
	line := `{"ID":"` + id + `","Image":"example/web:1","Name":"x","Status":"` + status + `","time":` +
		strconv.FormatInt(at.Unix(), 10) + `,"timeNano":` + strconv.FormatInt(at.UnixNano(), 10) + `,"Type":"container"`
	if health != "" {
		line += `,"health_status":"` + health + `"`
	}
	return line + `,"Attributes":{"com.docker.compose.project":"owned-app"}}` + "\n"
}

func TestEventSinkReadsBothPodmanFormats(t *testing.T) {
	for name, event := range map[string]func(id, status, health string, at time.Time) string{
		"podman 4": podman4Event,
		"podman 6": podman6Event,
	} {
		t.Run(name, func(t *testing.T) {
			sink := newEventSink(func() {})
			out := event("a", "restart", "", eventsNow.Add(-10*time.Minute)) +
				event("a", "health_status", "unhealthy", eventsNow.Add(-6*time.Minute)) +
				event("a", "restart", "", eventsNow.Add(-90*time.Second)) +
				event("a", "health_status", "unhealthy", eventsNow.Add(-8*time.Minute)) +
				event("a", "health_status", "healthy", eventsNow.Add(-30*time.Second)) +
				event("b", "health_status", "starting", eventsNow.Add(-time.Minute)) +
				// Other events are not activity.
				event("c", "died", "", eventsNow.Add(-time.Minute))
			// Podman's output arrives in arbitrary pieces.
			for len(out) > 0 {
				n := min(37, len(out))
				// Writers must report every byte as consumed, or exec's
				// copy stops with a short write.
				if written, err := sink.Write([]byte(out[:n])); err != nil || written != n {
					t.Fatalf("Write() = %d, %v; want %d", written, err, n)
				}
				out = out[n:]
			}
			got, err := sink.finish()
			if err != nil {
				t.Fatal(err)
			}
			a := got["a"]
			if a == nil || a.restarts != 2 || !a.lastRestart.Equal(eventsNow.Add(-90*time.Second)) || !a.lastUnhealthy.Equal(eventsNow.Add(-6*time.Minute)) {
				t.Fatalf("activity of a = %+v", a)
			}
			if b := got["b"]; b == nil || b.restarts != 0 || !b.lastUnhealthy.IsZero() {
				t.Fatalf("activity of b = %+v", b)
			}
			if _, ok := got["c"]; ok {
				t.Fatalf("a died event became activity: %+v", got["c"])
			}
		})
	}
}

func TestEventSinkDecodesALastLineWithoutNewline(t *testing.T) {
	sink := newEventSink(func() {})
	line := strings.TrimSuffix(podman6Event("a", "restart", "", eventsNow.Add(-time.Minute)), "\n")
	if _, err := sink.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	got, err := sink.finish()
	if err != nil || got["a"] == nil || got["a"].restarts != 1 {
		t.Fatalf("finish() = %+v, %v", got, err)
	}
}

func TestEventSinkStopsPodmanOnAnOverlongLine(t *testing.T) {
	cancelled := false
	sink := newEventSink(func() { cancelled = true })
	chunk := []byte(strings.Repeat("x", 64<<10))
	for i := 0; i < 5; i++ {
		if _, err := sink.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if !cancelled {
		t.Fatal("an overlong line did not cancel podman")
	}
	if err := sink.failure(); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("failure() = %v", err)
	}
	// It keeps no more than one line's bound.
	if len(sink.partial) > maxEventLine {
		t.Fatalf("partial line holds %d bytes", len(sink.partial))
	}
}

func TestEventSinkRejectsMalformedEvents(t *testing.T) {
	for name, line := range map[string]string{
		"not JSON":     "not json\n",
		"no time":      `{"ID":"a","Status":"restart"}` + "\n",
		"bad time":     `{"ID":"a","Status":"restart","Time":"yesterday"}` + "\n",
		"numeric junk": `{"ID":"a","Status":"restart","time":1.5}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			cancelled := false
			sink := newEventSink(func() { cancelled = true })
			_, _ = sink.Write([]byte(line))
			if _, err := sink.finish(); err == nil || !strings.Contains(err.Error(), "decode podman events JSON") {
				t.Fatalf("finish() error = %v", err)
			}
			if !cancelled {
				t.Fatal("a malformed event did not cancel podman")
			}
		})
	}
}

func TestRecentCapsRestartsAtThePolicyCount(t *testing.T) {
	a := &activity{restarts: 3, lastRestart: eventsNow.Add(-90 * time.Second), lastUnhealthy: eventsNow.Add(-100 * time.Millisecond)}
	if got, want := a.recent(eventsNow, 5), (protocol.Recent{RecentRestarts: 3, LastRestartAgo: 90, UnhealthyAgo: 1}); got != want {
		t.Fatalf("recent() = %+v, want %+v", got, want)
	}
	// A user restarted it since: Podman's count was reset to 1, so two of the
	// three restart events were not the restart policy's.
	if got := a.recent(eventsNow, 1); got.RecentRestarts != 1 {
		t.Fatalf("recent() after a user restart = %+v", got)
	}
	// Every restart event was a user's.
	if got := a.recent(eventsNow, 0); got.RecentRestarts != 0 || got.LastRestartAgo != 0 || got.UnhealthyAgo != 1 {
		t.Fatalf("recent() with no policy restarts = %+v", got)
	}
	var none *activity
	if got := none.recent(eventsNow, 4); got != (protocol.Recent{}) {
		t.Fatalf("recent() without events = %+v", got)
	}
}

func TestSecondsAgoRoundsUpToAtLeastOne(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want int64
	}{
		{0, 1}, {-time.Second, 1}, {time.Second, 1}, {1500 * time.Millisecond, 2}, {90 * time.Second, 90}, {15 * time.Minute, 900},
	} {
		if got := secondsAgo(eventsNow, eventsNow.Add(-tc.d)); got != tc.want {
			t.Errorf("secondsAgo(%v) = %d, want %d", tc.d, got, tc.want)
		}
	}
}

func eventsCall(now time.Time) []string {
	return []string{"events", "--stream=false", "--since", now.Add(-protocol.RecentWindow).Format(time.RFC3339Nano),
		"--filter", "event=restart", "--filter", "event=health_status", "--format", "json"}
}

func TestContainersReportRecentRestartsAndHealthFailures(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `[{"Id":"a","Names":["owned-web"],"Image":"example/web:1","State":"running","Status":"Up 1 second","Restarts":40},` +
			`{"Id":"b","Names":["owned-db"],"Image":"example/db:1","State":"running","Status":"Up 2 hours (healthy)"},` +
			`{"Id":"c","Names":["owned-cache"],"Image":"example/cache:1","State":"running","Status":"Up 2 hours"}]`},
		{stdout: podman6Event("a", "restart", "", eventsNow.Add(-3*time.Minute)) +
			podman6Event("a", "restart", "", eventsNow.Add(-2*time.Second)) +
			podman6Event("b", "health_status", "unhealthy", eventsNow.Add(-5*time.Minute)) +
			podman6Event("b", "health_status", "healthy", eventsNow.Add(-time.Minute)) +
			podman6Event("other", "restart", "", eventsNow.Add(-time.Minute))},
	}}
	client := NewWithRunner(runner, "owned-")
	client.now = func() time.Time { return eventsNow }
	containers, err := client.Containers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.Container{
		{ID: "a", Name: "owned-web", Image: "example/web:1", State: "running", Status: "Up 1 second", Restarts: 40,
			Recent: protocol.Recent{RecentRestarts: 2, LastRestartAgo: 2}},
		{ID: "b", Name: "owned-db", Image: "example/db:1", State: "running", Status: "Up 2 hours", Health: "healthy",
			Recent: protocol.Recent{UnhealthyAgo: 300}},
		{ID: "c", Name: "owned-cache", Image: "example/cache:1", State: "running", Status: "Up 2 hours"},
	}
	if !reflect.DeepEqual(containers, want) {
		t.Fatalf("Containers() = %+v, want %+v", containers, want)
	}
	if got := runner.calls[1]; !reflect.DeepEqual(got, eventsCall(eventsNow)) {
		t.Fatalf("events call = %q", got)
	}
}

func TestContainersWithoutContainersReadNoEvents(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `[{"Id":"x","Names":["other-web"],"Image":"example/web:1","State":"running","Status":"Up 1 second"}]`},
	}}
	containers, err := NewWithRunner(runner, "owned-").Containers(context.Background())
	if err != nil || len(containers) != 0 {
		t.Fatalf("Containers() = %+v, %v", containers, err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %q, want only ps", runner.calls)
	}
}

func TestContainersFailWhenEventsFail(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `[{"Id":"a","Names":["owned-web"],"Image":"example/web:1","State":"running","Status":"Up 1 second"}]`},
		{stderr: "Error: no events backend", err: errors.New("exit status 125")},
	}}
	_, err := NewWithRunner(runner, "owned-").Containers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "podman events") || !strings.Contains(err.Error(), "no events backend") {
		t.Fatalf("Containers() error = %v", err)
	}
}

func TestComposeContainersReportRecentActivity(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `[{"Id":"a","Names":["owned-app-web-1"],"Image":"example/web:1","State":"running","Status":"Up 1 second","Restarts":7,` +
			`"Labels":{"com.docker.compose.project":"owned-app","com.docker.compose.service":"web"}}]`},
		{stdout: podman4Event("a", "restart", "", eventsNow.Add(-4*time.Minute))},
	}}
	client := NewWithRunner(runner, "owned-")
	client.now = func() time.Time { return eventsNow }
	all, err := client.ComposeContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := all["owned-app"]; len(got) != 1 || got[0].Recent != (protocol.Recent{RecentRestarts: 1, LastRestartAgo: 240}) {
		t.Fatalf("ComposeContainers() = %+v", all)
	}
}

func TestProjectContainersReadNoEvents(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `[{"Id":"a","Names":["owned-app-web-1"],"Image":"example/web:1","State":"running","Status":"Up 1 second","Restarts":7,` +
			`"Labels":{"com.docker.compose.project":"owned-app","com.docker.compose.service":"web"}}]`},
	}}
	got, err := NewWithRunner(runner, "owned-").ProjectContainers(context.Background(), "owned-app")
	if err != nil || len(got) != 1 {
		t.Fatalf("ProjectContainers() = %+v, %v", got, err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %q, want only ps", runner.calls)
	}
}
