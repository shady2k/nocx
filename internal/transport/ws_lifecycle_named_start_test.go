package transport

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// A command submitted from nocx's own editor is an app attempt; the shipped
// shells (bash, zsh) name every start with their OWN attempt id, which the
// kernel records as an alias and attaches to the app attempt. The lane's
// fact is the same before and after that attach — running, the app attempt
// open — so the publisher's change dedupe would swallow it, and the ledger
// would never learn the command started: no execution, no block. The
// started-attempt projection is what carries it, for a named start exactly
// as for an unnamed one (nocx-zg3k3.5.11, found writing the repeat test).
func TestANamedStartAttachingToAnAppAttemptStartsItsExecution(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 2, lifecyclePromptEvt()))
	got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt",
		lifecycleSubmitParams(string(h.Domain), "make build"), 41))
	waittest.WaitFor(t, "the submitted entry to be stored", func() bool {
		row, err := db.Ledger().Entry(context.Background(), got.ID)
		return err == nil && row != nil
	})

	shellID := lifecycle.AttemptID("s-dom-shell-0")
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleStartEvt(&shellID, "make build")))

	row := mustEntry(t, db, got.ID)
	if len(row.Executions) != 1 {
		t.Fatalf("the app attempt has %d executions after the shell's named start, want 1: the start never reached the ledger", len(row.Executions))
	}
	if row.Phase == content.PhaseOpen {
		t.Fatalf("the app attempt is still %q after its start", row.Phase)
	}
}
