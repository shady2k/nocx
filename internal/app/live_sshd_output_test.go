//go:build nocx_local_ssh

package app

import "sync"

// sessionOutput is a session's terminal output with the END of the far side's
// stream recorded beside it: the fact endTheSession waits on, and the one
// `ch.Done()` is not (nocx-evj4b.1.5).
//
// WHY A WRAPPER AND NOT FIELDS ON outputBuffer. `outputBuffer` is shared with
// the untagged tests (ssh_child_assembly_test.go builds its own), and the
// macOS lint runs this package WITHOUT the nocx_local_ssh tag — where nothing
// would read the extra field, and an unexported field nothing in that build
// touches is reported as unused (golangci-lint: `field endOnce is unused`,
// which is how this file came to exist). So the shared buffer stays exactly
// what it was, and the end-recording lives beside it in a file that only
// exists in the build that uses it.
type sessionOutput struct {
	outputBuffer
	// ended is closed when the session's output stream ends — the copier's
	// return, i.e. the far side's terminal closing, a fact both operating
	// systems produce for a shell that has exited.
	ended   chan struct{}
	endOnce sync.Once
}

func newSessionOutput() *sessionOutput {
	return &sessionOutput{ended: make(chan struct{})}
}

// end records that the session's output stream is over. Idempotent, so the
// fixture may reach it from more than one place without deciding which.
func (o *sessionOutput) end() {
	o.endOnce.Do(func() { close(o.ended) })
}

// Ended reports whether the session's output stream has ended.
func (o *sessionOutput) Ended() bool {
	select {
	case <-o.ended:
		return true
	default:
		return false
	}
}
