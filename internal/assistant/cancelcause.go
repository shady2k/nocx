package assistant

import (
	"context"
	"errors"
)

// cancelCause names a cancellation initiated by the assistant process. It
// wraps context.Canceled so existing cancellation checks remain true while
// callers that present the outcome to a person can distinguish it from a
// lost connection.
type cancelCause struct {
	why      string
	sentence string
}

func (c *cancelCause) Error() string { return c.why }

func (c *cancelCause) Unwrap() error { return context.Canceled }

// Sentence is what the transport says about a run this cause ended.
func (c *cancelCause) Sentence() string { return c.sentence }

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
