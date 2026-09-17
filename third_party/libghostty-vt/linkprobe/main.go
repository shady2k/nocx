// Command linkprobe is the smallest caller that proves a libghostty-vt archive
// LINKS for one of nocx's build targets, and that the result is what the target
// promises it is.
//
// It is NOT a binding and not a candidate for production — the product's CGo
// adapter is internal/emulator/ghostty (nocx-ygxjv.2), which owns the real
// lifetime and error model. This program exists because the acceptance of the
// pin (nocx-ygxjv.10) asks for three things a scan of a file listing cannot
// answer:
//
//  1. every archive in the manifest links at all, for its own target, with its
//     own C compiler;
//  2. the Linux ones link STATICALLY — no PT_INTERP, no DT_NEEDED — which is
//     the property a helper on an unknown host needs and which the archive's
//     libc ABI, not a build flag, decides;
//  3. the archive was really linked in — asserted by finding a ghostty symbol
//     in the built file, because a static archive contributes nothing to a
//     link that references nothing in it, and "it linked" would then be a
//     statement about the toolchain rather than about the library.
//
// It creates a terminal, reads its geometry back, resizes it, feeds it bytes
// and frees it: a real call into the library's own code, so the archive cannot
// be satisfied by a stub. Its output is one JSON object per run, which
// scripts/verify-link.sh asserts on.
package main

/*
#cgo CFLAGS: -DGHOSTTY_STATIC
#include <stdlib.h>
#include <ghostty/vt.h>
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"os"
	"unsafe"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "linkprobe:", err)
		os.Exit(1)
	}
}

func run() error {
	report := map[string]any{
		"target": os.Getenv("LINKPROBE_TARGET"),
		"cols":   0,
		"rows":   0,
	}
	var terminal C.GhosttyTerminal
	if result := C.ghostty_terminal_new(nil, &terminal, C.uint16_t(80), C.uint16_t(24)); result != C.GHOSTTY_SUCCESS {
		return fmt.Errorf("ghostty_terminal_new: result %d", int(result))
	}
	defer C.ghostty_terminal_free(terminal)

	var cols, rows C.uint16_t
	if result := C.ghostty_terminal_get(terminal, C.GHOSTTY_TERMINAL_DATA_COLS, unsafe.Pointer(&cols)); result != C.GHOSTTY_SUCCESS {
		return fmt.Errorf("ghostty_terminal_get(COLS): result %d", int(result))
	}
	if result := C.ghostty_terminal_get(terminal, C.GHOSTTY_TERMINAL_DATA_ROWS, unsafe.Pointer(&rows)); result != C.GHOSTTY_SUCCESS {
		return fmt.Errorf("ghostty_terminal_get(ROWS): result %d", int(result))
	}
	if cols != 80 || rows != 24 {
		return fmt.Errorf("new terminal is %dx%d, want 80x24", int(cols), int(rows))
	}

	text := []byte("linkprobe")
	C.ghostty_terminal_vt_write(terminal, (*C.uint8_t)(unsafe.Pointer(&text[0])), C.size_t(len(text)))
	if result := C.ghostty_terminal_resize(terminal, C.uint16_t(120), C.uint16_t(40), C.uint32_t(10), C.uint32_t(20)); result != C.GHOSTTY_SUCCESS {
		return fmt.Errorf("ghostty_terminal_resize: result %d", int(result))
	}
	if result := C.ghostty_terminal_get(terminal, C.GHOSTTY_TERMINAL_DATA_COLS, unsafe.Pointer(&cols)); result != C.GHOSTTY_SUCCESS {
		return fmt.Errorf("ghostty_terminal_get(COLS) after resize: result %d", int(result))
	}
	if cols != 120 {
		return fmt.Errorf("resize left the terminal %d columns wide, want 120", int(cols))
	}

	report["cols"] = int(cols)
	report["rows"] = int(rows)
	report["ok"] = true
	return json.NewEncoder(os.Stdout).Encode(report)
}
