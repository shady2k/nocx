//go:build darwin && arm64 && stublibs

// See link_darwin_amd64_stub.go. This target needs one stub more than amd64,
// because runtime/cgo asks for -framework CoreFoundation on darwin/arm64
// (runtime/cgo/cgo.go:15) and zig has no framework search path of its own.
package ghostty

/*
#cgo LDFLAGS: -L${SRCDIR}/../stubs -F${SRCDIR}/../stubs/frameworks ${SRCDIR}/../dist/darwin-arm64/libghostty-vt.a
*/
import "C"
