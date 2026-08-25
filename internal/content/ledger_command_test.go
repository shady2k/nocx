package content_test

// RecordCompleted — the ledger's write path for a command that already ended
// (nocx-rtg0.19), which is what history.record lands through now that
// command_history is gone.
//
// These assert the ROW a user's command becomes, and the two rules that make
// it safe: it is one transaction, and a pane that cannot be resolved costs the
// anchor rather than the command.

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// aCompletedCommand is THE factory for a completed command in this package's
// tests — the durable row an assertion is about, whether it is looked for in
// the file's bytes, read back after a reopen, or paged over.
//
// It is ONE factory because a struct literal in a test is a promise that ages
// badly: CompletedCommand will gain fields, every literal keeps compiling with
// a zero value for them, and the test goes on passing over a shape the product
// no longer writes. AGENTS.md records that exact failure — a struct literal in
// a test that predated a new required dependency, surfacing as a nil
// dereference on a merge neither branch could have caught.
//
// So a caller names only what its assertion is ABOUT, and everything a valid
// row needs lives here, where a new required field breaks loudly and once.
func aCompletedCommand(intent string) content.CompletedCommand {
	return content.CompletedCommand{
		Client: "test-client",
		Env:    content.Environment{ID: "local", Kind: content.EnvLocal},
		Cwd:    "/repo",
		Intent: intent,
		// The factory stands in for a person's command: RecordCompleted
		// refuses an empty source (provenance never silently becomes the
		// person), and a caller names the assistant's explicitly.
		Source: content.SourceUser,
		Status: content.EntrySuccess,
	}
}

// The headline: a command the renderer reports lands as a closed entry with a
// finished execution, and comes back through the ordinary recall read. Before
// the cutover this row went to a table that could hold no output and had no
// anchor; nothing about the wire changed, only where it lands.
func TestRecordCompleted_WritesAClosedEntryWithItsExecution(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)

	id, err := led.RecordCompleted(ctx, aCompletedCommand("make ci"))
	if err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	if id == "" {
		t.Fatal("RecordCompleted returned an empty id — the backend mints one here")
	}

	got, err := led.Entry(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("Entry(%q) = %+v, %v", id, got, err)
	}
	if got.Phase != content.PhaseClosed || got.Status != content.EntrySuccess {
		t.Fatalf("entry phase/status = %q/%q, want closed/success", got.Phase, got.Status)
	}
	if got.Kind != content.EntryShell {
		t.Fatalf("entry kind = %q, want shell", got.Kind)
	}
	if got.Intent != "make ci" {
		t.Fatalf("entry intent = %q, want the command", got.Intent)
	}
	// ONE execution, and it is finished. A closed entry with no execution
	// would say a command was intended and nothing about whether it ran.
	if len(got.Executions) != 1 {
		t.Fatalf("%d executions, want exactly 1", len(got.Executions))
	}
	if got.Executions[0].EndedAt == nil {
		t.Fatal("the execution has no end — a command that is being reported has already ended")
	}
	// And it is on the ordinary recall read, not in a corner of its own.
	page, err := led.QueryEntries(ctx, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10})
	if err != nil {
		t.Fatalf("QueryEntries: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].ID != id {
		t.Fatalf("recall page = %+v, want the recorded command", page.Entries)
	}
}

func TestRecordCompleted_UpdatesLifecycleAttemptWithoutMintingRow(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	envReady(t, led, "local")
	const id = "att-0000000000000001"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: id, Client: "test-client", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Source: content.SourceUser, Intent: "masked --token=sk-a...GHIJ", Payload: "{}",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	executionID, err := led.StartExecution(ctx, content.StartExecution{EntryID: id})
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	endedAt := int64(1234)
	in := aCompletedCommand("raw --token=sk-a...GHIJ")
	in.AttemptID = id
	in.EndedAt = &endedAt
	gotID, err := led.RecordCompleted(ctx, in)
	if err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	if gotID != id {
		t.Fatalf("RecordCompleted id = %q, want existing attempt %q", gotID, id)
	}
	got, err := led.Entry(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("Entry(%q) = %+v, %v", id, got, err)
	}
	if got.Phase != content.PhaseClosed || len(got.Executions) != 1 || got.Executions[0].ID != executionID || got.Executions[0].EndedAt == nil {
		t.Fatalf("completed lifecycle entry = %+v, want one closed execution", got)
	}
	page, err := led.QueryEntries(ctx, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10})
	if err != nil {
		t.Fatalf("QueryEntries: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].ID != id {
		t.Fatalf("entries = %+v, want one row under %q", page.Entries, id)
	}
}

func TestRecordCompleted_RejectsLifecycleAttemptOwnedByAnotherClient(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	envReady(t, led, "local")
	const id = "att-0000000000000002"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: id, Client: "client-a", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Source: content.SourceUser, Intent: "echo hi", Payload: "{}",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	in := aCompletedCommand("echo hi")
	in.AttemptID = id
	in.Client = "client-b"
	if _, err := led.RecordCompleted(ctx, in); err == nil {
		t.Fatal("RecordCompleted accepted an attempt owned by another client")
	}
	row, err := led.Entry(ctx, id)
	if err != nil || row == nil {
		t.Fatalf("Entry(%q) = %+v, %v", id, row, err)
	}
	if row.Phase != content.PhaseOpen {
		t.Fatalf("foreign completion changed phase to %q, want open", row.Phase)
	}
}

