// Package ghosttytest carries the bridge counters the ghostty package's tests
// measure against. The accessor cannot live in terminal.go: deadcode — which
// the ratchet runs without -test on purpose — reports a function reachable
// only from tests as NEW unreachable. It cannot live in a _test.go file of
// the ghostty package either: the go command refuses cgo in an in-package
// test file as soon as another package imports the package (measured
// 2026-09-23: go build ./... refused, error reported at the importing
// package). This package contributes no definitions;
// nocxGridResolveCount resolves from the one bridge.c object in the pinned
// archive the ghostty package already links.
package ghosttytest

/*
#cgo linux,amd64,vtmusl LDFLAGS: ${SRCDIR}/../../../../build/libghostty-vt/vendor/linux-amd64/libghostty-vt.a -lm -lpthread
#cgo linux,arm64,vtmusl LDFLAGS: ${SRCDIR}/../../../../build/libghostty-vt/vendor/linux-arm64/libghostty-vt.a -lm -lpthread
#cgo linux,amd64,!vtmusl LDFLAGS: ${SRCDIR}/../../../../build/libghostty-vt/vendor/linux-amd64-gnu/libghostty-vt.a -lm -lpthread
#cgo linux,arm64,!vtmusl LDFLAGS: ${SRCDIR}/../../../../build/libghostty-vt/vendor/linux-arm64-gnu/libghostty-vt.a -lm -lpthread
#cgo darwin,amd64 LDFLAGS: ${SRCDIR}/../../../../build/libghostty-vt/vendor/darwin-amd64/libghostty-vt.a
#cgo darwin,arm64 LDFLAGS: ${SRCDIR}/../../../../build/libghostty-vt/vendor/darwin-arm64/libghostty-vt.a

#include <stdint.h>

extern uint64_t nocxGridResolveCount(void);
*/
import "C"

// GridResolutions reports the bridge's running total of grid-reference
// resolutions — every ghostty_terminal_grid_ref the shim has issued. Tests
// read it: the one-per-row cost of a history range is a property no reading
// of the code can establish, so the count is the evidence.
func GridResolutions() uint64 {
	return uint64(C.nocxGridResolveCount())
}
