package assistant

import (
	"context"
	"errors"

	"github.com/shady2k/nocx/internal/content"
)

// cancelCause names a cancellation initiated by the assistant process. It
// wraps context.Canceled so existing cancellation checks remain true while
// callers that present the outcome to a person can distinguish it from a
// lost connection.
type cancelCause struct {
	why      string
	sentence string
	// reason is the ledger's own word for an ending somebody chose — the
	// person's Stop, an answer taken back. A turn ended on purpose carries
	// it, so a call the end caught closes its attempt with the same reason
	// the run closes with instead of reporting a tool failure that never
	// happened (nocx-a62cl.4.1). Empty on the causes that name a process
	// ending rather than a decision.
	reason content.TerminationReason
}

func (c *cancelCause) Error() string { return c.why }

func (c *cancelCause) Unwrap() error { return context.Canceled }

// Sentence is what the transport says about a run this cause ended.
func (c *cancelCause) Sentence() string { return c.sentence }

// TurnEndedCause is the cause a turn's context is cancelled with when the run
// was ended ON PURPOSE, and reason is the ledger's word for why. It is a
// cancellation like any other — errors.Is(err, context.Canceled) stays true —
// so only a caller that records the ending distinguishes it from the window
// closing.
func TurnEndedCause(reason content.TerminationReason) error {
	return &cancelCause{why: "the turn was ended on purpose", reason: reason}
}

// TurnEndedReason reads that word back. False is an ending nobody chose: a
// closed window, a cancelled request, a dead connection.
func TurnEndedReason(err error) (content.TerminationReason, bool) {
	var c *cancelCause
	if errors.As(err, &c) && c.reason != "" {
		return c.reason, true
	}
	return "", false
}

// ProgramEndedSentence returns the person-facing sentence for a named
// process cancellation. Ordinary context cancellation and unrelated errors
// return no sentence, so the transport's lost-connection handling remains
// distinct.
func ProgramEndedSentence(err error) (string, bool) {
	var c *cancelCause
	if errors.As(err, &c) && c.sentence != "" {
		return c.sentence, true
	}
	return "", false
}
