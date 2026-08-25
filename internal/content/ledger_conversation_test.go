package content_test

// The conversation read (ADR-0040's closing consequence, bead nocx-dc2fr.5):
// "the conversation is assembled from the children, in pos order, per run".
//
// A turn's answer used to be ONE string in ONE column, and asking a follow-up
// question meant handing that string to the model. It is several rows now —
// one `text` block per run of prose between two tool calls — so the answer is
// a JOIN, and these tests are about the four ways that join can be silently
// wrong: it can splice two attempts together, it can present an unfinished
// attempt as an answer, it can leave a hole where retention took the text, and
// it can read the wrong pane's conversation.
//
// Every "assembles nothing when…" below is paired with the "and with prose
// present it assembles" that keeps it honest (AGENTS.md testing rule 3): a
// read that answered nothing to everything would satisfy half of these tests
// and be an outage.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// ── fixtures ─────────────────────────────────────────────────────────────

// conversationPane is one store with a pane and an environment ready — the
// shape every ask has (a turn is anchored to a pane, nocx-4em1z).
func conversationPane(t *testing.T) content.LedgerRepository {
	t.Helper()
	db, led := newLedger(t)
	aPaneUnder(t, db, "ws-conv", "tab-conv", "pane-conv")
	envReady(t, led, "local")
	return led
}

// saying opens one run of prose under the turn, for the given run, and writes
// text into it — the two calls the stream makes on the first delta after a
// call. It returns the block so a caller can talk about the seat it took.
func saying(t *testing.T, led content.LedgerRepository, turnID string, runID int64, text string) content.ProseBlock {
	t.Helper()
	ctx := context.Background()
	block, err := led.OpenProse(ctx, turnID, runID)
	if err != nil {
		t.Fatalf("OpenProse under %s for run %d: %v", turnID, runID, err)
	}
	if err := led.AppendChunk(ctx, block.ArtifactID, 1, []byte(text)); err != nil {
		t.Fatalf("AppendChunk into %s: %v", block.ArtifactID, err)
	}
	return block
}

// callingATool seats one action entry under the turn — the thing that ENDS a
// run of prose and starts the next one. The tests use it to make the seats
// realistic: the prose either side of a call is what "in pos order" is about.
func callingATool(t *testing.T, led content.LedgerRepository, turnID, entryID, tool string) {
	t.Helper()
	action := submitAction(t, led, entryID, tool, content.EffectObserve, nil)
	if _, err := led.AddCause(context.Background(), turnID, action); err != nil {
		t.Fatalf("AddCause(%s under %s): %v", action, turnID, err)
	}
}

// finished closes a run in a terminal state, which is the fact that separates
// a real message from an unfinished attempt.
func finished(t *testing.T, led content.LedgerRepository, runID int64, state content.RunState, reason content.TerminationReason) {
	t.Helper()
	if err := led.TransitionRun(context.Background(), runID, content.RunStreaming); err != nil {
		t.Fatalf("TransitionRun(%d, streaming): %v", runID, err)
	}
	if err := led.FinishAgentRun(context.Background(), runID, content.FinishAgentRun{
		State: state, TerminationReason: reason, EndedAt: 1,
	}); err != nil {
		t.Fatalf("FinishAgentRun(%d, %s): %v", runID, state, err)
	}
}

// priorTo is the read under test, with the error handling every call site
// would otherwise repeat.
func priorTo(t *testing.T, led content.LedgerRepository, paneID, entryID string) *content.PriorTurn {
	t.Helper()
	prior, err := led.PriorTurn(context.Background(), paneID, entryID)
	if err != nil {
		t.Fatalf("PriorTurn(%s, before %s): %v", paneID, entryID, err)
	}
	return prior
}

// ── acceptance 1: the prose of a turn that interleaved is sent WHOLE ─────

