package proto

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// The PROXIED-CHANNEL data plane: the bytes of an ssh channel the HELPER
// holds and the coordinator consumes, on their own frame type and their own
// identity.
//
// # Why a second identity, rather than the session one
//
// TypeSessionData keys raw bytes by a HOST SESSION (session_frame.go), and
// that identity is the helper's own PTY: it has a window, a subscriber, a
// lease epoch and an inventory entry because a session is something the
// helper OWNS. A proxied channel is none of those. It is a stream the helper
// opened on a pooled connection at somebody else's request — an SFTP
// subsystem today — and the only fact the two ends have to agree about is
// which stream a byte belongs to.
//
// Reusing the session identity for it would be the defect AD-8 names, in the
// direction that costs most: a channel id in the Session field would decode
// perfectly and be routed to the session service, which would look it up in
// an inventory that has never heard of it and DROP the bytes. A frame that
// decodes into the wrong router is worse than one that does not decode.
//
// So the type byte is new (TypeChannelData) and so is the layout below: this
// is not the frozen session layout with a different meaning, it is a smaller
// header that says exactly what it needs to and nothing a session needs.
//
// # Layout
//
//	bytes 0..15  channel-id   16 raw bytes
//	bytes 16..   payload      raw channel bytes
//
// Sixteen bytes rather than a name: the id is minted by the helper, opaque to
// the coordinator, and carried as 32 hex characters wherever a person or a
// JSON document sees it (ChannelID's own encoding). There is no offset, no
// subscriber and no epoch — a proxied channel has exactly ONE reader at each
// end by construction, because the end that opened it is the end that reads
// it — and no length: the frame header already carries one.
//
// # Where the END of a channel is, and why it is not here
//
// The stream ends with an explicit `ssh.channel-closed` notification, never
// with a zero-length frame. A payload exactly the length of the header is a
// legitimate write of no bytes (the session codec says so too, and a
// zero-length write is a thing a stream does), so inferring end-of-stream
// from it would make "the peer wrote nothing" and "the peer is gone"
// indistinguishable on a protocol where the difference decides whether SFTP
// waits or gives up.

// ChannelFrameHeaderLen is 16: the channel id, and nothing else.
const ChannelFrameHeaderLen = 16

// MaxChannelPayloadBytes is the largest payload ONE TypeChannelData frame can
// carry: MaxFrameBytes minus this frame's own header.
//
// It is stated here, beside the layout, rather than left to whoever sizes a
// read buffer — because getting it wrong by sixteen bytes is not a truncated
// transfer, it is an EncodeFrame PANIC in whichever process assembled the
// frame, and the arithmetic that avoids it is this file's business and not
// each caller's.
const MaxChannelPayloadBytes = MaxFrameBytes - ChannelFrameHeaderLen

// ErrChannelFrameTooShort reports a payload that cannot hold the header. Like
// ErrSessionFrameTooShort it is an answer, not a failure of the connection:
// the frame is dropped and the wire continues.
var ErrChannelFrameTooShort = errors.New("proto: channel frame shorter than its header")

// ChannelID identifies one proxied channel for the life of the connection
// that carries it. It is minted by the HELPER — the end that owns the channel
// — and echoed verbatim by the coordinator, which never interprets it.
//
// It is deliberately not an authorization: same-UID trust already decides who
// may connect (abi.go's D12 note), and a guessed id buys a second stream on a
// connection the guesser is already authenticated for. Its job is routing,
// and routing is all it does.
type ChannelID [16]byte

// String is the 32-hex form: what a log line, a JSON document and a test
// vector all show.
func (c ChannelID) String() string { return hex.EncodeToString(c[:]) }

// IsZero reports whether the id was never minted. A zero id is refused at
// every boundary rather than looked up: an unset field that resolves to "some
// channel" is how a frame reaches the wrong stream.
func (c ChannelID) IsZero() bool {
	var zero ChannelID
	return c == zero
}

// ParseChannelID reads the 32-hex form back.
func ParseChannelID(s string) (ChannelID, error) {
	var id ChannelID
	if len(s) != hex.EncodedLen(len(id)) {
		return id, fmt.Errorf("proto: channel id %q is not 32 hex characters", s)
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return id, fmt.Errorf("proto: channel id %q: %w", s, err)
	}
	copy(id[:], raw)
	return id, nil
}

// MarshalJSON writes the hex form. The wire's identity is a string on every
// surface a person can read; an array of 16 numbers would be a second
// spelling of one value.
func (c ChannelID) MarshalJSON() ([]byte, error) { return json.Marshal(c.String()) }

// UnmarshalJSON reads the hex form. Malformed hex is an error and not a zero
// id: a payload that cannot name a channel must be refused, never routed to
// whichever stream happens to be keyed by the zero value.
func (c *ChannelID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	id, err := ParseChannelID(s)
	if err != nil {
		return err
	}
	*c = id
	return nil
}

// ChannelFrame is one decoded proxied-channel frame.
type ChannelFrame struct {
	// Channel is the stream these bytes belong to.
	Channel ChannelID
	// Payload is the raw channel bytes. Never interpreted here, and never
	// interpreted by the helper either: it reads bytes to MOVE them (AD-6).
	Payload []byte
}

// DecodeChannelFrame reads one TypeChannelData payload.
func DecodeChannelFrame(payload []byte) (ChannelFrame, error) {
	if len(payload) < ChannelFrameHeaderLen {
		return ChannelFrame{}, ErrChannelFrameTooShort
	}
	var f ChannelFrame
	copy(f.Channel[:], payload[0:ChannelFrameHeaderLen])
	f.Payload = make([]byte, len(payload)-ChannelFrameHeaderLen)
	copy(f.Payload, payload[ChannelFrameHeaderLen:])
	return f, nil
}

// EncodeChannelFrame builds one TypeChannelData payload: the identity and the
// bytes, never JSON and never base64 (AD-1).
//
// A zero-length payload encodes to exactly the header, which the decoder
// accepts: a write of no bytes is not a malformed frame, and it is not
// end-of-stream either (see the file header).
func EncodeChannelFrame(f ChannelFrame) []byte {
	out := make([]byte, ChannelFrameHeaderLen+len(f.Payload))
	copy(out[0:ChannelFrameHeaderLen], f.Channel[:])
	copy(out[ChannelFrameHeaderLen:], f.Payload)
	return out
}
