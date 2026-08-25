package content_test

// Entries are an ordered tree, and prose is a block (ADR-0040, amending
// ADR-0039).
//
// A turn is drawn as a sequence — the model writes prose, calls a tool,
// writes more prose, runs a command, concludes — and on screen that ORDER is
// the meaning: a sentence written before a command is why the command was
// run. The store did not have that sequence. A turn was ONE entry whose
// answer was ONE artifact, the things it caused were separate entries joined
// by a `caused-by` edge, and the edge carried `at` — how many UTF-16 units of
// the answer had been written when the cause happened — so that the renderer
// could CUT the stored answer back into fragments.
//
// `at` existed only because the unit that is DRAWN (a run of prose) and the
// unit that is STORED (the whole answer) were different things. These tests
// are the assertion that they are now the same thing: everything drawn is an
// entry, entries have a parent and a seat, and the sequence is the children
// in `pos` order.
//
// Every refusal here is paired with the ordinary write that succeeds
// (AGENTS.md testing rule 3): a constraint that refuses everything is not a
// constraint, it is an outage.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// ── fixtures ─────────────────────────────────────────────────────────────

// treeStore opens a store and hands back the seam plus the file path and key,
// so a test can also ask the DATABASE directly — which is where the CHECK and
// the UNIQUE live, and therefore where "refused by the database, not silently
// reordered" has to be read.
func treeStore(t *testing.T) (content.LedgerRepository, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "content.db")
	db, err := content.Open(context.Background(), content.Config{
		Path: path, Key: testKey(), Budget: testBudget,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	led := db.Ledger()
	envReady(t, led, "local")
	return led, path, hex.EncodeToString(testKey())
}

// submitChild records one entry at a named seat under a parent.
func submitChild(t *testing.T, led content.LedgerRepository, id, parent string, pos int, kind content.EntryKind, intent string) error {
	t.Helper()
	_, err := led.Submit(context.Background(), content.SubmitEntry{
		ID: id, Client: "test-client", EnvironmentID: "local", Cwd: "/repo",
		ParentID: &parent, Pos: intPtr(pos), Kind: kind, Intent: intent,
	})
	return err
}

// ── acceptance 1: the seat is the database's, and it is unique ───────────

// Two children cannot hold one seat. The order on screen is the order in the
// tree, so two rows at one position is a drawing order with two answers —
// refused by the DATABASE rather than resolved by whichever writer went last.
func TestASecondChildAtATakenSeatIsRefusedByTheDatabase(t *testing.T) {
	led, _, _ := treeStore(t)
	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "do two things")

	if err := submitChild(t, led, "00000000-0000-7000-8000-000000000001", turn, 0,
		content.EntryShell, "ls"); err != nil {
		t.Fatalf("the first child at seat 0: %v", err)
	}
	// The paired refusal: same parent, same seat, a different entry.
	err := submitChild(t, led, "00000000-0000-7000-8000-000000000002", turn, 0,
		content.EntryShell, "pwd")
	if err == nil {
		t.Fatal("a second child took a seat that was already taken")
	}
	// And it was REFUSED, not reordered: the store still holds exactly one
	// child, the one that got there first.
	kids, err := led.Caused(context.Background(), turn)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	if len(kids) != 1 || kids[0].Intent != "ls" {
		t.Fatalf("children after the refusal = %+v, want only the first", kids)
	}

	// The paired success, which is the whole point of the constraint being a
	// constraint: the NEXT seat is free and the same write lands.
	if err = submitChild(t, led, "00000000-0000-7000-8000-000000000002", turn, 1,
		content.EntryShell, "pwd"); err != nil {
		t.Fatalf("a child at the next free seat: %v", err)
	}
	if kids, err = led.Caused(context.Background(), turn); err != nil || len(kids) != 2 {
		t.Fatalf("children = %+v (%v), want both", kids, err)
	}
}