// The anchor arrives with the command when the pane is real (nocx-rtg0.28).
func TestRecordCompleted_AnchorsOnAPaneThatExists(t *testing.T) {
	ctx := context.Background()
	db, led := newLedger(t)
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")

	in := aCompletedCommand("go test ./...")
	in.PaneID = strPtr("pane-1")
	id, err := led.RecordCompleted(ctx, in)
	if err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	got, _ := led.Entry(ctx, id)
	if got == nil || got.PaneID == nil || *got.PaneID != "pane-1" {
		t.Fatalf("entry paneId = %+v, want pane-1", got)
	}
}

// AND A PANE THE CHAIN DOES NOT HOLD MUST NOT COST THE COMMAND. This is the
// difference from Submit, which refuses: history.record's caller cannot fix
// the id, and a user losing a recorded command because a layout row was late
// is a worse answer than a block that cannot be restored into a pane.
func TestRecordCompleted_KeepsTheCommandWhenThePaneIsUnknown(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)

	in := aCompletedCommand("echo hi")
	in.PaneID = strPtr("pane-that-was-never-created")
	id, err := led.RecordCompleted(ctx, in)
	if err != nil {
		t.Fatalf("RecordCompleted with an unknown pane: %v — the command must survive", err)
	}
	got, _ := led.Entry(ctx, id)
	if got == nil {
		t.Fatal("no row: the command was lost to an unresolvable pane")
	}
	if got.PaneID != nil {
		t.Fatalf("paneId = %q, want nil rather than a dangling anchor", *got.PaneID)
	}
}

// `pending` is not an outcome. history.record reports commands that ended, so
// a status that means "not started yet" is a caller error rather than a row.
func TestRecordCompleted_RefusesAStatusThatIsNotAnOutcome(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)

	in := aCompletedCommand("sleep 100")
	in.Status = content.EntryPending
	if _, err := led.RecordCompleted(ctx, in); err == nil {
		t.Fatal("RecordCompleted accepted status=pending, want a refusal")
	}
	in.Status = ""
	if _, err := led.RecordCompleted(ctx, in); err == nil {
		t.Fatal("RecordCompleted accepted an empty status, want a refusal")
	}
}

// ONE TRANSACTION, asserted the way the retention tests assert theirs: refuse
// the execution insert, and the entry must not be there either. A row saying a
// command was intended, with nothing about whether it ran, is the state this
// method exists to make unreachable.
func TestRecordCompleted_LeavesNoEntryWhenItsExecutionCannotBeWritten(t *testing.T) {
	ctx := context.Background()
	db, _, path := newLedgerAt(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := rawLedger(t, path, hex.EncodeToString(testKey()),
		`CREATE TRIGGER exec_boom BEFORE INSERT ON executions
		 BEGIN SELECT RAISE(ABORT, 'execution refused'); END`,
	); err != nil {
		t.Fatalf("install trigger: %v", err)
	}
	again, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()
	led := again.Ledger()

	if _, recErr := led.RecordCompleted(ctx, aCompletedCommand("make ci")); recErr == nil {
		t.Fatal("RecordCompleted succeeded while the execution insert was refused")
	}
	page, pageErr := led.QueryEntries(ctx, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10})
	if pageErr != nil {
		t.Fatalf("QueryEntries: %v", pageErr)
	}
	if len(page.Entries) != 0 {
		t.Fatalf("%d entries after a rolled-back record, want none", len(page.Entries))
	}
	if page.HasRows {
		t.Fatal("the store reports rows after a rolled-back record")
	}
}

// ── paging by the handle the wire actually carries (nocx-rtg0.19) ────────

// history.query has only ever put ONE handle on the wire — the row's id — and
// after the cutover that id is the entry's UUID. So the cursor takes it and
// resolves it, rather than the wire growing a second handle.
func TestQueryEntries_PagesFromTheEntryIdItWasGiven(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	ids := make([]string, 0, 3)
	for _, intent := range []string{"first", "second", "third"} {
		id, err := led.RecordCompleted(ctx, aCompletedCommand(intent))
		if err != nil {
			t.Fatalf("RecordCompleted %q: %v", intent, err)
		}
		ids = append(ids, id)
	}

	// Newest first, so the page starts at "third".
	page, err := led.QueryEntries(ctx, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 1})
	if err != nil {
		t.Fatalf("QueryEntries: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].ID != ids[2] {
		t.Fatalf("first page = %+v, want the newest entry", page.Entries)
	}

	next, err := led.QueryEntries(ctx, content.LedgerQuery{
		Scope: content.ScopeEverywhere, Limit: 1, BeforeID: page.Entries[0].ID,
	})
	if err != nil {
		t.Fatalf("QueryEntries after a cursor: %v", err)
	}
	if len(next.Entries) != 1 || next.Entries[0].ID != ids[1] {
		t.Fatalf("second page = %+v, want the entry before the cursor", next.Entries)
	}
	// The ORDER is ingest_seq, not the id: the second page's row is the one
	// recorded before the cursor's, whatever the ids sort like.
	if next.Entries[0].IngestSeq >= page.Entries[0].IngestSeq {
		t.Fatalf("cursor did not move backwards in commit order: %d then %d",
			page.Entries[0].IngestSeq, next.Entries[0].IngestSeq)
	}
}