// A turn says something, calls a tool, says something else, calls another,
// concludes — and a follow-up question gets that turn's prose as ONE message,
// in the order it was written.
//
// The order is the whole meaning and not a tidiness: a sentence written before
// a call explains why the call was made, and a sentence written after it is a
// conclusion drawn from its output. A message that shuffled them would still
// contain every word and would say something the model never said.
func TestPriorTurn_TheProseOfAnInterleavedTurnIsOneMessageInSeatOrder(t *testing.T) {
	led := conversationPane(t)
	first := askIn(t, led, "session-1", "pane-conv")

	saying(t, led, first.EntryID, first.RunID, "let me look: ")
	callingATool(t, led, first.EntryID, "00000000-0000-7000-8000-0000000000b1", "files.read")
	saying(t, led, first.EntryID, first.RunID, "it says hello, ")
	callingATool(t, led, first.EntryID, "00000000-0000-7000-8000-0000000000b2", "files.read")
	saying(t, led, first.EntryID, first.RunID, "so the config is fine.")
	finished(t, led, first.RunID, content.RunCompleted, content.TermCompleted)

	second := askIn(t, led, "session-1", "pane-conv")
	prior := priorTo(t, led, "pane-conv", second.EntryID)
	if prior == nil {
		t.Fatal("the follow-up sees no earlier turn — the conversation is one turn long")
	}
	if prior.EntryID != first.EntryID {
		t.Fatalf("the earlier turn is %s, want the turn that was actually asked first (%s)",
			prior.EntryID, first.EntryID)
	}
	if prior.Question != "what does this screen mean?" {
		t.Errorf("the earlier question reads %q, want what was asked", prior.Question)
	}
	const whole = "let me look: it says hello, so the config is fine."
	if prior.Prose.Text != whole {
		t.Fatalf("the earlier answer reads\n  %q\nwant the three runs of prose in seat order\n  %q",
			prior.Prose.Text, whole)
	}
	if prior.Prose.Blocks != 3 {
		t.Errorf("the answer was joined from %d blocks, want the three the run wrote", prior.Prose.Blocks)
	}
	if prior.Prose.RunID != first.RunID {
		t.Errorf("the answer names run %d, want the run that wrote it (%d)", prior.Prose.RunID, first.RunID)
	}
	if prior.Prose.Evicted {
		t.Error("the answer says its text was evicted while every byte of it is here")
	}
}

// ── acceptance 4: a turn that made no calls is unchanged by any of this ──

// The case that existed before this epic and must read exactly as it did: one
// question, one answer, one message. It is the paired positive for the whole
// mechanism — if the join only worked for turns that interleaved, the ordinary
// turn would have been the regression nobody looked for.
func TestPriorTurn_ATurnThatMadeNoCallsIsOneWholeAnswer(t *testing.T) {
	led := conversationPane(t)
	first := askIn(t, led, "session-1", "pane-conv")
	saying(t, led, first.EntryID, first.RunID, "the config is fine.")
	finished(t, led, first.RunID, content.RunCompleted, content.TermCompleted)

	second := askIn(t, led, "session-1", "pane-conv")
	prior := priorTo(t, led, "pane-conv", second.EntryID)
	if prior == nil {
		t.Fatal("a turn with one run of prose is not seen at all")
	}
	if prior.Prose.Text != "the config is fine." {
		t.Fatalf("the answer reads %q, want the whole of it", prior.Prose.Text)
	}
	if prior.Prose.Blocks != 1 {
		t.Errorf("the answer was joined from %d blocks, want the one the run wrote", prior.Prose.Blocks)
	}
}

// ── acceptance 2: two attempts do not interleave ─────────────────────────

