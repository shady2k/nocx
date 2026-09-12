//go:build darwin && amd64 && !stublibs

package ghostty

/*
#cgo LDFLAGS: ${SRCDIR}/../dist/darwin-amd64/libghostty-vt.a
*/
import "C"
