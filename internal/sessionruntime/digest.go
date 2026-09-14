package sessionruntime

// The structural digest a one-shot target is bound to (nocx-6q1uh.4, spec
// §6.2). It is the helper's own answer to "does the screen this token names
// still say what it said when it was minted" — content, not appearance:
// [Digest] hashes exactly the cells a program could redraw differently and
// nothing that a renderer alone would change, and it is deliberately blind to
// anything outside the row range it is asked about, because a spinner
// spinning two rows below a menu must never invalidate that menu's token.

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"

	"github.com/shady2k/nocx/internal/emulator"
)

// TargetKind is what a target names on the screen (spec §6.3): a menu's
// question through its selected option, an input box, the zone a menu would
// appear in, or an arbitrary named region. It is a string rather than an int
// enumeration because it crosses the wire unchanged (contracts/helper's
// session.target, nocx-6q1uh.5) and a caller reading a capture years from now
// should not need this package's source to know what "1" meant.
type TargetKind string

const (
	// TargetMenu names a menu's question through its last option, selection
	// included.
	TargetMenu TargetKind = "menu"
	// TargetInput names the input box, cursor included — the only kind that
	// carries the caret into its digest (includeCursor, below).
	TargetInput TargetKind = "input"
	// TargetWorking names the zone a menu would appear in: a target minted
	// against it is refused the moment a menu actually appears there, because
	// the rows it named have stopped meaning "no menu yet".
	TargetWorking TargetKind = "working"
	// TargetRegion names the whole visible screen, or a caller-chosen row
	// range: any descendant pane a key or a message is aimed at.
	TargetRegion TargetKind = "region"
)

// RowRange is the rows a target's digest covers, both ends inclusive. First
// and Last are counted the same way [emulator.Row] indices are — from the top
// of the active area — so a caller never converts between two row
// vocabularies to mint or verify a target.
type RowRange struct {
	First, Last int
}

// ScreenIdentity is what a digest is bound to BEFORE any cell is examined: the
// incarnation, which buffer is active and which instance of it, and the
// geometry. A change to any of these makes the rows underneath it
// [ScreenIdentity]-incomparable to whatever was minted — spec §6.2's
// "buffer switch, resize or incarnation change since the snapshot →
// incomparable" — which [Digest] enforces structurally by hashing identity
// first: two screens that differ only here hash differently even with byte-
// identical rows, and a caller need not special-case the comparison.
//
// BufferInstance exists because [emulator.Terminal.Screen] answers only WHICH
// buffer is active (primary or alternate), never an identity for the buffer
// ITSELF: a full-screen program that exits and a second one that immediately
// enters the alternate screen both report AltScreen true, with nothing here
// to tell the two apart. sessionruntime derives an identity the port does not
// carry by counting every observed toggle between the two buffers
// (Session.screenStateLocked, runtime.go) — entering the alternate screen and
// returning to the primary one each count, so a token minted against one
// full-screen program's buffer is incomparable to the next even on the one
// row range that happens to look the same on both.
type ScreenIdentity struct {
	At             Incarnation
	AltScreen      bool
	BufferInstance uint64
	Cols, Rows     int
}

// DigestVersion names the hash's own shape, so a future format change is a
// version bump rather than a silent reinterpretation of old bytes — nothing
// in this package reads it back today, and it exists for the caller that will
// (a token or a capture is free to record it beside the digest it names).
const DigestVersion = 1

