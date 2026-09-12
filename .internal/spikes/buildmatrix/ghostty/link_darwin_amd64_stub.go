//go:build darwin && amd64 && stublibs

// The `stublibs` tag is the cross-linking experiment and nothing else: it adds
// a hand-written libresolv.tbd so a Linux host can satisfy the flag Go's own
// internal/syscall/unix/net_darwin.go puts on every darwin link line. The
// default build deliberately does NOT carry it, so that `go build` without the
// tag still reports the real missing library.
package ghostty

/*
#cgo LDFLAGS: -L${SRCDIR}/../stubs ${SRCDIR}/../dist/darwin-amd64/libghostty-vt.a
*/
import "C"
