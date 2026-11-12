package protocol

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestFrameDecodesLikeEncode: a frame, joined, decodes to the message Encode
// would send, with and without a payload and with an error.
func TestFrameDecodesLikeEncode(t *testing.T) {
	payload, err := json.Marshal(RegistrySync{SetID: "s", Logins: []RegistryLogin{{Key: "docker.io", Username: "u", Password: []byte(`"<&>"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []Message{
		{ID: "a", Type: TypeRequest, Operation: "registries.sync", HostID: "h", TimeoutMS: 5, Payload: payload},
		{ID: "b", Type: TypeCancel},
		{ID: "c", Type: TypeResponse, Payload: json.RawMessage(`{}`), Error: &Error{Code: CodeBusy, Message: "x"}},
	} {
		frame, err := EncodeFrame(m)
		if err != nil {
			t.Fatal(err)
		}
		whole, err := Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decode(frame.Bytes())
		if err != nil {
			t.Fatalf("%s: %v (%s)", m.ID, err, frame.Bytes())
		}
		want, _ := Decode(whole)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: frame decodes to %+v, Encode to %+v", m.ID, got, want)
		}
		if frame.Len() != len(frame.Bytes()) {
			t.Fatalf("%s: Len %d, bytes %d", m.ID, frame.Len(), len(frame.Bytes()))
		}
	}
}

// TestFrameSharesThePayload: the payload is not copied, so one encoding can
// serve many frames.
func TestFrameSharesThePayload(t *testing.T) {
	payload := json.RawMessage(`{"set_id":"s"}`)
	frame, err := EncodeFrame(Message{ID: "a", Type: TypeRequest, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if &frame.Payload[0] != &payload[0] {
		t.Fatal("the frame copied its payload")
	}
}

// TestFrameSizeLimit: a frame over MaxMessageSize is refused, as Encode
// refuses the message.
func TestFrameSizeLimit(t *testing.T) {
	m := Message{ID: "a", Type: TypeRequest}
	head, err := EncodeFrame(m)
	if err != nil {
		t.Fatal(err)
	}
	// The largest payload that fits: the head, `,"payload":` and the payload's two quotes, plus "}".
	room := MaxMessageSize - (len(head.Head) - 1 + len(`,"payload":`) + 1) - 2
	fits := json.RawMessage(`"` + strings.Repeat("x", room) + `"`)
	m.Payload = fits
	if frame, err := EncodeFrame(m); err != nil || frame.Len() != MaxMessageSize {
		t.Fatalf("a frame of exactly the limit: %v, %d bytes", err, frame.Len())
	}
	m.Payload = json.RawMessage(`"` + strings.Repeat("x", room+1) + `"`)
	if _, err := EncodeFrame(m); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("a frame one byte over the limit: %v", err)
	}
	if _, err := Encode(m); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("Encode of the same message: %v", err)
	}
}
