// Package ghostty is a minimal, throwaway CGo binding to libghostty-vt, the
// C ABI of the Ghostty terminal emulator (github.com/ghostty-org/ghostty,
// include/ghostty/vt.h). It exists to run the emulator-qualification probes
// against the same terminal API a production binding would use, and it is
// not meant to be kept.
package ghostty

/*
#cgo CFLAGS: -I${SRCDIR}/../.vendor/ghostty/zig-out/include -DGHOSTTY_STATIC
#cgo LDFLAGS: ${SRCDIR}/../.vendor/ghostty/zig-out/lib/libghostty-vt.a -lm -lpthread
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unsafe"
)

// ClipboardWrite records one clipboard-write effect the terminal requested.
type ClipboardWrite struct {
	Location int
	Length   int
}

// Terminal wraps a GhosttyTerminal handle and records the effects it asks its
// embedder to perform.
type Terminal struct {
	t  C.GhosttyTerminal
	id uintptr

	mu      sync.Mutex
	replies [][]byte
	bells   int
	titles  []string
	pwds    []string
	clips   []ClipboardWrite
	freed   bool
}

var (
	regMu  sync.Mutex
	reg    = map[uintptr]*Terminal{}
	nextID = uintptr(1)
)

// New creates a terminal of cols x rows and installs the effect callbacks.
func New(cols, rows int) *Terminal {
	var t C.GhosttyTerminal
	if r := C.ghostty_terminal_new(nil, &t, C.uint16_t(cols), C.uint16_t(rows)); r != C.GHOSTTY_SUCCESS {
		panic(fmt.Sprintf("ghostty_terminal_new: result %d", int(r)))
	}
	tm := &Terminal{t: t}
	regMu.Lock()
	tm.id = nextID
	nextID++
	reg[tm.id] = tm
	regMu.Unlock()
	if r := C.nocxInstallEffects(t, C.uintptr_t(tm.id)); r != C.GHOSTTY_SUCCESS {
		panic(fmt.Sprintf("nocxInstallEffects: result %d", int(r)))
	}
	return tm
}

// Free releases the terminal and unregisters its effect callbacks.
func (t *Terminal) Free() {
	if t.freed {
		return
	}
	t.freed = true
	regMu.Lock()
	delete(reg, t.id)
	regMu.Unlock()
	C.ghostty_terminal_free(t.t)
}

// Write feeds bytes to the terminal's VT parser.
func (t *Terminal) Write(b []byte) {
	if len(b) == 0 {
		return
	}
	C.ghostty_terminal_vt_write(t.t, (*C.uint8_t)(unsafe.Pointer(&b[0])), C.size_t(len(b)))
	runtime.KeepAlive(b)
}

// Reply returns the bytes the terminal asked to be written back to the PTY
// since the previous call, concatenated in order.
func (t *Terminal) Reply() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []byte
	for _, r := range t.replies {
		out = append(out, r...)
	}
	t.replies = nil
	return out
}

// Resize changes the terminal geometry. Cell pixel dimensions are set to a
// plausible non-zero value; the probes address columns and rows only.
func (t *Terminal) Resize(cols, rows int) {
	C.ghostty_terminal_resize(t.t, C.uint16_t(cols), C.uint16_t(rows), 10, 20)
}

// RenderState is an incremental render state over a terminal: the API a diff
// encoder would drive, at the granularity the library exposes.
type RenderState struct {
	s C.GhosttyRenderState
}

// NewRenderState creates a render state.
func NewRenderState() *RenderState {
	var s C.GhosttyRenderState
	if r := C.nocxRenderStateNew(&s); r != C.GHOSTTY_SUCCESS {
		panic(fmt.Sprintf("nocxRenderStateNew: result %d", int(r)))
	}
	return &RenderState{s: s}
}

// Update refreshes the state from the terminal and marks what changed.
func (r *RenderState) Update(t *Terminal) {
	C.nocxRenderStateUpdate(r.s, t.t)
}

// Dirty reports the global dirty state: 0 none, 1 partial, 2 full.
func (r *RenderState) Dirty() int {
	var d C.int
	if res := C.nocxRenderStateDirty(r.s, &d); res != C.GHOSTTY_SUCCESS {
		return -1
	}
	return int(d)
}

// DirtyRows returns the viewport y of every row that needs a redraw.
func (r *RenderState) DirtyRows() []int {
	buf := make([]C.uint16_t, 1024)
	var n C.size_t
	if res := C.nocxRenderStateDirtyRows(r.s, &buf[0], C.size_t(len(buf)), &n); res != C.GHOSTTY_SUCCESS {
		return nil
	}
	out := make([]int, 0, int(n))
	for i := range int(n) {
		out = append(out, int(buf[i]))
	}
	return out
}

// Clean marks the frame drawn, so the next Update reports only new damage.
func (r *RenderState) Clean() {
	C.nocxRenderStateClean(r.s)
}

// Free releases the render state.
func (r *RenderState) Free() {
	if r.s == nil {
		return
	}
	C.nocxRenderStateFree(r.s)
	r.s = nil
}

// BuildInfoKittyGraphics reports whether the linked library was compiled with
// the Kitty graphics protocol.
func BuildInfoKittyGraphics() bool {
	var v C.int
	if r := C.nocxBuildInfoBool(C.GHOSTTY_BUILD_INFO_KITTY_GRAPHICS, &v); r != C.GHOSTTY_SUCCESS {
		return false
	}
	return v != 0
}

// BuildInfoTmuxControlMode reports whether tmux control mode is compiled in.
func BuildInfoTmuxControlMode() bool {
	var v C.int
	if r := C.nocxBuildInfoBool(C.GHOSTTY_BUILD_INFO_TMUX_CONTROL_MODE, &v); r != C.GHOSTTY_SUCCESS {
		return false
	}
	return v != 0
}

// SetScrollbackLines sets the maximum number of scrollback lines.
func (t *Terminal) SetScrollbackLines(n int) {
	v := C.size_t(n)
	C.ghostty_terminal_set(t.t, C.GHOSTTY_TERMINAL_OPT_SCROLLBACK_MAX_LINES, unsafe.Pointer(&v))
}

// Cursor returns the cursor position in viewport cells.
func (t *Terminal) Cursor() (x, y int) {
	var cx, cy C.uint16_t
	C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_CURSOR_X, unsafe.Pointer(&cx))
	C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_CURSOR_Y, unsafe.Pointer(&cy))
	return int(cx), int(cy)
}

// Cols and Rows report the terminal's current geometry.
func (t *Terminal) Cols() int {
	var v C.uint16_t
	C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_COLS, unsafe.Pointer(&v))
	return int(v)
}

func (t *Terminal) Rows() int {
	var v C.uint16_t
	C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_ROWS, unsafe.Pointer(&v))
	return int(v)
}

// ScrollbackRows reports how many rows are above the viewport.
func (t *Terminal) ScrollbackRows() int {
	var v C.size_t
	C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_SCROLLBACK_ROWS, unsafe.Pointer(&v))
	return int(v)
}

// CellWidth classifies a cell's column footprint.
type CellWidth int

// Cell widths that affect column accounting.
const (
	WidthNarrow     CellWidth = 0
	WidthWide       CellWidth = 1
	WidthSpacerTail CellWidth = 2
	WidthSpacerHead CellWidth = 3
)

// Cell returns the grapheme cluster stored in a cell, its column footprint and
// whether the cell holds text at all.
func (t *Terminal) Cell(x, y int) (content string, width CellWidth, hasText bool) {
	var ref C.GhosttyGridRef
	if r := C.nocxGridRefAt(t.t, C.uint16_t(x), C.uint16_t(y), &ref); r != C.GHOSTTY_SUCCESS {
		return "", WidthNarrow, false
	}
	var cell C.GhosttyCell
	if r := C.ghostty_grid_ref_cell(&ref, &cell); r != C.GHOSTTY_SUCCESS {
		return "", WidthNarrow, false
	}
	var cp C.uint32_t
	var wide, ht C.int
	if r := C.nocxCellFacts(cell, &cp, &wide, &ht); r != C.GHOSTTY_SUCCESS {
		return "", WidthNarrow, false
	}
	if ht == 0 {
		return "", CellWidth(wide), false
	}
	buf := make([]C.uint32_t, 16)
	n := C.size_t(len(buf))
	r := C.nocxCellGraphemes(&ref, &buf[0], C.size_t(len(buf)), &n)
	if r == C.GHOSTTY_OUT_OF_SPACE {
		buf = make([]C.uint32_t, n+1)
		n = C.size_t(len(buf))
		r = C.nocxCellGraphemes(&ref, &buf[0], C.size_t(len(buf)), &n)
	}
	if r != C.GHOSTTY_SUCCESS {
		return "", CellWidth(wide), true
	}
	var sb strings.Builder
	for i := range int(n) {
		sb.WriteRune(rune(buf[i]))
	}
	return sb.String(), CellWidth(wide), true
}

// RowWrap reports whether a row soft-wraps, and whether it is the continuation
// of a soft-wrapped row above it.
func (t *Terminal) RowWrap(y int) (wrap, continuation bool) {
	var ref C.GhosttyGridRef
	if r := C.nocxGridRefAt(t.t, 0, C.uint16_t(y), &ref); r != C.GHOSTTY_SUCCESS {
		return false, false
	}
	var row C.GhosttyRow
	if r := C.ghostty_grid_ref_row(&ref, &row); r != C.GHOSTTY_SUCCESS {
		return false, false
	}
	var w, c C.int
	if r := C.nocxRowFacts(row, &w, &c); r != C.GHOSTTY_SUCCESS {
		return false, false
	}
	return w != 0, c != 0
}

// Title returns the title the terminal holds.
func (t *Terminal) Title() string {
	var s C.GhosttyString
	if r := C.nocxTerminalTitle(t.t, &s); r != C.GHOSTTY_SUCCESS {
		return ""
	}
	if s.ptr == nil || s.len == 0 {
		return ""
	}
	return C.GoStringN((*C.char)(unsafe.Pointer(s.ptr)), C.int(s.len))
}

// Pwd returns the working directory the terminal holds from OSC 7.
func (t *Terminal) Pwd() string {
	var s C.GhosttyString
	if r := C.nocxTerminalPwd(t.t, &s); r != C.GHOSTTY_SUCCESS {
		return ""
	}
	if s.ptr == nil || s.len == 0 {
		return ""
	}
	return C.GoStringN((*C.char)(unsafe.Pointer(s.ptr)), C.int(s.len))
}

// Mode reports a terminal mode's value: set is the value, ok is false when the
// mode is not one this terminal knows.
func (t *Terminal) Mode(value int, ansi bool) (set bool, ok bool) {
	var s C.int
	a := 0
	if ansi {
		a = 1
	}
	r := C.nocxTerminalMode(t.t, C.uint16_t(value), C.int(a), &s)
	return s != 0, r == C.GHOSTTY_SUCCESS
}

// Events returns the non-visual effects recorded since the previous call.
func (t *Terminal) Events() (bells int, titles, pwds []string, clips []ClipboardWrite) {
	t.mu.Lock()
	defer t.mu.Unlock()
	bells = t.bells
	titles = append([]string(nil), t.titles...)
	pwds = append([]string(nil), t.pwds...)
	clips = append([]ClipboardWrite(nil), t.clips...)
	t.bells, t.titles, t.pwds, t.clips = 0, nil, nil, nil
	return bells, titles, pwds, clips
}

// Key is a key identity, mirroring GhosttyKey.
type Key int32

// Mods is a key modifier bit set, mirroring the GHOSTTY_MODS_* constants.
type Mods uint16

// Key identities and modifiers named for the probes.
const (
	KeyLeft Key = Key(C.GHOSTTY_KEY_ARROW_LEFT)
	KeyUp   Key = Key(C.GHOSTTY_KEY_ARROW_UP)
	KeyF1   Key = Key(C.GHOSTTY_KEY_F1)
	KeyF5   Key = Key(C.GHOSTTY_KEY_F5)
	KeyF12  Key = Key(C.GHOSTTY_KEY_F12)
	KeyF13  Key = Key(C.GHOSTTY_KEY_F13)

	ModShift Mods = Mods(C.GHOSTTY_MODS_SHIFT)
	ModCtrl  Mods = Mods(C.GHOSTTY_MODS_CTRL)
	ModAlt   Mods = Mods(C.GHOSTTY_MODS_ALT)
)

// EncodeKey encodes one key press. kittyFlags selects the Kitty keyboard
// protocol feature set: 0 is the legacy encoding a plain xterm sends.
// host, when non-nil, supplies the modes (application cursor keys, keypad)
// that the terminal has set.
func EncodeKey(key Key, mods Mods, kittyFlags int, host *Terminal) []byte {
	var enc C.GhosttyKeyEncoder
	if r := C.ghostty_key_encoder_new(nil, &enc); r != C.GHOSTTY_SUCCESS {
		panic(fmt.Sprintf("ghostty_key_encoder_new: result %d", int(r)))
	}
	defer C.ghostty_key_encoder_free(enc)

	if kittyFlags != 0 {
		f := C.uint8_t(kittyFlags)
		C.ghostty_key_encoder_setopt(enc, C.GHOSTTY_KEY_ENCODER_OPT_KITTY_FLAGS, unsafe.Pointer(&f))
	}
	if host != nil {
		C.ghostty_key_encoder_setopt_from_terminal(enc, host.t)
	}

	var ev C.GhosttyKeyEvent
	if r := C.ghostty_key_event_new(nil, &ev); r != C.GHOSTTY_SUCCESS {
		panic(fmt.Sprintf("ghostty_key_event_new: result %d", int(r)))
	}
	defer C.ghostty_key_event_free(ev)
	C.ghostty_key_event_set_action(ev, C.GHOSTTY_KEY_ACTION_PRESS)
	C.ghostty_key_event_set_key(ev, C.GhosttyKey(key))
	C.ghostty_key_event_set_mods(ev, C.GhosttyMods(mods))

	buf := make([]C.char, 128)
	var n C.size_t
	r := C.ghostty_key_encoder_encode(enc, ev, &buf[0], C.size_t(len(buf)), &n)
	if r != C.GHOSTTY_SUCCESS {
		return nil
	}
	return C.GoBytes(unsafe.Pointer(&buf[0]), C.int(n))
}

// KittyAll is the full Kitty keyboard protocol feature set.
const KittyAll = int(C.GHOSTTY_KITTY_KEY_ALL)
