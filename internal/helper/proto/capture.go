package proto

// The capture half of the session service: ONE record of a settled execution
// interval, pushed UP to the coordinator when the rendezvous completes.
//
// # Why this op is a REVERSE one
//
// The record is built by the session runtime at the authenticated render
// boundary — the instant the meeting completes — because only the runtime
// was there for the whole of it. The op therefore travels helper →
// coordinator (host.Ask), like the ssh service's credential asks, and its
// RESULT is the coordinator's storage answer, which the helper's caller
// reads: kept, or the reason nothing was kept. A coordinator-pulled op would
// have to build the record on request rather than at the boundary and could
// not carry that answer at all. The two dispatchers stay disjoint by op
// name: the session service's forward ops never spell `capture`, exactly as
// the ssh service's forward and reverse halves share one service name with
// disjoint op names.

// OpCapture carries one settled interval's capture record up to the
// coordinator, which stores it against the command's entry.
const OpCapture = "capture"

// CaptureColor is one colour of a cell, in the emulator's own vocabulary:
// the kinds kept apart because they mean different things and a flattened
// triple would not survive a theme. The helper holds what the emulator held;
// resolving a palette index into somebody's theme is the READER's act, never
// the wire's.
type CaptureColor struct {
	// Kind is "default" | "palette" | "rgb". The fields a kind does not name
	// are meaningless and are left zero, exactly as the emulator's own
	// Color documents.
	Kind string `json:"kind"`
	// Palette is the index for kind "palette".
	Palette int `json:"palette,omitempty"`
	// RGB is "#rrggbb" for kind "rgb".
	RGB string `json:"rgb,omitempty"`
}

// CaptureCell is one column of a capture row, with the visual facts a
// restore needs to repaint what the emulator held: the grapheme, the columns
// it occupies, and its style.
//
// This is the capture RECORD's cell and not ScreenFrame's: the live-frame
// shape deliberately carries no style, and the record is the durable content
// the card's wire format (design §6.3) derives views from — a second shape
// here is two vocabularies staying honest about two producers, not two
// answers to one question.
type CaptureCell struct {
	// Text is the grapheme the cell carries; empty when HasText is false.
	Text string `json:"text"`
	// Width is 0 (a continuation or spacer), 1 or 2.
	Width int `json:"width"`
	// HasText is separate from Text being empty: a cell can carry a
	// background colour and no text, and a restore paints the first and not
	// the second.
	HasText bool `json:"hasText"`
	// Fg, Bg and UnderlineColor are the cell's colours; nil is the default.
	Fg             *CaptureColor `json:"fg,omitempty"`
	Bg             *CaptureColor `json:"bg,omitempty"`
	UnderlineColor *CaptureColor `json:"underlineColor,omitempty"`
	// Attrs is the emulator's attribute bitset verbatim: bold 1, italic 2,
	// faint 4, blink 8, inverse 16, invisible 32, strikethrough 64,
	// overline 128. The set is closed and the bits cross as numbers, so a
	// reader needs no package source to decode them.
	Attrs int `json:"attrs"`
	// Underline is the underline SHAPE, 0 (none) through 5 (dashed) — SGR
	// 4:0 through 4:5, not a boolean.
	Underline int `json:"underline"`
}

// CaptureRow is one physical row of a capture, with the wrap facts that let
// a serialiser join the physical lines of one logical line without losing a
// hard newline. They are not each other's negation, exactly as the
// emulator's Row documents.
type CaptureRow struct {
	Wrap         bool          `json:"wrap"`
	Continuation bool          `json:"continuation"`
	Cells        []CaptureCell `json:"cells"`
}

// CaptureScreen is one instant of a screen: the whole grid with style, the
// caret, and which buffer was active.
type CaptureScreen struct {
	Cols          int          `json:"cols"`
	Rows          int          `json:"rows"`
	AltScreen     bool         `json:"altScreen"`
	CursorX       int          `json:"cursorX"`
	CursorY       int          `json:"cursorY"`
	CursorVisible bool         `json:"cursorVisible"`
	Lines         []CaptureRow `json:"lines"`
}

// CaptureParams is the record, addressed like every op on this service: the
// generation-qualified session is the lookup, the incarnation is the identity
// the runtime judges, and the nonce names the meeting whose completion closed
// the interval.
type CaptureParams struct {
	Session     HostSessionID `json:"session"`
	Incarnation Incarnation   `json:"incarnation"`
	// Nonce is the settled meeting's fence, 64 lowercase hex characters —
	// the spelling session.lifecycle-complete carries down.
	Nonce string `json:"nonce"`
	// Revision is the runtime's clock at the boundary; Completeness is the
	// runtime's own answer there, never a default.
	Revision     uint64       `json:"revision"`
	Completeness Completeness `json:"completeness"`

	// Opening is the screen as the interval opened; Departed the rows the
	// interval pushed off it, oldest first; Closing the screen at the
	// boundary. DepartedHole is the departure report's own retention flag:
	// true means some of what left could not be read, so the list holds
	// what was read and the rest is unknowable — never presented as whole.
	Opening      CaptureScreen `json:"opening"`
	Departed     []CaptureRow  `json:"departed"`
	DepartedHole bool          `json:"departedHole"`
	Closing      CaptureScreen `json:"closing"`
}

// CaptureResult is the coordinator's storage answer, and it carries a legal
// "nothing was kept, and why" outcome: refusing to store is not an error,
// and a shape that could only express success would leave the helper that
// built the record unable to say what became of it.
type CaptureResult struct {
	// Kept is whether the record's body was stored against the command's
	// entry.
	Kept bool `json:"kept"`
	// Reason is why nothing was kept, from a closed set spelled like the
	// store's own stance: "outputOff" (output retention off), "sensitive"
	// (a sensitive entry), "critical" (a critical pinned observation) and
	// "noEntry" (the command's row was never recorded — history off, or the
	// bind never landed). Empty exactly when Kept is true.
	Reason string `json:"reason,omitempty"`
}
