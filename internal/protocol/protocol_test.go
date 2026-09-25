package protocol

import (
	"encoding/json"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	b, e := Encode(Message{ID: "1", Type: "request", Operation: "containers.list", Payload: json.RawMessage(`{"limit":10}`)})
	if e != nil {
		t.Fatal(e)
	}
	m, e := Decode(b)
	if e != nil || m.Version != Version || m.Operation != "containers.list" {
		t.Fatalf("%+v %v", m, e)
	}
}
func TestLimit(t *testing.T) {
	_, e := Encode(Message{ID: "1", Type: "x", Payload: json.RawMessage(`"` + string(make([]byte, MaxMessageSize)) + `"`)})
	if e == nil {
		t.Fatal("expected limit")
	}
}
