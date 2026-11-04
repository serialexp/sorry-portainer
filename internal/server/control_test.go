package server

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestFailureLogLimitsPerAddressAndIsBounded(t *testing.T) {
	var lines []string
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	f := newFailureLog(2, time.Minute, func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	f.now = func() time.Time { return now }
	from := func(addr string) *http.Request { return &http.Request{RemoteAddr: addr + ":1234"} }

	f.log(from("192.0.2.1"), "refused")
	f.log(from("192.0.2.1"), "refused")
	f.log(from("192.0.2.1"), "refused")
	if len(lines) != 1 {
		t.Fatalf("%d lines within the interval: %v", len(lines), lines)
	}
	now = now.Add(time.Minute)
	f.log(from("192.0.2.1"), "refused")
	if len(lines) != 2 || lines[1] != "control 192.0.2.1: refused (2 similar from this address suppressed)" {
		t.Fatalf("lines %v", lines)
	}
	// The table holds two addresses; a third drops the oldest.
	f.log(from("192.0.2.2"), "refused")
	f.log(from("192.0.2.3"), "refused")
	if len(f.entries) != 2 || f.entries["192.0.2.1"] != nil {
		t.Fatalf("table %v", f.entries)
	}
}
