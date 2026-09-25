package proto

// The ROW STREAM data plane (nocx-2v80t.3.6): the rows a session's runtime
// hands out as they leave the screen, and the interval end markers that
// close them, carried from the helper to the coordinator on their own frame
// types and their own layout.
//
// # Layout
//
// The payload of a TypeOutputRows frame:
//
//	bytes 0..15   session-id     16 raw bytes
//	bytes 16..31  subscriber-id  16 raw bytes
//	bytes 32..39  from-row       uint64 big-endian
//	bytes 40..    payload        one session.output-rows document
//
// The payload of a TypeIntervalEnd frame:
//
//	bytes 0..15   session-id     16 raw bytes
//	bytes 16..31  subscriber-id  16 raw bytes
//	bytes 32..39  end-row        uint64 big-endian
//	bytes 40..    payload        one session.interval-end document
//
// It is the screen frame's layout reduced to what a row needs, and its
// reasons are the screen frame's own (ADR-0073): identity is the HOST
// SESSION's id plus the SUBSCRIBER it is for; there is no lease epoch,
// because nothing is authorised helper-to-coordinator; and the row index
// rides in the header so a receiver orders and routes without parsing JSON.
//
// The index in the header is the SESSION's absolute row count at the batch —
// FromRow for a rows batch (the absolute index of the document's first row),
// EndRow for an end marker (one past the interval's last row). Rows and end
// markers are sent on the SAME ordered connection in stream order, so the
// pair is what attributes a row to its interval without either end trusting
// a clock.
//
// # No parts
//
// The screen frame splits at the carrier because a whole screen is a
// document of unbounded width. A rows batch is bounded twice before it is
// encoded: the runtime's feed bound (sessionruntime.MaxIngestBytes) bounds
// the input a batch's rows came from, and the bridge splits its batches at a
// row count whose document is measured far below this carrier's bound at
// every geometry the product sizes. An output-rows or interval-end payload
// above MaxOutputRowsPayloadBytes is therefore a caller bug, and Encode
// refuses it by name rather than splitting or padding — the same discipline
// as EncodeFrame, with the split this one never expects left unbuilt. A
// future bound that makes the refusal reachable arrives together with the
// split it needs, and not before.
//
// # Ordered, exactly once
//
// The wire is ordered (one connection, frames in send order), and the
// runtime hands each row exactly once to the stream that feeds these
// frames. A frame the carrier drops is a loss the ends can already state —
// the connection's send path reports it — so this layout carries no
// sequence number to reconcile; the absolute row index is the reconciliation
// key, and the coordinator's confirmed-written mark is what names the
// position a resend would start from.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
)

const (
	// TypeOutputRows carries one batch of the rows that left a session's
	// screen, oldest first, in the frame contract's row vocabulary.
	TypeOutputRows FrameType = 14
	// TypeIntervalEnd closes the interval the payload names, after every
	// row that belongs to it, carrying the screen as the boundary sat on
	// it.
	TypeIntervalEnd FrameType = 15
	// TypeClearBoundary carries one sighted erase-saved-lines
	// (nocx-2v80t.3.17), on the same ordered carrier and in the position
	// it occurred: a consumer that reads this plane in order can never
	// attribute it to the wrong side of a row. It names no row index and
	// no document — unlike a rows batch or an end marker the fact needs
	// neither, because the store resolves what it bounds from the
	// session id alone (content.RecordClearBoundary).
	TypeClearBoundary FrameType = 16
)

// OutputRowsFrameHeaderLen is 16 (session) + 16 (subscriber) + 8 (row
// index) — the screen frame's header with the two part fields it has no use
// for.
const OutputRowsFrameHeaderLen = 40

// MaxOutputRowsPayloadBytes is the largest payload one rows-plane frame can
// carry, derived from MaxFrameBytes the way every carrier here derives its
// own.
const MaxOutputRowsPayloadBytes = MaxFrameBytes - OutputRowsFrameHeaderLen

// ErrOutputRowsFrameTooShort reports a payload that cannot hold the header.
// Like its siblings it is an answer, not a failure of the connection: the
// frame is dropped and the wire continues.
var ErrOutputRowsFrameTooShort = errors.New("proto: rows frame shorter than its header")

