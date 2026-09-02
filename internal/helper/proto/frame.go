// Package proto frames the backend-to-remote-helper protocol on the wire:
// [type:1][seq:4][ack:4][len:4][payload:len], big-endian, payload JSON
// (design §4). It is the framing half of every helper connection; the host
// and the backend client share it, so the wire contract lives here once.
//
// The codec frames; it does not authenticate and does not interpret. A
// payload that maps to a frame is delivered even when the host will reject
// the envelope inside it — the host is the validator. A frame that cannot
// map — an implausible or oversize length prefix, an unknown type byte — is
// garbage, and the decoder scans past it to the next frame boundary one byte
// at a time, reporting every skipped region through the gap callback. The
// garbage is real: a remote shell prints "command not found" before the
// helper ever runs, and a valid frame can start anywhere inside it.
//
// "payload JSON" is true of every frame type but one. TypeSessionData is the
// DATA PLANE, and AD-1 governs it here exactly as it governs the WebSocket:
// raw PTY bytes are never wrapped in JSON, JSON-RPC or base64. Its payload is
// a fixed binary header followed by the bytes, and the codec frames it without
// looking at it — see SessionFrame in session_frame.go.
//
// seq and ack are written by the sender and ignored by every reader in this
// deliverable; they are reserved so a later PTY-owning service can resume
// without a wire break (D15).
package proto

import (
	"encoding/binary"
	"errors"
)

// FrameType is the first header byte.
type FrameType uint8

const (
	TypeHello     FrameType = 1
	TypeHelloOK   FrameType = 2
	TypeRequest   FrameType = 3
	TypeResponse  FrameType = 4
	TypeNotify    FrameType = 5
	TypeCancel    FrameType = 6
	TypeChunk     FrameType = 7
	TypeKeepAlive FrameType = 9

	// TypeSessionData carries raw PTY bytes in both directions, and it is
	// the helper wire's DATA PLANE: its payload is a fixed binary header
	// followed by the bytes themselves, never JSON and never base64 (AD-1).
	// The type byte is allocated NOW, before the session service exists,
	// for the reason AD-1 allocated its own metadata msg-type up front: the
	// decoder below treats an unknown type byte as garbage and scans past
	// it one byte at a time, so a generation that did not know this type
	// would resync THROUGH a live PTY stream rather than dropping one
	// frame. See SessionFrame.
	TypeSessionData FrameType = 10
	// TypeLifecycleData carries the raw lifecycle protocol on its own data
	// plane tag. It shares SessionFrame's binary identity only as a carrier;
	// the helper routes bytes and never decodes lifecycle envelopes.
	TypeLifecycleData FrameType = 11
)

// valid reports whether the type belongs to the closed set above. A byte
// outside it cannot be a frame header, so the decoder scans past it without
// trusting anything after it.
func (t FrameType) valid() bool {
	switch t {
	case TypeHello, TypeHelloOK, TypeRequest, TypeResponse, TypeNotify, TypeCancel, TypeChunk, TypeKeepAlive, TypeSessionData, TypeLifecycleData:
		return true
	}
	return false
}

// HeaderLen is [type:1][seq:4][ack:4][len:4].
const HeaderLen = 13

// MaxFrameBytes bounds one frame's payload (D14). A response above it is sent
// as a Response carrying ChunkedResult plus TypeChunk frames.
const MaxFrameBytes = 1 << 20

// ErrFrameTooLarge reports an EncodeFrame whose payload exceeds MaxFrameBytes.
// Decoding never returns it: an oversize prefix is garbage that the decoder
// scans past, and it is rejected before any allocation.
var ErrFrameTooLarge = errors.New("proto: frame exceeds MaxFrameBytes")

