package protocol

import "encoding/json"

// Frame is an encoded message in three parts, with its payload kept as given
// rather than copied into the message: one encoded payload can go out in many
// messages. Written in order, Head, Payload and Tail form the same JSON
// object Encode produces, with the payload last.
type Frame struct {
	Head, Payload, Tail []byte
}

// Len is the encoded message's size.
func (f Frame) Len() int { return len(f.Head) + len(f.Payload) + len(f.Tail) }

// Bytes joins the parts, for tests and callers that need one buffer.
func (f Frame) Bytes() []byte {
	out := make([]byte, 0, f.Len())
	out = append(out, f.Head...)
	out = append(out, f.Payload...)
	return append(out, f.Tail...)
}

var frameTail = []byte("}")

// EncodeFrame is Encode without copying m.Payload, which must be valid JSON
// as encoding/json writes it (callers marshal it; it is not checked again).
// The frame shares m.Payload, so the payload must not change until the frame
// is written.
func EncodeFrame(m Message) (Frame, error) {
	if m.Version == 0 {
		m.Version = Version
	}
	payload := m.Payload
	m.Payload = nil
	head, err := json.Marshal(m)
	if err != nil {
		return Frame{}, err
	}
	f := Frame{Head: head}
	if len(payload) > 0 {
		// head is a JSON object with at least "version"; reopen it for the payload.
		f.Head = append(head[:len(head)-1], `,"payload":`...)
		f.Payload, f.Tail = payload, frameTail
	}
	if f.Len() > MaxMessageSize {
		return Frame{}, ErrMessageTooLarge
	}
	return f, nil
}

// EncodedRegistrySync is a RegistrySync encoded once, so a round of pushes
// sends the same bytes to every host. JSON holds the passwords: Clear it once
// every push that uses it has been written.
type EncodedRegistrySync struct {
	SetID string
	JSON  json.RawMessage
}

func EncodeRegistrySync(set RegistrySync) (EncodedRegistrySync, error) {
	data, err := json.Marshal(set)
	if err != nil {
		return EncodedRegistrySync{}, err
	}
	return EncodedRegistrySync{SetID: set.SetID, JSON: data}, nil
}

// Clear overwrites the encoded passwords.
func (e EncodedRegistrySync) Clear() { clear(e.JSON) }