// ErrOutputRowsFrameTooLarge reports an encode whose payload would not fit
// one frame. The bridge that produces these documents bounds them by
// construction, so this names a bug rather than a shape the wire carries.
var ErrOutputRowsFrameTooLarge = errors.New("proto: rows frame payload above the carrier's bound")

// OutputRowsFrame is one decoded rows-plane frame: one batch of departed
// rows, or one interval's end marker, for one subscriber of one session.
type OutputRowsFrame struct {
	Session    [16]byte
	Subscriber [16]byte
	// FromRow is the batch's first row's absolute index; an end marker's
	// frame carries its EndRow here instead.
	FromRow uint64
	Payload []byte
}

// DecodeOutputRowsFrame reads one rows-plane frame payload. A payload
// exactly the header length decodes with no bytes; a rows document is never
// empty, and the contract layer refuses one in the same breath it refuses a
// malformed one.
func DecodeOutputRowsFrame(payload []byte) (OutputRowsFrame, error) {
	if len(payload) < OutputRowsFrameHeaderLen {
		return OutputRowsFrame{}, ErrOutputRowsFrameTooShort
	}
	f := OutputRowsFrame{
		FromRow: binary.BigEndian.Uint64(payload[32:40]),
	}
	copy(f.Session[:], payload[0:16])
	copy(f.Subscriber[:], payload[16:32])
	f.Payload = append([]byte(nil), payload[40:]...)
	return f, nil
}

// EncodeOutputRowsFrame builds one rows-plane frame payload from the header
// fields and the document bytes, never base64 and never wrapped in an
// envelope. A payload above MaxOutputRowsPayloadBytes is refused by name:
// the producer bounds its documents by construction, so the refusal names a
// bug rather than a shape the wire carries.
func EncodeOutputRowsFrame(f OutputRowsFrame) ([]byte, error) {
	return encodeRowsPlaneFrame(f.Session, f.Subscriber, f.FromRow, f.Payload)
}

// IntervalEndFrame is one decoded end marker: one interval's boundary, for
// one subscriber of one session. Its layout is the rows frame's, with the
// header's index carrying EndRow — the absolute index one past the
// interval's last departed row.
type IntervalEndFrame struct {
	Session    [16]byte
	Subscriber [16]byte
	EndRow     uint64
	Payload    []byte
}

// DecodeIntervalEndFrame reads one end-marker frame payload.
func DecodeIntervalEndFrame(payload []byte) (IntervalEndFrame, error) {
	if len(payload) < OutputRowsFrameHeaderLen {
		return IntervalEndFrame{}, ErrOutputRowsFrameTooShort
	}
	f := IntervalEndFrame{
		EndRow: binary.BigEndian.Uint64(payload[32:40]),
	}
	copy(f.Session[:], payload[0:16])
	copy(f.Subscriber[:], payload[16:32])
	f.Payload = append([]byte(nil), payload[40:]...)
	return f, nil
}

// EncodeIntervalEndFrame builds one end-marker frame payload. The same
// bound and the same refusal as the rows frame: the producer bounds its
// documents by construction.
func EncodeIntervalEndFrame(f IntervalEndFrame) ([]byte, error) {
	return encodeRowsPlaneFrame(f.Session, f.Subscriber, f.EndRow, f.Payload)
}

// encodeRowsPlaneFrame is the one encoding of the rows plane's layout: every
// frame of this plane is the session, the subscriber, the row index and the
// document bytes, and two spellings of one layout would be two layouts
// disagreeing somewhere nobody looked.
func encodeRowsPlaneFrame(session, subscriber [16]byte, rowIndex uint64, payload []byte) ([]byte, error) {
	if len(payload) > MaxOutputRowsPayloadBytes {
		return nil, ErrOutputRowsFrameTooLarge
	}
	out := make([]byte, OutputRowsFrameHeaderLen+len(payload))
	copy(out[0:16], session[:])
	copy(out[16:32], subscriber[:])
	binary.BigEndian.PutUint64(out[32:40], rowIndex)
	copy(out[OutputRowsFrameHeaderLen:], payload)
	return out, nil
}