// EncodeFrame builds one wire frame. It panics on a payload above
// MaxFrameBytes: the signature carries no error, and emitting a truncated or
// oversize frame would corrupt the wire silently — an oversize payload is a
// caller bug, and the plan's chunking path exists precisely so it never
// happens.
func EncodeFrame(t FrameType, seq, ack uint32, payload []byte) []byte {
	if len(payload) > MaxFrameBytes {
		panic(ErrFrameTooLarge)
	}
	buf := make([]byte, HeaderLen+len(payload))
	buf[0] = byte(t)
	binary.BigEndian.PutUint32(buf[1:5], seq)
	binary.BigEndian.PutUint32(buf[5:9], ack)
	binary.BigEndian.PutUint32(buf[9:13], uint32(len(payload))) // #nosec G115 — bounded by the MaxFrameBytes check above
	copy(buf[HeaderLen:], payload)
	return buf
}

// Decoder reassembles frames from a byte stream and recovers from framing
// corruption by scanning forward one byte at a time for the next frame
// boundary. It is safe for use by one goroutine.
//
// The resync-region accounting lives on the Decoder, not in a Feed local: a
// garbage region split across Feed calls must still be reported once, whole.
type Decoder struct {
	onFrame func(t FrameType, seq, ack uint32, payload []byte)
	onGap   func(bytes int)

	pending []byte // bytes fed but not yet consumed
	inScan  bool   // a garbage region is open
	region  int    // garbage bytes scanned in the current region
}

// NewDecoder builds a decoder that delivers each frame to onFrame and each
// skipped garbage region to onGap. A nil onGap disables reporting.
func NewDecoder(onFrame func(t FrameType, seq, ack uint32, payload []byte), onGap func(bytes int)) *Decoder {
	return &Decoder{onFrame: onFrame, onGap: onGap}
}

// Feed hands the decoder more stream bytes. Complete frames are delivered
// through onFrame; garbage regions are skipped and reported through onGap.
// Feed never returns an error: an implausible prefix is garbage, not a
// failure.
func (d *Decoder) Feed(b []byte) error {
	d.pending = append(d.pending, b...)
	for {
		if !d.inScan {
			if len(d.pending) < HeaderLen {
				return nil // not enough bytes to judge the next position
			}
			n, ok := d.prefix()
			if !ok {
				d.inScan = true // this position is garbage; scan from it
				d.region = 0
				continue
			}
			if len(d.pending) < HeaderLen+n {
				return nil // a plausible frame whose payload is incomplete
			}
			d.deliver(d.pending[:HeaderLen+n])
			d.pending = d.pending[HeaderLen+n:]
			continue
		}
		// Scan: advance one byte at a time until a frame resyncs. A valid
		// frame starting inside the garbage is still found this way.
		for d.inScan {
			if len(d.pending) < HeaderLen {
				return nil // not enough bytes to judge this position
			}
			n, ok := d.prefix()
			if ok {
				if len(d.pending) >= HeaderLen+n {
					if d.onGap != nil && d.region > 0 {
						d.onGap(d.region)
					}
					d.region = 0
					d.inScan = false
					d.deliver(d.pending[:HeaderLen+n])
					d.pending = d.pending[HeaderLen+n:]
					break
				}
				return nil // a plausible frame start whose payload is incomplete
			}
			d.pending = d.pending[1:]
			d.region++
		}
	}
}

// prefix judges the position at the head of pending. It requires a full
// header, a known type byte, and a length that is neither zero nor above
// MaxFrameBytes: any of those failing means this position is not a frame
// boundary, and the claimed length is never trusted.
func (d *Decoder) prefix() (int, bool) {
	if !FrameType(d.pending[0]).valid() {
		return 0, false
	}
	n := int(binary.BigEndian.Uint32(d.pending[9:13]))
	if n == 0 || n > MaxFrameBytes {
		return 0, false
	}
	return n, true
}

// deliver hands one frame to onFrame. The payload is copied before the
// callback runs: handing out a view into pending would alias the decoder's
// internal buffer, and the next Feed's append can write into that same
// backing array through spare capacity.
func (d *Decoder) deliver(f []byte) {
	n := int(binary.BigEndian.Uint32(f[9:13]))
	payload := make([]byte, n)
	copy(payload, f[HeaderLen:HeaderLen+n])
	if d.onFrame != nil {
		d.onFrame(FrameType(f[0]), binary.BigEndian.Uint32(f[1:5]), binary.BigEndian.Uint32(f[5:9]), payload)
	}
}
