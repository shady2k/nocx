package proto

// The SCREEN data plane: the full snapshot a session's runtime publishes per
// revision, carried from the helper to the coordinator on its own frame type
// and its own layout.
//
// # Layout
//
// The payload of a TypeScreenFrame frame:
//
//	bytes 0..15   session-id     16 raw bytes
//	bytes 16..31  subscriber-id  16 raw bytes
//	bytes 32..39  revision       uint64 big-endian
//	bytes 40..43  part-index     uint32 big-endian
//	bytes 44..47  part-count     uint32 big-endian
//	bytes 48..    payload        one session.frame document (whole, or the
//	                            part of one this frame carries)
//
// It is SessionFrame's layout reduced to what a screen frame needs. The
// differences from it are all deliberate, and their reasons are argued once
// in ADR-0073
// (docs/decisions/0073-the-screen-frame-is-keyed-by-its-session-and-its-reader-and-continues-on-its-own-carrier.md):
// the identity is the HOST SESSION's id plus the SUBSCRIBER it is for and
// nothing here is minted; there is NO lease epoch, because nothing is
// authorized helper-to-coordinator; and the REVISION rides in the header so
// a receiver orders, drops and reassembles without parsing JSON.
//
// # Oversize: parts at the carrier, never in the contract
//
// The house's first answer to "this payload does not fit one wire frame" is
// ChunkedResult plus TypeChunk frames. It was considered and does not fit —
// it is bound to the request/response envelope, and a published screen frame
// has no Response; the whole argument is ADR-0073's. So this type owns its
// own continuation, the way channel_frame.go owns its own layout and derives
// its own bound from MaxFrameBytes: a frame above MaxScreenDataPayloadBytes
// leaves the sender as parts ([SplitScreenDataFrame]), each part within the
// bound, and the receiver reassembles the bytes ([ScreenAssembler]) before
// anything parses JSON. The contract the payload carries never learns that
// parts exist, and the client stays ignorant of transport detail.
//
// The invariant, with both ends: from the moment the runtime publishes a
// frame to the moment a consumer holds it, that consumer either holds the
// whole frame at that revision or has been TOLD it lost it. Every drop the
// assembler names below is such a telling; the connection's ends route each
// one into the loss report the delivery contract already carries
// (Consumer.Coalesced), and it is visible in the product rather than only in
// a log. A frame silently dropped for its size is the outcome this design
// refuses.
//
// Two halves of one revision are never assembled into one screen: a part
// that arrives against a partial of another revision drops the partial WHOLE
// and refuses the part (ErrScreenAssemblySuperseded). Half of revision N and
// half of N+1 is a screen that never existed.
//
// # Bounds, named before anything buffers
//
//   - MaxScreenDataPayloadBytes bounds one wire frame's payload, derived
//     from MaxFrameBytes the way channel_frame.go derives its own.
//   - MaxScreenAssemblyParts bounds the part count an assembly may declare;
//     a part 0 declaring more is refused at first contact, before a byte is
//     buffered. Count times the per-part bound is therefore the most memory
//     one partial assembly can occupy, and no buffer grows past it.
//   - MaxScreenAssemblies bounds how many partial assemblies one connection
//     holds at once; a part 0 beyond it is refused by name. Abandon frees
//     the slot and the bytes.
//
// The strictness — parts must arrive contiguously, in order, one revision
// at a time — rests on the wire being ordered, which it is: one connection,
// frames delivered in send order. The sender's obligation, owned by the
// drain that publishes these frames, is to send a frame's parts together or
// not at all, and never to interleave two frames to one subscriber.

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// ScreenDataFrameHeaderLen is 16 (session) + 16 (subscriber) + 8 (revision)
// + 4 (part index) + 4 (part count).
const ScreenDataFrameHeaderLen = 48

// MaxScreenDataPayloadBytes is the largest payload ONE TypeScreenFrame frame
// can carry: MaxFrameBytes minus this frame's own header. It is stated here,
// beside the layout, for the reason channel_frame.go gives — the arithmetic
// that avoids an EncodeFrame panic is this file's business, not each
// caller's.
const MaxScreenDataPayloadBytes = MaxFrameBytes - ScreenDataFrameHeaderLen

