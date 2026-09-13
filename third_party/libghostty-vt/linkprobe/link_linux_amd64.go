//go:build linux && amd64 && !gnu

// One archive per target, chosen by build constraint rather than by an
// environment variable a build script could get wrong without failing. The
// path is the contract the fetch publishes — build/libghostty-vt/vendor/<target>
// — and it is spelled here exactly as internal/emulator/ghostty will spell it,
// three directories up from third_party/libghostty-vt/linkprobe.
package main

/*
#cgo CFLAGS: -I${SRCDIR}/../../../build/libghostty-vt/vendor/linux-amd64/include
#cgo LDFLAGS: ${SRCDIR}/../../../build/libghostty-vt/vendor/linux-amd64/libghostty-vt.a -lm -lpthread
*/
import "C"
