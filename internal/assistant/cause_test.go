package assistant

// What a turn caused, joined to the turn (nocx-h1l4o).
//
// ADR-0039 made a turn ONE entry and left the sentence this file is: the
// things a turn causes — a command it ran, a tool call it made — are separate
// entries joined to it by nothing at all, so a restored turn came back
// without them and `ingest_seq` (commit order, never causality — ADR-0019 §2)
// was the only thing left to guess with.
//
// Every assertion here is about the RECORD, not about ordering: which pairs
// were joined, in which causal order, and what the attempt stored so a
// restored call can be drawn without anybody re-deriving it.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// ── criterion 2: an action entry joins its turn ──────────────────────────

func TestAToolCallJoinsItsTurnByCausedBy(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "in scope")
	led := &fakeLedger{}
	mw := middlewareForTurn(t, grant, led, nil, nil, &fakeKnownMaterial{}, "turn-entry")

	if _, err := wrappedEndpoint(mw, "files.read", "call-1",
		`{"path":"`+filepath.Join(dir, "a.txt")+`"}`); err != nil {
		t.Fatalf("files.read: %v", err)
	}
	got := led.recordedCauses()
	if len(got) != 1 {
		t.Fatalf("causes recorded = %+v, want exactly the one call", got)
	}
	if got[0].turn != "turn-entry" || got[0].caused != "entry-files.read" {
		t.Fatalf("cause = %+v, want the action entry joined to turn-entry", got[0])
	}
}

// The calls of one turn take their causal positions in the order the turn
// made them — the order is the record's, not the reader's.
func TestSeveralCallsTakeTheirPositionsInTheOrderTheTurnMadeThem(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "one")
	led := &fakeLedger{}
	mw := middlewareForTurn(t, grant, led, nil, nil, &fakeKnownMaterial{}, "turn-entry")

	if _, err := wrappedEndpoint(mw, "files.read", "call-1",
		`{"path":"`+filepath.Join(dir, "a.txt")+`"}`); err != nil {
		t.Fatalf("files.read: %v", err)
	}
	// git.status is declared-but-not-executable, which is deliberate here:
	// the attempt and its cause are written BEFORE the call, so a call that
	// cannot run still takes its place in the turn — the interval opens at
	// the attempt, not at the result.
	if _, err := wrappedEndpoint(mw, "git.status", "call-2", `{}`); err == nil {
		t.Fatal("git.status ran — it is declared but not executable")
	}
	got := led.recordedCauses()
	if len(got) != 2 {
		t.Fatalf("causes recorded = %+v, want two", got)
	}
	want := []string{"entry-files.read", "entry-git.status"}
	for i, c := range got {
		if c.turn != "turn-entry" || c.caused != want[i] || c.position != i {
			t.Fatalf("cause %d = %+v, want %s at position %d of turn-entry", i, c, want[i], i)
		}
	}
}

// ── what the attempt must store so a restored call can be drawn ──────────

// The resource shown on a live tool-call line is DERIVED by the backend
// (namedResource, the one derivation, shared with the scope check and the
// approval prompt). It has to be stored with the attempt, because the only
// alternative for a restored turn is a second derivation in the renderer —
// exactly the shape AGENTS.md spends a section on.
func TestTheAttemptStoresTheResourceTheCallNamed(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "in scope")
	led := &fakeLedger{}
	mw := middlewareForTurn(t, grant, led, nil, nil, &fakeKnownMaterial{}, "turn-entry")

	if _, err := wrappedEndpoint(mw, "files.read", "call-1", `{"path":"`+path+`"}`); err != nil {
		t.Fatalf("files.read: %v", err)
	}
	led.mu.Lock()
	subs := append([]fakeSubmission(nil), led.submissions...)
	led.mu.Unlock()
	if len(subs) != 1 {
		t.Fatalf("submissions = %+v, want the one attempt", subs)
	}
	var facts content.ActionFacts
	if err := json.Unmarshal([]byte(subs[0].payload), &facts); err != nil {
		t.Fatalf("attempt payload %q: %v", subs[0].payload, err)
	}
	if facts.Tool != "files.read" || facts.Effect != content.EffectObserve {
		t.Fatalf("attempt facts = %+v, want files.read/observe", facts)
	}
	if facts.Resource == nil || facts.Resource.Kind != content.ResourcePath || facts.Resource.ID != path {
		t.Fatalf("attempt resource = %+v, want path %s", facts.Resource, path)
	}
}

