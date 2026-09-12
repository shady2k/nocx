package ghostty

/*
#include "bridge.h"
*/
import "C"

import (
	"unsafe"

	"github.com/shady2k/nocx/internal/emulator"
)

// This file is one half of the effect path: the Go functions bridge.c's
// callbacks call. Its mirror is those callbacks, which read the borrowed value
// each effect carries — the terminal's own title or pwd, the notification's
// text, the clipboard write's payload — and hand the bytes over here.
//
// # These run with mu already held, and that is why they take no lock
//
// A callback is invoked SYNCHRONOUSLY, from inside ghostty_terminal_vt_write,
// on the goroutine that is holding the terminal's lock. Taking mu here would
// deadlock on the first title the program set; touching t.effects without it is
// safe precisely because the holder of mu is the only other writer. This is the
// same arrangement — and the same reasoning — as nocxGoWritePty, and the
// terminal struct's doc carries it at length.
//
// # Why one function per kind rather than one with a kind argument
//
// The kind is a PORT value, and the port's numbering exists so that no other
// library's numbering has to be mirrored anywhere. Passing a number through C
// would put a second copy of that enumeration in bridge.c, and the day the port
// gains a kind the second copy would be silently wrong.

//export nocxGoBell
func nocxGoBell(handle C.uintptr_t) {
	t := lookup(uintptr(handle))
	if t == nil {
		return
	}
	t.effects = append(t.effects, emulator.Effect{Kind: emulator.EffectBell})
}

//export nocxGoTitle
func nocxGoTitle(handle C.uintptr_t, data *C.uint8_t, n C.size_t) {
	t := lookup(uintptr(handle))
	if t == nil {
		return
	}
	t.effects = append(t.effects, emulator.Effect{
		Kind: emulator.EffectTitle,
		Body: copyBorrowed(data, n),
	})
}

//export nocxGoPwd
func nocxGoPwd(handle C.uintptr_t, data *C.uint8_t, n C.size_t) {
	t := lookup(uintptr(handle))
	if t == nil {
		return
	}
	t.effects = append(t.effects, emulator.Effect{
		Kind: emulator.EffectCwdReport,
		Body: copyBorrowed(data, n),
	})
}

//export nocxGoClipboard
func nocxGoClipboard(handle C.uintptr_t, data *C.uint8_t, n C.size_t) {
	t := lookup(uintptr(handle))
	if t == nil {
		return
	}
	t.effects = append(t.effects, emulator.Effect{
		Kind: emulator.EffectClipboard,
		Body: copyBorrowed(data, n),
	})
}

//export nocxGoNotification
func nocxGoNotification(handle C.uintptr_t, data *C.uint8_t, n C.size_t) {
	t := lookup(uintptr(handle))
	if t == nil {
		return
	}
	t.effects = append(t.effects, emulator.Effect{
		Kind: emulator.EffectNotification,
		Body: copyBorrowed(data, n),
	})
}

// copyBorrowed copies bytes out of the library's borrowed memory. It is the
// one place that copy happens for the effect path, and it is a copy because the
// memory dies when the callback that handed it over returns — the title is a
// borrowed string, the notification's text is a borrowed string, the clipboard
// payload is a borrowed string, and none of them outlives the call that
// produced the effect.
//
// A zero length is no bytes rather than an empty allocation: a bell has no
// argument, and an effect whose argument is empty (a cleared title, a clipboard
// write that asks for a clear) is the same nil body as far as a caller can
// read it.
func copyBorrowed(data *C.uint8_t, n C.size_t) []byte {
	if n == 0 || data == nil {
		return nil
	}
	out := make([]byte, int(n))
	copy(out, unsafe.Slice((*byte)(unsafe.Pointer(data)), int(n)))
	return out
}

// Effects returns the effects the program's output produced since the previous
// call, oldest first, and starts a fresh list.
//
// The drain is under mu, like every other field: a callback appending while
// this read is in flight would otherwise be writing to the slice this call is
// handing out. A closed terminal has no effects to report — an effect is
// something a program ASKED for, and there is no program — so it reports none
// rather than failing, which is the same answer the port gives for the other
// read with no error to return.
func (t *terminal) Effects() []emulator.Effect {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return nil
	}
	return t.takeEffects()
}

// takeEffects hands the accumulated effects to the caller and starts a fresh
// list. The effects in it already own their bytes — each was copied on the way
// in, out of memory that died with its callback — so this is a handover and not
// a second copy, exactly as takeReplies is for the program's replies.
func (t *terminal) takeEffects() []emulator.Effect {
	if len(t.effects) == 0 {
		return nil
	}
	out := t.effects
	t.effects = nil
	return out
}
