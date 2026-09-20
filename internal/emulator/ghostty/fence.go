package ghostty

import (
	"github.com/shady2k/nocx/internal/emulator"
)

// The render fence is the one sequence nocx needs out of the stream that the
// pinned library will not report: its unknown-sequence hook covers APC only
// ("Only APC sequences are currently reported", pinned vt/terminal.h at
// doc.go's commit), and there is no OSC hook at all. So the adapter matches
// the fence itself, on its own way into vt_write — this is the emulator
// parsing the stream it owns, not the backend sniffing: nothing outside this
// call sees a byte of it, and the library still parses every byte, fence
// included, exactly as before.
//
// # The shape is exact, and it is one contract with two other readers
//
// ESC ] 1 3 3 7 ; N O C X _ F E N C E ; <64 lowercase hex> BEL — the same
// bytes the shell writes (internal/shellintegration/scripts/nocx.bash) and
// the renderer parses (frontend/src/renderers/xterm.ts, parseRenderFence).
// Anything else in the OSC 1337 namespace — the recovery fence's
// NOCX_RECOVERY key, iTerm2's, a nonce that is not exactly 64 lowercase hex
// chars — matches nothing and yields no effect. BEL is the only terminator
// matched because BEL is what the writer writes; a parser that also accepted
// ESC \ would be a second spelling of the writer's contract with nobody
// writing it.
//
// # The scanner is state, never a buffer
//
// scanFence walks the chunk once, keeping only how many bytes of the sequence
// the stream has matched (t.fenceIdx) and the nonce bytes seen so far
// (t.fenceNonce). It never holds a byte back from the library and nothing is
// fed twice: Ingest feeds every byte exactly once, in arrival order, and a
// fence only decides WHERE that one feed splits — before its BEL's snapshot,
// after it the rest. A candidate that straddles two Ingest calls costs
// nothing: the position is the terminal's state under mu, like every other
// field.
//
// At most one candidate can be open at a time, and the state machine needs no
// backtracking to prove it: every byte of the fixed prefix is either a
// non-hex literal (so a nested start inside it dies with the parent) or the
// hex/BEL region (where the parent has already died at the first non-matching
// byte, which is the same byte that would start a child).
const (
	fenceFixed = "\x1b]1337;NOCX_FENCE;"
	fenceLen   = len(fenceFixed) + 64 + 1
)

// fenceMatches reports whether b is the idx'th byte of the fence sequence:
// the fixed prefix literally, the nonce as lowercase hex, and BEL last.
func fenceMatches(idx int, b byte) bool {
	switch {
	case idx < len(fenceFixed):
		return b == fenceFixed[idx]
	case idx < fenceLen-1:
		return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')
	default:
		return b == 0x07
	}
}

// scanFence advances the scanner over b and answers how many bytes of it may
// be fed to the library before the fence must be sighted: through the BEL
// that completes a sequence, or all of b when none completes in it. The
// caller feeds exactly the bytes named and sights the fence when fired is
// true, so the snapshot sees the screen at the fence and nothing after it.
func (t *terminal) scanFence(b []byte) (n int, fired bool) {
	for i, c := range b {
		if !fenceMatches(t.fenceIdx, c) {
			// The candidate dies at this byte, and the byte itself may begin
			// the next one: an ESC in the stream is always a potential fence.
			if c == fenceFixed[0] {
				t.fenceIdx = 1
			} else {
				t.fenceIdx = 0
			}
			continue
		}
		if t.fenceIdx == fenceLen-1 {
			t.fenceIdx = 0
			return i + 1, true
		}
		if t.fenceIdx >= len(fenceFixed) {
			t.fenceNonce[t.fenceIdx-len(fenceFixed)] = c
		}
		t.fenceIdx++
	}
	return len(b), false
}

// sightFence appends the fence effect: the nonce as the stream carried it and
// the rows the fence was drawn over, both copied — the nonce out of the
// scanner's own state, which the next chunk would overwrite, and the source
// out of a grid the next vt_write is free to move. The caller holds mu (it is
// inside Ingest), which is what makes the snapshot one moment: the fence's
// BEL is in the library and nothing after it is.
//
// A sighting carries no authority. It LOCATES an event the authenticated half
// of the handshake owns (ADR-0024 decision 1); this method completes nothing
// and calls nothing in the runtime — it produces the effect and returns.
func (t *terminal) sightFence() {
	nonce := make([]byte, len(t.fenceNonce))
	copy(nonce, t.fenceNonce[:])
	t.effects = append(t.effects, emulator.Effect{
		Kind:   emulator.EffectFence,
		Body:   nonce,
		Source: t.fenceSource(),
	})
}

// fenceSource reads the text of the rows the fence was drawn over: the
// logical line the cursor sits on, its physical rows joined with newlines, a
// row's text being its cells' graphemes with the wide-cluster spacers
// skipped — the same reading the port's own serialisers are built on
// (emulator.Row's Wrap and Continuation exist for exactly this join).
//
// The walk is best effort on read errors and the nonce never is: the grid
// cannot be re-asked later (the screen moves), but a sighting whose nonce is
// exact is worth reporting even if a cell read failed mid-row, and a failed
// read here has no caller to return to. The bounds are the cursor's own row
// and the geometry's, both of which the library has already accepted.
func (t *terminal) fenceSource() []byte {
	cur, err := t.cursorLocked()
	if err != nil {
		return nil
	}
	y := cur.Y
	for y > 0 {
		_, cont, err := t.rowFlags(y)
		if err != nil || !cont {
			break
		}
		y--
	}
	var text []byte
	for {
		wrapped, _, err := t.rowFlags(y)
		if err != nil {
			return text
		}
		text = t.rowText(y, text)
		if !wrapped {
			return text
		}
		y++
		if y >= t.geom.Rows {
			return text
		}
		text = append(text, '\n')
	}
}

// rowText appends one physical row's text to into: each cell's grapheme in
// column order, the spacer cells a wide cluster leaves behind skipped — they
// carry nothing, and carrying them would put a second NUL-ish hole in text
// the screen does not have. A read that fails returns what the row has so
// far, for the same reason fenceSource is best effort.
func (t *terminal) rowText(y int, into []byte) []byte {
	for x := range t.geom.Cols {
		cell, err := t.readCell(x, y)
		if err != nil {
			return into
		}
		if cell.Width == emulator.WidthSpacerTail || cell.Width == emulator.WidthSpacerHead {
			continue
		}
		into = append(into, cell.Grapheme...)
	}
	return into
}
