package content_test

// Containment with an order (nocx-h1l4o), through the public seam. ADR-0039
// ends on the sentence these tests are: an assistant turn is one entry, the
// things it causes are separate entries, and until now nothing joined them —
// `ingest_seq` is commit order, never causality (ADR-0019 §2), so a restored
// turn came back with neither its calls nor a reason to draw its command
// anywhere in particular.
//
// ADR-0040 made that relation a COLUMN. Two facts are still asserted IN THE
// STORE and never inferred from ordering: that the child is inside the
// parent, and that it holds the seat the parent gave it. The seat is the
// ledger's own, taken inside the transaction that writes it — see AddCause's
// doc for why the caller may not supply one.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// submitTurn records one assistant turn entry (kind=agent) and returns its
// id — the entry every cause below points at.
func submitTurn(t *testing.T, led content.LedgerRepository, id, question string) string {
	t.Helper()
	res, err := led.Submit(context.Background(), content.SubmitEntry{
		ID: id, Client: "test-client", EnvironmentID: "local",
		Cwd: "/repo", Kind: content.EntryAsk, Intent: question,
	})
	if err != nil {
		t.Fatalf("Submit turn: %v", err)
	}
	return res.ID
}

// submitAction records one tool call the way internal/assistant does: an
// action entry whose payload names the tool, the effect and the resource the
// call touched.
func submitAction(t *testing.T, led content.LedgerRepository, id, tool string, effect content.Effect, res *content.GrantScope) string {
	t.Helper()
	body := map[string]any{"tool": tool, "effect": string(effect)}
	if res != nil {
		body["resource"] = res
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("action payload: %v", err)
	}
	out, err := led.Submit(context.Background(), content.SubmitEntry{
		ID: id, Client: "agent", EnvironmentID: "local",
		Cwd: "/", Kind: content.EntryAction, Intent: tool, Payload: string(payload),
	})
	if err != nil {
		t.Fatalf("Submit action: %v", err)
	}
	return out.ID
}

// causePosition reads the seat off the child's OWN ROW — the assertion is IN
// THE STORE, and since ADR-0040 the store's answer is a column rather than an
// edge payload: parent_id says which block draws it and pos says where.
func causePosition(t *testing.T, led content.LedgerRepository, child, parent string) int {
	t.Helper()
	row, err := led.Entry(context.Background(), child)
	if err != nil {
		t.Fatalf("Entry(%s): %v", child, err)
	}
	if row == nil {
		t.Fatalf("Entry(%s) = nil — the child is not in the store", child)
	}
	if row.ParentID == nil || *row.ParentID != parent {
		t.Fatalf("%s is drawn inside %v, want %s", child, row.ParentID, parent)
	}
	if row.Pos == nil {
		t.Fatalf("%s is a child of %s with no seat", child, parent)
	}
	return *row.Pos
}

// ── the relation and its order ───────────────────────────────────────────

func TestACommandAnAssistantTurnRanCarriesACausedByEdgeWithAPosition(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "what is in here?")
	ids := submitIntents(t, led, "ls -la", "cat go.mod")

	first, err := led.AddCause(ctx, turn, ids[0])
	if err != nil {
		t.Fatalf("AddCause: %v", err)
	}
	second, err := led.AddCause(ctx, turn, ids[1])
	if err != nil {
		t.Fatalf("AddCause 2: %v", err)
	}
	if first != 0 || second != 1 {
		t.Fatalf("positions = %d, %d; want 0, 1 — one per entry the turn caused", first, second)
	}

	// The stored fact, read back off the edges themselves.
	if got := causePosition(t, led, ids[0], turn); got != 0 {
		t.Fatalf("stored position of the first command = %d, want 0", got)
	}
	if got := causePosition(t, led, ids[1], turn); got != 1 {
		t.Fatalf("stored position of the second command = %d, want 1", got)
	}
}