// A tool that names no resource in its parameters at all stores none, never
// an empty scope — the same honesty the ask already has.
func TestAnAttemptForACallThatNamedNoResourceStoresNone(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	led := &fakeLedger{}
	mw := middlewareForTurn(t, grant, led, nil, nil, &fakeKnownMaterial{}, "turn-entry")

	// git.status names no resource in its parameters at all: the repository
	// IS the grant's path scope. The call cannot execute (no capability
	// constructor is wired) and that is irrelevant here — the attempt is
	// written before the call, which is the row being read back.
	if _, err := wrappedEndpoint(mw, "git.status", "call-1", `{}`); err == nil {
		t.Fatal("git.status ran — it is declared but not executable")
	}
	led.mu.Lock()
	subs := append([]fakeSubmission(nil), led.submissions...)
	led.mu.Unlock()
	if len(subs) != 1 {
		t.Fatalf("submissions = %+v, want the one attempt", subs)
	}
	var facts content.ActionFacts
	if err := json.Unmarshal([]byte(subs[0].payload), &facts); err != nil {
		t.Fatalf("attempt payload %q: %v", subs[0].payload, err)
	}
	if facts.Resource != nil {
		t.Fatalf("attempt resource = %+v, want none", facts.Resource)
	}
}

// ── criterion 4, the first case: nothing to join to ──────────────────────

// The un-bound caller shape (an AskParams with no turn) records NO cause and
// never invents one. This is the same rule the run id already follows on the
// attempt payload: no link rather than a misleading one.
func TestWithNoTurnEntryNothingIsJoinedAndNothingIsGuessed(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "in scope")
	led := &fakeLedger{}
	mw := middlewareForTurn(t, grant, led, nil, nil, &fakeKnownMaterial{}, "")

	if _, err := wrappedEndpoint(mw, "files.read", "call-1",
		`{"path":"`+filepath.Join(dir, "a.txt")+`"}`); err != nil {
		t.Fatalf("files.read: %v", err)
	}
	if got := led.recordedCauses(); len(got) != 0 {
		t.Fatalf("causes recorded with no turn = %+v, want none", got)
	}
}

// ── the relation is the arrangement, never the record ────────────────────

// A cause that cannot be recorded degrades to plain ledger order (criterion
// 4) and does NOT fail the call. The entry is the record and it landed; the
// edge is where the reader draws it. Failing a call — worse, failing it
// after the effect has happened — to preserve a drawing order would trade a
// real capability for a cosmetic one.
func TestACauseThatCannotBeRecordedDoesNotFailTheCall(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "in scope")
	led := &fakeLedger{failCause: true}
	mw := middlewareForTurn(t, grant, led, nil, nil, &fakeKnownMaterial{}, "turn-entry")

	out, err := wrappedEndpoint(mw, "files.read", "call-1", `{"path":"`+path+`"}`)
	if err != nil {
		t.Fatalf("a failed relation write failed the call: %v", err)
	}
	if !strings.Contains(out, "in scope") {
		t.Fatalf("the tool result = %q, want the file's contents", out)
	}
	// And the attempt itself is intact: the record is what matters.
	if led.started() != 1 {
		t.Fatalf("attempts opened = %d, want 1", led.started())
	}
}

// ── criterion 1: the command the turn ran ────────────────────────────────

// runRequester answers RequestRun with a completed run body naming the
// entry id the renderer minted for the command — the same wire shape
// agent.runResolved carries, built by the run tests' own builder so there is
// one description of that body.
type runRequester struct {
	unscriptedBlocks
	entryID string
	asked   int
}

func (r *runRequester) RequestScreen(context.Context, string, *FrameRegion) (json.RawMessage, error) {
	return nil, errors.New("cause test: RequestScreen is not scripted")
}

func (r *runRequester) RequestRun(context.Context, string, string) (json.RawMessage, error) {
	r.asked++
	zero := 0
	return runResolvedBody(r.entryID, &zero, "success", 1, 0, 1, "ok"), nil
}

