package content

// What survives of the interim table's own tests (nocx-rtg0.19).
//
// command_history is gone and with it every test that exercised its rows: the
// round-trip, the incrementing rowid, and the five rewrite cases are all
// answered for the ledger in ledger_redaction_test.go, which is the store
// that holds masked command text now. What could not move was the rebuild's
// own promise — how many rows a schema change discarded, in the log and
// through Config.OnDiscard to the product.
//
// THAT PROMISE NO LONGER EXISTS (nocx-lmb6v.1). A schema difference is
// migrated or refused, so nothing discards rows at open and there is no count
// to announce; the three tests that asserted it are gone with the mechanism,
// and the callback they exercised is gone from Config. What is left here is
// the logger the ledger's own tests capture with.

import (
	"context"

	"github.com/shady2k/nocx/internal/log"
)

type captureLogger struct {
	warn func(msg string, args ...any)
}

func (c *captureLogger) Debug(string, ...any)                   {}
func (c *captureLogger) Info(string, ...any)                    {}
func (c *captureLogger) Warn(msg string, args ...any)           { c.warn(msg, args...) }
func (c *captureLogger) Error(string, ...any)                   {}
func (c *captureLogger) With(...any) log.Logger                 { return c }
func (c *captureLogger) WithContext(context.Context) log.Logger { return c }
