//go:build linux && amd64 && gnu

// The `gnu` tag builds against the archive target the brief names literally
// (`x86_64-linux`, Zig's glibc ABI) instead of the `-musl` one the static
// property needs. It exists so the two readings of "the Linux target" are both
// measured rather than equated.
package ghostty

/*
#cgo LDFLAGS: ${SRCDIR}/../dist/linux-amd64-gnu/libghostty-vt.a -lm -lpthread
*/
import "C"
