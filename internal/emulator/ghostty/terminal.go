package ghostty

/*
#cgo CFLAGS: -I${SRCDIR}/.vendor/ghostty/zig-out/include -DGHOSTTY_STATIC
#cgo LDFLAGS: ${SRCDIR}/.vendor/ghostty/zig-out/lib/libghostty-vt.a -lm -lpthread
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/shady2k/nocx/internal/emulator"
)

// maxGraphemeCodepoints bounds the stack buffer a grapheme cluster is read
// into. A cluster longer than this is read again into an exactly-sized buffer
// rather than truncated: a silently clipped cluster is a wrong screen, and the
// library reports the required size instead of hiding the overflow.
const maxGraphemeCodepoints = 16

// maxEncodedKey bounds the stack buffer one key event is encoded into. Escape
// sequences are short by construction; the library reports the required size
// when one is not, and the caller retries into an exactly-sized buffer.
const maxEncodedKey = 128

// maxEncodedPaste bounds the stack buffer one paste is encoded into. A paste is
// arbitrarily long, so this is a first attempt rather than a limit: the library
// reports the required size and the caller retries into an exactly-sized
// buffer. It is the size that covers a line or two of pasted text — the common
// case at a prompt — without a heap allocation.
const maxEncodedPaste = 256

// maxEncodedMouse bounds the stack buffer one mouse event is encoded into. A
// mouse sequence is a fixed prefix, two numbers and a terminator — the numbers
// cannot exceed the grid, which is at most five digits — so this is a bound
// rather than an estimate, and the retry exists only so that a library that one
// day encodes something longer is answered with bytes instead of a failure.
const maxEncodedMouse = 32

// maxEncodedFocus bounds the stack buffer one focus report is encoded into.
// CSI I and CSI O are three bytes; the bound is written with room to spare for
// the same reason as the mouse event's.
const maxEncodedFocus = 16

// The program's own DEC private modes this adapter asks about. They are named
// here rather than written at the call sites because the number IS the
// protocol, and a call site reading `t.mode(2004)` says nothing about what 2004
// is.
const (
	// modeFocusEvent is DEC private mode 1004: the program asks to be told when
	// the terminal's window gains and loses focus.
	modeFocusEvent = 1004
	// modeBracketedPaste is DEC private mode 2004: the program asks for pastes
	// to be wrapped in the bracketed-paste sequences.
	modeBracketedPaste = 2004
)

// terminal is one libghostty-vt terminal. It is the only implementation of
// emulator.Terminal in the tree, and it owns the upstream handle for its whole
// life: nothing outside this package can name, copy or free it.
//
// # Every field is guarded by mu, from the moment New returns
//
// CONSTRUCTION IS THE EXEMPTION, AND IT IS NOT A LOCK. New fills these fields,
// sizes the terminal and installs its callbacks before the value is handed to
// anybody, so no other goroutine can observe a half-built terminal and there is
// nothing for a mutex to exclude. (The registry entry is taken before the
// callbacks are installed, because the callbacks need the handle; a callback
// firing during construction would run on this same goroutine and append to
// t.replies or t.effects without a lock, which is why the ordering is written
// down here rather than left to be re-derived.)
//
// From then on, every field is under mu, and ADR-0065 point 3 requires exactly
// that. There are three reasons it is a mutex rather than a convention:
//
//   - The library is not reentrant. A grid reference dies at the next mutating
//     call, so a read that yielded to a write would read a cell that no longer
//     exists.
//   - A runtime reads a row while a reader goroutine ingests the program's
//     output, and neither should have to coordinate with the other.
//   - The effect callbacks run SYNCHRONOUSLY inside the call that is holding mu,
//     on the same goroutine. They therefore take no lock: taking mu inside them
//     would deadlock on the first reply, and writing t.replies without one is
//     safe precisely because the holder is the only writer.
type terminal struct {
	mu sync.Mutex
	// t is the upstream handle, and it is nil exactly when the terminal is
	// closed. Every method checks it under mu rather than holding a separate
	// closed flag, so there is no state in which one is true and the other is
	// not.
	t   C.GhosttyTerminal
	enc C.GhosttyKeyEncoder
	// menc is the mouse encoder. It is kept for the terminal's whole life
	// rather than allocated per event because an encoder holds the state the
	// port must NOT: the terminal's tracking mode and output format are
	// re-derived from the terminal before every event, so nothing the PROGRAM
	// did can go stale between two calls (EncodeKey's comment carries the same
	// argument for the key encoder).
	menc C.GhosttyMouseEncoder
	// id is this terminal's key in the registry the callbacks look it up by. A
	// C callback cannot carry a Go pointer, so it carries a number instead.
	id   uintptr
	geom emulator.Geometry
	// replies holds what the program asked to be written back to the PTY. See
	// the struct doc: only the goroutine holding mu writes it, and the bytes in
	// it are already copied out of the library's borrowed memory.
	replies []byte
	// effects holds the non-visual effects the program's output produced, in
	// the order they arrived. It is written by the effect callbacks under the
	// same rule as replies — the goroutine holding mu is the only writer — and
	// is drained whole by Effects.
	effects []emulator.Effect
}

var (
	regMu sync.Mutex
	// reg maps a callback handle to the terminal that owns it. Handles are
	// minted from 1 so that a zero handle is obviously not one.
	reg            = map[uintptr]*terminal{}
	nextID uintptr = 1
)

// register takes a handle for t and records it. The handle is a number the C
// side passes back to the callbacks; it is never a Go pointer, because cgo
// forbids one crossing into C and the library stores it for the terminal's
// whole life.
func register(t *terminal) uintptr {
	regMu.Lock()
	defer regMu.Unlock()
	id := nextID
	nextID++
	reg[id] = t
	return id
}

func unregister(id uintptr) {
	regMu.Lock()
	defer regMu.Unlock()
	delete(reg, id)
}

func lookup(id uintptr) *terminal {
	regMu.Lock()
	defer regMu.Unlock()
	return reg[id]
}

// New creates a terminal of the given geometry.
//
// The returned value owns everything the call allocated: [emulator.Terminal]
// .Close releases the terminal, its two encoders and its registry entry, and
// there is no other way to reach them.
func New(g emulator.Geometry) (emulator.Terminal, error) {
	if !g.Valid() {
		return nil, fmt.Errorf("ghostty: %w: %dx%d cells, %dx%d px", emulator.ErrOutOfRange,
			g.Cols, g.Rows, g.CellWidthPx, g.CellHeightPx)
	}
	var handle C.GhosttyTerminal
	if r := C.ghostty_terminal_new(nil, &handle, C.uint16_t(g.Cols), C.uint16_t(g.Rows)); r != C.GHOSTTY_SUCCESS {
		return nil, resultError("terminal_new", r)
	}
	t := &terminal{t: handle, geom: g}
	t.id = register(t)
	if err := t.install(g); err != nil {
		// release and not a hand-rolled free: install owns three handles by the
		// time it can fail (the terminal, the key encoder, the mouse encoder),
		// and the path that has to remember all three is the path that
		// eventually forgets one.
		t.release()
		return nil, err
	}
	return t, nil
}

// install sizes the terminal and wires its callbacks. It is separate from New
// so that every failure after the handle exists goes down one path that frees
// it — release — rather than four that must each remember what was allocated
// before them.
func (t *terminal) install(g emulator.Geometry) error {
	// The pixel size is part of the geometry a program can ask about (XTWINOPS,
	// and mode 2048's in-band reports), and ghostty_terminal_new takes only the
	// cell grid. A resize that does not change the grid still applies it — the
	// library documents that — so this is a resize rather than a second way to
	// construct a terminal.
	if r := C.ghostty_terminal_resize(t.t, C.uint16_t(g.Cols), C.uint16_t(g.Rows),
		C.uint32_t(g.CellWidthPx), C.uint32_t(g.CellHeightPx)); r != C.GHOSTTY_SUCCESS {
		return resultError("terminal_resize", r)
	}
	if r := C.nocxInstall(t.t, C.uintptr_t(t.id)); r != C.GHOSTTY_SUCCESS {
		return resultError("terminal_set", r)
	}
	var enc C.GhosttyKeyEncoder
	if r := C.ghostty_key_encoder_new(nil, &enc); r != C.GHOSTTY_SUCCESS {
		return resultError("key_encoder_new", r)
	}
	t.enc = enc
	var menc C.GhosttyMouseEncoder
	if r := C.ghostty_mouse_encoder_new(nil, &menc); r != C.GHOSTTY_SUCCESS {
		return resultError("mouse_encoder_new", r)
	}
	t.menc = menc
	return nil
}

// nocxGoWritePty is the program's replies arriving from the library. It is
// called synchronously from inside Ingest or Resize, on the goroutine that
// holds the terminal's lock, and the bytes it is handed die when the parser
// that produced them returns — which is why they are copied here and nowhere
// else (ADR-0065 point 3).
//
//export nocxGoWritePty
func nocxGoWritePty(handle C.uintptr_t, data *C.uint8_t, n C.size_t) {
	t := lookup(uintptr(handle))
	if t == nil {
		return
	}
	t.replies = append(t.replies, copyBorrowed(data, n)...)
}

func (t *terminal) Geometry() (emulator.Geometry, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return emulator.Geometry{}, emulator.ErrClosed
	}
	return t.geom, nil
}

func (t *terminal) Resize(g emulator.Geometry) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return nil, emulator.ErrClosed
	}
	if !g.Valid() {
		return nil, fmt.Errorf("ghostty: %w: %dx%d cells, %dx%d px", emulator.ErrOutOfRange,
			g.Cols, g.Rows, g.CellWidthPx, g.CellHeightPx)
	}
	if r := C.ghostty_terminal_resize(t.t, C.uint16_t(g.Cols), C.uint16_t(g.Rows),
		C.uint32_t(g.CellWidthPx), C.uint32_t(g.CellHeightPx)); r != C.GHOSTTY_SUCCESS {
		// The geometry in force is the one the library refused to leave, so
		// nothing here changes: a refused resize leaves the previous size
		// standing on both sides of the port.
		return nil, resultError("terminal_resize", r)
	}
	t.geom = g
	return t.takeReplies(), nil
}

func (t *terminal) Ingest(b []byte) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return nil, emulator.ErrClosed
	}
	if len(b) > 0 {
		C.ghostty_terminal_vt_write(t.t, (*C.uint8_t)(unsafe.Pointer(&b[0])), C.size_t(len(b)))
	}
	return t.takeReplies(), nil
}

// takeReplies hands the accumulated replies to the caller and starts a fresh
// buffer, so no reply is copied twice: each was copied once, out of the
// library's borrowed memory, on the way in.
func (t *terminal) takeReplies() []byte {
	if len(t.replies) == 0 {
		return nil
	}
	out := t.replies
	t.replies = nil
	return out
}

func (t *terminal) Screen() (emulator.Screen, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return emulator.ScreenPrimary, emulator.ErrClosed
	}
	var screen C.GhosttyTerminalScreen
	if r := C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_ACTIVE_SCREEN,
		unsafe.Pointer(&screen)); r != C.GHOSTTY_SUCCESS {
		return emulator.ScreenPrimary, resultError("active_screen", r)
	}
	switch screen {
	case C.GHOSTTY_TERMINAL_SCREEN_PRIMARY:
		return emulator.ScreenPrimary, nil
	case C.GHOSTTY_TERMINAL_SCREEN_ALTERNATE:
		return emulator.ScreenAlternate, nil
	default:
		// A buffer upstream did not name is not the primary one: a caller that
		// was told "primary" here would read the wrong screen and believe it.
		return emulator.ScreenPrimary, fmt.Errorf("ghostty: screen %d: %w", screen, emulator.ErrUnsupported)
	}
}

func (t *terminal) Row(y int) (emulator.Row, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return emulator.Row{}, emulator.ErrClosed
	}
	if y < 0 || y >= t.geom.Rows {
		return emulator.Row{}, fmt.Errorf("ghostty: row %d of %d: %w", y, t.geom.Rows, emulator.ErrOutOfRange)
	}
	wrapped, continuation, err := t.rowFlags(y)
	if err != nil {
		return emulator.Row{}, err
	}
	cells := make([]emulator.Cell, 0, t.geom.Cols)
	for x := range t.geom.Cols {
		cell, err := t.readCell(x, y)
		if err != nil {
			return emulator.Row{}, err
		}
		cells = append(cells, cell)
	}
	return emulator.Row{Cells: cells, Wrap: wrapped, Continuation: continuation}, nil
}

func (t *terminal) Cell(x, y int) (emulator.Cell, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return emulator.Cell{}, emulator.ErrClosed
	}
	if x < 0 || x >= t.geom.Cols || y < 0 || y >= t.geom.Rows {
		return emulator.Cell{}, fmt.Errorf("ghostty: cell %d,%d of %dx%d: %w",
			x, y, t.geom.Cols, t.geom.Rows, emulator.ErrOutOfRange)
	}
	return t.readCell(x, y)
}

// rowFlags reads a line's soft-wrap state. The row is read through a
// reference at column 0 because the wrap flags live on the line and not on a
// cell, and any cell of the line yields the same line.
func (t *terminal) rowFlags(y int) (wrapped, continuation bool, err error) {
	var ref C.GhosttyGridRef
	if r := C.nocxGridRefAt(t.t, 0, C.uint16_t(y), &ref); r != C.GHOSTTY_SUCCESS {
		return false, false, resultError("grid_ref", r)
	}
	var row C.GhosttyRow
	if r := C.ghostty_grid_ref_row(&ref, &row); r != C.GHOSTTY_SUCCESS {
		return false, false, resultError("grid_ref_row", r)
	}
	var wrap C.bool
	if r := C.ghostty_row_get(row, C.GHOSTTY_ROW_DATA_WRAP, unsafe.Pointer(&wrap)); r != C.GHOSTTY_SUCCESS {
		return false, false, resultError("row_wrap", r)
	}
	var cont C.bool
	if r := C.ghostty_row_get(row, C.GHOSTTY_ROW_DATA_WRAP_CONTINUATION, unsafe.Pointer(&cont)); r != C.GHOSTTY_SUCCESS {
		return false, false, resultError("row_wrap_continuation", r)
	}
	return bool(wrap), bool(cont), nil
}

// readCell reads one position. It assumes the caller holds mu: the reference it
// takes is only valid until the next mutating call, so it is used here and
// never returned.
//
// Each position costs its own grid reference and the four calls behind it — the
// reference itself, the cell, two cell fields and the style, with a grapheme
// read on top when the cell holds text. That is the CAPTURE path and not the
// frame path: the library's incremental render-state API (render.h) is the one
// built for per-frame work, a Row here is O(columns) references, and this port
// does not carry the render state at all (see the port's package doc).
func (t *terminal) readCell(x, y int) (emulator.Cell, error) {
	var ref C.GhosttyGridRef
	if r := C.nocxGridRefAt(t.t, C.uint16_t(x), C.uint16_t(y), &ref); r != C.GHOSTTY_SUCCESS {
		return emulator.Cell{}, resultError("grid_ref", r)
	}
	var cell C.GhosttyCell
	if r := C.ghostty_grid_ref_cell(&ref, &cell); r != C.GHOSTTY_SUCCESS {
		return emulator.Cell{}, resultError("grid_ref_cell", r)
	}
	var hasText C.bool
	if r := C.ghostty_cell_get(cell, C.GHOSTTY_CELL_DATA_HAS_TEXT, unsafe.Pointer(&hasText)); r != C.GHOSTTY_SUCCESS {
		return emulator.Cell{}, resultError("cell_has_text", r)
	}
	var wide C.GhosttyCellWide
	if r := C.ghostty_cell_get(cell, C.GHOSTTY_CELL_DATA_WIDE, unsafe.Pointer(&wide)); r != C.GHOSTTY_SUCCESS {
		return emulator.Cell{}, resultError("cell_wide", r)
	}
	var facts C.nocxStyleFacts
	if r := C.nocxStyleAt(&ref, &facts); r != C.GHOSTTY_SUCCESS {
		return emulator.Cell{}, resultError("grid_ref_style", r)
	}
	style, err := styleOf(facts)
	if err != nil {
		return emulator.Cell{}, err
	}
	out := emulator.Cell{
		Width:   cellWidth(wide),
		HasText: bool(hasText),
		Style:   style,
	}
	if out.HasText {
		grapheme, err := t.readGrapheme(&ref)
		if err != nil {
			return emulator.Cell{}, err
		}
		out.Grapheme = grapheme
	}
	return out, nil
}

// readGrapheme reads the whole cluster at a reference: the base codepoint and
// every combining codepoint the terminal assembled into the same cell. This is
// the property ADR-0065 chose libghostty-vt for, and it is only visible from
// here because the cluster is read whole rather than one rune at a time.
func (t *terminal) readGrapheme(ref *C.GhosttyGridRef) (string, error) {
	buf := make([]C.uint32_t, maxGraphemeCodepoints)
	n := C.size_t(len(buf))
	r := C.ghostty_grid_ref_graphemes(ref, &buf[0], C.size_t(len(buf)), &n)
	if r == C.GHOSTTY_OUT_OF_SPACE {
		// The library reports the required size rather than truncating, so a
		// cluster longer than the stack buffer costs one allocation and no
		// loss.
		//
		// A required size of zero is a contradiction — out of space with
		// nothing to write — and it is reported as one rather than answered
		// with an empty cluster, which would be a cell whose text silently
		// disappeared.
		if n == 0 {
			return "", fmt.Errorf("ghostty: graphemes: %w: out of space with nothing required", emulator.ErrFailure)
		}
		buf = make([]C.uint32_t, n)
		r = C.ghostty_grid_ref_graphemes(ref, &buf[0], C.size_t(len(buf)), &n)
	}
	if r != C.GHOSTTY_SUCCESS {
		return "", resultError("grid_ref_graphemes", r)
	}
	var sb strings.Builder
	for _, cp := range buf[:int(n)] {
		sb.WriteRune(rune(cp))
	}
	return sb.String(), nil
}

func (t *terminal) EncodeKey(ev emulator.KeyEvent) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return nil, emulator.ErrClosed
	}
	key, ok := cKey(ev.Key)
	if !ok {
		return nil, fmt.Errorf("ghostty: key %d: %w", ev.Key, emulator.ErrUnsupported)
	}
	mods := cMods(ev.Mods)
	action, ok := cAction(ev.Action)
	if !ok {
		return nil, fmt.Errorf("ghostty: key action %d: %w", ev.Action, emulator.ErrUnsupported)
	}
	// The encoder is re-derived from the terminal before every event rather
	// than configured once at construction: application cursor keys, the
	// keypad, modifyOtherKeys and the Kitty flags belong to the PROGRAM, and it
	// changes them whenever it likes (ADR-0065 reason 3 — the gate this bead
	// exists to discharge).
	C.ghostty_key_encoder_setopt_from_terminal(t.enc, t.t)

	var text *C.char
	if len(ev.Text) > 0 {
		text = (*C.char)(unsafe.Pointer(unsafe.StringData(ev.Text)))
	}
	var buf [maxEncodedKey]C.char
	var n C.size_t
	r := C.nocxKeyEncode(t.enc, key, mods, action, text, C.size_t(len(ev.Text)),
		&buf[0], C.size_t(len(buf)), &n)
	if r == C.GHOSTTY_OUT_OF_SPACE {
		// As in readGrapheme: out of space with nothing required is a
		// contradiction, and it is reported rather than answered with no bytes.
		if n == 0 {
			return nil, fmt.Errorf("ghostty: key_encode: %w: out of space with nothing required", emulator.ErrFailure)
		}
		grown := make([]C.char, n)
		r = C.nocxKeyEncode(t.enc, key, mods, action, text, C.size_t(len(ev.Text)),
			&grown[0], C.size_t(len(grown)), &n)
		if r != C.GHOSTTY_SUCCESS {
			return nil, resultError("key_encode", r)
		}
		return goBytes(grown[:int(n)]), nil
	}
	if r != C.GHOSTTY_SUCCESS {
		return nil, resultError("key_encode", r)
	}
	return goBytes(buf[:int(n)]), nil
}

// goBytes copies an encoded sequence out of the C buffer it was written into.
// It is a copy rather than a reinterpretation because []C.char is not []byte
// and because the buffer it may come from is a stack local.
func goBytes(in []C.char) []byte {
	out := make([]byte, len(in))
	for i, c := range in {
		out[i] = byte(c)
	}
	return out
}

// Paste hands the terminal a paste of text and returns the bytes the program is
// to be sent, framed per the terminal's own state.
//
// # Why the library encodes the paste and not this adapter
//
// The framing IS mode 2004, and mode 2004 is the program's: a shell that turned
// bracketed paste on must receive the text wrapped in the bracketed-paste
// sequences, and one that did not must receive it bare, and nothing in the
// caller's request says which. ghostty_paste_encode applies that rule from the
// mode this adapter reads out of the terminal, strips the bytes that cannot
// travel through a paste (NUL, ESC and DEL are replaced with spaces), and on
// the unbracketed path turns newlines into carriage returns — every one of
// which is the terminal's own rule rather than a caller's.
//
// The paste-event path (Kitty's OSC 5522, in which a paste becomes an event the
// program then reads through the clipboard) is NOT taken, because upstream
// enables it only when a clipboard_read callback is installed and this adapter
// installs none: nocx's clipboard is the runtime's to mediate (design §6.2),
// and an adapter that answered reads with an empty clipboard would be making
// that decision here.
//
// # Why the bytes come back instead of being written
//
// The caller owns the single ordered write path to the PTY (see the port's
// Paste). Upstream streams a paste through the write_pty callback in chunks,
// which is the right shape for an embedder that writes as it goes and the wrong
// one here: the chunks would land in the reply buffer and be indistinguishable
// from a cursor report the program is waiting for.
func (t *terminal) Paste(text []byte) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return nil, emulator.ErrClosed
	}
	if len(text) == 0 {
		// Nothing to paste is nothing to say. An empty paste is not an error:
		// an empty clipboard is a real thing, and the caller has nothing to
		// write either way.
		return nil, nil
	}
	bracketed, err := t.mode(modeBracketedPaste)
	if err != nil {
		return nil, err
	}
	// ghostty_paste_encode REWRITES ITS INPUT: the byte stripping happens in
	// the buffer it is given. The buffer it is given is therefore a copy, so a
	// caller's slice cannot change under it — and the copy is taken per attempt
	// because a second pass would be encoding bytes the first pass already
	// rewrote.
	encode := func(out *C.char, outLen C.size_t) (C.GhosttyResult, C.size_t) {
		scratch := make([]byte, len(text))
		copy(scratch, text)
		var n C.size_t
		r := C.ghostty_paste_encode((*C.char)(unsafe.Pointer(&scratch[0])), C.size_t(len(scratch)),
			C.bool(bracketed), out, outLen, &n)
		return r, n
	}
	var buf [maxEncodedPaste]C.char
	r, n := encode(&buf[0], C.size_t(len(buf)))
	if r == C.GHOSTTY_OUT_OF_SPACE {
		// As in readGrapheme: out of space with nothing required is a
		// contradiction, and it is reported rather than answered with no bytes.
		if n == 0 {
			return nil, fmt.Errorf("ghostty: paste_encode: %w: out of space with nothing required", emulator.ErrFailure)
		}
		grown := make([]C.char, n)
		r, n = encode(&grown[0], C.size_t(len(grown)))
		if r != C.GHOSTTY_SUCCESS {
			return nil, resultError("paste_encode", r)
		}
		return goBytes(grown[:int(n)]), nil
	}
	if r != C.GHOSTTY_SUCCESS {
		return nil, resultError("paste_encode", r)
	}
	return goBytes(buf[:int(n)]), nil
}

// Mouse hands the terminal one mouse event and returns the bytes the program is
// to be sent, in the tracking mode and output format the program set.
//
// # The mode is re-derived, and the coordinates are the port's
//
// ghostty_mouse_encoder_setopt_from_terminal reads the tracking mode (X10,
// normal, button, any-event) and the output format (X10, UTF-8, SGR, URXVT,
// SGR-pixels) out of the terminal, because the same click is a different
// sequence under each and the program chose. It is done before EVERY event
// rather than once at construction: a program that turns tracking on, changes
// format or turns it off again between two clicks would otherwise be sent the
// previous program's encoding.
//
// The size context is what makes the port's cell coordinates mean anything to
// the library, which thinks in surface pixels: one pixel per column and one per
// row makes a position of (x, y) mean cell (x, y), and the screen is exactly
// the grid because a position outside the grid is outside the terminal.
//
// Two encoder options are set from the event rather than read from the
// terminal, and both are about this port's shape rather than about the library.
// Motion deduplication is turned OFF so that the same event twice is the same
// bytes twice: a library that answered the second one with nothing would make
// this method's answer depend on the call before it. And "any button pressed"
// is set from the event's own button, because that is all the caller has told
// this port about the mouse: a motion that names a button is a motion with that
// button held — which is the whole of what DECSET 1002 exists to report — and
// without the flag the encoder declines to encode it at all.
func (t *terminal) Mouse(ev emulator.MouseEvent) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return nil, emulator.ErrClosed
	}
	action, ok := cMouseAction(ev.Action)
	if !ok {
		return nil, fmt.Errorf("ghostty: mouse action %d: %w", ev.Action, emulator.ErrUnsupported)
	}
	button, ok := cMouseButton(ev.Button)
	if !ok {
		return nil, fmt.Errorf("ghostty: mouse button %d: %w", ev.Button, emulator.ErrUnsupported)
	}
	hasButton := ev.Button != emulator.MouseNone
	size := C.GhosttyMouseEncoderSize{
		size:          C.size_t(unsafe.Sizeof(C.GhosttyMouseEncoderSize{})),
		screen_width:  C.uint32_t(t.geom.Cols),
		screen_height: C.uint32_t(t.geom.Rows),
		cell_width:    1,
		cell_height:   1,
	}
	C.ghostty_mouse_encoder_setopt(t.menc, C.GHOSTTY_MOUSE_ENCODER_OPT_SIZE,
		unsafe.Pointer(&size))
	off := C.bool(false)
	C.ghostty_mouse_encoder_setopt(t.menc, C.GHOSTTY_MOUSE_ENCODER_OPT_TRACK_LAST_CELL,
		unsafe.Pointer(&off))
	held := C.bool(hasButton)
	C.ghostty_mouse_encoder_setopt(t.menc, C.GHOSTTY_MOUSE_ENCODER_OPT_ANY_BUTTON_PRESSED,
		unsafe.Pointer(&held))
	C.ghostty_mouse_encoder_setopt_from_terminal(t.menc, t.t)

	encode := func(out *C.char, outLen C.size_t) (C.GhosttyResult, C.size_t) {
		var n C.size_t
		r := C.nocxMouseEncode(t.menc, action, button, C.bool(hasButton), cMods(ev.Mods),
			C.float(ev.X), C.float(ev.Y), out, outLen, &n)
		return r, n
	}
	var buf [maxEncodedMouse]C.char
	r, n := encode(&buf[0], C.size_t(len(buf)))
	if r == C.GHOSTTY_OUT_OF_SPACE {
		// As in readGrapheme: out of space with nothing required is a
		// contradiction, and it is reported rather than answered with no bytes.
		if n == 0 {
			return nil, fmt.Errorf("ghostty: mouse_encode: %w: out of space with nothing required", emulator.ErrFailure)
		}
		grown := make([]C.char, n)
		r, n = encode(&grown[0], C.size_t(len(grown)))
		if r != C.GHOSTTY_SUCCESS {
			return nil, resultError("mouse_encode", r)
		}
		return goBytes(grown[:int(n)]), nil
	}
	if r != C.GHOSTTY_SUCCESS {
		return nil, resultError("mouse_encode", r)
	}
	if n == 0 {
		// No bytes is the program not being interested: either no tracking mode
		// is enabled at all, or the event is one the mode it chose does not
		// report (a motion under normal tracking, a button the encoder has no
		// identity for). All of them are [ErrUnsupported] rather than an empty
		// success, because the port's answer to "what does this event put in
		// the program's input stream" is "nothing does", and a caller that
		// could not tell that from "the bytes are empty" would write nothing
		// and believe it had written something.
		return nil, fmt.Errorf("ghostty: mouse action %d button %d: %w",
			ev.Action, ev.Button, emulator.ErrUnsupported)
	}
	return goBytes(buf[:int(n)]), nil
}

// Focus hands the terminal one focus report and returns the bytes the program
// is to be sent when it asked for focus reporting.
//
// The mode IS the method: DEC private mode 1004 is the program saying it wants
// to be told, and a terminal that sent the report anyway would be writing into
// a program's input stream because a window moved. The report itself is the
// library's encoding (CSI I and CSI O), for the same reason every other
// sequence here is: the bytes are the protocol's, not this adapter's.
func (t *terminal) Focus(gained bool) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return nil, emulator.ErrClosed
	}
	on, err := t.mode(modeFocusEvent)
	if err != nil {
		return nil, err
	}
	if !on {
		return nil, fmt.Errorf("ghostty: mode %d: %w", modeFocusEvent, emulator.ErrUnsupported)
	}
	var event C.GhosttyFocusEvent = C.GHOSTTY_FOCUS_LOST
	if gained {
		event = C.GHOSTTY_FOCUS_GAINED
	}
	var buf [maxEncodedFocus]C.char
	var n C.size_t
	r := C.ghostty_focus_encode(event, &buf[0], C.size_t(len(buf)), &n)
	if r == C.GHOSTTY_OUT_OF_SPACE {
		grown := make([]C.char, n)
		r = C.ghostty_focus_encode(event, &grown[0], C.size_t(len(grown)), &n)
		if r != C.GHOSTTY_SUCCESS {
			return nil, resultError("focus_encode", r)
		}
		return goBytes(grown[:int(n)]), nil
	}
	if r != C.GHOSTTY_SUCCESS {
		return nil, resultError("focus_encode", r)
	}
	return goBytes(buf[:int(n)]), nil
}

// mode reads one of the program's own DEC private modes. The read is a query of
// the terminal rather than a field this adapter keeps: a mode the program set
// is the library's state, and a copy of it here would be a second answer to the
// same question.
func (t *terminal) mode(value int) (bool, error) {
	var on C.bool
	if r := C.nocxModeValue(t.t, C.uint16_t(value), &on); r != C.GHOSTTY_SUCCESS {
		return false, resultError("terminal_mode", r)
	}
	return bool(on), nil
}

func (t *terminal) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return
	}
	t.release()
}

// release frees everything the terminal owns and leaves it closed: every field
// that names an upstream handle is cleared, so t.t is nil afterwards exactly as
// it is after Close and before New returned — the two paths out of this value
// are one path.
//
// It is safe to call with nothing allocated, which is what makes it usable from
// a construction that failed half way through. The caller holds mu, or is
// construction holding the only reference.
func (t *terminal) release() {
	// The registry entry goes first: a callback in flight would look the
	// terminal up and find nothing rather than a handle being freed. None can
	// be, because every call that can produce one holds mu — but the order
	// costs nothing and does not depend on that reasoning staying true.
	unregister(t.id)
	if t.enc != nil {
		C.ghostty_key_encoder_free(t.enc)
		t.enc = nil
	}
	if t.menc != nil {
		C.ghostty_mouse_encoder_free(t.menc)
		t.menc = nil
	}
	C.ghostty_terminal_free(t.t)
	t.t = nil
	t.replies = nil
	t.effects = nil
}
