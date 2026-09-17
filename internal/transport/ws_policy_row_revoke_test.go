package transport

// Moving one of the seven effect rows says what happens to the work already
// running (nocx-4yjwk.8), over the real socket, against real runs whose
// authorities were really minted from the store.
//
// It is the same feature nocx-r4fh8 built for a rule, asked of the other kind
// of answer, so these tests deliberately use ws_policy_revoke_test.go's
// harness and its held runs: a second harness would be a second account of
// what "a run in flight" is.
//
// The bystander in these tests is the interesting part. Every live run mints
// from one global document, so a row write reaches all of them unless their
// authorities have already come apart — which is exactly what the approval
// prompt's own row write does (GlobalPolicyStore.SetRowDecision, the seam
// "always allow" lands on). A run started after that answer holds it, a run
// started before does not, and only the second is still deciding under the old
// row.

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// askMatrix is the seven rows a settings page sends: every row stated, so a
// row moved back to its default is moved back in the document too.
func askMatrix() map[string]any {
	m := map[string]any{}
	for _, e := range []string{
		"observe", "mutate-reversible", "mutate-destructive", "privilege-change",
		"disclose", "cross-boundary", "delegate",
	} {
		m[e] = map[string]any{"decision": "ask", "scopes": []any{}}
	}
	return m
}

// withRow is one row of that matrix moved off its default — the gesture the
// page makes when a person answers a row.
func withRow(effect, decision string, scopes ...any) map[string]any {
	m := askMatrix()
	if scopes == nil {
		scopes = []any{}
	}
	m[effect] = map[string]any{"decision": decision, "scopes": scopes}
	return m
}

// setPolicyRuns drives policy.set with a stated timing.
func setPolicyRuns(t *testing.T, h *revokeHarness, matrix map[string]any, runs string) policySetResult {
	t.Helper()
	params := map[string]any{"policy": matrix}
	if runs != "" {
		params["runs"] = runs
	}
	raw := jsonrpcCall(t, h.conn, "policy.set", params)
	var env struct {
		Result policySetResult  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.set %s: %v", raw, err)
	}
	if env.Error != nil {
		t.Fatalf("policy.set: %+v", env.Error)
	}
	return env.Result
}

// ── criterion 1: it says how many, in the words rules already use ─────────

// A row moved off its answer while runs are in flight writes NOTHING and
// answers with how many runs would go on deciding under the old row — and it
// counts the runs whose authority still states it, not the registry. The
// bystander was minted after the same answer had already landed through the
// approval prompt's seam, so the write moves nothing it decides.
func TestPolicySet_SaysHowManyLiveRunsAreStillDecidingUnderTheOldRow(t *testing.T) {
	h := newRevokeHarness(t)

	stillAsking := h.startHeldRun("still-asking")
	// The approval prompt's "always allow" for this row, through the store's
	// own one-row seam — no run is consulted and none is reached.
	if err := h.store.SetRowDecision(content.EffectObserve, content.DecisionPermit); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	bystander := h.startHeldRun("bystander")

	got := setPolicyRuns(t, h, withRow("observe", "permit"), "")
	if got.Applied {
		t.Fatalf("set = %+v, want applied=false: nothing may change until the person has answered", got)
	}
	if got.AffectedRuns != 1 {
		t.Fatalf("affectedRuns = %d, want 1 (run %d, not %d)",
			got.AffectedRuns, stillAsking.RunID, bystander.RunID)
	}
	if got.StoppedRuns != 0 || got.FinishedBeforeStop != 0 {
		t.Fatalf("set = %+v, want nothing stopped", got)
	}
	for _, run := range []int64{stillAsking.RunID, bystander.RunID} {
		if !h.isLive(run) {
			t.Fatalf("run %d ended while the question was being asked", run)
		}
	}
}

