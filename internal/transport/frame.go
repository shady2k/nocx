// Package transport implements the nocx WebSocket wire protocol.
//
// # Wire contract
//
// The WebSocket carries two planes multiplexed on one connection (AD-1):
//
//   - Data plane — binary frames carrying raw PTY I/O. PTY bytes are never
//     wrapped in JSON, JSON-RPC, or base64.
//   - Control plane — JSON-RPC 2.0 over text frames: open, close, resize,
//     and the exit notification.
//
// # Binary frame layout (data plane)
//
// The TS half of this codec lives in frontend/src/frame.ts; a golden-vector
// test on each side (frame_test.go / frame.test.ts) pins the layout so a
// unilateral change to either codec fails before reaching a user.
//
//	byte 0      version        = 0x01
//	byte 1      msg-type       0x01 = data (PTY I/O, both directions)
//	                           0x02 = metadata (reserved for Phase-2 Tier-B helper)
//	bytes 2..17 session-id     16 raw bytes
//	bytes 18..  payload        raw PTY bytes
//
// Forward-compat: the version byte and the metadata msg-type are allocated now
// so the Phase-2 helper feed can ship without a wire break. An inbound metadata
// frame is logged and dropped — it spawns no session and does not tear down
// the connection. A frame shorter than 18 bytes, carrying an unknown version,
// an unknown msg-type, or an unknown session-id is logged at warn and dropped;
// it must never panic, never implicitly spawn a session, and never tear down
// the connection.
//
// # Control plane (JSON-RPC 2.0)
//
// Methods: open, close, resize, attach. The server assigns the authoritative
// session-id (AD-7) and returns it in the open result. The JSON-RPC request id
// serves as the correlation-id — we do not add a second correlationId field (AD-7).
//
//	open:   --> {"jsonrpc":"2.0","id":1,"method":"open","params":{"cols":132,"rows":43,"xpixel":0,"ypixel":0}}
//	        <-- {"jsonrpc":"2.0","id":1,"result":{"sessionId":"<32 lowercase hex chars>"}}
//	resize: --> {"jsonrpc":"2.0","id":2,"method":"resize","params":{"sessionId":"...","cols":100,"rows":30,"xpixel":0,"ypixel":0}}
//	        <-- {"jsonrpc":"2.0","id":2,"result":{}}
//	close:  --> {"jsonrpc":"2.0","id":3,"method":"close","params":{"sessionId":"..."}}
//	        <-- {"jsonrpc":"2.0","id":3,"result":{}}
//	attach: --> {"jsonrpc":"2.0","id":4,"method":"attach","params":{"sessionId":"...","offset":1234}}
//	        <-- {"jsonrpc":"2.0","id":4,"result":{"resumed":true,"from":1234}}
//	        <-- {"jsonrpc":"2.0","id":4,"result":{"reset":true,"from":5678}}
//	ack:    <-- {"jsonrpc":"2.0","method":"ack","params":{"sessionId":"...","offset":1234}}   (notification, no id)
//	exit:   <-- {"jsonrpc":"2.0","method":"exit","params":{"sessionId":"..."}}                 (notification)
//
// The attach method (AD-9 reconnect) requests replay from a byte offset. If
// the offset is still in the ring the result is {resumed:true,from:<offset>}
// followed by replayed binary frames. If the offset predates the ring the
// result is {reset:true,from:<current offset>} and the client must clear its
// terminal. The ack notification trims the ring: the server discards bytes
// up to the acked offset, freeing space for new output without growing
// unbounded. Offset semantics: the client counts received payload bytes per
// session; the binary frame header carries no offset field (no wire break).
//
// Errors use standard JSON-RPC 2.0 codes: -32700 parse error, -32600 invalid
// request, -32601 method not found, -32602 invalid params, -32603 internal error.
// Malformed control frames produce an error response, not a dropped connection.
//
// # Resize contract (AD-1)
//
// open carries the initial {cols, rows, xpixel, ypixel} and the PTY is created
// at that size. Never spawn-then-resize. resize carries the same shape.
//
// # Ordering invariant (AD-7)
//
// The client MUST NOT send data frames for a session before the open result
// arrives. The server enforces this: unknown session-id → drop + warn.
package transport

import "fmt"

const (
	FrameVersion = 0x01

	MsgTypeData     = 0x01
	MsgTypeMetadata = 0x02

	FrameHeaderSize = 18 // 1 (version) + 1 (msg-type) + 16 (session-id)
)

var (
	ErrFrameTooShort  = fmt.Errorf("frame too short: minimum %d bytes", FrameHeaderSize)
	ErrUnknownVersion = fmt.Errorf("unknown frame version")
	ErrUnknownMsgType = fmt.Errorf("unknown message type")
)

type Frame struct {
	Version   byte
	MsgType   byte
	SessionID [16]byte
	Payload   []byte
}

func (f *Frame) Encode() []byte {
	buf := make([]byte, FrameHeaderSize+len(f.Payload))
	buf[0] = f.Version
	buf[1] = f.MsgType
	copy(buf[2:18], f.SessionID[:])
	copy(buf[18:], f.Payload)
	return buf
}

func DecodeFrame(data []byte) (Frame, error) {
	if len(data) < FrameHeaderSize {
		return Frame{}, ErrFrameTooShort
	}
	if data[0] != FrameVersion {
		return Frame{}, ErrUnknownVersion
	}
	var f Frame
	f.Version = data[0]
	f.MsgType = data[1]
	copy(f.SessionID[:], data[2:18])
	f.Payload = make([]byte, len(data)-FrameHeaderSize)
	copy(f.Payload, data[18:])
	return f, nil
}