func TestAnActionEntryJoinsItsTurnByTheSameRelation(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "read the config")
	action := submitAction(t, led, "00000000-0000-7000-8000-00000000000b",
		"files.read", content.EffectObserve,
		&content.GrantScope{Kind: content.ResourcePath, ID: "/repo/go.mod"})

	if _, err := led.AddCause(ctx, turn, action); err != nil {
		t.Fatalf("AddCause: %v", err)
	}
	if got := causePosition(t, led, action, turn); got != 0 {
		t.Fatalf("stored position of the action = %d, want 0", got)
	}

	// And the read resolves it into what a restored turn draws: the tool,
	// the effect the gate decided and the resource the backend derived —
	// never re-derived by the reader.
	caused, err := led.Caused(ctx, turn)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	if len(caused) != 1 {
		t.Fatalf("Caused = %+v, want exactly the one action", caused)
	}
	got := caused[0]
	if got.EntryID != action || got.Position != 0 || got.Kind != content.EntryAction {
		t.Fatalf("caused entry = %+v, want the action at position 0", got)
	}
	if got.Intent != "files.read" || got.Effect != content.EffectObserve {
		t.Fatalf("caused entry tool facts = %q/%q, want files.read/observe", got.Intent, got.Effect)
	}
	if got.Resource == nil || got.Resource.Kind != content.ResourcePath || got.Resource.ID != "/repo/go.mod" {
		t.Fatalf("caused entry resource = %+v, want path /repo/go.mod", got.Resource)
	}
}

// One relation, one order, whatever the turn caused: an action and a command
// interleave by the position the turn assigned, never by ingest_seq.
func TestCausedComesBackInCausalPositionOrder(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "check it")
	cmds := submitIntents(t, led, "ls", "pwd")
	action := submitAction(t, led, "00000000-0000-7000-8000-00000000000b",
		"readScreen", content.EffectObserve, nil)

	// Caused in an order that is NOT the order the rows were submitted in.
	for _, id := range []string{action, cmds[1], cmds[0]} {
		if _, err := led.AddCause(ctx, turn, id); err != nil {
			t.Fatalf("AddCause(%s): %v", id, err)
		}
	}
	caused, err := led.Caused(ctx, turn)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	var order []string
	for _, c := range caused {
		order = append(order, c.EntryID)
	}
	want := []string{action, cmds[1], cmds[0]}
	if len(order) != len(want) {
		t.Fatalf("Caused = %+v, want three", caused)
	}
	for i := range want {
		if order[i] != want[i] || caused[i].Position != i {
			t.Fatalf("Caused[%d] = %+v, want %s at position %d", i, caused[i], want[i], i)
		}
	}
	// The action's resource is honestly absent, never an empty scope: the
	// tool named none (readScreen's own session is the grant's).
	if caused[0].Resource != nil {
		t.Fatalf("a call that named no resource came back with %+v", caused[0].Resource)
	}
}

// A replay of the same pair is the SAME cause, not a second one: the
// approval resume re-runs the pipeline over a call that already happened,
// and a counter that advanced on the replay would put the resumed call after
// everything that followed it.
func TestAddCauseIsIdempotentOnThePair(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "twice")
	ids := submitIntents(t, led, "ls", "pwd")

	if _, err := led.AddCause(ctx, turn, ids[0]); err != nil {
		t.Fatalf("AddCause: %v", err)
	}
	if _, err := led.AddCause(ctx, turn, ids[1]); err != nil {
		t.Fatalf("AddCause 2: %v", err)
	}
	again, err := led.AddCause(ctx, turn, ids[0])
	if err != nil {
		t.Fatalf("AddCause replay: %v", err)
	}
	if again != 0 {
		t.Fatalf("replayed position = %d, want the original 0", again)
	}
	caused, err := led.Caused(ctx, turn)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	if len(caused) != 2 {
		t.Fatalf("Caused = %+v, want two — a replay is not a third cause", caused)
	}
}

// Two turns keep two counters: a position is a place INSIDE one turn, not a
// number handed out by the store.
func TestEachTurnCountsItsOwnCauses(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	turnA := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "first")
	turnB := submitTurn(t, led, "00000000-0000-7000-8000-00000000000b", "second")
	ids := submitIntents(t, led, "ls", "pwd", "id")

	if pos, err := led.AddCause(ctx, turnA, ids[0]); err != nil || pos != 0 {
		t.Fatalf("A's first cause = %d (%v), want 0", pos, err)
	}
	if pos, err := led.AddCause(ctx, turnB, ids[1]); err != nil || pos != 0 {
		t.Fatalf("B's first cause = %d (%v), want 0 — B counts its own", pos, err)
	}
	if pos, err := led.AddCause(ctx, turnA, ids[2]); err != nil || pos != 1 {
		t.Fatalf("A's second cause = %d (%v), want 1", pos, err)
	}
}

// ── what the relation refuses ────────────────────────────────────────────

