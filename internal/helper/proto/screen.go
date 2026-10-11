package proto

// The screen half of the session service: what a session's runtime holds, and
// the PTY-less replay a recorded capture is read through.
//
// # Why the frame is on this wire at all
//
// ADR-0066 put the one emulator beside the PTY, so the coordinator has no
// terminal state of its own and cannot derive a screen from bytes it receives:
// the helper's bounded output window reclaims what nobody read, which makes an
// emulator fed the surviving suffix non-authoritative (design §6.7). What a
// caller wants to know about a pane — where the caret is, what is on the screen
// — is therefore a QUESTION, and this is its shape.
//
// # One frame shape, two readers
//
// ScreenFrame is the reusable half: cells with their column widths, the caret,
// the buffer. OpScreen answers one of them for a live session and adds the two
// facts only a RUNTIME has (its revision and what it can claim about the
// stream); OpReplay answers a list of them for a capture, because a replay has
// no runtime and nothing to be complete about beyond the bytes it was handed.
// Two shapes would have been two decoders, and the second would have been
// written by reading this one.

// OpScreen reports the screen a session's runtime holds right now.
const OpScreen = "screen"

// OpScreenCapture reads an absolute row range and its buffer identity from one
// runtime lock acquisition. It serves assistant reads, not renderer frames.
const OpScreenCapture = "screen-capture"

// OpScreenResend asks a session's runtime to hand its consumers the frame it
// last published — a full snapshot, the runtime's own Resend — over the same
// carrier every published frame rides (nocx-zg3k3.2.15).
//
// It exists because the coordinator's ONE runtime consumer outlives the
// renderers it serves: frames the runtime published while no renderer was
// attached reached the coordinator and found nobody to show them to, and the
// runtime's per-consumer baseline was spent when the coordinator attached,
// not when a window did. A window attaching to a pane at rest would otherwise
// wait for a program to print something. The op answers nothing but "asked":
// the frame arrives on the screen carrier, in order with every other frame,
// and never in this op's result — a second route for one frame would be a
// second ordering to reconcile.
const OpScreenResend = "screen-resend"

// OpReplay feeds a capture's bytes to a PTY-less emulator and answers the
// screen after each mark.
//
// It exists because the calibration path has to turn a recorded capture back
// into frames — rules are verified against the screens a person labelled
// (internal/agentcalib) — and the emulator that may do that is this one. The
// coordinator is forbidden it: cmd/nocx-server is built CGO_ENABLED=0, and a
// second emulator beside the runtime is what ADR-0066 refuses.
const OpReplay = "replay"

// ScreenCell is one column of a ScreenFrame.
//
// Width is the load-bearing field. A double-width cluster occupies two columns:
// the first carries the grapheme with width 2, the second is a continuation
// with width 0 which a text reader skips and a POSITION reader counts. The
// distinction is also what keeps it out of the message text: a caller that
// wants a character takes Text, a caller that wants an anchor reads the index.
type ScreenCell struct {
	Text string `json:"text"`
	// Width is 0 (a continuation or spacer), 1 or 2.
	Width int `json:"width"`
}

// ScreenFrame is one screen as the emulator holds it: a rectangle of cells, the
// caret, and which buffer it is.
//
// The rows are a RECTANGLE and not trimmed text: Lines is Rows long and every
// line is Cols long, because a consumer that reads a position must be able to
// index a column without knowing where a program stopped writing.
type ScreenFrame struct {
	Cols          int            `json:"cols"`
	Rows          int            `json:"rows"`
	CursorX       int            `json:"cursorX"`
	CursorY       int            `json:"cursorY"`
	CursorVisible bool           `json:"cursorVisible"`
	AltScreen     bool           `json:"altScreen"`
	Lines         [][]ScreenCell `json:"lines"`
}

// ScreenParams names the session whose screen is being read.
type ScreenParams struct {
	Session HostSessionID `json:"session"`
}

// ScreenResendParams names the session whose runtime re-offers its frame.
type ScreenResendParams struct {
	Session HostSessionID `json:"session"`
}

// ScreenResendResult is deliberately empty: the frame rides the screen
// carrier, and the answer to "was it asked" is the absence of an error.
type ScreenResendResult struct{}

// ScreenResult is the frame, plus the two facts that belong to the runtime
// rather than to the screen.
type ScreenResult struct {
	Frame ScreenFrame `json:"frame"`
	// Revision is the runtime's monotonic clock at the read: the number an
	// attaching client relates its cards and its live frame to (design §5).
	Revision uint64 `json:"revision"`
	// Completeness is what the runtime can claim about the stream it holds. It
	// is what a write gate reads before it decides anything (design §6.7).
	Completeness Completeness `json:"completeness"`
}

