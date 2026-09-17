//go:build linux && arm64 && !gnu

package ghostty

/*
#cgo LDFLAGS: ${SRCDIR}/../dist/linux-arm64/libghostty-vt.a -lm -lpthread
*/
import "C"