func TestAddCauseRefusesAnEntryThatIsNotThere(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "q")
	ids := submitIntents(t, led, "ls")

	if _, err := led.AddCause(ctx, turn, "ghost"); err == nil {
		t.Fatal("AddCause accepted a caused entry that does not exist")
	}
	if _, err := led.AddCause(ctx, "ghost", ids[0]); err == nil {
		t.Fatal("AddCause accepted a turn that does not exist")
	}
	// Nothing was left behind by either refusal.
	caused, err := led.Caused(ctx, turn)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	if len(caused) != 0 {
		t.Fatalf("a refused AddCause left %+v behind", caused)
	}
}

func TestCausedIsEmptyForAnEntryThatCausedNothing(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "quiet")
	caused, err := led.Caused(ctx, turn)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	if len(caused) != 0 {
		t.Fatalf("Caused = %+v, want none", caused)
	}
	// And an entry nobody ever heard of is the same answer, not an error:
	// the read is "what did this cause", and the honest answer is nothing.
	if caused, err = led.Caused(ctx, "ghost"); err != nil || len(caused) != 0 {
		t.Fatalf("Caused(ghost) = %+v (%v), want empty", caused, err)
	}
}

// ── both ends of the interval (AGENTS.md testing rule 3) ─────────────────

// The relation exists from before the caused entry ends until the entry is
// deleted — it does NOT close when the turn closes, which is the whole point
// of criterion 5: a command that outlives its turn keeps its place in it.
func TestTheRelationOutlivesTheTurnThatClosedFirst(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "run it")
	ids := submitIntents(t, led, "sleep 30")

	if _, err := led.AddCause(ctx, turn, ids[0]); err != nil {
		t.Fatalf("AddCause: %v", err)
	}
	// The turn closes while the command is still open.
	turnExec, err := led.StartExecution(ctx, content.StartExecution{EntryID: turn})
	if err != nil {
		t.Fatalf("StartExecution(turn): %v", err)
	}
	if err = led.FinishExecution(ctx, turnExec, content.FinishExecution{
		EndedAt: 1, TerminationReason: content.TermCompleted, Status: content.EntrySuccess,
	}); err != nil {
		t.Fatalf("FinishExecution(turn): %v", err)
	}
	stillThere, causedErr := led.Caused(ctx, turn)
	if causedErr != nil || len(stillThere) != 1 {
		t.Fatalf("after the turn closed, Caused = %+v (%v), want the command still there", stillThere, causedErr)
	}

	// The command finishes afterwards, and the relation is untouched by it.
	cmdExec, err := led.StartExecution(ctx, content.StartExecution{EntryID: ids[0]})
	if err != nil {
		t.Fatalf("StartExecution(command): %v", err)
	}
	if err = led.FinishExecution(ctx, cmdExec, content.FinishExecution{
		EndedAt: 2, TerminationReason: content.TermCompleted, Status: content.EntrySuccess,
	}); err != nil {
		t.Fatalf("FinishExecution(command): %v", err)
	}
	caused, err := led.Caused(ctx, turn)
	if err != nil || len(caused) != 1 || caused[0].EntryID != ids[0] || caused[0].Position != 0 {
		t.Fatalf("after the command closed, Caused = %+v (%v), want it at position 0", caused, err)
	}
	if got := causePosition(t, led, ids[0], turn); got != 0 {
		t.Fatalf("stored position after both ends closed = %d, want 0", got)
	}
}

// A turn that failed mid-call keeps the calls it had already made: the
// relation is written when the cause happens, not when the turn succeeds.
func TestATurnThatFailedMidCallKeepsTheCallsItMade(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "do three things")
	done := submitAction(t, led, "00000000-0000-7000-8000-00000000000b",
		"files.read", content.EffectObserve, nil)
	dying := submitAction(t, led, "00000000-0000-7000-8000-00000000000c",
		"blocks.read", content.EffectObserve, nil)

	if _, err := led.AddCause(ctx, turn, done); err != nil {
		t.Fatalf("AddCause(done): %v", err)
	}
	if _, err := led.AddCause(ctx, turn, dying); err != nil {
		t.Fatalf("AddCause(dying): %v", err)
	}
	// The second call fails and the turn fails with it.
	exec, err := led.StartExecution(ctx, content.StartExecution{EntryID: dying})
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	if err = led.FinishExecution(ctx, exec, content.FinishExecution{
		EndedAt: 1, TerminationReason: content.TermFailed, Status: content.EntryFailure,
	}); err != nil {
		t.Fatalf("FinishExecution: %v", err)
	}
	turnExec, err := led.StartExecution(ctx, content.StartExecution{EntryID: turn})
	if err != nil {
		t.Fatalf("StartExecution(turn): %v", err)
	}
	if err = led.FinishExecution(ctx, turnExec, content.FinishExecution{
		EndedAt: 2, TerminationReason: content.TermFailed, Status: content.EntryFailure,
	}); err != nil {
		t.Fatalf("FinishExecution(turn): %v", err)
	}

	caused, err := led.Caused(ctx, turn)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	if len(caused) != 2 || caused[0].EntryID != done || caused[1].EntryID != dying {
		t.Fatalf("a failed turn came back with %+v, want both calls in order", caused)
	}
}