// The constraint binds SIBLINGS and nothing else. SQLite counts NULLs as
// distinct in a unique index, which is what lets every top-level block carry
// (NULL, NULL) — if it did not, the second command a user ever ran would be
// refused.
func TestTopLevelBlocksAllShareTheEmptySeat(t *testing.T) {
	led, _, _ := treeStore(t)
	ids := submitIntents(t, led, "ls", "pwd", "id")
	for _, id := range ids {
		row, err := led.Entry(context.Background(), id)
		if err != nil || row == nil {
			t.Fatalf("Entry(%s): %v", id, err)
		}
		if row.ParentID != nil || row.Pos != nil {
			t.Fatalf("a top-level block came back seated: parent %v pos %v", row.ParentID, row.Pos)
		}
	}
}

// Two blocks may each have a child at seat 0: the seat is a place inside ONE
// parent, never a number the store hands out.
func TestTwoParentsEachHaveTheirOwnSeatZero(t *testing.T) {
	led, _, _ := treeStore(t)
	a := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "first")
	b := submitTurn(t, led, "00000000-0000-7000-8000-00000000000b", "second")

	if err := submitChild(t, led, "00000000-0000-7000-8000-000000000001", a, 0,
		content.EntryShell, "ls"); err != nil {
		t.Fatalf("A's seat 0: %v", err)
	}
	if err := submitChild(t, led, "00000000-0000-7000-8000-000000000002", b, 0,
		content.EntryShell, "pwd"); err != nil {
		t.Fatalf("B's seat 0: %v", err)
	}
}

// ── acceptance 2: the `text` kind, and each clause of its CHECK ──────────

// textRow builds the raw INSERT for a text entry, so one clause at a time can
// be made false. Written raw and not through Submit deliberately: Submit
// writes the shape the CHECK demands, so a Go caller CANNOT express four of
// these five refusals — and the constraint is the schema's promise, which
// must hold against any writer, not only against the one that is careful.
func textRow(id, parent, pos, intent, phase, status string) string {
	return fmt.Sprintf(`INSERT INTO entries
		(id, ingest_seq, client, digest, environment_id, parent_id, pos, cwd, kind, source, intent,
		 phase, status, submitted_at)
		VALUES ('%s', %s, 'c', 'd', 'local', %s, %s, '/repo', 'text', 'assistant', '%s', '%s', '%s', 1)`,
		id, id[len(id)-3:], parent, pos, intent, phase, status)
}

func TestTheTextKindIsAcceptedOnlyInItsDeclaredShape(t *testing.T) {
	led, path, keyHex := treeStore(t)
	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "explain it")

	// THE PAIRED SUCCESS FIRST, so the five refusals below cannot be passing
	// because the statement is broken for some other reason: the declared
	// shape lands, on an ordinary database, through the same raw path.
	if err := rawLedger(t, path, keyHex,
		textRow("t900", "'"+turn+"'", "0", "", "closed", "success")); err != nil {
		t.Fatalf("the declared shape was refused: %v", err)
	}

	// One clause per case, each with everything else valid.
	cases := []struct {
		clause string
		sql    string
	}{
		{"no parent", textRow("t901", "NULL", "1", "", "closed", "success")},
		{"no pos", textRow("t902", "'"+turn+"'", "NULL", "", "closed", "success")},
		{"non-empty intent", textRow("t903", "'"+turn+"'", "1", "said something", "closed", "success")},
		{"phase other than closed", textRow("t904", "'"+turn+"'", "1", "", "open", "success")},
		{"status other than success", textRow("t905", "'"+turn+"'", "1", "", "closed", "pending")},
	}
	for _, c := range cases {
		if err := rawLedger(t, path, keyHex, c.sql); err == nil {
			t.Errorf("a text row with %s was accepted", c.clause)
		}
	}
}