// TestPriorTurn_TwoAgentRunsDoNotInterleaveTheirProse.
//
// WHICH RUN'S PROSE THE MESSAGE IS, and why. It is the LATEST agent-lane
// execution of the turn — the attempt whose text a person actually read, and
// therefore the one their follow-up question is about. An earlier attempt is
// not merged in (that is the interleaving) and is not reported missing either:
// an attempt that was superseded is not part of the answer that stands.
//
// The fixture builds the shape by hand because no production path makes it:
// SubmitAgentAsk writes attempt 1, and the approval resume drives that SAME
// execution to completion — the resume is a real checkpoint resume, so the
// deltas after an approval continue the prose the question interrupted rather
// than opening a second set beside it (internal/transport, askRunContext).
// `executions` nevertheless permits a second agent-lane row per entry by
// design (ADR-0020 decision 4), and a reader that assumed otherwise would
// splice two attempts the day one arrived — silently, because both sets of
// prose are `text` children of the same turn at ascending seats.
func TestPriorTurn_TwoAgentRunsDoNotInterleaveTheirProse(t *testing.T) {
	led := conversationPane(t)
	ctx := context.Background()
	first := askIn(t, led, "session-1", "pane-conv")

	// Attempt 1 said one thing…
	saying(t, led, first.EntryID, first.RunID, "the first attempt said this. ")

	// …and a second agent-lane execution of the SAME turn said another.
	lane := "agent"
	secondRun, err := led.StartExecution(ctx, content.StartExecution{
		EntryID: first.EntryID, Lane: &lane, Attempt: 2,
	})
	if err != nil {
		t.Fatalf("StartExecution as attempt 2: %v", err)
	}
	saying(t, led, first.EntryID, secondRun, "the second attempt said that.")

	next := askIn(t, led, "session-1", "pane-conv")
	prior := priorTo(t, led, "pane-conv", next.EntryID)
	if prior == nil {
		t.Fatal("a turn with two attempts is not seen at all")
	}
	if prior.Prose.RunID != secondRun || prior.Prose.Attempt != 2 {
		t.Fatalf("the message is run %d / attempt %d, want the LATEST attempt (run %d, attempt 2)",
			prior.Prose.RunID, prior.Prose.Attempt, secondRun)
	}
	if prior.Prose.Text != "the second attempt said that." {
		t.Fatalf("the message reads\n  %q\nwant only the latest attempt's prose — the first attempt's "+
			"sentence spliced in front of it is the defect this test exists for",
			prior.Prose.Text)
	}
	if prior.Prose.Blocks != 1 {
		t.Errorf("the message was joined from %d blocks, want only the latest attempt's one",
			prior.Prose.Blocks)
	}
}

// The paired positive for the same mechanism: with ONE run on the turn, every
// `text` child is that run's prose and all of it assembles. Without this,
// "only the latest attempt's block" above could be passing because the read
// keeps at most one block, and an ordinary three-block answer would come back
// as its last sentence.
func TestPriorTurn_OneRunKeepsEveryBlockItWrote(t *testing.T) {
	led := conversationPane(t)
	first := askIn(t, led, "session-1", "pane-conv")
	saying(t, led, first.EntryID, first.RunID, "one. ")
	saying(t, led, first.EntryID, first.RunID, "two. ")
	saying(t, led, first.EntryID, first.RunID, "three.")

	next := askIn(t, led, "session-1", "pane-conv")
	prior := priorTo(t, led, "pane-conv", next.EntryID)
	if prior == nil || prior.Prose.Text != "one. two. three." || prior.Prose.Blocks != 3 {
		t.Fatalf("a single run's answer read back as %+v, want its three blocks whole", prior)
	}
}

// ── acceptance 3: evicted prose SAYS SO, and is never a hole ─────────────