// MaxScreenAssemblyParts is the most parts one screen frame may be split
// into. At eight, a frame of eight megabytes minus eight headers fits; the
// frames the runtime can produce at shipped geometries measure two to three
// parts at the worst screen content measured (nocx-zg3k3.2.2's close). A
// part 0 declaring more is refused before anything is buffered.
const MaxScreenAssemblyParts = 8

// MaxScreenAssemblies is the most partial assemblies one connection holds at
// once. A connection serves a handful of panes for one coordinator; the
// bound is generous on purpose and exists so a hostile or broken sender
// cannot open assemblies without end.
const MaxScreenAssemblies = 64

// ErrScreenDataFrameTooShort reports a payload that cannot hold the header.
// Like its siblings it is an answer, not a failure of the connection: the
// frame is dropped and the wire continues.
var ErrScreenDataFrameTooShort = errors.New("proto: screen frame shorter than its header")

// The assembler's named drops. Each one is the receiver TELLING the
// connection's end that a screen was lost — the ends route them into the
// loss report the delivery contract carries — and never a silent discard.
var (
	// ErrScreenAssemblyPartCount: a part declared more parts than the
	// carrier assembles. Refused before anything is buffered.
	ErrScreenAssemblyPartCount = errors.New("proto: screen frame declares more parts than the carrier assembles")
	// ErrScreenAssemblyOrphanPart: a part that continues no assembly — an
	// index nobody is waiting for, or a tail of an assembly given up.
	ErrScreenAssemblyOrphanPart = errors.New("proto: screen part continues no assembly")
	// ErrScreenAssemblySuperseded: a part arrived against a partial of
	// another revision. The partial is dropped whole and the part refused;
	// parts of two revisions are never spliced into one screen.
	ErrScreenAssemblySuperseded = errors.New("proto: screen assembly superseded by another revision and dropped whole")
	// ErrScreenAssemblyConnections: the connection already holds the bound
	// of partial assemblies, and a new one was refused.
	ErrScreenAssemblyConnections = errors.New("proto: screen assemblies at the connection bound; part dropped")
	// ErrScreenAssemblyPartTooLarge: a part above the per-part bound. The
	// wire cannot produce one — the frame layer refuses a length above
	// MaxFrameBytes and this layout's header is part of that bound — so a
	// part this size was built by hand, and buffering it would break the
	// assembly ceiling the part count exists to give.
	ErrScreenAssemblyPartTooLarge = errors.New("proto: screen part above the per-part bound")
)

// ScreenDataFrame is one decoded screen-plane frame: one part of one screen
// frame at one revision, for one subscriber of one session. A frame whose
// part count is one carries the whole document ([ScreenDataFrame.Whole]).
type ScreenDataFrame struct {
	// Session is the host session's 16 raw id bytes — the same 16 bytes
	// SessionFrame carries, the session whose screen this is.
	Session [16]byte
	// Subscriber is the reader this frame is for. What each reader is owed
	// differs — a mid-session attacher is owed one snapshot at the current
	// revision, an established reader the stream — so the frame names its
	// reader rather than leaving the fan-out to guesswork.
	Subscriber [16]byte
	// Revision is the revision the screen was read at, carried in the
	// header so a receiver can order, drop and reassemble without parsing
	// the payload.
	Revision uint64
	// PartIndex and PartCount say which part this is and how many there
	// are. Index counts from zero; a frame that is not split is part 0 of 1.
	PartIndex uint32
	PartCount uint32
	// Payload is this part's bytes of the session.frame document, or the
	// whole document when the frame is whole. Never interpreted here.
	Payload []byte
}

// Whole reports whether this frame carries an entire session.frame document.
func (f ScreenDataFrame) Whole() bool { return f.PartCount == 1 && f.PartIndex == 0 }

