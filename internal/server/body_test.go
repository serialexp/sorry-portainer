package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func shortBodyTimeout(t *testing.T) {
	t.Helper()
	saved := bodyReadTimeout
	bodyReadTimeout = 200 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = saved })
}

// TestBodyReadIsBounded: a client that stops sending its body part-way is
// cut off, although the web server has no ReadTimeout.
func TestBodyReadIsBounded(t *testing.T) {
	shortBodyTimeout(t)
	decoded := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]string
		decoded <- jsonDecode(w, r, &v)
	}))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"a\":")
	select {
	case err := <-decoded:
		if err == nil {
			t.Fatal("decoded a body that never arrived")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the body read was not bounded")
	}
}

// TestBodyDeadlineLiftedForLongHandlers: once the body is read, a handler may
// run past bodyReadTimeout without its request being cancelled. The token
// request decoder reads to the end of the body, which starts the server's
// background read on the connection; net/http clears the deadline then, and
// a deadline left in place would cancel the request.
func TestBodyDeadlineLiftedForLongHandlers(t *testing.T) {
	shortBodyTimeout(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v TokenRequest
		if err := decodeTokenRequest(w, r, &v); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		select {
		case <-r.Context().Done():
			http.Error(w, "cancelled", http.StatusServiceUnavailable)
		case <-time.After(3 * bodyReadTimeout):
			fmt.Fprint(w, v.HostID)
		}
	}))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	body := `{"host_id":"done"}`
	fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got strings.Builder
	_, _ = bufio.NewReader(resp.Body).WriteTo(&got)
	if resp.StatusCode != http.StatusOK || got.String() != "done" {
		t.Fatalf("status %d body %q", resp.StatusCode, got.String())
	}
}