// A cursor the store cannot place is REFUSED. Answering it with the newest
// page would silently restart a person's paging at the top, which reads as
// "there is nothing older" — the opposite of what happened.
func TestQueryEntries_RefusesACursorNoRowCarries(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	if _, err := led.RecordCompleted(ctx, aCompletedCommand("make ci")); err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}

	page, err := led.QueryEntries(ctx, content.LedgerQuery{
		Scope: content.ScopeEverywhere, Limit: 10, BeforeID: "no-such-entry",
	})
	if err == nil {
		t.Fatalf("QueryEntries accepted an unknown cursor and answered %+v", page.Entries)
	}
	if !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound so a caller can tell it apart", err)
	}
}

// Two cursors are two answers to "where does this page start", and the store
// refuses rather than picking one.
func TestQueryEntries_RefusesBothCursorsAtOnce(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	id, err := led.RecordCompleted(ctx, aCompletedCommand("make ci"))
	if err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	seq := int64(1)
	if _, err := led.QueryEntries(ctx, content.LedgerQuery{
		Scope: content.ScopeEverywhere, Limit: 10, BeforeID: id, Before: &seq,
	}); err == nil {
		t.Fatal("QueryEntries accepted both a seq cursor and an id cursor")
	}
}

// THE ROW KEEPS THE AUTHOR IT WAS OPENED WITH, across the transition from
// open to closed (nocx-iadtt, design §3.1). Since the row is opened at submit
// under the attempt id (nocx-kpqr3), the OPEN is the only write that decides
// the author — the close moves phase, status, times and payload and must
// leave `source` alone. That makes the open the one place the submitting
// target's author has to arrive, which is what nocx-1druc found missing:
// lifecycle.submitAttempt stamped every row 'user', so a command the
// assistant ran came back from a restart as the person's (the restore badge
// is painted from this column). The interval is stated at both ends:
// assistant at the open, assistant after the close.
func TestRecordCompleted_KeepsTheAuthorOfTheAttemptItCloses(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	envReady(t, led, "local")
	const id = "att-0000000000000009"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: id, Client: "test-client", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Source: content.SourceAssistant, Intent: "echo ran-by-the-assistant", Payload: "{}",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	open, openErr := led.Entry(ctx, id)
	if openErr != nil || open == nil {
		t.Fatalf("Entry(%q) while open = %+v, %v", id, open, openErr)
	}
	if open.Source != content.SourceAssistant {
		t.Fatalf("open row source = %q, want %q", open.Source, content.SourceAssistant)
	}
	if _, startErr := led.StartExecution(ctx, content.StartExecution{EntryID: id}); startErr != nil {
		t.Fatalf("StartExecution: %v", startErr)
	}
	in := aCompletedCommand("echo ran-by-the-assistant")
	in.AttemptID = id
	in.Source = content.SourceAssistant
	if _, recordErr := led.RecordCompleted(ctx, in); recordErr != nil {
		t.Fatalf("RecordCompleted: %v", recordErr)
	}
	got, getErr := led.Entry(ctx, id)
	if getErr != nil || got == nil {
		t.Fatalf("Entry(%q) after close = %+v, %v", id, got, getErr)
	}
	if got.Source != content.SourceAssistant {
		t.Fatalf("closed row source = %q, want %q — the author the renderer minted", got.Source, content.SourceAssistant)
	}
}

// The other end of the same rule: a close may not RE-AUTHOR a row. The open
// said the person and the close says the person; a close whose source
// disagreed with the open would be a second owner of the fact, and the
// store's answer is the one the submitting target minted at submit.
func TestRecordCompleted_DoesNotReauthorAPersonsAttempt(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	envReady(t, led, "local")
	const id = "att-000000000000000a"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: id, Client: "test-client", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Source: content.SourceUser, Intent: "echo typed-by-a-person", Payload: "{}",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, startErr := led.StartExecution(ctx, content.StartExecution{EntryID: id}); startErr != nil {
		t.Fatalf("StartExecution: %v", startErr)
	}
	in := aCompletedCommand("echo typed-by-a-person")
	in.AttemptID = id
	if _, recordErr := led.RecordCompleted(ctx, in); recordErr != nil {
		t.Fatalf("RecordCompleted: %v", recordErr)
	}
	got, getErr := led.Entry(ctx, id)
	if getErr != nil || got == nil {
		t.Fatalf("Entry(%q) after close = %+v, %v", id, got, getErr)
	}
	if got.Source != content.SourceUser {
		t.Fatalf("closed row source = %q, want %q", got.Source, content.SourceUser)
	}
}
