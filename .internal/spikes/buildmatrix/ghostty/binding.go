// Package ghosttyvt is the smallest CGo path that proves libghostty-vt's
// static archive LINKS for one of the helper's build targets. It is not a
// binding and not a candidate for production: it creates a terminal, feeds it
// bytes that make it parse a title and answer a cursor query, reads the title
// and the geometry back, and frees. Anything more would measure ghostty rather
// than the linker, which is what this spike is about.
//
// The include path is the vendored source tree's own header directory rather
// than a build output, because the header set is target-independent and this
// spike builds four targets out of one checkout.
package ghostty

/*
#cgo CFLAGS: -I${SRCDIR}/../.vendor/ghostty/include -DGHOSTTY_STATIC
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"
)

// Terminal is one libghostty-vt terminal.
type Terminal struct {
	t  C.GhosttyTerminal
	id uintptr

	mu      sync.Mutex
	replies []byte
}

var (
	regMu  sync.Mutex
	reg    = map[uintptr]*Terminal{}
	nextID uintptr
)

// New creates a terminal of cols x rows and installs the write_pty effect, so
// that a query the program sends comes back through the C callback path.
func New(cols, rows int) (*Terminal, error) {
	t := &Terminal{}
	if r := C.ghostty_terminal_new(nil, &t.t, C.uint16_t(cols), C.uint16_t(rows)); r != C.GHOSTTY_SUCCESS {
		return nil, fmt.Errorf("ghostty_terminal_new: result %d", int(r))
	}
	regMu.Lock()
	nextID++
	t.id = nextID
	reg[t.id] = t
	regMu.Unlock()
	if r := C.bmInstallWritePty(t.t, C.uintptr_t(t.id)); r != C.GHOSTTY_SUCCESS {
		t.Free()
		return nil, fmt.Errorf("install write_pty: result %d", int(r))
	}
	return t, nil
}

// Free releases the terminal and its registration.
func (t *Terminal) Free() {
	if t.t == nil {
		return
	}
	regMu.Lock()
	delete(reg, t.id)
	regMu.Unlock()
	C.ghostty_terminal_free(t.t)
	t.t = nil
}

// Write feeds bytes to the VT parser. Effects it triggers run synchronously
// inside this call.
func (t *Terminal) Write(b []byte) {
	if len(b) == 0 {
		return
	}
	C.ghostty_terminal_vt_write(t.t, (*C.uint8_t)(unsafe.Pointer(&b[0])), C.size_t(len(b)))
}

// Resize sets the terminal geometry.
func (t *Terminal) Resize(cols, rows int) error {
	if r := C.ghostty_terminal_resize(t.t, C.uint16_t(cols), C.uint16_t(rows), 10, 20); r != C.GHOSTTY_SUCCESS {
		return fmt.Errorf("ghostty_terminal_resize: result %d", int(r))
	}
	return nil
}

// Title returns the title set by OSC 0/2, copied out of the library's borrowed
// string before the next mutating call invalidates it.
func (t *Terminal) Title() (string, error) {
	buf := make([]byte, 256)
	var n C.size_t
	if r := C.bmTitle(t.t, (*C.char)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)), &n); r != C.GHOSTTY_SUCCESS {
		return "", fmt.Errorf("title: result %d", int(r))
	}
	if uint64(n) > uint64(len(buf)) {
		return "", fmt.Errorf("title is %d bytes, buffer holds %d", uint64(n), len(buf))
	}
	return string(buf[:int(n)]), nil
}

// Size returns the terminal's geometry in cells.
func (t *Terminal) Size() (int, int, error) {
	var cols, rows C.uint16_t
	if r := C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_COLS, unsafe.Pointer(&cols)); r != C.GHOSTTY_SUCCESS {
		return 0, 0, fmt.Errorf("cols: result %d", int(r))
	}
	if r := C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_ROWS, unsafe.Pointer(&rows)); r != C.GHOSTTY_SUCCESS {
		return 0, 0, fmt.Errorf("rows: result %d", int(r))
	}
	return int(cols), int(rows), nil
}

// Reply returns the bytes the terminal asked to be written back to the PTY
// since the previous call, concatenated in order.
func (t *Terminal) Reply() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.replies
	t.replies = nil
	return out
}

//export bmGoWritePty
func bmGoWritePty(handle C.uintptr_t, data *C.uint8_t, length C.size_t) {
	regMu.Lock()
	t := reg[uintptr(handle)]
	regMu.Unlock()
	if t == nil || length == 0 {
		return
	}
	b := C.GoBytes(unsafe.Pointer(data), C.int(length))
	t.mu.Lock()
	t.replies = append(t.replies, b...)
	t.mu.Unlock()
}
