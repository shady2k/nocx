package transport

// Revoking an answer says what happens to the work already running
// (nocx-r4fh8), over the real socket, against real runs whose grants were
// really minted from the store.
//
// The runs here are made the way the product makes them — agent.ask, a live
// stream, a grant minted by runGrantFor — rather than by writing entries into
// the registry, because the whole question is what a RUN's own grant decides
// and a hand-built one would be a grant nobody minted.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/storage"
)

// heldAskClient keeps every run it is asked to answer ALIVE until either the
// run's own context is cancelled or the test releases them all. That is what
// makes a run "in flight" for these tests: the grant is minted, the run is in
// the registry, and nothing has terminalized it.
type heldAskClient struct {
	started chan int64
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	discard []string
}

func newHeldAskClient() *heldAskClient {
	return &heldAskClient{started: make(chan int64, 8), release: make(chan struct{})}
}

func (c *heldAskClient) Probe(context.Context, assistant.ProbeParams) (assistant.ProbeResult, error) {
	return assistant.ProbeResult{}, nil
}

func (c *heldAskClient) Ask(ctx context.Context, _ assistant.AskParams, onEvent func(assistant.AskEvent) error) error {
	if err := onEvent(assistant.AskEvent{Kind: assistant.AskAnswer, Text: "in flight"}); err != nil {
		return err
	}
	c.started <- 1
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *heldAskClient) Discard(runID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.discard = append(c.discard, runID)
}

// releaseAll lets every held run finish normally. Idempotent: a test that
// releases and then defers a release must not panic on the second close.
func (c *heldAskClient) releaseAll() { c.once.Do(func() { close(c.release) }) }

// revokeHarness is newPolicyHarness with a client that holds its runs open.
type revokeHarness struct {
	*askHarness
	store  *assistant.GlobalPolicyStore
	client *heldAskClient
	sid    string
	nextID int
}

func newRevokeHarness(t *testing.T) *revokeHarness {
	t.Helper()
	client := newHeldAskClient()
	store := assistant.NewGlobalPolicyStore(storage.NewDocumentStore(t.TempDir()), "agent-policy.json")
	h := newAskHarnessWithOpts(t, client,
		WithAgentPolicy(store),
		WithLiveEffects(agenttools.LiveEffects()),
	)
	t.Cleanup(client.releaseAll)
	h.createEndpoint()
	return &revokeHarness{askHarness: h, store: store, client: client, sid: openLocalSession(t, h.conn), nextID: 100}
}