// Digest hashes identity, then every cell of every row in r — grapheme,
// width and style — then that row's wrap flag, and finally the cursor when
// includeCursor is set. It is deliberately silent about anything outside r:
// a row the caller did not ask about contributes nothing, which is what lets
// a spinner two rows below a menu spin forever without invalidating the
// menu's own target.
//
// r is clamped to the rows actually supplied: a range naming a row past
// len(rows) — a geometry mismatch, or a caller that resolved rows before a
// resize landed — is read as far as rows goes and no further, rather than
// panicking on a slice a caller's own bookkeeping got wrong. That is a
// property of THIS function, not a way to detect a resize: [ScreenIdentity]'s
// Cols/Rows is what a caller compares to notice one.
func Digest(id ScreenIdentity, rows []emulator.Row, r RowRange, cur emulator.Cursor, includeCursor bool) [32]byte {
	h := sha256.New()
	writeDigestUint64(h, DigestVersion)
	writeDigestIdentity(h, id)

	first, last := r.First, r.Last
	if first < 0 {
		first = 0
	}
	if last >= len(rows) {
		last = len(rows) - 1
	}
	// r.First/r.Last are hashed as GIVEN, not as the clamped first/last used
	// for indexing above: they are part of what a target names (a range
	// naming different rows must digest differently even if both happen to
	// clamp to the same slice), never used to index memory here, so a
	// negative or out-of-range value changes only which digest results.
	writeDigestUint64(h, uint64(r.First)) // #nosec G115 -- hashed only, never indexed
	writeDigestUint64(h, uint64(r.Last))  // #nosec G115 -- hashed only, never indexed
	for y := first; y <= last; y++ {
		writeDigestRow(h, rows[y])
	}

	writeDigestBool(h, includeCursor)
	if includeCursor {
		// A real terminal's caret is always within 0..geometry-1; hashed
		// only, the same as above.
		writeDigestUint64(h, uint64(cur.X)) // #nosec G115 -- a caret column, never negative on a real terminal
		writeDigestUint64(h, uint64(cur.Y)) // #nosec G115 -- a caret row, never negative on a real terminal
		writeDigestBool(h, cur.Visible)
	}

	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func writeDigestIdentity(h hash.Hash, id ScreenIdentity) {
	writeDigestString(h, string(id.At.Session))
	writeDigestUint64(h, uint64(id.At.Generation))
	writeDigestBool(h, id.AltScreen)
	writeDigestUint64(h, id.BufferInstance)
	writeDigestUint64(h, uint64(id.Cols)) // #nosec G115 -- a terminal geometry, always positive
	writeDigestUint64(h, uint64(id.Rows)) // #nosec G115 -- a terminal geometry, always positive
}

func writeDigestRow(h hash.Hash, row emulator.Row) {
	writeDigestBool(h, row.Wrap)
	writeDigestBool(h, row.Continuation)
	writeDigestUint64(h, uint64(len(row.Cells)))
	for _, c := range row.Cells {
		writeDigestString(h, c.Grapheme)
		writeDigestUint64(h, uint64(c.Width))
		writeDigestBool(h, c.HasText)
		writeDigestStyle(h, c.Style)
	}
}

func writeDigestStyle(h hash.Hash, s emulator.Style) {
	writeDigestColor(h, s.Foreground)
	writeDigestColor(h, s.Background)
	writeDigestColor(h, s.UnderlineColor)
	writeDigestUint64(h, uint64(s.Attributes))
	writeDigestUint64(h, uint64(s.Underline))
}

func writeDigestColor(h hash.Hash, c emulator.Color) {
	writeDigestUint64(h, uint64(c.Kind))
	writeDigestUint64(h, uint64(c.Palette))
	writeDigestUint64(h, uint64(c.RGB.R))
	writeDigestUint64(h, uint64(c.RGB.G))
	writeDigestUint64(h, uint64(c.RGB.B))
}

// writeDigestUint64, writeDigestString and writeDigestBool are Digest's own
// canonical encoding: fixed-width and length-prefixed throughout, so that no
// two distinct field sequences can ever hash the same way — a grapheme "ab"
// followed by "c" must never collide with "a" followed by "bc", which a
// delimiter-based encoding could not promise and a length prefix always does.
func writeDigestUint64(h hash.Hash, v uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	h.Write(buf[:])
}

func writeDigestBool(h hash.Hash, b bool) {
	if b {
		writeDigestUint64(h, 1)
		return
	}
	writeDigestUint64(h, 0)
}

func writeDigestString(h hash.Hash, s string) {
	writeDigestUint64(h, uint64(len(s)))
	h.Write([]byte(s))
}
