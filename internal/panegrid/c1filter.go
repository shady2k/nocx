package panegrid

// nocx-nru89.5: x/vt (github.com/charmbracelet/x/vt, pinned by ADR-0041) uses
// github.com/charmbracelet/x/ansi's DEC ANSI transition table
// (x/ansi/parser/transition_table.go). Inside an OSC or DCS string, that
// table extends the "printable" range to 0x20-0xFF so a title's raw UTF-8
// bytes pass through unexamined — except for one byte value it special-cases
// unconditionally: 0x9C, the 8-bit form of the String Terminator (ST). A
// title containing a multi-byte UTF-8 rune whose encoding happens to include
// the byte 0x9C — U+2733 (✳, `E2 9C B3`) is the reported case, and every
// codepoint in U+2700-U+273F shares the same middle byte — ends the OSC
// early. The parser returns to its ground state and prints whatever comes
// after 0x9C onto the grid at the cursor, because printable ASCII bytes
// (title text is usually ASCII after the dingbat) are ordinary Print actions
// in that state. The dialog underneath is unaffected; the grid is not.
//
// x/ansi has no mode to disable 8-bit C1 recognition (checked: no such
// option exists in Parser, and the transition table is a package-level
// var with no override hook), so it cannot be told "this stream is UTF-8,
// there are no raw 8-bit controls in it" — which is what real terminals do
// to resolve exactly this collision. Upstream at HEAD
// (github.com/charmbracelet/x/ansi@3986e91, 2026-09-06) still pins the same
// transition table; there is nothing to upgrade to.
//
// This filter recovers the missing disambiguation from OUR side of the
// Write call, using only the structure x/vt itself already relies on to
// find OSC/DCS string boundaries — the 7-bit ESC introducer and the
// terminators (ESC \, BEL for OSC, CAN/SUB) — plus the generic UTF-8 lead-
// byte/continuation-byte byte-class rules from RFC 3629. It does not read
// what a title SAYS: two titles that differ only in which dingbat they use
// are filtered identically, and a title with no multi-byte rune at all
// passes through this file's logic having touched nothing. That is the
// same class of structural parsing x/vt performs for the same bytes; the
// fix is corrected disambiguation of a control-vs-data byte, not a content
// judgement, so it does not cross AD-6.
//
// Scope: only OSC and DCS strings are covered — the two string types x/ansi
// itself extends to the 0x20-0xFF range, and the only two the reported
// defect and its captures exercise. SOS/PM/APC strings keep x/ansi's
// existing (narrower, ASCII-only) handling; nocx does not emit or observe
// them today, and covering their C1-wide behaviour is a different, larger
// change than the one bit this bead reports.
//
// A GENUINE standalone 0x9C — one that is not a UTF-8 continuation byte of
// an in-progress multi-byte sequence — still terminates the string exactly
// as x/vt already does correctly; this filter only removes the false
// positive, it does not remove 8-bit ST support.
type c1FilterState uint8

const (
	c1Ground c1FilterState = iota
	c1Escape
	c1OSCString
	c1DCSString
)

// c1Filter tracks, byte by byte and ACROSS calls, whether the stream is
// currently inside an OSC or DCS string and how many UTF-8 continuation
// bytes remain of a multi-byte rune in progress. It must be per-pane state
// (one instance per grid) rather than a stateless per-call scan: a live PTY
// stream is fragmented at arbitrary Feed() boundaries — see
// TestAStreamSplitAcrossFeedsLandsWhereAWholeOneDoes for the same property
// already required of the grid itself — and an escape sequence, or a rune
// inside one, can straddle two Feed calls.
type c1Filter struct {
	state         c1FilterState
	utf8Remaining int
}

const (
	stringTerminatorByte = 0x9C
	replacementByte      = '?' // ordinary ASCII; never special in any state below
)

// filter returns b unmodified — same slice, no allocation — unless a byte in
// it must be rewritten. That is the common case: most output carries no
// escape sequence at all, and of those that do, only a title whose text
// contains the exact byte value 0x9C at a continuation-byte position ever
// needs a substitution.
func (f *c1Filter) filter(b []byte) []byte {
	var out []byte // allocated lazily, only on the first actual replacement
	for i, c := range b {
		replace := false
		switch f.state {
		case c1Ground:
			if c == 0x1B {
				f.state = c1Escape
			}

		case c1Escape:
			switch c {
			case ']':
				f.state, f.utf8Remaining = c1OSCString, 0
			case 'P':
				f.state, f.utf8Remaining = c1DCSString, 0
			case 0x1B:
				// Two ESCs in a row: still pending, matches x/ansi's own
				// "Anywhere -> Escape" transition on repeated ESC.
			default:
				f.state = c1Ground
			}

		case c1OSCString, c1DCSString:
			switch {
			case c == 0x1B:
				// ESC always dispatches the current string in x/ansi,
				// pending or not.
				f.state, f.utf8Remaining = c1Escape, 0
			case c == 0x07 && f.state == c1OSCString:
				// BEL terminates OSC only; x/ansi's DCS passthrough treats
				// it as data (transition_table.go:212).
				f.state, f.utf8Remaining = c1Ground, 0
			case c == 0x18 || c == 0x1A:
				// CAN/SUB abort the string in x/ansi.
				f.state, f.utf8Remaining = c1Ground, 0
			case c == stringTerminatorByte:
				if f.utf8Remaining > 0 {
					// A continuation byte of a rune we are still inside —
					// not a real ST. Neutralize it so x/ansi's transition
					// table cannot dispatch on it, and stay in the string.
					replace = true
					f.utf8Remaining--
				} else {
					// Not inside a multi-byte sequence: a genuine 8-bit ST.
					f.state = c1Ground
				}
			case c >= 0xC2 && c <= 0xDF:
				f.utf8Remaining = 1 // 2-byte sequence lead
			case c >= 0xE0 && c <= 0xEF:
				f.utf8Remaining = 2 // 3-byte sequence lead
			case c >= 0xF0 && c <= 0xF4:
				f.utf8Remaining = 3 // 4-byte sequence lead
			case c >= 0x80 && c <= 0xBF:
				// An ordinary continuation byte (not the ambiguous value).
				if f.utf8Remaining > 0 {
					f.utf8Remaining--
				}
			default:
				// Any other byte (ASCII, or a stray byte outside a
				// well-formed sequence) cannot be mid-rune; resync.
				f.utf8Remaining = 0
			}
		}

		if replace {
			if out == nil {
				out = make([]byte, len(b))
				copy(out, b[:i])
			}
			out[i] = replacementByte
		} else if out != nil {
			out[i] = c
		}
	}
	if out != nil {
		return out
	}
	return b
}
