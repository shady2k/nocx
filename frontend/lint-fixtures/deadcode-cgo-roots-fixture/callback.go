// Package fixture stands in for internal/emulator/ghostty: a cgo package whose
// callbacks are called from C, and whose helpers are called by those callbacks
// and by nothing else.
//
// It is small on purpose. What the ratchet's test has to watch is three
// classifications in one package — the `//export`ed callback, what only it
// reaches, and an ordinary function nothing calls — and a fourth outside it
// (plain.go's directive in a file that is not a cgo file).
package fixture

/*
#include <stdint.h>
*/
import "C"

//export fixtureGoBell
func fixtureGoBell(handle C.uintptr_t) {
	fixtureRemember(uintptr(handle), nil)
}

//export fixtureGoTitle
func fixtureGoTitle(handle C.uintptr_t, data *C.uint8_t, n C.size_t) {
	fixtureRemember(uintptr(handle), fixtureCopy(data, n))
}

// fixtureCopy is reached by the two callbacks above and by nothing else. It is
// the half of the finding that a rule merely suppressing the callbacks' own
// reports would leave in the gate, exactly as copyBorrowed is in ghostty.
func fixtureCopy(data *C.uint8_t, n C.size_t) []byte {
	if data == nil || n == 0 {
		return nil
	}
	return make([]byte, int(n))
}

func fixtureRemember(id uintptr, body []byte) {
	_, _ = id, body
}

// fixtureDeadHelper is called by nothing at all. It is what proves the cgo rule
// is a ROOT and not a suppression of the package: this stays a violation.
func fixtureDeadHelper() int {
	return 1
}