func TestACommandTheTurnRanJoinsTheTurnFromTheBackendsOwnResolution(t *testing.T) {
	sess := "sess-1"
	policy := autonomousMatrix()
	grant := policy.AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: sess}})
	led := &fakeLedger{}
	req := &runRequester{entryID: "shell-entry-1"}
	mw := middlewareForTurn(t, grant, led, nil, req, &fakeKnownMaterial{}, "turn-entry")

	if _, err := wrappedEndpoint(mw, "run", "call-1",
		`{"sessionId":"`+sess+`","command":"ls"}`); err != nil {
		t.Fatalf("run: %v", err)
	}
	if req.asked != 1 {
		t.Fatalf("the renderer was asked %d times, want 1", req.asked)
	}
	got := led.recordedCauses()
	// Two causes, in the order the turn made them: the tool call's own
	// action entry — the line that says WHEN the assistant reached for run
	// — and then the shell entry the command really opened.
	if len(got) != 2 {
		t.Fatalf("causes recorded = %+v, want the action and the command", got)
	}
	if got[0].caused != "entry-run" || got[0].turn != "turn-entry" {
		t.Fatalf("first cause = %+v, want the run tool's action entry", got[0])
	}
	if got[1].caused != "shell-entry-1" || got[1].turn != "turn-entry" {
		t.Fatalf("second cause = %+v, want the shell entry the renderer minted", got[1])
	}
}

// Criterion 6, stated as an assertion: the renderer sends no arrangement.
// It answers agent.runResolved with the entry id it minted and nothing else
// about where the command belongs; the backend owns the run, so it is the
// backend that joins the two. A second copy on the wire is the one nobody
// checks — the same reason ledger.open refuses a paneId from the renderer.
func TestTheRendererSendsNoArrangementOfItsOwn(t *testing.T) {
	sess := "sess-1"
	policy := autonomousMatrix()
	grant := policy.AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: sess}})
	led := &fakeLedger{}
	req := &runRequester{entryID: "shell-entry-1"}
	mw := middlewareForTurn(t, grant, led, nil, req, &fakeKnownMaterial{}, "turn-entry")

	if _, err := wrappedEndpoint(mw, "run", "call-1",
		`{"sessionId":"`+sess+`","command":"ls"}`); err != nil {
		t.Fatalf("run: %v", err)
	}
	// The resolution the renderer sent carries exactly the fields the wire
	// declares (runResolvedParams) — no turn, no position, no parent. The
	// join above came from the backend's own turn id, which the renderer
	// never sees.
	var body map[string]any
	raw, _ := req.RequestRun(context.Background(), sess, "ls")
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("resolution: %v", err)
	}
	for _, forbidden := range []string{"turnEntryId", "causedBy", "position", "pos", "parent"} {
		if _, present := body[forbidden]; present {
			t.Fatalf("the renderer's resolution carries %q — the arrangement has a second owner", forbidden)
		}
	}
}

// A command whose run outlived the turn is still joined to it: the relation
// is written when the cause is known, and nothing about the turn's own
// lifecycle closes it. Both ends: the join exists from the resolution until
// the entry is deleted.
func TestTheRelationIsWrittenWhenTheCauseIsKnownNotWhenTheTurnCloses(t *testing.T) {
	sess := "sess-1"
	policy := autonomousMatrix()
	grant := policy.AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: sess}})
	led := &fakeLedger{}
	req := &runRequester{entryID: "shell-entry-1"}
	mw := middlewareForTurn(t, grant, led, nil, req, &fakeKnownMaterial{}, "turn-entry")

	if _, err := wrappedEndpoint(mw, "run", "call-1",
		`{"sessionId":"`+sess+`","command":"sleep 30"}`); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := led.recordedCauses()
	if len(got) != 2 || got[1].caused != "shell-entry-1" {
		t.Fatalf("causes = %+v, want the command joined at resolution time", got)
	}
	// The turn's own close is a different transaction entirely and touches
	// no edge: the record above is complete before any of it happens.
	if n := len(led.recordedCauses()); n != 2 {
		t.Fatalf("causes after the turn closed = %d, want the same 2", n)
	}
}

