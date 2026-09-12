//go:build cgo

package main

import "nocx.internal/spikes/buildmatrix/ghostty"

// run constructs a terminal, feeds it the same two byte strings the probe does
// and frees it: enough that the linker cannot discard the archive.
func run() {
	term, err := ghostty.New(80, 24)
	if err != nil {
		panic(err)
	}
	term.Write([]byte("\x1b]0;hello\x07"))
	term.Write([]byte("\x1b[6n"))
	_, _ = term.Title()
	_ = term.Reply()
	term.Free()
}
