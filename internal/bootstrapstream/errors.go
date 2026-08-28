// Package bootstrapstream owns the two errors a bootstrap line reader can
// return, and owns them for every implementation of that reader.
//
// It exists because the alternative was tried and failed silently. The reader
// is declared as an interface by its CONSUMER (shellintegration's
// BootstrapStream) and implemented by two producers that know nothing about
// frames — internal/ssh over a channel, internal/session over a local pty. Each
// of the three declared its own `ErrBootstrapDeadline`, with this rationale
// written above one of them:
//
//	"It is declared here as well as in internal/shellintegration because
//	 neither package may import the other; the values are compared by the
//	 CALLER's Is, so each side matches its own."
//
// Both halves were wrong. shellintegration DOES import internal/ssh (the
// publisher's SFTP carrier), and `errors.Is` compares the value it is GIVEN
// against the value it is asked about — so a deadline raised by either producer
// never matched the sentinel the consumer tested for. The consequence was not
// a lint-level tidiness problem: `bootstrap-timeout` became unreachable in
// production, every deadline reached the user as "the connection ended", and
// the branch that writes the abort header — the one that gets a far side off
// its blocked read and back to a prompt — never ran. Three packages of tests
// stayed green throughout, because each one's fake returned its own package's
// sentinel.
//
// So the errors live in a leaf package that all three import, and there is one
// value per fact. The INTERFACE stays with its consumer, where Go puts it; only
// the vocabulary that crosses the seam is shared (AD-8: one owner per
// behaviour, and the owner is whoever the behaviour is about).
package bootstrapstream

import "errors"

// ErrDeadline is what a bootstrap ReadLine returns when its deadline passed
// before a line completed. It is a deadline and nothing else: the stream is
// still open, the far side is still there, and the bytes read so far are left
// where the user's terminal will get them.
var ErrDeadline = errors.New("bootstrap: the deadline passed before a line arrived")

// ErrLineTooLong is what a bootstrap ReadLine returns when the far side wrote
// more than the reader's bound with no line ending. It says nothing about the
// connection — which is precisely why it is not the same error as EOF: what
// arrived is output that is not this protocol, and the reader stops rather than
// accumulate an unbounded stream looking for a newline that is not coming.
//
// Wrap it with the counts so the log can say how much arrived without the
// bytes themselves ever being written anywhere (AD-6: the backend does not
// read the stream, and a "bounded prefix" in a log file is reading it).
var ErrLineTooLong = errors.New("bootstrap: a line exceeded the bound")