// ScreenCaptureRange is an absolute, half-open row interval in the active
// buffer. A nil range requests every row still retained by that buffer.
type ScreenCaptureRange struct {
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
}

// ScreenCaptureParams names the session and, optionally, the exact absolute
// row interval to capture.
type ScreenCaptureParams struct {
	Session HostSessionID       `json:"session"`
	Range   *ScreenCaptureRange `json:"range,omitempty"`
}

// ScreenCaptureIdentity describes the buffer instance, viewport geometry and
// content generation for a capture. Instance changes whenever the active
// primary/alternate buffer changes; generation is the runtime screen revision.
type ScreenCaptureIdentity struct {
	Kind       string `json:"kind"`
	Instance   uint64 `json:"instance"`
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
	Generation uint64 `json:"generation"`
}

// ScreenCaptureResult returns rows and every boundary needed to interpret them.
// Rows is exactly [Start, End), never a silently clipped request.
type ScreenCaptureResult struct {
	Floor         uint64                `json:"floor"`
	Start         uint64                `json:"start"`
	End           uint64                `json:"end"`
	Total         uint64                `json:"total"`
	Rows          [][]ScreenCell        `json:"rows"`
	CursorX       int                   `json:"cursorX"`
	CursorY       uint64                `json:"cursorY"`
	CursorVisible bool                  `json:"cursorVisible"`
	Identity      ScreenCaptureIdentity `json:"identity"`
	Revision      uint64                `json:"revision"`
	Completeness  Completeness          `json:"completeness"`
}

// Completeness is the wire spelling of what a runtime can claim about the
// stream it holds. The set is closed and it is the coordinator's own vocabulary
// (internal/sessionruntime.Completeness) spelled once, here, because both ends
// of this socket are the same Go package.
//
// A spelling this coordinator has never heard of does NOT arrive as unknown by
// itself, and it is worth being exact about that because the difference is
// where the safety lives: encoding/json decodes an unmatched string into a
// named string type UNCHANGED, so the value that reaches a reader is whatever
// the peer wrote. What turns it into `unknown` is the CONVERSION at the
// reading end (client.completenessFromWire), whose default arm maps anything
// outside this set onto the value a write gate refuses in. A decoder that
// leaned on a zero value instead would read an invented claim as whatever its
// zero happened to be.
type Completeness string

const (
	// CompletenessUnknown is what a runtime says before it has established
	// anything, and what an unrecognised spelling decodes to.
	CompletenessUnknown Completeness = "unknown"
	// CompletenessComplete means every byte of the interval reached the
	// emulator and the screen is the whole of it.
	CompletenessComplete Completeness = "complete"
	// CompletenessLostIngest means output was lost before the emulator saw it.
	CompletenessLostIngest Completeness = "lostIngest"
	// CompletenessNoFence means ingest was whole but the interval has no
	// authenticated boundary.
	CompletenessNoFence Completeness = "noFence"
	// CompletenessEvicted means retention deliberately kept less than the
	// whole.
	CompletenessEvicted Completeness = "evicted"
)

// ReplayParams is one capture, and where in it the caller wants screens.
//
// Through is how many chunks have been consumed at each mark, non-decreasing,
// computed by the caller that owns the capture FORMAT
// (internal/agentcapture.ChunksThrough). The arithmetic is deliberately NOT
// repeated on this side: two derivations of "which chunk belongs to this mark"
// agree everywhere anybody looks and disagree on the day one of them moves.
type ReplayParams struct {
	Cols    int   `json:"cols"`
	Rows    int   `json:"rows"`
	Through []int `json:"through"`
	// Chunks is the capture's bytes, in stream order, as raw bytes rather than
	// as text: a PTY stream is not UTF-8, and a JSON string would replace every
	// byte that is not with U+FFFD on the way through.
	Chunks [][]byte `json:"chunks"`
}

// ReplayResult is one frame per mark, in the order the marks were given.
type ReplayResult struct {
	Frames []ScreenFrame `json:"frames"`
}

// MaxReplayCells bounds the geometry a replay may ask for.
//
// The size comes from a CAPTURE, which is a file a person may have written or
// edited, so it is not the size of a terminal nocx spawned. Without this bound
// a header claiming 60000x60000 would have the helper allocate four billion
// cells for a request that fits in a few hundred bytes. The ceiling is far
// above any real terminal — 1000x400 is 400k cells — and far below what one
// machine will allocate for a stranger.
const MaxReplayCells = 1 << 20