// And through the SEAM, which is what a caller actually reaches: a run of
// prose is submitted with a parent and a seat, and comes back closed and
// successful without anybody having opened an execution for it. It was
// printed, not attempted.
func TestProseIsSubmittedAsABlockAndIsBornClosed(t *testing.T) {
	led, _, _ := treeStore(t)
	ctx := context.Background()
	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "explain it")

	const proseID = "00000000-0000-7000-8000-000000000001"
	if err := submitChild(t, led, proseID, turn, 0, content.EntryText, ""); err != nil {
		t.Fatalf("submitting a run of prose: %v", err)
	}
	row, err := led.Entry(ctx, proseID)
	if err != nil || row == nil {
		t.Fatalf("Entry(prose): %v (nil=%v)", err, row == nil)
	}
	if row.Kind != content.EntryText {
		t.Fatalf("kind = %q, want text", row.Kind)
	}
	if row.Phase != content.PhaseClosed || row.Status != content.EntrySuccess {
		t.Fatalf("prose phase/status = %q/%q, want closed/success — it was printed, not attempted",
			row.Phase, row.Status)
	}
	if len(row.Executions) != 0 {
		t.Fatalf("prose has %d executions, want none", len(row.Executions))
	}
	if row.ParentID == nil || *row.ParentID != turn || row.Pos == nil || *row.Pos != 0 {
		t.Fatalf("prose is seated at %v/%v, want %s/0", row.ParentID, row.Pos, turn)
	}

	// The paired refusal at the seam: prose with no parent is not a block a
	// reader could draw anywhere, and the store says so.
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: "00000000-0000-7000-8000-000000000002", Client: "test-client",
		EnvironmentID: "local", Cwd: "/repo", Kind: content.EntryText,
	}); err == nil {
		t.Fatal("a run of prose with no parent was accepted")
	}
}

// ── acceptance 3: an artifact belongs to its block ───────────────────────

// A body has an OWNER (the block) and, when there was an attempt, a
// PROVENANCE (the execution). The prose case has the first and not the
// second; the command case has both. Both land.
func TestAnArtifactBelongsToItsBlockWithOrWithoutAnExecution(t *testing.T) {
	led, _, _ := treeStore(t)
	ctx := context.Background()
	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "explain it")

	// The text case: a body, no attempt.
	const proseID = "00000000-0000-7000-8000-000000000001"
	if err := submitChild(t, led, proseID, turn, 0, content.EntryText, ""); err != nil {
		t.Fatalf("submitting prose: %v", err)
	}
	proseArt, err := led.AppendArtifact(ctx, content.AppendArtifact{
		EntryID: proseID, ID: "aaaaaaaa-0000-7000-8000-000000000001",
		MediaType: content.MediaText,
	})
	if err != nil {
		t.Fatalf("a body with no execution was refused: %v", err)
	}
	if err = led.AppendChunk(ctx, proseArt, 1, []byte("Let me look.")); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}
	got, err := led.Artifact(ctx, proseArt)
	if err != nil || got == nil {
		t.Fatalf("Artifact: %v (nil=%v)", err, got == nil)
	}
	if got.EntryID != proseID {
		t.Fatalf("the prose body belongs to %q, want %q", got.EntryID, proseID)
	}
	if got.ExecutionID != nil {
		t.Fatalf("the prose body names execution %d — prose was printed, not run", *got.ExecutionID)
	}
	if len(got.Chunks) != 1 || string(got.Chunks[0]) != "Let me look." {
		t.Fatalf("the prose body reads %q, want the sentence", got.Chunks)
	}

	// The command case: a body AND the attempt that produced it.
	const cmdID = "00000000-0000-7000-8000-000000000002"
	if err = submitChild(t, led, cmdID, turn, 1, content.EntryShell, "ls -la"); err != nil {
		t.Fatalf("submitting the command: %v", err)
	}
	execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: cmdID})
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	cmdArt, err := led.AppendArtifact(ctx, content.AppendArtifact{
		EntryID: cmdID, ExecutionID: &execID, ID: "aaaaaaaa-0000-7000-8000-000000000002",
		MediaType: content.MediaVT, CaptureMethod: content.CaptureRawOutput,
	})
	if err != nil {
		t.Fatalf("a body with an execution was refused: %v", err)
	}
	if got, err = led.Artifact(ctx, cmdArt); err != nil || got == nil {
		t.Fatalf("Artifact(command): %v (nil=%v)", err, got == nil)
	}
	if got.EntryID != cmdID {
		t.Fatalf("the command body belongs to %q, want %q", got.EntryID, cmdID)
	}
	if got.ExecutionID == nil || *got.ExecutionID != execID {
		t.Fatalf("the command body names execution %v, want %d", got.ExecutionID, execID)
	}
}

