package proto

import (
	"encoding/binary"
	"errors"
)

// The data plane of the helper wire, and the FROZEN half of it.
//
// # Layout
//
// The payload of a TypeSessionData frame:
//
//	bytes 0..15   session-id     16 raw bytes
//	bytes 16..31  subscriber-id  16 raw bytes
//	bytes 32..39  lease-epoch    uint64 big-endian
//	bytes 40..    payload        raw PTY bytes
//
// It is internal/transport's own data frame extended by exactly what D8 adds
// and no more. That frame is
//
//	byte 0 version | byte 1 msg-type | bytes 2..17 session-id | payload
//
// and the two differences are the whole of this design:
//
//   - a SUBSCRIBER, because a host session's output now has several readers
//     and a frame has to say which one it is for. The coordinator's frame
//     needs none: its ring has exactly one subscriber, by construction.
//   - a LEASE EPOCH, because exactly one of those readers may also write, and
//     a displaced holder's bytes can still be in flight when it is displaced.
//     It is zero on a helper→coordinator frame, where there is nothing to
//     authorize.
//
// What is NOT here is as deliberate. There is no version byte, because the
// protocol already has one (proto.Version) and a second would be a second
// owner of the same question. There is no msg-type byte, because the outer
// frame's type already says which plane this is, and the direction is known to
// whichever side is reading. There is no generation, because a connection
// reaches exactly one generation's endpoint — the endpoint is derived from the
// generation — so qualifying every frame with it would restate a fact the
// connection already fixes. And there is NO OFFSET, which is the reuse
// decision: AD-9 already says "the client counts received payload bytes per
// session; the binary frame header carries no offset field", and each
// subscriber counts from the `from` its attach answered. An offset per frame
// would be a second statement of the reader's position, and two statements of
// one position eventually disagree.
//
// # Encoding lives with its caller
//
// This file decodes and does not encode, and that is not an omission. Nothing
// produces these frames until the helper owns a PTY (nocx-k6p18.3), and a
// function with no caller is a function nobody can tell is right. The layout
// is pinned by literal-byte golden vectors rather than by an encoder's own
// output, which is the stronger pin anyway: a vector built by the codec under
// test proves only that the codec agrees with itself.
//
// Decoding, by contrast, is needed today. Generations coexist for months, so a
// coordinator newer than the helper it reached will send TypeSessionData at a
// generation that has no session service, and a helper newer than the
// coordinator will send it to a client with nowhere to route it. Both must
// recognise the frame and drop it — logged, never resynced past as garbage,
// never a torn-down connection.

// SessionFrameHeaderLen is 16 (session) + 16 (subscriber) + 8 (lease epoch).
const SessionFrameHeaderLen = 40

// ErrSessionFrameTooShort reports a payload that cannot hold the header. Like
// internal/transport's ErrFrameTooShort it is an answer, not a failure of the
// connection: the frame is dropped and the wire continues.
var ErrSessionFrameTooShort = errors.New("proto: session frame shorter than its header")

// SessionFrame is one decoded data-plane frame.
type SessionFrame struct {
	// Session is the host session's 16 raw id bytes — the same 16 bytes the
	// HostSessionID's 32 hex characters spell, within this connection's
	// generation.
	Session [16]byte
	// Subscriber is the reader this frame is for (helper→coordinator) or the
	// reader whose keystrokes these are (coordinator→helper).
	Subscriber [16]byte
	// Epoch is the writer's lease on a coordinator→helper frame, and zero
	// the other way. A frame carrying a stale epoch is rejected by the
	// holder of the capability, never applied late.
	Epoch LeaseEpoch
	// Payload is the raw PTY bytes. Never interpreted here, and never
	// interpreted by the helper either: it reads bytes to MOVE them (AD-6).
	Payload []byte
}

// DecodeSessionFrame reads one TypeSessionData payload. A payload exactly the
// length of the header is legitimate and carries no bytes — a zero-length
// write is not a malformed frame.
func DecodeSessionFrame(payload []byte) (SessionFrame, error) {
	if len(payload) < SessionFrameHeaderLen {
		return SessionFrame{}, ErrSessionFrameTooShort
	}
	var f SessionFrame
	copy(f.Session[:], payload[0:16])
	copy(f.Subscriber[:], payload[16:32])
	f.Epoch = LeaseEpoch(binary.BigEndian.Uint64(payload[32:40]))
	f.Payload = make([]byte, len(payload)-SessionFrameHeaderLen)
	copy(f.Payload, payload[SessionFrameHeaderLen:])
	return f, nil
}