// DecodeScreenDataFrame reads one TypeScreenFrame payload. A payload exactly
// the header length decodes with no bytes; whether an empty part is
// legitimate is the SENDER's discipline — a session.frame document is never
// empty — and the contract layer refuses an empty document in the same
// breath it refuses a malformed one.
func DecodeScreenDataFrame(payload []byte) (ScreenDataFrame, error) {
	if len(payload) < ScreenDataFrameHeaderLen {
		return ScreenDataFrame{}, ErrScreenDataFrameTooShort
	}
	var f ScreenDataFrame
	copy(f.Session[:], payload[0:16])
	copy(f.Subscriber[:], payload[16:32])
	f.Revision = binary.BigEndian.Uint64(payload[32:40])
	f.PartIndex = binary.BigEndian.Uint32(payload[40:44])
	f.PartCount = binary.BigEndian.Uint32(payload[44:48])
	f.Payload = make([]byte, len(payload)-ScreenDataFrameHeaderLen)
	copy(f.Payload, payload[ScreenDataFrameHeaderLen:])
	return f, nil
}

// EncodeScreenDataFrame builds one TypeScreenFrame payload from the header
// fields and this part's bytes, never base64 and never wrapped in an
// envelope. A payload above MaxScreenDataPayloadBytes is a caller bug: the
// parts come from [SplitScreenDataFrame], which cannot produce one, and the
// wire's own EncodeFrame panics on what would otherwise be a silent oversize.
func EncodeScreenDataFrame(f ScreenDataFrame) []byte {
	out := make([]byte, ScreenDataFrameHeaderLen+len(f.Payload))
	copy(out[0:16], f.Session[:])
	copy(out[16:32], f.Subscriber[:])
	binary.BigEndian.PutUint64(out[32:40], f.Revision)
	binary.BigEndian.PutUint32(out[40:44], f.PartIndex)
	binary.BigEndian.PutUint32(out[44:48], f.PartCount)
	copy(out[ScreenDataFrameHeaderLen:], f.Payload)
	return out
}

// SplitScreenDataFrame splits one session.frame document into parts, each
// within the carrier's bound, for one subscriber of one session at one
// revision. A document within the bound becomes one part that answers
// [ScreenDataFrame.Whole]; the split never pads, so the last part carries
// only the remainder. Parts are sent together or not at all, in order, by
// the drain that publishes them: the assembler's strictness below rests on
// it.
//
// A document that would need more parts than [MaxScreenAssemblyParts] is
// REFUSED with [ErrScreenAssemblyPartCount], not split anyway: the receiver
// refuses such an assembly before buffering a byte, so producing one could
// only end in the loss the ends report. That refusal is also what makes the
// part fields' width safe — parts never exceeds the assembly bound, so the
// int-to-uint32 conversion cannot overflow.
func SplitScreenDataFrame(session, subscriber [16]byte, revision uint64, payload []byte) ([]ScreenDataFrame, error) {
	if len(payload) > MaxScreenAssemblyParts*MaxScreenDataPayloadBytes {
		return nil, ErrScreenAssemblyPartCount
	}
	if len(payload) == 0 {
		return []ScreenDataFrame{{
			Session: session, Subscriber: subscriber, Revision: revision,
			PartIndex: 0, PartCount: 1,
		}}, nil
	}
	parts := (len(payload) + MaxScreenDataPayloadBytes - 1) / MaxScreenDataPayloadBytes
	out := make([]ScreenDataFrame, 0, parts)
	for i := 0; i < parts; i++ {
		begin := i * MaxScreenDataPayloadBytes
		end := begin + MaxScreenDataPayloadBytes
		if end > len(payload) {
			end = len(payload)
		}
		out = append(out, ScreenDataFrame{
			Session: session, Subscriber: subscriber, Revision: revision,
			// The refusal at the top of this function bounds parts by
			// MaxScreenAssemblyParts, so neither conversion can lose a bit.
			PartIndex: uint32(i),     // #nosec G115 -- parts <= MaxScreenAssemblyParts
			PartCount: uint32(parts), // #nosec G115 -- parts <= MaxScreenAssemblyParts
			Payload:   payload[begin:end],
		})
	}
	return out, nil
}

// ScreenKey names one reader of one session's screen, the identity an
// assembly is buffered under.
type ScreenKey struct {
	Session    [16]byte
	Subscriber [16]byte
}

// AssembledScreenFrame is one whole session.frame document, reassembled from
// the parts that carried it.
type AssembledScreenFrame struct {
	Key      ScreenKey
	Revision uint64
	// Payload is the whole document, the bytes the sender split — a copy,
	// owned by the receiver, never a view into any part.
	Payload []byte
}