// The block is the owner, so deleting it takes its bodies — including a
// text block's, which no execution ever held.
func TestDeletingABlockTakesTheBodyNoExecutionHeld(t *testing.T) {
	led, _, _ := treeStore(t)
	ctx := context.Background()
	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "explain it")

	const proseID = "00000000-0000-7000-8000-000000000001"
	if err := submitChild(t, led, proseID, turn, 0, content.EntryText, ""); err != nil {
		t.Fatalf("submitting prose: %v", err)
	}
	artID, err := led.AppendArtifact(ctx, content.AppendArtifact{
		EntryID: proseID, ID: "aaaaaaaa-0000-7000-8000-000000000001",
		MediaType: content.MediaText,
	})
	if err != nil {
		t.Fatalf("AppendArtifact: %v", err)
	}
	// The paired success: it is there before the delete.
	if a, aErr := led.Artifact(ctx, artID); aErr != nil || a == nil {
		t.Fatalf("the body is not in the store before the delete: %v", aErr)
	}
	if err = led.DeleteEntry(ctx, proseID); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	a, err := led.Artifact(ctx, artID)
	if err != nil {
		t.Fatalf("Artifact after the delete: %v", err)
	}
	if a != nil {
		t.Fatalf("the body outlived its block: %+v", a)
	}
}

// ── acceptance 4: `caused-by` is retired from the vocabulary ─────────────

// The relation is a column now, and the database no longer knows the word.
// Asserted raw, because the Go constant is gone: the check that matters is
// that the SCHEMA refuses it, not that our own enum stopped naming it.
func TestTheCausedByRelationIsRefusedByTheSchema(t *testing.T) {
	led, path, keyHex := treeStore(t)
	ids := submitIntents(t, led, "ls", "pwd")

	edge := func(rel string) string {
		return fmt.Sprintf(`INSERT INTO edges (from_id, to_id, rel) VALUES ('%s', '%s', '%s')`,
			ids[0], ids[1], rel)
	}
	if err := rawLedger(t, path, keyHex, edge("caused-by")); err == nil {
		t.Fatal("a caused-by edge was accepted — containment is a column, not a relation")
	}
	// The paired success: what is left in the vocabulary still goes in. A
	// CHECK that refused every value would pass the assertion above and be
	// an outage.
	if err := rawLedger(t, path, keyHex, edge("rerun-of")); err != nil {
		t.Fatalf("a rerun-of edge was refused: %v", err)
	}
	if err := led.AddEdge(context.Background(), content.Edge{
		From: ids[1], To: ids[0], Rel: content.RelSupersedes,
	}); err != nil {
		t.Fatalf("AddEdge(supersedes): %v", err)
	}
}

// ── acceptance 6: the ledger writes and reads the tree ───────────────────

