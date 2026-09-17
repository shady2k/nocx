//go:build linux && arm64 && gnu

// See link_linux_amd64_gnu.go.
package ghostty

/*
#cgo LDFLAGS: ${SRCDIR}/../dist/linux-arm64-gnu/libghostty-vt.a -lm -lpthread
*/
import "C"