// Retention takes the prose of one run as a unit (ADR-0040's retention rule),
// and a turn whose prose has gone keeps every block. So the read must report
// the absence rather than answering with an empty string: "there was an answer
// and it is no longer kept" and "this run printed nothing" are different facts,
// and a caller that could not tell them apart would either leave a hole in the
// conversation or invent text to fill it.
//
// The receipt is the sweep's own mark on the body — the one ProseEvicted reads
// (LedgerEntry.ProseEvicted) — narrowed to this run's blocks, so there is one
// stored fact and one reading of it.
func TestPriorTurn_EvictedProseIsReportedGoneNotEmpty(t *testing.T) {
	led := conversationPane(t)
	first := askIn(t, led, "session-1", "pane-conv")
	saying(t, led, first.EntryID, first.RunID, "an answer nobody will read again. ")
	saying(t, led, first.EntryID, first.RunID, "and its second half.")
	finished(t, led, first.RunID, content.RunCompleted, content.TermCompleted)

	next := askIn(t, led, "session-1", "pane-conv")

	// The paired half, FIRST and on the same store: while the bodies are here
	// the read is not saying anything is gone. Without it, Evicted below could
	// be true for any reason at all.
	before := priorTo(t, led, "pane-conv", next.EntryID)
	if before == nil || before.Prose.Evicted || before.Prose.Text == "" {
		t.Fatalf("with the bodies present the read says %+v, want the whole answer and no eviction", before)
	}

	if _, err := led.EvictBodies(context.Background(),
		content.BodyEvictionRequest{KeepBytes: 0, Max: 100}); err != nil {
		t.Fatalf("EvictBodies: %v", err)
	}

	after := priorTo(t, led, "pane-conv", next.EntryID)
	if after == nil {
		t.Fatal("the turn disappeared with its bodies — eviction leaves the entries (ADR-0019 §7)")
	}
	if !after.Prose.Evicted {
		t.Fatal("the turn does not say its prose was evicted — a silent hole is what this must never be")
	}
	if after.Prose.Text != "" {
		t.Fatalf("the read invented %q for an answer whose bytes are gone", after.Prose.Text)
	}
	// And the question survives, which is what makes saying so possible: the
	// conversation still holds what was asked, so the absence has something to
	// be an absence OF.
	if after.Question != "what does this screen mean?" {
		t.Errorf("the earlier question reads %q after eviction, want the question", after.Question)
	}
}

// The other side of the same distinction, and the reason Evicted has to exist
// at all: a run that printed NOTHING reads back with empty text and NOT
// evicted. Nothing was lost, so nothing may be reported lost.
func TestPriorTurn_ARunThatPrintedNothingIsNotAnEvictedOne(t *testing.T) {
	led := conversationPane(t)
	first := askIn(t, led, "session-1", "pane-conv")
	finished(t, led, first.RunID, content.RunFailed, content.TermFailed)

	next := askIn(t, led, "session-1", "pane-conv")
	prior := priorTo(t, led, "pane-conv", next.EntryID)
	if prior == nil {
		t.Fatal("a turn that printed nothing is not seen at all — the question was still asked")
	}
	if prior.Prose.Evicted {
		t.Fatal("a run that never printed a word says its prose was evicted")
	}
	if prior.Prose.Text != "" || prior.Prose.Blocks != 0 {
		t.Fatalf("a run that printed nothing read back as %+v", prior.Prose)
	}
}

// ── the run's state travels with its text (trap 3) ───────────────────────

// Whether a partial answer is a REAL MESSAGE or an UNFINISHED ATTEMPT is a
// fact about the execution, not about the presence of `text` children: a run
// interrupted halfway leaves exactly the rows a run that finished leaves. So
// the state is carried out with the text and the caller is told, rather than
// inferring it from a length it cannot interpret.
func TestPriorTurn_CarriesTheRunsStateSoAPartialAnswerIsTellable(t *testing.T) {
	led := conversationPane(t)
	first := askIn(t, led, "session-1", "pane-conv")
	saying(t, led, first.EntryID, first.RunID, "I was halfway through saying")
	finished(t, led, first.RunID, content.RunFailed, content.TermInterrupted)

	next := askIn(t, led, "session-1", "pane-conv")
	prior := priorTo(t, led, "pane-conv", next.EntryID)
	if prior == nil {
		t.Fatal("an interrupted turn is not seen at all")
	}
	if prior.Prose.State != content.RunFailed {
		t.Fatalf("the run's state reads %q, want the terminal state it actually reached", prior.Prose.State)
	}
	if prior.Prose.Text != "I was halfway through saying" {
		t.Fatalf("the partial answer reads %q, want what was written before the run stopped", prior.Prose.Text)
	}
}