// The order that comes back is the SEAT's, and the test only proves that if
// the two orders differ: the children are submitted 2, 0, 1, so `ingest_seq`
// order and `pos` order disagree on every position, and a read that quietly
// fell back to commit order would return them in the order they were written.
func TestAParentReadsBackItsChildrenInSeatOrderAndNotInCommitOrder(t *testing.T) {
	led, _, _ := treeStore(t)
	ctx := context.Background()
	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a",
		"how much disk is left?")

	// Submitted last-seat first. The prose at seat 0 is what the model said
	// BEFORE it ran anything, and it is written to the store after both.
	written := []struct {
		id     string
		pos    int
		kind   content.EntryKind
		intent string
	}{
		{"00000000-0000-7000-8000-000000000003", 2, content.EntryText, ""},
		{"00000000-0000-7000-8000-000000000001", 0, content.EntryText, ""},
		{"00000000-0000-7000-8000-000000000002", 1, content.EntryShell, "df -h"},
	}
	for _, w := range written {
		if err := submitChild(t, led, w.id, turn, w.pos, w.kind, w.intent); err != nil {
			t.Fatalf("submitting the child at seat %d: %v", w.pos, err)
		}
	}

	kids, err := led.Caused(ctx, turn)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	want := []string{written[1].id, written[2].id, written[0].id}
	if len(kids) != len(want) {
		t.Fatalf("children = %+v, want three", kids)
	}
	for i := range want {
		if kids[i].EntryID != want[i] || kids[i].Position != i {
			t.Fatalf("child %d = %+v, want %s at seat %d", i, kids[i], want[i], i)
		}
	}
	// The two orders really do disagree, which is what makes the assertion
	// above evidence rather than a coincidence: commit order is the order
	// they were written in.
	commitOrder := []string{written[0].id, written[1].id, written[2].id}
	same := true
	for i := range want {
		if want[i] != commitOrder[i] {
			same = false
		}
	}
	if same {
		t.Fatal("the fixture's seat order equals its commit order — the test proves nothing")
	}
	// And the sequence is the meaning: prose, then the command, then the
	// prose written from its result.
	if kids[0].Kind != content.EntryText || kids[1].Kind != content.EntryShell ||
		kids[2].Kind != content.EntryText {
		t.Fatalf("the sequence reads %q, %q, %q; want prose, command, prose",
			kids[0].Kind, kids[1].Kind, kids[2].Kind)
	}
}

// The tree survives a reopen, because it is a column on a durable row and
// not state anybody rebuilds at start-up.
func TestTheTreeSurvivesAReopen(t *testing.T) {
	db, led, path := newLedgerAt(t)
	ctx := context.Background()
	envReady(t, led, "local")
	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "persist")

	if err := submitChild(t, led, "00000000-0000-7000-8000-000000000002", turn, 1,
		content.EntryShell, "pwd"); err != nil {
		t.Fatalf("seat 1: %v", err)
	}
	if err := submitChild(t, led, "00000000-0000-7000-8000-000000000001", turn, 0,
		content.EntryText, ""); err != nil {
		t.Fatalf("seat 0: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openLedgerAt(t, path)
	kids, err := reopened.Caused(ctx, turn)
	if err != nil {
		t.Fatalf("Caused after reopen: %v", err)
	}
	if len(kids) != 2 || kids[0].Position != 0 || kids[0].Kind != content.EntryText ||
		kids[1].Position != 1 || kids[1].Kind != content.EntryShell {
		t.Fatalf("after reopen the children are %+v, want prose at 0 and the command at 1", kids)
	}
}

// ── AddCause, the other writer of the same column ────────────────────────

// A block whose seat is not known when it is written is seated afterwards —
// a command a `run` call opened, submitted by the renderer before anyone knew
// which turn it belonged to. Both writers reach ONE column, so the seats they
// hand out cannot collide.
func TestAnEntrySeatedAfterwardsTakesTheNextFreeSeat(t *testing.T) {
	led, _, _ := treeStore(t)
	ctx := context.Background()
	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "run it")

	if err := submitChild(t, led, "00000000-0000-7000-8000-000000000001", turn, 0,
		content.EntryText, ""); err != nil {
		t.Fatalf("the prose at seat 0: %v", err)
	}
	late := submitIntents(t, led, "ls -la")[0]
	pos, err := led.AddCause(ctx, turn, late)
	if err != nil {
		t.Fatalf("AddCause: %v", err)
	}
	if pos != 1 {
		t.Fatalf("the late child took seat %d, want 1 — the prose already holds 0", pos)
	}
	kids, err := led.Caused(ctx, turn)
	if err != nil || len(kids) != 2 || kids[1].EntryID != late {
		t.Fatalf("children = %+v (%v), want the prose then the command", kids, err)
	}
}

