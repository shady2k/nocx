package ghostty

/*
#include <stddef.h>
#include <stdint.h>
*/
import "C"

import "unsafe"

// The exported callbacks below are what libghostty-vt calls synchronously
// during a write. They look the Terminal up by the userdata handle the binding
// passed in, so the C side needs no per-callback closure support.

func lookup(h C.uintptr_t) *Terminal {
	regMu.Lock()
	defer regMu.Unlock()
	return reg[uintptr(h)]
}

//export nocxGoWritePty
func nocxGoWritePty(h C.uintptr_t, data *C.uint8_t, n C.size_t) {
	t := lookup(h)
	if t == nil || n == 0 {
		return
	}
	b := C.GoBytes(unsafe.Pointer(data), C.int(n))
	t.mu.Lock()
	t.replies = append(t.replies, b)
	t.mu.Unlock()
}

//export nocxGoBell
func nocxGoBell(h C.uintptr_t) {
	t := lookup(h)
	if t == nil {
		return
	}
	t.mu.Lock()
	t.bells++
	t.mu.Unlock()
}

//export nocxGoTitleChanged
func nocxGoTitleChanged(h C.uintptr_t) {
	t := lookup(h)
	if t == nil {
		return
	}
	title := t.Title()
	t.mu.Lock()
	t.titles = append(t.titles, title)
	t.mu.Unlock()
}

//export nocxGoPwdChanged
func nocxGoPwdChanged(h C.uintptr_t) {
	t := lookup(h)
	if t == nil {
		return
	}
	pwd := t.Pwd()
	t.mu.Lock()
	t.pwds = append(t.pwds, pwd)
	t.mu.Unlock()
}

//export nocxGoClipboardWrite
func nocxGoClipboardWrite(h C.uintptr_t, location C.int, n C.size_t) {
	t := lookup(h)
	if t == nil {
		return
	}
	t.mu.Lock()
	t.clips = append(t.clips, ClipboardWrite{Location: int(location), Length: int(n)})
	t.mu.Unlock()
}
