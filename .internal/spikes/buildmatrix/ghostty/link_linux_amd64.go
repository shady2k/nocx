//go:build linux && amd64 && !gnu

// One archive per helper target, selected by build constraint. The Go
// toolchain cross-compiles with one CGo toolchain per target, and the archive
// that toolchain links against cannot be chosen at run time -- so the choice
// lives here, once, rather than in an environment variable a build script
// could get wrong without failing.
package ghostty

/*
#cgo LDFLAGS: ${SRCDIR}/../dist/linux-amd64/libghostty-vt.a -lm -lpthread
*/
import "C"