// The two answers are the SAME two words the rule writes take. A matrix write
// that spelled its timing differently would be a second vocabulary for one
// question, so the enum is asserted here through the method that refuses
// anything outside it.
func TestPolicySet_TakesTheSameTimingWordsARuleWriteDoes(t *testing.T) {
	h := newRevokeHarness(t)
	h.startHeldRun("in-flight")

	for _, runs := range []string{string(runsAsk), string(runsFuture), string(runsStop)} {
		if msg := validatePolicyRunsMode(policyRunsMode(runs)); msg != "" {
			t.Fatalf("policy.set refused the timing %q a rule write takes: %s", runs, msg)
		}
	}
	raw := jsonrpcCall(t, h.conn, "policy.set", map[string]any{
		"policy": askMatrix(), "runs": "stopp",
	})
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.set %s: %v", raw, err)
	}
	if env.Error == nil || env.Error.Code != -32602 {
		t.Fatalf("policy.set with a misspelled timing = %+v, want invalid params — "+
			"a timing that quietly became \"ask\" would leave runs alive after somebody asked to stop them", env.Error)
	}
}

// ── criterion 2: a row change affecting no live run asks nothing ──────────

// The question must not become noise. A matrix that says what every live run's
// authority already says writes straight through and reports zero, however
// many runs are in flight.
func TestPolicySet_AsksNothingWhenNoLiveRunDecidesDifferently(t *testing.T) {
	h := newRevokeHarness(t)
	h.startHeldRun("one")
	h.startHeldRun("two")

	got := setPolicyRuns(t, h, askMatrix(), "")
	if !got.Applied {
		t.Fatalf("set = %+v, want it applied with no question", got)
	}
	if got.AffectedRuns != 0 {
		t.Fatalf("affectedRuns = %d, want 0 — the count is about the answer, never about how many runs exist",
			got.AffectedRuns)
	}
}

// A change the MINT erases reaches no run, and the count knows it. An operator
// session selector on a row is dropped before a run's authority is minted
// (preserveRunSessionScope: the run owns exactly one session scope and a
// policy may not erase it), so writing one is a real edit to the document that
// no live run can decide differently under.
//
// This is the case a naive comparison of the STORED rows would get wrong, and
// it is why the count re-mints instead.
func TestPolicySet_AChangeTheMintErasesReachesNoRun(t *testing.T) {
	h := newRevokeHarness(t)
	h.startHeldRun("in-flight")

	matrix := withRow("observe", "ask", map[string]any{"kind": "session", "id": "some-other-session"})
	got := setPolicyRuns(t, h, matrix, "")
	if !got.Applied || got.AffectedRuns != 0 {
		t.Fatalf("set = %+v, want applied with affectedRuns=0 — the mint drops an operator session selector, "+
			"so no run's authority moves", got)
	}
	if scopes := h.store.Policy().Observe.Scopes; len(scopes) != 1 {
		t.Fatalf("stored observe scopes = %+v, want the write to have landed in the document", scopes)
	}
}

// ── criterion 3: future-only leaves the running work alone, both ends ─────

