//go:build darwin

package fixture

/*
#include <stdint.h>
*/
import "C"

// This file carries the two things a rendered root has to inherit from the
// file it stands for, and the test asserts both: the `//go:build` line above,
// and the `_darwin` suffix in its own name — go infers a build constraint from
// the file NAME as well as from that line, so a root named
// `zz_deadcode_cgo_roots.go` would be compiled on linux and would make a
// darwin-only callback live in an analysis that never sees its C side.
//
//export fixtureGoDarwin
func fixtureGoDarwin(handle C.uintptr_t) {
	fixtureRemember(uintptr(handle), nil)
}