// OutputRowsDoc is one batch of departed rows as the document declares it:
// the absolute index of the first row, the rows themselves in the frame
// contract's own text+marks+runs vocabulary (nocx-zg3k3.2.12), and LostRows
// — an exact accounting of the gap this batch's FromRow leaves against the
// row count after the delivery before it, never a manufactured guess. Two
// sources add into that one count: the FEEDS the emulator struck immediately
// before this batch's first row (a symbolic one per struck feed — the ABI
// carries no departure counter, so the rows a prune took are unknowable by
// contract), and rows the session's own bridge had to drop under
// backpressure before they ever reached a batch (an exact row count, the
// runtime's row index having already advanced past them; rows.go,
// nocx-2v80t.3.15). Rows rides pre-encoded: the vocabulary has ONE Go
// encoder (sessionruntime.EncodeRows) and this document carries its bytes
// unchanged.
//
// Incomplete is the helper's one marker that its row buffer overflowed
// (nocx-2v80t.3.36): the block in flight ends here, incomplete, and nothing
// the session departs is recorded again until the next command starts after
// the stream is healthy. A document carrying it has no rows, and FromRow is
// the first row that was not recorded.
type OutputRowsDoc struct {
	FromRow    uint64          `json:"fromRow"`
	LostRows   uint64          `json:"lostRows"`
	Rows       json.RawMessage `json:"rows"`
	Incomplete bool            `json:"incomplete"`
}

// ClearBoundaryFrameHeaderLen is 16 (session) + 16 (subscriber): this plane
// carries no row index, unlike its two siblings above — the store resolves
// what the boundary bounds from the session id alone
// (content.RecordClearBoundary), never from a position on this wire.
const ClearBoundaryFrameHeaderLen = 32

// ClearBoundaryFrame is one decoded clear-boundary sighting, for one
// subscriber of one session.
type ClearBoundaryFrame struct {
	Session    [16]byte
	Subscriber [16]byte
	Payload    []byte
}

// DecodeClearBoundaryFrame reads one clear-boundary frame payload.
func DecodeClearBoundaryFrame(payload []byte) (ClearBoundaryFrame, error) {
	if len(payload) < ClearBoundaryFrameHeaderLen {
		return ClearBoundaryFrame{}, ErrOutputRowsFrameTooShort
	}
	var f ClearBoundaryFrame
	copy(f.Session[:], payload[0:16])
	copy(f.Subscriber[:], payload[16:32])
	f.Payload = append([]byte(nil), payload[ClearBoundaryFrameHeaderLen:]...)
	return f, nil
}

// EncodeClearBoundaryFrame builds one clear-boundary frame payload. The same
// bound and the same refusal as the rows frame: the producer bounds its
// documents by construction.
func EncodeClearBoundaryFrame(f ClearBoundaryFrame) ([]byte, error) {
	if len(f.Payload) > MaxOutputRowsPayloadBytes {
		return nil, ErrOutputRowsFrameTooLarge
	}
	out := make([]byte, ClearBoundaryFrameHeaderLen+len(f.Payload))
	copy(out[0:16], f.Session[:])
	copy(out[16:32], f.Subscriber[:])
	copy(out[ClearBoundaryFrameHeaderLen:], f.Payload)
	return out, nil
}

// ClearBoundaryDoc is one sighted clear boundary as the document declares
// it: Kind is a closed discriminator, "clear" today and reserved so a later
// boundary kind (nocx-zg3k3.10.3's own paging) fits the same shape without
// widening this one. It carries no cursor: the store resolves what THIS
// boundary bounds from the session id alone (content.RecordClearBoundary),
// never from a position on this wire.
type ClearBoundaryDoc struct {
	Kind string `json:"kind"`
}

// IntervalEndDoc closes one interval: the boundary's meeting as 64 lowercase
// hex characters (the fixed spelling the lifecycle downlink already uses),
// the absolute row index one past the interval's last departed row, and the
// screen as the boundary sat on it — the same rows vocabulary, and null when
// the screen could not be read, because honest silence and an empty screen
// are different answers. NoFence says the interval was settled without its
// fence ever being sighted (ADR-0074 decision 3, nocx-2v80t.3.29): its
// completion arrived, the next event proved the fence would not, and it
// ended with no closing screen — so the block it closes may be missing
// output, and the coordinator stores it that way. Always present: false is
// an answer.
type IntervalEndDoc struct {
	Nonce   string          `json:"nonce"`
	EndRow  uint64          `json:"endRow"`
	Closing json.RawMessage `json:"closing"`
	NoFence bool            `json:"noFence"`
}