// The interval, stated at BOTH ends. From the write until the run in flight
// reaches its own terminal state it goes on deciding under the row it was
// minted with — and it FINISHES that way, completed rather than cancelled.
// From the same moment, every run started afterwards is minted with the new
// row.
func TestPolicySet_FutureOnlyLeavesTheRunningWorkAloneAtBothEnds(t *testing.T) {
	h := newRevokeHarness(t)
	inFlight := h.startHeldRun("in-flight")

	got := setPolicyRuns(t, h, withRow("observe", "refuse"), "future")
	if !got.Applied || got.AffectedRuns != 1 {
		t.Fatalf("set future = %+v, want applied and 1 affected", got)
	}

	// END ONE: the run in flight is still running, and still under the old
	// row. Asserted BEFORE the counts, so a future-only path that stopped
	// runs anyway is caught by the run being gone rather than by a number.
	if !h.isLive(inFlight.RunID) {
		t.Fatalf("run %d was stopped by a write that said it would leave it alone", inFlight.RunID)
	}
	if got.StoppedRuns != 0 || got.FinishedBeforeStop != 0 {
		t.Fatalf("set future = %+v, want nothing stopped", got)
	}
	grant := h.grantOf(inFlight.RunID)
	if grant == nil {
		t.Fatal("the run in flight carries no grant, so there is nothing to keep")
	}
	if d := grant.Policy.DecisionFor(content.EffectObserve); d != content.DecisionAsk {
		t.Fatalf("the run in flight decides observe = %s, want ask — its authority is fixed for the run", d)
	}

	// END TWO: the next run is minted with the new row.
	next := h.startHeldRun("next")
	nextGrant := h.grantOf(next.RunID)
	if nextGrant == nil {
		t.Fatal("the next run carries no grant")
	}
	if d := nextGrant.Policy.DecisionFor(content.EffectObserve); d != content.DecisionRefuse {
		t.Fatalf("the next run decides observe = %s, want refuse — the row was moved", d)
	}

	// AND THE INTERVAL CLOSES WHERE IT SHOULD: the run in flight reaches its
	// own ending, not one the write gave it.
	h.client.releaseAll()
	waitForRunTermination(t, h, inFlight.EntryID, content.RunCompleted, content.TermCompleted)
}

// ── criterion 4: stopping ends those runs, through the one path ───────────

// Choosing to stop terminalizes exactly the runs still deciding under the old
// row, through the path agent.cancel takes and with the reason a revoked
// answer has (TermAnswerRevoked, nocx-4yjwk.7) rather than the one a person
// pressing Stop leaves. The sentence names the row that moved, and a run whose
// authority already carried the new row survives.
func TestPolicySet_StopEndsTheRunsUnderTheOldRowAndSparesTheOthers(t *testing.T) {
	h := newRevokeHarness(t)

	stillAsking := h.startHeldRun("still-asking")
	if err := h.store.SetRowDecision(content.EffectObserve, content.DecisionRefuse); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	bystander := h.startHeldRun("bystander")

	got := setPolicyRuns(t, h, withRow("observe", "refuse"), "stop")
	if !got.Applied {
		t.Fatalf("set stop = %+v, want the write applied", got)
	}
	if got.AffectedRuns != 1 || got.StoppedRuns != 1 || got.FinishedBeforeStop != 0 {
		t.Fatalf("set stop = %+v, want 1 affected, 1 stopped, 0 already finished", got)
	}

	sentence := waitForRunTermination(t, h, stillAsking.EntryID, content.RunCancelled, content.TermAnswerRevoked)
	if !containsAll(sentence, "changed", "observe", "refuse") {
		t.Fatalf("the stopped run's sentence = %q, want it to name the answer that was changed", sentence)
	}
	if !h.isLive(bystander.RunID) {
		t.Fatalf("run %d was stopped, and its authority already said what the write says", bystander.RunID)
	}
}

// ── the document the count was taken against is the document stored ───────

// The count is worth nothing unless it was taken over the document the store
// ends up holding. The handler builds that document with WithRowWrite for the
// question and hands SetPolicy the rows-only matrix; this asserts the two
// agree, rules included — the merge must never be what gets written, because a
// document read a microsecond earlier is how standing answers were deleted
// once already (nocx-39bly).
func TestPolicySet_TheDocumentCountedAgainstIsTheDocumentStored(t *testing.T) {
	h := newRevokeHarness(t)
	rule := h.seedPermit("df", "-h")

	before := h.store.Policy()
	matrix := withRow("disclose", "refuse")
	if got := setPolicyRuns(t, h, matrix, ""); !got.Applied {
		t.Fatalf("set = %+v, want applied", got)
	}

	stored := h.store.Policy()
	counted := before.WithRowWrite(parseMatrix(t, matrix))
	if stored.ChangedByRowWrite(counted) {
		t.Fatal("the rows the count was taken over are not the rows the store holds")
	}
	if len(stored.Rules) != 1 || stored.Rules[0].ID != rule.ID {
		t.Fatalf("rules = %+v, want the standing answer kept by the store's own merge", stored.Rules)
	}
}