// ── durability ───────────────────────────────────────────────────────────

func TestTheCausalPositionSurvivesAReopen(t *testing.T) {
	db, led, path := newLedgerAt(t)
	ctx := context.Background()
	envReady(t, led, "local")

	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "persist")
	ids := submitIntents(t, led, "ls", "pwd")
	for _, id := range ids {
		if _, err := led.AddCause(ctx, turn, id); err != nil {
			t.Fatalf("AddCause: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openLedgerAt(t, path)
	caused, err := reopened.Caused(ctx, turn)
	if err != nil {
		t.Fatalf("Caused after reopen: %v", err)
	}
	if len(caused) != 2 || caused[0].EntryID != ids[0] || caused[1].EntryID != ids[1] {
		t.Fatalf("after reopen Caused = %+v, want both in their stored order", caused)
	}
	// The counter continues from what is stored, not from zero: a restart
	// mid-turn must not put the next cause on top of an earlier one.
	next, err := reopened.AddCause(ctx, turn, submitTurn(t, reopened,
		"00000000-0000-7000-8000-00000000000d", "another entry"))
	if err != nil {
		t.Fatalf("AddCause after reopen: %v", err)
	}
	if next != 2 {
		t.Fatalf("the first cause after a reopen took position %d, want 2", next)
	}
}

// A call whose work becomes a block of its own says so ON ITS ROW — the
// declaration's fact (agenttools.Declaration.OpensBlock), written with the
// attempt and read back here. The renderer needs it to know the call's
// occurrence is owned by the block rather than by a line, and deriving it
// from the tool NAME in the reader would be a second copy of the tool table.
func TestAnActionSaysWhetherItOpenedABlockOfItsOwn(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	turn := submitTurn(t, led, "00000000-0000-7000-8000-00000000000a", "how much disk is left?")
	opens := submitOpeningAction(t, led, "00000000-0000-7000-8000-00000000000b", "run")
	quiet := submitAction(t, led, "00000000-0000-7000-8000-00000000000c",
		"readScreen", content.EffectObserve,
		&content.GrantScope{Kind: content.ResourceSession, ID: "sess-1"})

	if _, err := led.AddCause(ctx, turn, opens); err != nil {
		t.Fatalf("AddCause: %v", err)
	}
	if _, err := led.AddCause(ctx, turn, quiet); err != nil {
		t.Fatalf("AddCause 2: %v", err)
	}

	caused, err := led.Caused(ctx, turn)
	if err != nil {
		t.Fatalf("Caused: %v", err)
	}
	byID := map[string]content.CausedEntry{}
	for _, c := range caused {
		byID[c.EntryID] = c
	}
	if !byID[opens].OpensBlock {
		t.Fatalf("the run call's row says OpensBlock=false; the block it opened is the account of that call")
	}
	if byID[quiet].OpensBlock {
		t.Fatalf("readScreen's row says OpensBlock=true; it opens nothing and its line is the only trace")
	}
}

// submitOpeningAction records the action entry of a call whose work becomes
// a block — the shape policy.go openAttempt writes for `run`.
func submitOpeningAction(t *testing.T, led content.LedgerRepository, id, tool string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"tool":       tool,
		"effect":     string(content.EffectMutateDestructive),
		"opensBlock": true,
	})
	if err != nil {
		t.Fatalf("action payload: %v", err)
	}
	out, err := led.Submit(context.Background(), content.SubmitEntry{
		ID: id, Client: "agent", EnvironmentID: "local",
		Cwd: "/", Kind: content.EntryAction, Intent: tool, Payload: string(payload),
	})
	if err != nil {
		t.Fatalf("Submit action: %v", err)
	}
	return out.ID
}