// startHeldRun asks a question and waits until the stream is really running,
// so the run is in the registry with its grant minted before anything else
// happens. Returns the run the person is now watching.
func (h *revokeHarness) startHeldRun(askID string) askWireResult {
	h.t.Helper()
	h.nextID++
	res, errObj := askOverWire(h.t, h.conn, map[string]any{
		"askId": askID, "sessionId": h.sid, "question": "keep going",
		"cwd": "/repo", "attachedContent": []any{},
	}, h.nextID)
	if errObj != nil {
		h.t.Fatalf("agent.ask %s: %+v", askID, errObj)
	}
	select {
	case <-h.client.started:
	case <-time.After(5 * time.Second):
		h.t.Fatalf("run %s never started streaming", askID)
	}
	// The registry entry is written before the stream is submitted, so a
	// started stream means the run is registered; assert it rather than
	// assume it, because every count below is over this map.
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.ws.pendingRunsMu.Lock()
		_, live := h.ws.pendingRuns[res.RunID]
		h.ws.pendingRunsMu.Unlock()
		if live {
			return res
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("run %d never reached the live registry", res.RunID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *revokeHarness) isLive(runID int64) bool {
	h.ws.pendingRunsMu.Lock()
	defer h.ws.pendingRunsMu.Unlock()
	_, ok := h.ws.pendingRuns[runID]
	return ok
}

// grantOf reads the run's own frozen policy — the thing the enforcement path
// consults. Both ends of criterion 3 are stated against it.
func (h *revokeHarness) grantOf(runID int64) *content.Grant {
	h.t.Helper()
	h.ws.pendingRunsMu.Lock()
	defer h.ws.pendingRunsMu.Unlock()
	rc, ok := h.ws.pendingRuns[runID]
	if !ok {
		h.t.Fatalf("run %d is not live, so it has no grant to read", runID)
	}
	return rc.grant
}

// seedPermit writes one standing answer over a literal command line, through
// the store's own seam — the same door the approval prompt uses.
func (h *revokeHarness) seedPermit(command ...string) content.InvocationRule {
	h.t.Helper()
	saved, err := h.store.SetRule(content.InvocationRule{
		Selector:         content.InvocationSelector{Exact: [][]string{command}},
		Decision:         content.DecisionPermit,
		EvaluatorVersion: content.EvaluatorVersion,
	})
	if err != nil {
		h.t.Fatalf("seed rule %v: %v", command, err)
	}
	return saved
}

// forgetRuleRuns drives policy.forgetRule with a stated timing.
func forgetRuleRuns(t *testing.T, h *revokeHarness, id string, runs string) policyForgetRuleResult {
	t.Helper()
	params := map[string]any{"id": id}
	if runs != "" {
		params["runs"] = runs
	}
	raw := jsonrpcCall(t, h.conn, "policy.forgetRule", params)
	var env struct {
		Result policyForgetRuleResult `json:"result"`
		Error  *jsonrpcErrorObj       `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.forgetRule %s: %v", raw, err)
	}
	if env.Error != nil {
		t.Fatalf("policy.forgetRule: %+v", env.Error)
	}
	return env.Result
}

func setRuleRuns(t *testing.T, h *revokeHarness, rule map[string]any, runs string) policySetRuleResult {
	t.Helper()
	params := map[string]any{"rule": rule}
	if runs != "" {
		params["runs"] = runs
	}
	raw := jsonrpcCall(t, h.conn, "policy.setRule", params)
	var env struct {
		Result policySetRuleResult `json:"result"`
		Error  *jsonrpcErrorObj    `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.setRule %s: %v", raw, err)
	}
	if env.Error != nil {
		t.Fatalf("policy.setRule: %+v", env.Error)
	}
	return env.Result
}

// dfInvocation is the probe the assertions below decide against — the same
// canonical parse a run's enforcement would build from the command line.
func dfInvocation() content.Invocation {
	return assistant.CanonicalInvocation("df -h")
}

// ── criterion 1 and 5: it says how many, and only the ones using it ────────

// A forget that affects live runs says how many, offers the choice by NOT
// having applied yet, and counts only the runs whose grant would decide
// differently. The bystander started before the answer existed, so its grant
// never held the rule and it is not counted.
func TestPolicyForgetRule_SaysHowManyLiveRunsAreStillUsingTheAnswer(t *testing.T) {
	h := newRevokeHarness(t)

	bystander := h.startHeldRun("bystander")
	rule := h.seedPermit("df", "-h")
	usingA := h.startHeldRun("using-a")
	usingB := h.startHeldRun("using-b")

	got := forgetRuleRuns(t, h, rule.ID, "")
	if got.Applied {
		t.Fatalf("forget = %+v, want applied=false: nothing may change until the person has answered", got)
	}
	if got.AffectedRuns != 2 {
		t.Fatalf("affectedRuns = %d, want 2 (runs %d and %d, not %d)",
			got.AffectedRuns, usingA.RunID, usingB.RunID, bystander.RunID)
	}
	if got.Removed || got.StoppedRuns != 0 || got.FinishedBeforeStop != 0 {
		t.Fatalf("forget = %+v, want nothing removed and nothing stopped", got)
	}
	if len(h.store.Policy().Rules) != 1 {
		t.Fatalf("rules = %+v, want the answer still there: the person has not chosen yet", h.store.Policy().Rules)
	}
	for _, run := range []int64{bystander.RunID, usingA.RunID, usingB.RunID} {
		if !h.isLive(run) {
			t.Fatalf("run %d ended while the question was being asked", run)
		}
	}
}

// ── criterion 2: no live run affected asks nothing and just applies ────────

// The question must not become noise. With no live run deciding differently,
// the default timing writes straight through and reports a count of zero.
func TestPolicyForgetRule_AsksNothingWhenNoLiveRunIsUsingTheAnswer(t *testing.T) {
	h := newRevokeHarness(t)

	// A run that starts BEFORE the answer exists: its grant never held the
	// rule, so forgetting it changes nothing this run decides.
	h.startHeldRun("bystander")
	rule := h.seedPermit("df", "-h")

	got := forgetRuleRuns(t, h, rule.ID, "")
	if !got.Applied || !got.Removed {
		t.Fatalf("forget = %+v, want it applied and removed with no question", got)
	}
	if got.AffectedRuns != 0 {
		t.Fatalf("affectedRuns = %d, want 0 — no live run decides differently without it", got.AffectedRuns)
	}
	if len(h.store.Policy().Rules) != 0 {
		t.Fatalf("rules = %+v, want the answer gone", h.store.Policy().Rules)
	}
}

// A rule write over a command NOTHING is running under still asks nothing,
// even with several live runs: the count is about the answer, never about how
// many runs exist.
func TestPolicySetRule_AsksNothingWhenTheAnswerGovernsNoLiveRun(t *testing.T) {
	h := newRevokeHarness(t)
	rule := h.seedPermit("df", "-h")
	h.startHeldRun("one")
	h.startHeldRun("two")

	// Re-write the rule with exactly what it already says — the Review
	// gesture. Nothing decides differently, so nobody is asked anything.
	got := setRuleRuns(t, h, map[string]any{
		"id":       rule.ID,
		"selector": map[string]any{"exact": []any{[]any{"df", "-h"}}},
		"decision": "permit",
	}, "")
	if !got.Applied || got.AffectedRuns != 0 {
		t.Fatalf("setRule = %+v, want applied with affectedRuns=0", got)
	}
	if got.ID != rule.ID || got.Added {
		t.Fatalf("setRule = %+v, want the rule replaced in place", got)
	}
}

// ── criterion 3: future-only leaves the running work alone, both ends ──────

// The interval, stated at BOTH ends. From the moment the write lands until the
// run in flight reaches its own terminal state, that run goes on deciding
// under the answer it was minted with — and it FINISHES that way, completed
// rather than cancelled. From the same moment, every run started afterwards is
// minted from the policy without it.
func TestPolicyForgetRule_FutureOnlyLeavesTheRunningWorkAloneAtBothEnds(t *testing.T) {
	h := newRevokeHarness(t)
	rule := h.seedPermit("df", "-h")
	inFlight := h.startHeldRun("in-flight")

	got := forgetRuleRuns(t, h, rule.ID, "future")
	if !got.Applied || !got.Removed || got.AffectedRuns != 1 {
		t.Fatalf("forget future = %+v, want applied, removed and 1 affected", got)
	}

	// END ONE: the run in flight still holds the old answer and is still
	// running under it. Asserted BEFORE the counts, so a future-only path that
	// stopped runs anyway is caught by the run being gone rather than only by
	// the number that describes it.
	if !h.isLive(inFlight.RunID) {
		t.Fatalf("run %d was stopped by a forget that said it would leave it alone", inFlight.RunID)
	}
	if got.StoppedRuns != 0 || got.FinishedBeforeStop != 0 {
		t.Fatalf("forget future = %+v, want nothing stopped", got)
	}
	grant := h.grantOf(inFlight.RunID)
	if grant == nil {
		t.Fatal("the run in flight carries no grant, so there is nothing to keep")
	}
	if d := grant.Policy.DecisionForInvocation(content.EffectObserve, dfInvocation()); d != content.DecisionPermit {
		t.Fatalf("the run in flight decides df -h = %s, want permit — its grant is fixed for the run", d)
	}

	// END TWO: the next run is minted without it.
	next := h.startHeldRun("next")
	nextGrant := h.grantOf(next.RunID)
	if nextGrant == nil {
		t.Fatal("the next run carries no grant")
	}
	if d := nextGrant.Policy.DecisionForInvocation(content.EffectObserve, dfInvocation()); d != content.DecisionAsk {
		t.Fatalf("the next run decides df -h = %s, want ask — the answer was forgotten", d)
	}

	// AND THE INTERVAL CLOSES WHERE IT SHOULD: the run in flight reaches its
	// own ending, not one the revocation gave it.
	h.client.releaseAll()
	waitForRunTermination(t, h, inFlight.EntryID, content.RunCompleted, content.TermCompleted)
}

// ── criterion 4 and 5: stopping ends those runs, through the one path ──────

// Choosing to stop terminalizes exactly the runs that were still using the
// answer, through the path agent.cancel takes, and the durable record names
// the revocation rather than a person killing that run. A run that was not
// using it survives.
func TestPolicyForgetRule_StopEndsTheRunsUsingItAndSparesTheOthers(t *testing.T) {
	h := newRevokeHarness(t)

	bystander := h.startHeldRun("bystander")
	rule := h.seedPermit("df", "-h")
	using := h.startHeldRun("using")

	got := forgetRuleRuns(t, h, rule.ID, "stop")
	if !got.Applied || !got.Removed {
		t.Fatalf("forget stop = %+v, want the answer applied and removed", got)
	}
	if got.AffectedRuns != 1 || got.StoppedRuns != 1 || got.FinishedBeforeStop != 0 {
		t.Fatalf("forget stop = %+v, want 1 affected, 1 stopped, 0 already finished", got)
	}

	// The terminal state AND the reason, off the durable record.
	waitForRunTermination(t, h, using.EntryID, content.RunCancelled, content.TermUserKilled)

	// The bystander is untouched: it was never deciding under the answer.
	if !h.isLive(bystander.RunID) {
		t.Fatalf("run %d was stopped, and it was not using the answer", bystander.RunID)
	}
}

// The sentence a person reads names the answer that was taken back, so the
// stopped run says WHY rather than just that it ended.
func TestPolicyForgetRule_TheStoppedRunSaysWhichAnswerWasTakenBack(t *testing.T) {
	h := newRevokeHarness(t)
	rule := h.seedPermit("df", "-h")
	using := h.startHeldRun("using")

	if got := forgetRuleRuns(t, h, rule.ID, "stop"); got.StoppedRuns != 1 {
		t.Fatalf("forget stop = %+v, want 1 stopped", got)
	}
	entry := waitForRunTermination(t, h, using.EntryID, content.RunCancelled, content.TermUserKilled)
	if entry == "" {
		t.Fatal("the stopped run recorded no sentence")
	}
	if !containsAll(entry, "taken back", "df -h") {
		t.Fatalf("the stopped run's sentence = %q, want it to name the answer that was taken back", entry)
	}
}

// ── criterion 6: the wire carries it ──────────────────────────────────────

// The three branches a rule write can answer with, off the REAL socket,
// against the declared shape: the question (nothing applied, a count), the
// future-only apply, and the stop with what it actually stopped. A test that
// validated a payload it built itself would prove the struct is well-formed,
// not that the server sends it.
func TestPolicyRuleWriteRuns_OverTheWireConformsToContract(t *testing.T) {
	forget := loadSchema(t, "policy.forgetRule.schema.json")
	set := loadSchema(t, "policy.setRule.schema.json")

	// The question: live runs are using it, so nothing was written.
	h := newRevokeHarness(t)
	rule := h.seedPermit("df", "-h")
	h.startHeldRun("using")
	raw := jsonrpcCall(t, h.conn, "policy.forgetRule", map[string]any{"id": rule.ID})
	validateJSON(t, forget, resultOf(t, raw), "policy.forgetRule asking (real socket)")

	// The stop: applied, and what it actually ended.
	raw = jsonrpcCall(t, h.conn, "policy.forgetRule", map[string]any{"id": rule.ID, "runs": "stop"})
	validateJSON(t, forget, resultOf(t, raw), "policy.forgetRule stopping (real socket)")

	// And the set side, future-only, on a fresh stand so the count is known.
	h2 := newRevokeHarness(t)
	second := h2.seedPermit("uname", "-a")
	h2.startHeldRun("using")
	raw = jsonrpcCall(t, h2.conn, "policy.setRule", map[string]any{
		"rule": map[string]any{
			"id":       second.ID,
			"selector": map[string]any{"exact": []any{[]any{"uname", "-r"}}},
			"decision": "permit",
		},
		"runs": "future",
	})
	validateJSON(t, set, resultOf(t, raw), "policy.setRule future-only (real socket)")
}

// ── the mode itself ────────────────────────────────────────────────────────

// A timing nobody declared is invalid params, not a silent fall back: a
// misspelled "stop" that quietly became "ask" would leave a person's runs
// alive after they asked for them to stop.
func TestPolicyRuleWrite_AnUnknownTimingIsRefused(t *testing.T) {
	h := newRevokeHarness(t)
	rule := h.seedPermit("df", "-h")

	raw := jsonrpcCall(t, h.conn, "policy.forgetRule", map[string]any{"id": rule.ID, "runs": "stopp"})
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.forgetRule %s: %v", raw, err)
	}
	if env.Error == nil || env.Error.Code != -32602 {
		t.Fatalf("an unknown runs value = %s, want -32602", raw)
	}
	if len(h.store.Policy().Rules) != 1 {
		t.Fatalf("rules = %+v, want the answer untouched by a refused call", h.store.Policy().Rules)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

// waitForRunTermination reads the DURABLE record — the ledger row the terminal
// close writes — and answers the run's error sentence. The notification may or
// may not reach a socket; the ledger is the record.
func waitForRunTermination(
	t *testing.T, h *revokeHarness, entryID string, state content.RunState, reason content.TerminationReason,
) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for {
		entry, err := h.db.Ledger().Entry(t.Context(), entryID)
		if err != nil {
			t.Fatalf("ledger entry %s: %v", entryID, err)
		}
		if len(entry.Executions) == 1 && entry.Executions[0].State != nil {
			exec := entry.Executions[0]
			if *exec.State == state {
				if exec.TerminationReason == nil || *exec.TerminationReason != reason {
					got := content.TerminationReason("")
					if exec.TerminationReason != nil {
						got = *exec.TerminationReason
					}
					t.Fatalf("run %s ended %s for %q, want %q", entryID, state, got, reason)
				}
				return execError(exec)
			}
			last = string(*exec.State)
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never reached %s (last state %q)", entryID, state, last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// execError reads the run's failure sentence out of the execution payload —
// the sparse extension FinishAgentRun writes it into. It is read from the
// DURABLE record rather than from a notification because the sentence is what
// a person sees when they come back to the run tomorrow.
func execError(exec content.Execution) string {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(exec.Payload), &payload) != nil {
		return ""
	}
	return payload.Error
}

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
