package panegrid

import (
	"github.com/charmbracelet/x/ansi/parser"
)

// nocx-nru89.5 found that github.com/charmbracelet/x/vt (pinned by
// ADR-0041) parses bytes through github.com/charmbracelet/x/ansi's DEC ANSI
// transition table (x/ansi/parser/transition_table.go). Inside an OSC or
// DCS string, that table extends the "printable" range to 0x20-0xFF so a
// title's raw UTF-8 bytes pass through unexamined — except for one byte
// value it special-cases unconditionally: 0x9C, the 8-bit form of the
// String Terminator (ST). A title containing a multi-byte UTF-8 rune whose
// encoding happens to include the byte 0x9C — U+2733 (✳, `E2 9C B3`) is the
// reported case — ends the OSC/DCS string early and prints whatever comes
// after onto the grid at the cursor.
//
// # One owner of the string state
//
// nocx-nru89.11 requires ONE owner of "are we inside an OSC/DCS string, and
// where". x/ansi offers no supported way to change what x/vt's own parser
// does with 0x9C:
//
//   - `parser.Table` (x/ansi/parser/transition_table.go:17) is a
//     package-level `var`, not a field on `ansi.Parser`, and every read of
//     it — including the one inside `(*ansi.Parser).advance`
//     (x/ansi/parser.go:207: `state, action := parser.Table.Transition(p.state, b)`)
//     — goes through this one global. There is no `SetTable`, no
//     constructor argument, no parser option (x/ansi/parser.go, the whole
//     `Parser` type: no such method exists).
//   - `ansi.Handler` (x/ansi/parser_handler.go) exposes only
//     Print/Execute/HandleCsi/HandleEsc/HandleDcs/HandleOsc/HandlePm/
//     HandleApc/HandleSos — callbacks that fire AFTER the transition table
//     has already decided the byte was 0x9C=ST and dispatched. There is no
//     hook that runs before that decision.
//   - Mutating the package-level `parser.Table` at init would not create a
//     second, scoped owner — it would create a single shared mutable
//     global: every user of github.com/charmbracelet/x/ansi in this
//     process, not just this pane's grid, would inherit the change. The
//     bead forbids this outright, and it would not even be a safe
//     workaround if it were not forbidden.
//
// So there is no supported way to change what x/vt's real parser decides,
// and the only path left is a bridge in front of it that decides — for
// itself — whether the byte it is about to hand to `xvt.Emulator.Write` is
// a genuine ST or a UTF-8 continuation byte, and rewrites it if not.
//
// # Why this bridge is a mirror, not a second parser
//
// nocx-nru89.5's first version of this file hand-transcribed its own
// four-state guess at "are we inside an OSC/DCS string" (Ground / Escape /
// OSCString / DCSString). The second review (.internal/sdd/fix-review.md,
// findings 7 and 8) found it disagreed with x/ansi's real table in both
// directions: it missed starts x/ansi recognises (an 8-bit `0x9D` OSC or
// `0x90` DCS introducer, or a C0 control byte between ESC and `]` — x/ansi
// stays in EscapeState and executes it, transition_table.go:135-137, while
// the old filter dropped to ground on anything but ']', 'P' or ESC), and it
// corrupted text x/ansi's own Utf8State already decoded correctly (a UTF-8
// lead byte in ground state, or right after `ESC P`, diverts x/ansi's real
// parser into Utf8State — x/ansi/parser.go:181-204 — which then swallows
// every following byte, ESC included, until the rune is complete; the old
// filter did not model this at all).
//
// This version keeps NO separate transcription of x/ansi's own state
// machine. `state` below is driven exclusively by `parser.Table.Transition`
// — the exact table x/vt's own parser calls — so every transition this
// filter has in common with x/ansi (which is almost all of them: every CSI,
// Escape, EscapeIntermediate, DcsEntry/Intermediate/Param byte, every 8-bit
// C1 introducer, the CAN/SUB/ESC "anywhere" transitions, and the Utf8State
// byte-swallowing rule reproduced below from x/ansi/parser.go:181-204) can
// never drift from upstream, because it is not transcribed, it is looked
// up. The one piece x/ansi's table cannot express is the thing this filter
// exists to add: while genuinely inside OscStringState or DcsStringState
// (as this filter's own `state`, kept in lock-step with x/vt's, says), a
// `0x9C` byte that is still a title's own UTF-8 continuation byte — tracked
// with the same RFC 3629 lead/continuation byte classes x/ansi's own table
// uses for its Anywhere -> Utf8State transition
// (transition_table.go:113-115) — is rewritten to ASCII '?' before x/vt
// ever sees it, so x/vt's transition table cannot mistake it for a real ST.
// A genuine standalone 0x9C — no continuation bytes pending — is passed
// through unchanged and still ends the string exactly as x/vt already does
// correctly.
//
// This does not interpret what a title SAYS: two titles differing only in
// which dingbat glyph they use are filtered identically, and the byte class
// checks below are the same structural UTF-8 classification x/ansi's own
// table performs for the ranges it hands to Utf8State — this is completing
// that classification for the one case (0x9C inside a string x/ansi itself
// never decodes as UTF-8) where x/ansi's table gets it wrong, not a content
// judgment. AD-6 is unaffected for the same reason it was unaffected
// before: this is emulation, not sniffing.
//
// Scope stays OSC and DCS only — the two string types x/ansi extends to the
// 0x20-0xFF range (transition_table.go:264,217), and the only two the
// reported defect and its captures exercise. SOS/PM/APC strings keep
// x/ansi's existing (narrower, ASCII-only) handling, unaffected by anything
// in this file: their table rows are reached the same way every other
// state's rows are, through the plain `parser.Table.Transition` call below,
// with no special-casing for them anywhere in this filter.
type c1Filter struct {
	// state mirrors x/vt's own ansi.Parser.state. It is advanced with the
	// SAME table x/vt's parser uses (parser.Table), on the SAME byte x/vt
	// will actually receive (post-neutralisation), so the two never
	// diverge except for the one byte value this filter deliberately
	// changes.
	state parser.State

	// utf8Lead and utf8Collected reproduce ansi.Parser.advanceUtf8's
	// bookkeeping (x/ansi/parser.go:181-204) for the one piece of that
	// function the transition table itself cannot express: once a byte
	// diverts `state` to Utf8State, x/ansi's real Advance() stops
	// consulting the transition table AT ALL and instead collects bytes
	// blindly — including ESC, BEL, or any other byte that would otherwise
	// be special — until enough bytes for the rune started by the lead
	// byte have been seen, then returns to GroundState unconditionally.
	// Mirroring the table alone is not enough; this filter must also
	// mirror this bypass, or a byte "eaten" by a genuine ground-state rune
	// is misread here as a real escape introducer (the corruption in
	// fix-review.md finding 8).
	utf8Lead      byte
	utf8Collected int

	// oscDcsUTF8Remaining counts down the continuation bytes of a title's
	// own multi-byte rune while `state` is genuinely OscStringState or
	// DcsStringState. x/ansi does not decode UTF-8 inside those states at
	// all — it Puts every byte in the extended printable range unchanged
	// (transition_table.go:217,264) — so this is the one piece of
	// information x/ansi's table has no row for, and it is derived here
	// purely from RFC 3629 byte classes, never from what the title says.
	oscDcsUTF8Remaining int
}