// The pair: a run that COMPLETED says so, on the same read. The two states are
// what a caller decides between, so a test that only saw one of them would not
// notice a read that always reported the same one.
func TestPriorTurn_AFinishedRunSaysItCompleted(t *testing.T) {
	led := conversationPane(t)
	first := askIn(t, led, "session-1", "pane-conv")
	saying(t, led, first.EntryID, first.RunID, "all of it.")
	finished(t, led, first.RunID, content.RunCompleted, content.TermCompleted)

	next := askIn(t, led, "session-1", "pane-conv")
	prior := priorTo(t, led, "pane-conv", next.EntryID)
	if prior == nil || prior.Prose.State != content.RunCompleted {
		t.Fatalf("a finished run reads back as %+v, want state completed", prior)
	}
}

// ── the thread is the pane, and its ends are honest ──────────────────────

// The FIRST turn in a pane has nothing before it, and that is an answer rather
// than a failure. Paired with the second turn, which does see it — otherwise
// "nil" here would be satisfied by a read that never finds anything.
func TestPriorTurn_TheFirstTurnHasNothingBeforeItAndTheSecondSeesIt(t *testing.T) {
	led := conversationPane(t)
	first := askIn(t, led, "session-1", "pane-conv")
	saying(t, led, first.EntryID, first.RunID, "an answer.")

	if prior := priorTo(t, led, "pane-conv", first.EntryID); prior != nil {
		t.Fatalf("the first turn in a pane sees %+v before it, want nothing", prior)
	}
	second := askIn(t, led, "session-1", "pane-conv")
	if prior := priorTo(t, led, "pane-conv", second.EntryID); prior == nil || prior.EntryID != first.EntryID {
		t.Fatalf("the second turn sees %+v before it, want the first turn (%s)", prior, first.EntryID)
	}
}

// Another tab's conversation is not this one's. The pane is the thread, and a
// read that ignored it would put a question the person asked somewhere else
// into this answer's context. Paired with the same turn read from its OWN
// pane, so "nil" is not simply what this read always says.
func TestPriorTurn_AnotherPanesTurnIsNotThisPanesHistory(t *testing.T) {
	db, led := newLedger(t)
	aPaneUnder(t, db, "ws-a", "tab-a", "pane-a")
	aPaneUnder(t, db, "ws-b", "tab-b", "pane-b")
	envReady(t, led, "local")

	elsewhere := askIn(t, led, "session-a", "pane-a")
	saying(t, led, elsewhere.EntryID, elsewhere.RunID, "an answer in another tab.")
	here := askIn(t, led, "session-b", "pane-b")

	if prior := priorTo(t, led, "pane-b", here.EntryID); prior != nil {
		t.Fatalf("this pane's first turn sees %+v before it — that turn is in another tab", prior)
	}
	// The pair: read from the pane it really is in, the same turn IS history.
	later := askIn(t, led, "session-a", "pane-a")
	if prior := priorTo(t, led, "pane-a", later.EntryID); prior == nil || prior.EntryID != elsewhere.EntryID {
		t.Fatalf("in its own pane the earlier turn reads back as %+v, want %s", prior, elsewhere.EntryID)
	}
}