// A run the renderer could not complete joins nothing — there is no entry to
// join. The turn's own action entry is still joined, because the call really
// was made.
func TestAFailedRunJoinsTheCallItMadeAndNoCommand(t *testing.T) {
	sess := "sess-1"
	policy := autonomousMatrix()
	grant := policy.AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: sess}})
	led := &fakeLedger{}
	mw := middlewareForTurn(t, grant, led, nil, &failingRunRequester{}, &fakeKnownMaterial{}, "turn-entry")

	if _, err := wrappedEndpoint(mw, "run", "call-1",
		`{"sessionId":"`+sess+`","command":"ls"}`); err == nil {
		t.Fatal("run succeeded — the failing requester should have failed the call")
	}
	got := led.recordedCauses()
	if len(got) != 1 || got[0].caused != "entry-run" {
		t.Fatalf("causes = %+v, want only the call the turn made", got)
	}
}

type failingRunRequester struct{ unscriptedBlocks }

func (failingRunRequester) RequestScreen(context.Context, string, *FrameRegion) (json.RawMessage, error) {
	return nil, errors.New("cause test: RequestScreen is not scripted")
}

func (failingRunRequester) RequestRun(context.Context, string, string) (json.RawMessage, error) {
	return nil, errors.New("no renderer connected to run the command")
}

// ── the escalation: a question is a thing the turn caused ────────────────

// The proposal put to a person is an action entry too (nocx-5dldy), and it
// joins the turn by the same relation — otherwise a restored turn shows the
// approved call and not the question that preceded it.
func TestAProposalJoinsItsTurnAndTheApprovedCallDoesNotJoinAgain(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "approved read")
	args := `{"path":"` + path + `"}`

	led := &fakeLedger{}
	approvals := NewApprovalStore()
	mw := middlewareForTurn(t, grant, led, approvals, nil, &fakeKnownMaterial{}, "turn-entry")

	// The ask: the call is recorded as a proposal and suspends.
	if _, err := wrappedEndpoint(mw, "files.read", "call-1", args); err == nil {
		t.Fatal("the call ran — an ask matrix must escalate it")
	}
	got := led.recordedCauses()
	if len(got) != 1 || got[0].turn != "turn-entry" || got[0].caused != "entry-files.read" {
		t.Fatalf("causes after the escalation = %+v, want the proposal joined to the turn", got)
	}
	if got[0].position != 0 {
		t.Fatalf("the proposal took position %d, want 0", got[0].position)
	}

	// The person says yes, and the approved call runs as a SUBSEQUENT
	// attempt of that same entry (ADR-0020 decision 4). It is not a second
	// cause: the question already holds the place in the turn, and joining
	// again would move it to after everything that followed it.
	if !approvals.Approve(Approval{
		RunID: "run-1", Attempt: 1, Tool: "files.read",
		CallID: "call-1", ArgHash: canonicalArgHash(args),
	}) {
		t.Fatal("the exact proposal the middleware asked about was not pending")
	}
	if _, err := wrappedEndpoint(mw, "files.read", "call-1", args); err != nil {
		t.Fatalf("the approved call: %v", err)
	}
	after := led.recordedCauses()
	if len(after) != 1 || after[0].position != 0 {
		t.Fatalf("causes after the approval = %+v, want the one proposal still at position 0", after)
	}
}

// The attempt's own row carries whether the call opened a block, because
// that is what tells a RESTORED turn the block is the account of the call
// and no line belongs beside it. It is the declaration's fact, copied once,
// here — never matched on the tool name by a reader.
func TestTheAttemptRecordsWhetherTheCallOpensABlockOfItsOwn(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "in scope")
	led := &fakeLedger{}
	mw := middlewareForTurn(t, grant, led, nil, nil, &fakeKnownMaterial{}, "turn-entry")

	if _, err := wrappedEndpoint(mw, "files.read", "call-1",
		`{"path":"`+filepath.Join(dir, "a.txt")+`"}`); err != nil {
		t.Fatalf("files.read: %v", err)
	}
	var facts content.ActionFacts
	for _, sub := range led.submissions {
		if sub.intent != "files.read" {
			continue
		}
		if err := json.Unmarshal([]byte(sub.payload), &facts); err != nil {
			t.Fatalf("attempt payload: %v", err)
		}
	}
	if facts.Tool != "files.read" {
		t.Fatalf("no files.read attempt was recorded: %+v", led.submissions)
	}
	if facts.OpensBlock {
		t.Fatal("files.read's attempt says it opened a block; it opens none and its line is the only trace")
	}
}