// parseMatrix reads a matrix the way the handler does, through the one strict
// gate — so the test compares against what the wire actually meant.
func parseMatrix(t *testing.T, matrix map[string]any) content.EffectPolicy {
	t.Helper()
	raw, err := json.Marshal(matrix)
	if err != nil {
		t.Fatalf("marshal matrix: %v", err)
	}
	p, err := content.ParseEffectPolicy(raw)
	if err != nil {
		t.Fatalf("parse matrix: %v", err)
	}
	return p
}

// ── criterion 5: the wire carries the timing ──────────────────────────────

// The three branches a matrix write can answer with, off the REAL socket,
// against the declared shape: the question (nothing applied, a count), the
// future-only apply, and the stop with what it actually stopped.
func TestPolicySetRuns_OverTheWireConformsToContract(t *testing.T) {
	set := loadSchema(t, "policy.set.schema.json")

	h := newRevokeHarness(t)
	h.startHeldRun("using")

	// The question: a live run is deciding under the old row, so nothing was
	// written.
	raw := jsonrpcCall(t, h.conn, "policy.set", map[string]any{"policy": withRow("observe", "permit")})
	validateJSON(t, set, resultOf(t, raw), "policy.set asking (real socket)")

	// Future-only: applied, nothing stopped.
	raw = jsonrpcCall(t, h.conn, "policy.set",
		map[string]any{"policy": withRow("observe", "permit"), "runs": "future"})
	validateJSON(t, set, resultOf(t, raw), "policy.set future (real socket)")

	// The stop: applied, and what it actually ended.
	raw = jsonrpcCall(t, h.conn, "policy.set",
		map[string]any{"policy": withRow("observe", "refuse"), "runs": "stop"})
	validateJSON(t, set, resultOf(t, raw), "policy.set stopping (real socket)")
}

// ── the failure path: several stores, and what the person was told ────────

// A MATRIX WRITE THAT CANNOT BE PERSISTED STOPS NOTHING, and this is the
// interval's other end.
//
// The gesture touches two things — the policy document and the run registry —
// and settleRuns states the order once: the write first, the stopping second.
// This is the partial failure that order exists for. The store refuses, the
// method answers the error, and every run that was going to be stopped is
// still running: the person is told less than they asked for, which is
// recoverable, rather than being told "stopped 2" while holding a permission
// they believe they removed.
//
// The other partial failure of this procedure is the terminal LEDGER write
// failing after a run has been cancelled, and it is deliberately not asserted
// here: that degrade belongs to agent.cancel, is shared unchanged, and is
// repaired by the startup sweep marking the run interrupted
// (StopRunsForRevokedAnswer says so where it happens). Asserting it here would
// be a second account of somebody else's failure path.
func TestPolicySet_AStoreThatRefusesTheWriteStopsNothing(t *testing.T) {
	client := newHeldAskClient()
	ah := newAskHarnessWithOpts(t, client,
		WithAgentPolicy(failingPolicyStore{err: errors.New("the policy document could not be written")}),
		WithLiveEffects(agenttools.LiveEffects()),
	)
	t.Cleanup(client.releaseAll)
	ah.createEndpoint()
	// A revokeHarness around a store that refuses every write. It carries no
	// *GlobalPolicyStore because there is nothing to seed and nothing to read
	// back — the assertion is entirely about what did NOT happen.
	h := &revokeHarness{askHarness: ah, client: client, sid: openLocalSession(t, ah.conn), nextID: 300}

	inFlight := h.startHeldRun("in-flight")

	raw := jsonrpcCall(t, h.conn, "policy.set", map[string]any{
		"policy": withRow("observe", "permit"), "runs": "stop",
	})
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.set %s: %v", raw, err)
	}
	if env.Error == nil {
		t.Fatal("policy.set answered success while the store had refused the write")
	}
	if !h.isLive(inFlight.RunID) {
		t.Fatalf("run %d was stopped for a change that never landed", inFlight.RunID)
	}
}