// screenPartial is one assembly in progress. buf's ceiling is
// PartCount×MaxScreenDataPayloadBytes: every buffered part was decoded
// within MaxScreenDataPayloadBytes and no more than PartCount parts will be
// appended, so the bound holds by construction and no buffer grows past it.
type screenPartial struct {
	revision uint64
	count    uint32
	next     uint32 // the part index the assembler is waiting for
	buf      []byte
}

// ScreenAssembler reassembles the parts of split screen frames on one
// connection. It is safe for use by one goroutine, like the decoder that
// feeds it. One assembly per ScreenKey; parts of one assembly arrive
// contiguously and in order, which is the sender's obligation and the
// wire's guarantee together.
type ScreenAssembler struct {
	partials map[ScreenKey]*screenPartial
}

// NewScreenAssembler builds an assembler with no partial assemblies.
func NewScreenAssembler() *ScreenAssembler {
	return &ScreenAssembler{partials: map[ScreenKey]*screenPartial{}}
}

// Pending is how many partial assemblies are open. It exists so the ends —
// and the tests — can see that a dropped or abandoned assembly is memory
// returned, not memory renamed.
func (a *ScreenAssembler) Pending() int { return len(a.partials) }

// Abandon gives up one assembly: its bytes are freed and its key forgotten.
// A later part for this key continues nothing, and answers
// ErrScreenAssemblyOrphanPart by name.
func (a *ScreenAssembler) Abandon(key ScreenKey) {
	delete(a.partials, key)
}

// Feed takes one decoded frame. It returns the assembled whole when the last
// part of an assembly lands, and nil otherwise; every refusal is one of the
// named drops above, which the connection's end reports as a lost screen —
// nothing here discards silently.
func (a *ScreenAssembler) Feed(f ScreenDataFrame) (*AssembledScreenFrame, error) {
	if len(f.Payload) > MaxScreenDataPayloadBytes {
		// Checked for EVERY part, before anything is created or appended:
		// the assembly's memory ceiling is part count times this bound, and
		// one hand-built oversized part would break it by construction.
		return nil, ErrScreenAssemblyPartTooLarge
	}
	key := ScreenKey{Session: f.Session, Subscriber: f.Subscriber}
	partial := a.partials[key]

	if partial != nil && partial.revision != f.Revision {
		// A part arrived against a partial of another revision. The partial
		// is dropped WHOLE and the part refused: half of revision N and
		// half of N+1 is a screen that never existed. The sender's next
		// part 0 starts clean, and the ends report the lost screen of the
		// assembly that died here.
		delete(a.partials, key)
		return nil, ErrScreenAssemblySuperseded
	}

	if partial == nil {
		if f.PartIndex != 0 {
			return nil, ErrScreenAssemblyOrphanPart
		}
		if f.PartCount == 0 {
			// A frame that declares zero parts declares nothing; calling
			// it whole would invent what the sender did not say.
			return nil, ErrScreenAssemblyOrphanPart
		}
		if f.PartCount > MaxScreenAssemblyParts {
			return nil, ErrScreenAssemblyPartCount
		}
		if len(a.partials) >= MaxScreenAssemblies {
			return nil, ErrScreenAssemblyConnections
		}
		partial = &screenPartial{revision: f.Revision, count: f.PartCount}
		a.partials[key] = partial
	} else {
		if f.PartIndex != partial.next {
			// Not the index the assembly is waiting for. The part is an
			// orphan; the assembly keeps waiting for the real one, since
			// on an ordered wire this cannot be its replacement.
			return nil, ErrScreenAssemblyOrphanPart
		}
	}

	if f.PartIndex != partial.next || f.PartCount != partial.count {
		return nil, ErrScreenAssemblyOrphanPart
	}
	partial.buf = append(partial.buf, f.Payload...)
	partial.next++

	if partial.next == partial.count {
		delete(a.partials, key)
		return &AssembledScreenFrame{
			Key:      key,
			Revision: f.Revision,
			Payload:  bytes.Clone(partial.buf),
		}, nil
	}
	return nil, nil
}
