package ghostty

/*
#cgo CFLAGS: -DGHOSTTY_STATIC

// The archive per target, chosen by build constraint — the layout cmd/vtfetch
// publishes (build/libghostty-vt/vendor/<target>), which is gitignored build
// output and is materialised by `make vt-archives` before anything links it.
//
// ONE TARGET HAS TWO ARCHIVES, selected by build constraint because the target
// a helper is FOR is not something a compiler can be asked: the shipped helper
// runs on a host nobody knows and is cross-compiled with the pinned Zig's musl
// triple by `make helpers`, while every ordinary build here — go test,
// golangci-lint, CI — is the host's glibc compiler. Each links the archive the
// manifest names for that target. The musl side is the OPT-IN, `vtmusl`, so the
// many untagged builds are the ones the native toolchain builds for, and the
// helper states the target it is being made for where it is made.
//
// Measured rather than assumed (2026-09-13, this tree): untagged and native
// links vendor/linux-amd64-gnu, and `make helpers` with -tags vtmusl links
// vendor/linux-amd64. The gnu archive does still link under the musl triple
// today, so the constraint is not a workaround for a link that fails — it is
// what keeps the helper on the bytes pinned for the host it will run on, and
// what gives the musl archives in the manifest a consumer at all.
#cgo linux,amd64,vtmusl CFLAGS: -I${SRCDIR}/../../../build/libghostty-vt/vendor/linux-amd64/include
#cgo linux,amd64,vtmusl LDFLAGS: ${SRCDIR}/../../../build/libghostty-vt/vendor/linux-amd64/libghostty-vt.a -lm -lpthread
#cgo linux,arm64,vtmusl CFLAGS: -I${SRCDIR}/../../../build/libghostty-vt/vendor/linux-arm64/include
#cgo linux,arm64,vtmusl LDFLAGS: ${SRCDIR}/../../../build/libghostty-vt/vendor/linux-arm64/libghostty-vt.a -lm -lpthread
#cgo linux,amd64,!vtmusl CFLAGS: -I${SRCDIR}/../../../build/libghostty-vt/vendor/linux-amd64-gnu/include
#cgo linux,amd64,!vtmusl LDFLAGS: ${SRCDIR}/../../../build/libghostty-vt/vendor/linux-amd64-gnu/libghostty-vt.a -lm -lpthread
#cgo linux,arm64,!vtmusl CFLAGS: -I${SRCDIR}/../../../build/libghostty-vt/vendor/linux-arm64-gnu/include
#cgo linux,arm64,!vtmusl LDFLAGS: ${SRCDIR}/../../../build/libghostty-vt/vendor/linux-arm64-gnu/libghostty-vt.a -lm -lpthread
#cgo darwin,amd64 CFLAGS: -I${SRCDIR}/../../../build/libghostty-vt/vendor/darwin-amd64/include
#cgo darwin,amd64 LDFLAGS: ${SRCDIR}/../../../build/libghostty-vt/vendor/darwin-amd64/libghostty-vt.a
#cgo darwin,arm64 CFLAGS: -I${SRCDIR}/../../../build/libghostty-vt/vendor/darwin-arm64/include
#cgo darwin,arm64 LDFLAGS: ${SRCDIR}/../../../build/libghostty-vt/vendor/darwin-arm64/libghostty-vt.a

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

// The grid reference's coordinate spaces, named once because every call
// site reads better as the space it means than as the enum it passes. The
// numbers are the pinned ABI's (include/ghostty/vt/point.h): a tag is an
// enum value, not a guess.
const (
	pointActive  = C.GHOSTTY_POINT_TAG_ACTIVE
	pointHistory = C.GHOSTTY_POINT_TAG_HISTORY
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
	// fenceIdx is the render fence scanner's position in the fence sequence:
	// how many bytes of it the stream has matched (0 is between sequences,
	// which is why the zero value needs no initialisation), and fenceNonce
	// holds the nonce bytes matched so far. The scanner is pure state — it
	// never holds a byte back from the library, and nothing is fed twice; see
	// fence.go for the shape it matches and why the match lives in this
	// adapter at all.
	fenceIdx   int
	fenceNonce [64]byte
	// departed holds the rows that left the screen, captured at the instant
	// of their departure during Ingest and drained whole by DepartedRows. It
	// follows the replies/effects rule: the goroutine holding mu is the only
	// writer, and everything in it was already copied out of the library.
	departed []emulator.Row
	// departedErr is the first read failure a capture hit, handed to the
	// caller with the rows that were read: a report with a hole in it is the
	// caller's to judge, not this adapter's to pass off as whole.
	departedErr error
	// sb is the scrollback baseline of each buffer, indexed by
	// emulator.Screen: how many history rows that buffer had when last
	// measured, and whether it has been measured at all. Departures are the
	// growth of a buffer's own baseline, so an alternate-screen excursion
	// never mistakes the primary's restored history for new rows, and rows
	// that left during the excursion itself are reported at the buffer's
	// next measurement. New seeds the primary's baseline, because a fresh
	// terminal's zero history is a real measurement; the alternate screen's
	// stays invalid until first read.
	sb [2]sbBaseline
}

// sbBaseline is one buffer's scrollback at its last measurement. valid is
// false exactly when the count is not a measurement — before the first read,
// or after a reflow this adapter could not observe (a resize of the buffer
// the resize did not leave active, whose history it cannot read).
type sbBaseline struct {
	rows  int
	valid bool
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
	// A fresh terminal is on the primary screen with no history, and both
	// facts are measured rather than assumed: seeding the baseline here is
	// what lets the FIRST feed report its departures instead of silently
	// becoming one.
	t.sb[0] = sbBaseline{valid: true}
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
	// A resize reflows, and reflow rewrites history's line breaks: the rows
	// it reshapes did not leave the screen, so the departure baseline is
	// taken again rather than let a reflowed count read as departures.
	t.rebaselineLocked()
	return t.takeReplies(), nil
}

func (t *terminal) Ingest(b []byte) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return nil, emulator.ErrClosed
	}
	t.ingestLocked(b)
	return t.takeReplies(), nil
}

// ingestLocked feeds b to the library through the fence scanner. The
// invariant is the feed, not the scan: every byte reaches vt_write exactly
// once, in arrival order, whether or not a fence is in it. The only thing a
// fence changes is WHERE the feed splits — the bytes up to and including the
// fence's BEL go in, the fence is sighted against the screen as it stands at
// that instant, and only then do the bytes after it go in. A chunk with no
// fence costs exactly one vt_write, and a fence that straddles two chunks
// costs nothing at all: the scanner's state is a byte position, not a buffer
// of withheld input.
func (t *terminal) ingestLocked(b []byte) {
	for start := 0; start < len(b); {
		n, fired := t.scanFence(b[start:])
		if n > 0 {
			C.ghostty_terminal_vt_write(t.t, (*C.uint8_t)(unsafe.Pointer(&b[start])), C.size_t(n))
			t.noteDepartedLocked()
		}
		if fired {
			t.sightFence()
		}
		start += n
	}
}

// The departure capture. Rows leave one at a time, off the top, and the
// library raises no event for it: what it does give is each buffer's
// scrollback depth, and the depth grows by exactly the rows that left while
// THAT buffer was active. So the depth is read after every feed and compared
// against the buffer's own baseline — per buffer, because the depth is
// answered for the active screen (zero while the alternate screen holds the
// pane), and a comparison across buffers would read the primary's restored
// history as a departure. This is also why the check rides the feed and
// nothing else: vt_write is the only call that scrolls, and a write that
// ends on the other buffer books its rows against the buffer that lost them,
// reporting them at that buffer's next measurement.

// scrollbackLocked reads the active screen's scrollback depth: how many
// history rows sit above the active area right now.
func (t *terminal) scrollbackLocked() (int, error) {
	var n C.size_t
	if r := C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_SCROLLBACK_ROWS,
		unsafe.Pointer(&n)); r != C.GHOSTTY_SUCCESS {
		return 0, resultError("scrollback_rows", r)
	}
	return int(n), nil
}

// noteDepartedLocked runs after every vt_write. The rows that left in that
// feed are history rows [h-d, h) — the NEWEST history — and they are read
// now, before anything else can move. Capture at departure is load-bearing
// twice over: eviction cannot beat the report, because the rows eviction
// takes are always older than the ones being read; and a later reflow cannot
// rewrite a row that was reported, because the copy was made while the row
// was still the terminal's own.
// What capture at departure cannot survive is the library pruning retention
// inside a feed: the feed's own departures and the pruned pages land in one
// depth reading, and that interval is flagged incomplete rather than
// reported whole.
func (t *terminal) noteDepartedLocked() {
	screen, err := t.screenLocked()
	h := 0
	if err == nil {
		h, err = t.scrollbackLocked()
	}
	base := &t.sb[sbIndex(screen)]
	if err != nil {
		// The buffer's depth is unknown, so no delta can be taken: mark the
		// gap in the report rather than guess, and let the next feed
		// re-baseline from a fresh measurement.
		base.valid = false
		t.failDeparted(fmt.Errorf("ghostty: departed rows unmeasured: %w", err))
		return
	}
	if base.valid {
		if d := h - base.rows; d > 0 {
			t.captureDepartedLocked(h-d, h)
		} else if d < 0 && h > 0 {
			// The depth shrank while rows were still retained: the
			// library's retention budget pruned whole pages inside this
			// feed (a 10,000-byte budget applies by default, pruned at
			// page granularity). The feed's own departures are mixed with
			// pages the count lost, and no scalar says which rows left —
			// so the interval is reported incomplete, never as an empty
			// success. A consumer can carry "output was lost"; it cannot
			// carry a lie.
			t.failDeparted(fmt.Errorf(
				"ghostty: scrollback retention pruned during one feed (depth %d -> %d): the rows that left in that feed cannot be read",
				base.rows, h))
		}
		// d < 0 at zero depth is a reset or an erase-saved-lines: the
		// history was DESTROYED, and destroyed rows did not leave the
		// screen — they ceased. The baseline follows the buffer down
		// without a report, exactly as it does for d == 0, the feed that
		// scrolls nothing.
	}
	base.rows, base.valid = h, true
}

// captureDepartedLocked copies history rows [from, to) — the rows that just
// left — out of the terminal, in order. A read failure stops the capture:
// rows after it are ordered after it, and reading past a hole would report
// the rest as though the hole were not there.
func (t *terminal) captureDepartedLocked(from, to int) {
	for y := from; y < to; y++ {
		row, err := t.rowAt(pointHistory, y)
		if err != nil {
			t.failDeparted(fmt.Errorf("ghostty: departed row %d of %d..%d: %w", y, from, to, err))
			return
		}
		t.departed = append(t.departed, row)
	}
}

// failDeparted records the interval's first hole; the report is handed out
// with it.
func (t *terminal) failDeparted(err error) {
	if t.departedErr == nil {
		t.departedErr = err
	}
}

// rowAt is the one row read: the line's soft-wrap flags and its cells,
// copied, from the coordinate space tag names — the active area for the
// port's Row, the scrollback history for a departed row. Bounds belong to
// the caller, because the two spaces are bounded differently: the active
// area by the geometry, history by what has not been evicted.
func (t *terminal) rowAt(tag C.GhosttyPointTag, y int) (emulator.Row, error) {
	wrapped, continuation, err := t.rowFlags(tag, y)
	if err != nil {
		return emulator.Row{}, err
	}
	cells := make([]emulator.Cell, 0, t.geom.Cols)
	for x := range t.geom.Cols {
		cell, err := t.readCell(tag, x, y)
		if err != nil {
			return emulator.Row{}, err
		}
		cells = append(cells, cell)
	}
	return emulator.Row{Cells: cells, Wrap: wrapped, Continuation: continuation}, nil
}

// rebaselineLocked takes fresh baselines after a mutation that reflows
// rather than scrolls. The active buffer is measured; the hidden one cannot
// be, so its next measurement starts a fresh baseline instead of a delta —
// which is also why the check rides the feed and nothing else.
func (t *terminal) rebaselineLocked() {
	screen, err := t.screenLocked()
	if err != nil {
		t.sb[0], t.sb[1] = sbBaseline{}, sbBaseline{}
		return
	}
	h, err := t.scrollbackLocked()
	if err != nil {
		t.sb[0], t.sb[1] = sbBaseline{}, sbBaseline{}
		return
	}
	t.sb = [2]sbBaseline{}
	t.sb[sbIndex(screen)] = sbBaseline{rows: h, valid: true}
}

// sbIndex maps a screen to its baseline slot.
func sbIndex(s emulator.Screen) int {
	if s == emulator.ScreenAlternate {
		return 1
	}
	return 0
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

// DepartedRows hands the caller the rows that left the screen since its
// previous call and starts a fresh report, with the interval's error
// alongside: the rows that were read go out even when the interval had a
// hole, because the caller is the one that must decide what an unread
// interval is worth. The drain follows takeReplies exactly — one reader,
// one copy, emptied by being read.
func (t *terminal) DepartedRows() ([]emulator.Row, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return nil, emulator.ErrClosed
	}
	out := t.departed
	t.departed = nil
	err := t.departedErr
	t.departedErr = nil
	return out, err
}

// HistoryRows reads a range of the active buffer's scrollback by position:
// what exists of the range, with the retention total alongside. The bounds
// are the method's own, stated on the port; the lock spans the whole read,
// so the page's total and its rows describe one instant of the buffer even
// though the walk is per row.
func (t *terminal) HistoryRows(start, count int) (emulator.HistoryPage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return emulator.HistoryPage{}, emulator.ErrClosed
	}
	if start < 0 || count < 0 {
		return emulator.HistoryPage{}, fmt.Errorf("ghostty: history range start %d count %d: %w",
			start, count, emulator.ErrOutOfRange)
	}
	total, err := t.scrollbackLocked()
	if err != nil {
		return emulator.HistoryPage{}, err
	}
	end := total
	if count < total-start {
		end = start + count
	}
	rows := make([]emulator.Row, 0, max(end-start, 0))
	for y := start; y < end; y++ {
		row, err := t.historyRow(y)
		if err != nil {
			// The rows read so far go out with the failure, exactly as a
			// departure report does: what was read is ordered, and the
			// caller judges what the rest is worth.
			return emulator.HistoryPage{Start: start, Rows: rows, Total: total},
				fmt.Errorf("ghostty: history row %d of %d..%d: %w", y, start, end, err)
		}
		rows = append(rows, row)
	}
	return emulator.HistoryPage{Start: start, Rows: rows, Total: total}, nil
}

func (t *terminal) Screen() (emulator.Screen, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return emulator.ScreenPrimary, emulator.ErrClosed
	}
	return t.screenLocked()
}

// screenLocked is Screen's read without the lock: the departure capture runs
// where mu is already held and needs the same answer, taken the same way.
func (t *terminal) screenLocked() (emulator.Screen, error) {
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

// Cursor reads the caret out of the terminal: its column, its row within the
// active area, and whether the program is showing it.
//
// Three reads and not one because the library reports three fields, and they
// are taken under this adapter's own lock — the same lock every mutating call
// holds — so the three describe one moment. A caller reading the position and
// the visibility from two separate calls could otherwise be handed a caret
// that never existed, which is the same defect the port's Cursor type exists to
// prevent between a position and a visibility.
//
// No bridge shim is needed for any of them: all three are plain scalars, which
// cgo can address directly, exactly as Screen's active buffer is read. The
// shims in bridge.c are for the calls a cgo call cannot express — a callback, a
// struct with a field to initialise, a union to flatten.
func (t *terminal) Cursor() (emulator.Cursor, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return emulator.Cursor{}, emulator.ErrClosed
	}
	return t.cursorLocked()
}

// cursorLocked is Cursor's read without the lock: the caller holds mu, which
// is what makes the three reads one moment. The fence sighting uses it to
// read the caret from inside Ingest, where mu is already held.
func (t *terminal) cursorLocked() (emulator.Cursor, error) {
	var x, y C.uint16_t
	if r := C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_CURSOR_X,
		unsafe.Pointer(&x)); r != C.GHOSTTY_SUCCESS {
		return emulator.Cursor{}, resultError("cursor_x", r)
	}
	if r := C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_CURSOR_Y,
		unsafe.Pointer(&y)); r != C.GHOSTTY_SUCCESS {
		return emulator.Cursor{}, resultError("cursor_y", r)
	}
	var visible C.bool
	if r := C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_CURSOR_VISIBLE,
		unsafe.Pointer(&visible)); r != C.GHOSTTY_SUCCESS {
		return emulator.Cursor{}, resultError("cursor_visible", r)
	}
	return emulator.Cursor{X: int(x), Y: int(y), Visible: bool(visible)}, nil
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
	return t.rowAt(pointActive, y)
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
	return t.readCell(pointActive, x, y)
}

// rowFlags reads a line's soft-wrap state. The row is read through a
// reference at column 0 because the wrap flags live on the line and not on a
// cell, and any cell of the line yields the same line. Tag names the
// coordinate space — the active area for the port's Row, the scrollback
// history for a departed row — and the flags are the line's own either way:
// carried by the terminal across the wrap, never re-measured from widths.
func (t *terminal) rowFlags(tag C.GhosttyPointTag, y int) (wrapped, continuation bool, err error) {
	var ref C.GhosttyGridRef
	if r := C.nocxGridRefAt(t.t, tag, 0, C.uint32_t(y), &ref); r != C.GHOSTTY_SUCCESS {
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
// Tag names the coordinate space the position is read in: the active area
// for the port's own reads, the scrollback history for a departed row.
func (t *terminal) readCell(tag C.GhosttyPointTag, x, y int) (emulator.Cell, error) {
	var ref C.GhosttyGridRef
	if r := C.nocxGridRefAt(t.t, tag, C.uint32_t(x), C.uint32_t(y), &ref); r != C.GHOSTTY_SUCCESS {
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

// historyRow reads one row of the active buffer's scrollback through the
// bridge's row traversal. The bridge resolves the row ONCE — the page-list
// walk a history lookup pays — and reads every cell of the row off the
// resolved reference, so a row of N columns costs one grid-reference
// resolution and not N. Bounds belong to the caller, as rowAt's do: history
// is bounded by what has not been evicted, and only the caller knows the
// range it asked for.
func (t *terminal) historyRow(y int) (emulator.Row, error) {
	cols := t.geom.Cols
	cells := make([]C.nocxRowCellFacts, cols)
	var wrap, cont C.bool
	// Clusters ride one row-wide UTF-8 buffer. The first budget is what a
	// full row of the port's largest clusters costs; a row of clusters past
	// even that re-runs the whole traversal on a fourfold budget rather than
	// truncating — the bridge holds no state between attempts, and each
	// attempt pays its own one resolution.
	graphemes := make([]C.uint8_t, cols*maxGraphemeCodepoints*4)
	for {
		r := C.nocxHistoryRow(t.t, C.uint32_t(y), C.uint16_t(cols), &cells[0],
			&graphemes[0], C.size_t(len(graphemes)), &wrap, &cont)
		if r == C.GHOSTTY_OUT_OF_SPACE {
			if len(graphemes) >= cols*maxGraphemeCodepoints*64 {
				return emulator.Row{}, fmt.Errorf("ghostty: history row %d utf8 budget %d: %w",
					y, len(graphemes), emulator.ErrExhausted)
			}
			graphemes = make([]C.uint8_t, len(graphemes)*4)
			continue
		}
		if r != C.GHOSTTY_SUCCESS {
			return emulator.Row{}, resultError("history_row", r)
		}
		break
	}
	out := make([]emulator.Cell, cols)
	for x := range cols {
		f := &cells[x]
		cell := emulator.Cell{
			Width:   cellWidth(f.wide),
			HasText: bool(f.has_text),
		}
		// A cell the terminal carries unstyled reads as the default style —
		// the value a full style read produces for it — so the style is
		// materialised only for the cells that have one.
		if f.styled {
			style, err := styleOf(f.style)
			if err != nil {
				return emulator.Row{}, err
			}
			cell.Style = style
		}
		if cell.HasText {
			off, n := int(f.grapheme_off), int(f.grapheme_len)
			cell.Grapheme = string(C.GoBytes(unsafe.Pointer(&graphemes[off]), C.int(n)))
		}
		out[x] = cell
	}
	return emulator.Row{Cells: out, Wrap: bool(wrap), Continuation: bool(cont)}, nil
}

// gridResolutions reports the bridge's running total of grid-reference
// resolutions — every ghostty_terminal_grid_ref this shim has issued. Tests
// read it: the one-per-row cost of a history range is a property no reading
// of the code can establish, so the count is the evidence.
func gridResolutions() uint64 {
	return uint64(C.nocxGridResolveCount())
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
