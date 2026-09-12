//go:build darwin && arm64 && !stublibs

package ghostty

/*
#cgo LDFLAGS: ${SRCDIR}/../dist/darwin-arm64/libghostty-vt.a
*/
import "C"