const (
	stringTerminatorByte = 0x9C
	replacementByte      = '?' // ordinary ASCII; never special in any state
)

// utf8ByteLen reproduces the unexported function of the same name in
// github.com/charmbracelet/x/ansi (parser.go:406-417) byte for byte: it is
// what ansi.Parser.advanceUtf8 uses, via Rune(), to know how many bytes the
// rune begun by the lead byte needs. It cannot be imported (unexported, and
// in package `ansi` rather than `ansi/parser`), so it is reproduced here
// rather than re-derived, to stay exactly in step with what x/vt's own
// parser will do with the same lead byte.
func utf8ByteLen(b byte) int {
	switch {
	case b <= 0b0111_1111: // 0x00-0x7F
		return 1
	case b >= 0b1100_0000 && b <= 0b1101_1111: // 0xC0-0xDF
		return 2
	case b >= 0b1110_0000 && b <= 0b1110_1111: // 0xE0-0xEF
		return 3
	case b >= 0b1111_0000 && b <= 0b1111_0111: // 0xF0-0xF7
		return 4
	default:
		return -1
	}
}

// filter returns b unmodified — same slice, no allocation — unless a byte in
// it must be rewritten. That is the common case: most output carries no
// escape sequence at all, and of those that do, only a title whose text
// contains the exact byte value 0x9C at a continuation-byte position ever
// needs a substitution.
func (f *c1Filter) filter(b []byte) []byte {
	var out []byte // allocated lazily, only on the first actual replacement
	for i, c := range b {
		replace := false

		if f.state == parser.Utf8State {
			// Mirror ansi.Parser.advanceUtf8 exactly: the transition table
			// is not consulted at all while collecting a rune's bytes.
			f.utf8Collected++
			if f.utf8Collected >= utf8ByteLen(f.utf8Lead) {
				f.state = parser.GroundState
				f.utf8Collected = 0
			}
		} else {
			origState := f.state
			effective := c

			// The one case x/ansi's table cannot express: genuinely inside
			// an OSC/DCS string, a 0x9C that is still a continuation byte
			// of the title's own rune is not a real ST.
			if (origState == parser.OscStringState || origState == parser.DcsStringState) &&
				c == stringTerminatorByte && f.oscDcsUTF8Remaining > 0 {
				effective = replacementByte
				replace = true
			}

			// Look up x/vt's own transition on the byte x/vt will actually
			// receive, so this filter's state never diverges from x/vt's.
			next, action := parser.Table.Transition(origState, effective)

			if next == parser.OscStringState || next == parser.DcsStringState {
				switch {
				case c >= 0xC2 && c <= 0xDF: // 2-byte UTF-8 lead
					f.oscDcsUTF8Remaining = 1
				case c >= 0xE0 && c <= 0xEF: // 3-byte UTF-8 lead
					f.oscDcsUTF8Remaining = 2
				case c >= 0xF0 && c <= 0xF4: // 4-byte UTF-8 lead
					f.oscDcsUTF8Remaining = 3
				case c >= 0x80 && c <= 0xBF: // continuation byte (0x9C included)
					if f.oscDcsUTF8Remaining > 0 {
						f.oscDcsUTF8Remaining--
					}
				default:
					f.oscDcsUTF8Remaining = 0
				}
			} else {
				f.oscDcsUTF8Remaining = 0
			}

			if action == parser.CollectAction && next == parser.Utf8State {
				f.utf8Lead = effective
				f.utf8Collected = 1
			}

			f.state = next
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