// A session that is the pipe of no recorded pane has no thread to read, and
// the read says so rather than answering from every pane at once. Paired with
// the anchored read on the same store.
func TestPriorTurn_NoPaneIsNoThreadButAnAnchoredOneReads(t *testing.T) {
	led := conversationPane(t)
	first := askIn(t, led, "session-1", "pane-conv")
	saying(t, led, first.EntryID, first.RunID, "an answer.")
	second := askIn(t, led, "session-1", "pane-conv")

	if prior := priorTo(t, led, "", second.EntryID); prior != nil {
		t.Fatalf("a turn with no pane sees %+v before it, want nothing", prior)
	}
	if prior := priorTo(t, led, "pane-conv", second.EntryID); prior == nil {
		t.Fatal("the same turn read from its pane sees nothing before it")
	}
}

// A cursor no row carries is REFUSED, never answered with the newest turn in
// the pane — which would be some other turn's answer presented as this one's
// context. Paired with the cursor that does name a row.
func TestPriorTurn_RefusesACursorNoRowCarriesAndAcceptsOneThatDoes(t *testing.T) {
	led := conversationPane(t)
	first := askIn(t, led, "session-1", "pane-conv")
	saying(t, led, first.EntryID, first.RunID, "an answer.")
	second := askIn(t, led, "session-1", "pane-conv")

	if _, err := led.PriorTurn(context.Background(), "pane-conv", "no-such-entry"); err == nil {
		t.Fatal("a cursor naming no row was answered instead of refused")
	}
	if prior := priorTo(t, led, "pane-conv", second.EntryID); prior == nil {
		t.Fatal("a cursor that does name a row found nothing before it")
	}
}

// A shell block between two turns is not a turn: the conversation is made of
// what was ASKED and ANSWERED, and a command the person ran in the same pane
// has no question and no run. Paired with the turn that IS found past it.
func TestPriorTurn_ACommandInThePaneIsNotAnEarlierTurn(t *testing.T) {
	led := conversationPane(t)
	first := askIn(t, led, "session-1", "pane-conv")
	saying(t, led, first.EntryID, first.RunID, "an answer.")

	paneID := "pane-conv"
	if _, err := led.Submit(context.Background(), content.SubmitEntry{
		ID: "00000000-0000-7000-8000-0000000000c1", Client: "test-client",
		EnvironmentID: "local", PaneID: &paneID, Cwd: "/repo",
		Kind: content.EntryShell, Intent: "ls -la",
	}); err != nil {
		t.Fatalf("Submit a command in the same pane: %v", err)
	}

	second := askIn(t, led, "session-1", "pane-conv")
	prior := priorTo(t, led, "pane-conv", second.EntryID)
	if prior == nil {
		t.Fatal("the command hid the turn behind it — a conversation is made of turns")
	}
	if prior.EntryID != first.EntryID {
		t.Fatalf("the earlier turn is %s, want the ask (%s) and not the command", prior.EntryID, first.EntryID)
	}
}

// ── the block records the run that printed it ────────────────────────────

// OpenProse refuses a run that is not this turn's: prose seated here and
// attributed there would be assembled into another turn's message, which is
// the one way recording the run could make a reader worse off than not
// recording it. Paired with the run that IS this turn's.
func TestOpenProse_RefusesARunThatIsNotThisTurnsAndAcceptsOneThatIs(t *testing.T) {
	led := conversationPane(t)
	ctx := context.Background()
	mine := askIn(t, led, "session-1", "pane-conv")
	other := askIn(t, led, "session-1", "pane-conv")

	if _, err := led.OpenProse(ctx, mine.EntryID, other.RunID); err == nil {
		t.Fatal("prose opened under one turn was attributed to another turn's run")
	}
	if _, err := led.OpenProse(ctx, mine.EntryID, 0); err == nil {
		t.Fatal("prose opened with no run at all — prose belongs to the run that printed it")
	}
	// The paired success on the same store, so the refusals above are not a
	// method that refuses everything.
	if _, err := led.OpenProse(ctx, mine.EntryID, mine.RunID); err != nil {
		t.Fatalf("OpenProse with the turn's own run: %v", err)
	}
}