// ONE PARENT, which is the whole reason containment is a column: an entry
// already drawn inside one block is refused a second, rather than moved. The
// edge it replaced could hold both and made the reader choose.
func TestAnEntryAlreadySeatedIsRefusedASecondParent(t *testing.T) {
	led, _, _ := treeStore(t)
	ctx := context.Background()
	a := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "first")
	b := submitTurn(t, led, "00000000-0000-7000-8000-00000000000b", "second")
	entries := submitIntents(t, led, "ls", "pwd")
	child, other := entries[0], entries[1]

	if _, err := led.AddCause(ctx, a, child); err != nil {
		t.Fatalf("seating under A: %v", err)
	}
	if _, err := led.AddCause(ctx, b, child); err == nil {
		t.Fatal("a block was drawn inside two turns at once")
	}
	// Nothing moved: it is still A's, at the seat A gave it.
	kids, err := led.Caused(ctx, a)
	if err != nil || len(kids) != 1 || kids[0].EntryID != child || kids[0].Position != 0 {
		t.Fatalf("A's children = %+v (%v), want the one child still at seat 0", kids, err)
	}
	if kids, err = led.Caused(ctx, b); err != nil || len(kids) != 0 {
		t.Fatalf("B's children = %+v (%v), want none", kids, err)
	}
	// The paired success: a DIFFERENT entry seats under B perfectly well.
	if _, err := led.AddCause(ctx, b, other); err != nil {
		t.Fatalf("seating a fresh entry under B: %v", err)
	}
}

// Either id naming nothing is refused, and the refusal leaves nothing
// half-written: an UPDATE that matched no row would silently change nothing,
// which is the one answer worse than an error.
func TestSeatingRefusesAnEntryThatIsNotThere(t *testing.T) {
	led, _, _ := treeStore(t)
	ctx := context.Background()
	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "q")
	child := submitIntents(t, led, "ls")[0]

	if _, err := led.AddCause(ctx, turn, "ghost"); !errors.Is(err, content.ErrNoSuchEntry) {
		t.Fatalf("seating a child that does not exist = %v, want ErrNoSuchEntry", err)
	}
	if _, err := led.AddCause(ctx, "ghost", child); !errors.Is(err, content.ErrNoSuchEntry) {
		t.Fatalf("seating under a parent that does not exist = %v, want ErrNoSuchEntry", err)
	}
	kids, err := led.Caused(ctx, turn)
	if err != nil || len(kids) != 0 {
		t.Fatalf("a refused seating left %+v behind (%v)", kids, err)
	}
	// The paired success, on the same store: the ordinary seating lands.
	if _, err := led.AddCause(ctx, turn, child); err != nil {
		t.Fatalf("seating a real child under a real parent: %v", err)
	}
}

// ── the parent going away ────────────────────────────────────────────────

// ON DELETE SET NULL, not CASCADE, for the reason pane_id and session_id
// already give: the container is gone, the block is not, and it must not be
// left pointing at a row that is not there. A tool call whose turn was
// evicted is still a command that ran.
func TestAChildOutlivesTheParentThatWasDeleted(t *testing.T) {
	led, _, _ := treeStore(t)
	ctx := context.Background()
	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "run it")
	child := submitIntents(t, led, "ls -la")[0]
	if _, err := led.AddCause(ctx, turn, child); err != nil {
		t.Fatalf("AddCause: %v", err)
	}

	if err := led.DeleteEntry(ctx, turn); err != nil {
		t.Fatalf("DeleteEntry(turn): %v", err)
	}
	row, err := led.Entry(ctx, child)
	if err != nil || row == nil {
		t.Fatalf("the command went with its turn: %v (nil=%v)", err, row == nil)
	}
	if row.ParentID != nil {
		t.Fatalf("the command still points at %q, which is gone", *row.ParentID)
	}
	if row.Intent != "ls -la" {
		t.Fatalf("the surviving row reads %q, want the command", row.Intent)
	}
}
