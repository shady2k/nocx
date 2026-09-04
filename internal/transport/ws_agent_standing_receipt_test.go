package transport

// A standing answer says so where it was given, and two answers cannot lose
// each other (nocx-2019q, nocx-4yjwk.4).
//
// Two things are asserted here and they are one change. The RECEIPT is the
// fact on the wire that lets the turn say what was configured — a person who
// clicks "Allow always" learns that they configured something at the moment
// they did it, rather than later, by being un-asked a question they have
// forgotten answering. Its ruleId is what makes an Undo exact, and getting an
// id back is exactly what going through the store's locked one-rule seam
// gives you — which is the other half: two prompts answered at the same
// moment used to read the whole policy, each append its own rule to what it
// read, and the later write won.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage"
)

// standingAnswerSaved is the agent.standingAnswerSaved notification as the
// renderer reads it.
type standingAnswerSaved struct {
	RunID    string `json:"runId"`
	EntryID  string `json:"entryId"`
	Approved bool   `json:"approved"`
	Scope    string `json:"scope"`
	Rule     string `json:"rule"`
	Effect   string `json:"effect"`
	RuleID   string `json:"ruleId"`
}

// receipt waits for the receipt this run's answer earned.
func (s *scopeHarness) receipt(t *testing.T) standingAnswerSaved {
	t.Helper()
	raw := readNotification(t, s.conn, "agent.standingAnswerSaved", 5*time.Second)
	var got standingAnswerSaved
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("agent.standingAnswerSaved: %v\nraw: %s", err, raw)
	}
	return got
}

// noReceipt asserts the answer earned none. The window is short on purpose:
// the notification is written before the response the caller has already
// read, so by the time this runs a receipt that was going to be sent has been.
func (s *scopeHarness) noReceipt(t *testing.T) {
	t.Helper()
	if raw, err := awaitNotification(s.conn, "agent.standingAnswerSaved", 300*time.Millisecond); err == nil {
		t.Fatalf("a receipt was sent for an answer that saved nothing: %s", raw)
	}
}

var dfCommand = content.Invocation{Commands: [][]string{{"df", "-h"}}, Parsed: true}

// The receipt names what was saved and how to take it back. The id is the
// STORE's — the one the document wears — because that is the only id an Undo
// can name without discarding somebody else's answer.
func TestAgentApprove_StandingAnswer_ReceiptCarriesTheStoredRuleID(t *testing.T) {
	h := suspendedCommandRun(t, askPolicyStore(t), dfCommand)

	if got := h.approve(t, "always"); got.Warning != "" {
		t.Fatalf("warning = %q, want none — the rule was saved", got.Warning)
	}
	got := h.receipt(t)

	stored := h.policy.Policy().Rules
	if len(stored) != 1 {
		t.Fatalf("stored rules = %+v, want the one the answer saved", stored)
	}
	if got.RuleID == "" || got.RuleID != stored[0].ID {
		t.Fatalf("receipt ruleId = %q, want the stored rule's own %q", got.RuleID, stored[0].ID)
	}
	if !got.Approved || got.Scope != "always" {
		t.Fatalf("receipt = %+v, want the direction and width that were answered", got)
	}
	if got.Rule != "df -h" {
		t.Fatalf("receipt rule = %q, want the canonical invocation the question offered", got.Rule)
	}
	if got.Effect != string(content.EffectObserve) {
		t.Fatalf("receipt effect = %q, want the class the gate decided under", got.Effect)
	}
	if got.RunID != h.asked.RunID {
		t.Fatalf("receipt runId = %q, want the run that asked %q", got.RunID, h.asked.RunID)
	}
	// AND IT IS ROUTED BY THE TURN'S ENTRY, NOT THE PROPOSAL'S. The renderer
	// drops a receipt whose entryId is not the block's, exactly as it drops a
	// stray delta, so this field decides whether the receipt is drawn at all.
	// The proposal has a ledger entry of its own and it is the WRONG one here:
	// every sibling notification in ws_agent.go sends the turn's as `entryId`
	// and the proposal's separately as `actionEntryId`, and the contract's
	// wording — "the ledger entry of the turn that asked" — rests on that
	// distinction.
	// The guard that keeps this assertion honest: two entries that happened
	// to be equal — which is what an empty one on both sides was — would
	// make it pass whichever the handler sent.
	if h.entryID == "" || h.entryID == proposalEntryID {
		t.Fatalf("turn entry %q and proposal entry %q must differ and be present, or this "+
			"assertion cannot fail", h.entryID, proposalEntryID)
	}
	if got.EntryID != h.entryID {
		t.Fatalf("receipt entryId = %q, want the TURN's entry %q — the renderer routes by that "+
			"and drops anything else, so a receipt carrying the proposal's entry is never drawn",
			got.EntryID, h.entryID)
	}
}

// A standing NO is a standing answer. "Never ask me to run this again"
// configured something exactly as much as "always" did.
func TestAgentApprove_StandingRefusal_AlsoEarnsAReceipt(t *testing.T) {
	h := suspendedCommandRun(t, askPolicyStore(t), dfCommand)

	if got := h.deny(t, "always"); got.Warning != "" {
		t.Fatalf("warning = %q, want none", got.Warning)
	}
	got := h.receipt(t)
	if got.Approved {
		t.Fatalf("receipt = %+v, want the refusal it was", got)
	}
	if got.RuleID == "" {
		t.Fatal("a refusal that can be taken back by id came back with none")
	}
}

// A session-scoped answer is saved and says so — and offers no id, because
// the overlay it wrote is not addressable by one and dies with its session.
func TestAgentApprove_SessionScope_ReceiptCarriesNoRuleID(t *testing.T) {
	h := suspendedCommandRun(t, askPolicyStore(t), dfCommand)

	if got := h.approve(t, "session"); got.Warning != "" {
		t.Fatalf("warning = %q, want none", got.Warning)
	}
	got := h.receipt(t)
	if got.Scope != "session" || got.Rule != "df -h" {
		t.Fatalf("receipt = %+v, want the session answer that was given", got)
	}
	if got.RuleID != "" {
		t.Fatalf("receipt ruleId = %q, want none — a session overlay is not addressable by id", got.RuleID)
	}
}

// "once" writes nothing anywhere, so there is nothing to report and no
// receipt. A receipt here would tell a person they had configured something
// when they had deliberately declined to.
func TestAgentApprove_ScopeOnce_EarnsNoReceipt(t *testing.T) {
	h := suspendedCommandRun(t, askPolicyStore(t), dfCommand)

	h.approve(t, "once")
	h.noReceipt(t)
}

// An egress answer earns none either, and for a different reason: the wire
// refuses a width to an egress question at all, so the only answer that can
// be given saves nothing. Both facts are true, neither implies the other.
func TestAgentApprove_Egress_EarnsNoReceipt(t *testing.T) {
	h := suspendedEgressRun(t, askPolicyStore(t))

	h.approve(t, "once")
	h.noReceipt(t)
}

// A save that FAILED must not produce a receipt. The warning is the honest
// report and it is on the response; a receipt beside it would say "saved" on
// screen about a rule the store refused.
func TestAgentApprove_FailedSave_EarnsNoReceipt(t *testing.T) {
	h := suspendedCommandRun(t, failingPolicyStore{err: errors.New("disk is full")}, dfCommand)

	got := h.approve(t, "always")
	if !strings.Contains(got.Warning, "could not be saved as a standing answer") ||
		!strings.Contains(got.Warning, "disk is full") {
		t.Fatalf("warning = %q, want the honest report of the refused write", got.Warning)
	}
	h.noReceipt(t)
}

// A rule write is a RULE write: the seven rows are whatever they were. Before
// this, the answer read the whole policy, appended to its copy and wrote the
// document back, so anything that changed in between — a row the settings
// page had just moved — went back to the value the prompt had read.
func TestAgentApprove_StandingRule_LeavesEverySevenRowsAlone(t *testing.T) {
	store := assistant.NewGlobalPolicyStore(storage.NewDocumentStore(t.TempDir()), "agent-policy.json")
	seeded := content.EffectPolicy{
		Observe:           content.EffectRow{Decision: content.DecisionAsk},
		MutateReversible:  content.EffectRow{Decision: content.DecisionPermit},
		MutateDestructive: content.EffectRow{Decision: content.DecisionRefuse},
		PrivilegeChange:   content.EffectRow{Decision: content.DecisionRefuse},
		Disclose:          content.EffectRow{Decision: content.DecisionPermit},
		CrossBoundary:     content.EffectRow{Decision: content.DecisionAsk},
		Delegate:          content.EffectRow{Decision: content.DecisionRefuse},
	}
	if err := store.SetPolicy(seeded); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	h := suspendedCommandRun(t, store, dfCommand)
	if got := h.approve(t, "always"); got.Warning != "" {
		t.Fatalf("warning = %q, want none", got.Warning)
	}

	after := h.policy.Policy()
	rows := []content.Effect{
		content.EffectObserve, content.EffectMutateReversible, content.EffectMutateDestructive,
		content.EffectPrivilegeChange, content.EffectDisclose, content.EffectCrossBoundary,
		content.EffectDelegate,
	}
	for _, effect := range rows {
		if got, want := after.DecisionFor(effect), seeded.DecisionFor(effect); got != want {
			t.Errorf("row %s = %q after a RULE was written, want the untouched %q", effect, got, want)
		}
	}
	if len(after.Rules) != 1 {
		t.Fatalf("rules = %+v, want the one the answer saved", after.Rules)
	}
}

// ── two answers cannot lose each other ────────────────────────────────────

// applyStandingAnswer is the prompt path's standing half, and this drives two
// of them at once against ONE store — the shape a person produces by
// answering the second question while the first is still settling.
//
// It is driven in ROUNDS rather than once, and that is not padding. The
// losing interleaving needs both answers to READ before either WRITES, and
// with the old read-modify-write that window was real but narrow: a single
// pair caught it perhaps one run in three, which is a test that reports a
// race as weather. Over the rounds below the old path loses an answer every
// time; the locked seam cannot lose one at all, so the test is decisive in
// the direction that matters and cannot go red on a correct store.
func TestApplyStandingAnswer_TwoAtOnceBothSurvive(t *testing.T) {
	store := assistant.NewGlobalPolicyStore(storage.NewDocumentStore(t.TempDir()), "agent-policy.json")
	ask := content.EffectRow{Decision: content.DecisionAsk}
	if err := store.SetPolicy(content.EffectPolicy{
		Observe: ask, MutateReversible: ask, MutateDestructive: ask,
		PrivilegeChange: ask, Disclose: ask, CrossBoundary: ask, Delegate: ask,
	}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	approvals := assistant.NewApprovalStore()
	h := agentHandlers{
		approvals:     approvals,
		sessionPolicy: newSessionPolicyStore(),
		globalPolicy:  store,
		log:           log.NewSlogAdapter(slog.New(slog.NewTextHandler(nil, &slog.HandlerOptions{Level: slog.LevelError}))),
	}

	const rounds = 40
	ids := make(map[string]bool, rounds*2)
	for round := range rounds {
		commands := [][]string{
			{"df", "-h", strconv.Itoa(round)},
			{"uname", "-a", strconv.Itoa(round)},
		}
		params := make([]approveParams, len(commands))
		asked := make([]assistant.Approval, len(commands))
		for i, command := range commands {
			runID := fmt.Sprintf("run-%d-%d", round, i)
			ap := assistant.Approval{
				RunID: runID, Attempt: 1, Tool: "run", CallID: "call-1", ArgHash: "hash-1",
				Effect:            content.EffectObserve,
				Invocation:        content.Invocation{Commands: [][]string{command}, Parsed: true},
				CommandInvocation: true,
			}
			approvals.Request(ap)
			asked[i] = ap
			params[i] = approveParams{
				RunID: runID, Attempt: 1, Tool: "run", CallID: "call-1", ArgHash: "hash-1",
				Approved: true, Scope: approveScopeAlways,
			}
		}

		// Both answers land in the same instant, which is what the store's
		// own lock is for. A start gate rather than a sleep: the point is
		// that they overlap, and a duration would only make that likely.
		var start, done sync.WaitGroup
		start.Add(1)
		done.Add(len(commands))
		outcomes := make([]standingAnswerOutcome, len(commands))
		for i := range commands {
			go func() {
				defer done.Done()
				start.Wait()
				outcomes[i] = h.applyStandingAnswer(params[i], asked[i], session.ID("session-a"))
			}()
		}
		start.Done()
		done.Wait()

		for i, outcome := range outcomes {
			if outcome.warning != "" {
				t.Fatalf("round %d answer %d warned %q, want a clean save", round, i, outcome.warning)
			}
			if outcome.ruleID == "" {
				t.Fatalf("round %d answer %d came back with no rule id", round, i)
			}
			if ids[outcome.ruleID] {
				t.Fatalf("round %d answer %d was given the id %q, which already names another rule",
					round, i, outcome.ruleID)
			}
			ids[outcome.ruleID] = true
		}

		stored := store.Policy().Rules
		if len(stored) != (round+1)*len(commands) {
			t.Fatalf("round %d: stored rules = %d, want %d — an answer was lost",
				round, len(stored), (round+1)*len(commands))
		}
		// And the id each answer was handed is the one the DOCUMENT wears,
		// which is the whole of what makes the receipt's Undo exact.
		for _, outcome := range outcomes {
			found := false
			for _, rule := range stored {
				if rule.ID == outcome.ruleID {
					found = true
				}
			}
			if !found {
				t.Fatalf("round %d: no stored rule wears the id %q the answer came back with",
					round, outcome.ruleID)
			}
		}
	}
}

// The other pair, and the reason the seven-row test above is worth having: a
// NON-command answer writes a matrix row while a command answer writes a
// rule, and the two used to be the same read-modify-write over one document.
// Whichever wrote second put back the copy it had read, so a row moved by the
// answer that lost went back to what it was — a person told nocx to stop
// asking, was told nothing, and was asked again.
func TestApplyStandingAnswer_ARowAnswerAndARuleAnswerAtOnceBothSurvive(t *testing.T) {
	store := assistant.NewGlobalPolicyStore(storage.NewDocumentStore(t.TempDir()), "agent-policy.json")
	ask := content.EffectRow{Decision: content.DecisionAsk}
	if err := store.SetPolicy(content.EffectPolicy{
		Observe: ask, MutateReversible: ask, MutateDestructive: ask,
		PrivilegeChange: ask, Disclose: ask, CrossBoundary: ask, Delegate: ask,
	}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	approvals := assistant.NewApprovalStore()
	h := agentHandlers{
		approvals:     approvals,
		sessionPolicy: newSessionPolicyStore(),
		globalPolicy:  store,
		log:           log.NewSlogAdapter(slog.New(slog.NewTextHandler(nil, &slog.HandlerOptions{Level: slog.LevelError}))),
	}

	const rounds = 40
	for round := range rounds {
		rowRun := fmt.Sprintf("row-%d", round)
		ruleRun := fmt.Sprintf("rule-%d", round)
		// Alternating so the row answer says something different every
		// round: a row that was already permit would survive being
		// overwritten by a stale copy and prove nothing.
		decision := content.DecisionPermit
		approved := true
		if round%2 == 1 {
			decision = content.DecisionRefuse
			approved = false
		}
		rowAsk := assistant.Approval{
			RunID: rowRun, Attempt: 1, Tool: "session.read", CallID: "call-1", ArgHash: "hash-1",
			Effect: content.EffectDisclose,
		}
		ruleAsk := assistant.Approval{
			RunID: ruleRun, Attempt: 1, Tool: "run", CallID: "call-1", ArgHash: "hash-1",
			Effect:            content.EffectObserve,
			Invocation:        content.Invocation{Commands: [][]string{{"df", "-h", strconv.Itoa(round)}}, Parsed: true},
			CommandInvocation: true,
		}
		approvals.Request(rowAsk)
		approvals.Request(ruleAsk)

		var start, done sync.WaitGroup
		start.Add(1)
		done.Add(2)
		go func() {
			defer done.Done()
			start.Wait()
			h.applyStandingAnswer(approveParams{
				RunID: rowRun, Attempt: 1, Tool: "session.read", CallID: "call-1", ArgHash: "hash-1",
				Approved: approved, Scope: approveScopeAlways,
			}, rowAsk, session.ID("session-a"))
		}()
		go func() {
			defer done.Done()
			start.Wait()
			h.applyStandingAnswer(approveParams{
				RunID: ruleRun, Attempt: 1, Tool: "run", CallID: "call-1", ArgHash: "hash-1",
				Approved: true, Scope: approveScopeAlways,
			}, ruleAsk, session.ID("session-a"))
		}()
		start.Done()
		done.Wait()

		after := store.Policy()
		if got := after.DecisionFor(content.EffectDisclose); got != decision {
			t.Fatalf("round %d: disclose = %q, want the %q the row answer saved — the rule write put back a stale copy",
				round, got, decision)
		}
		if len(after.Rules) != round+1 {
			t.Fatalf("round %d: stored rules = %d, want %d — the row write put back a stale copy",
				round, len(after.Rules), round+1)
		}
	}
}

// ── the wire is a party to the contract ───────────────────────────────────

// The DTO conformance: field tags, enum spelling, and the two fields that are
// legitimately empty strings rather than absent — a session answer's rule id
// and a non-command answer's rule. entryId is NOT one of them any more: the
// schema requires a non-empty one, because nothing can produce an empty one
// and a receipt built on one is invisible.
func TestAgentStandingAnswerSaved_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.standingAnswerSaved.schema.json")

	cases := map[string]agentStandingAnswerSaved{
		"a command rule, undoable by id": {
			RunID: "7", EntryID: "entry-1", Approved: true, Scope: "always",
			Rule: "df -h", Effect: "observe", RuleID: "rule-1",
		},
		"a session answer, addressable by nothing": {
			RunID: "7", EntryID: "entry-1", Approved: false, Scope: "session",
			Rule: "df -h", Effect: "mutate-destructive",
		},
		"an effect row, which has no invocation": {
			RunID: "7", EntryID: "entry-1", Approved: true, Scope: "always",
			Effect: "disclose",
		},
	}
	for name, dto := range cases {
		raw, err := json.Marshal(dto)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		validateJSON(t, schema, raw, "agent.standingAnswerSaved DTO ("+name+")")
	}
}

// And the real notification off the real socket — the assertion that catches
// the handler not sending what the DTO could have.
func TestAgentStandingAnswerSaved_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.standingAnswerSaved.schema.json")
	h := suspendedCommandRun(t, askPolicyStore(t), dfCommand)
	if got := h.approve(t, "always"); got.Warning != "" {
		t.Fatalf("warning = %q, want none", got.Warning)
	}
	raw := readNotification(t, h.conn, "agent.standingAnswerSaved", 5*time.Second)
	validateJSON(t, schema, raw, "agent.standingAnswerSaved params (real socket)")
}

// ── a receipt with no turn to land on ─────────────────────────────────────

// forgetTurnEntry blanks the TURN's entry on the stored run context.
//
// The shape cannot be reached through agent.ask — the ask id is refused
// empty, SubmitAgentAsk answers with it, and agent.ask is refused outright
// with no content store — so it is reached where the value lives. That is the
// point of the test: the field the schema used to permit empty has no
// producer, and the handler must not send a receipt built on one anyway.
func (s *scopeHarness) forgetTurnEntry(t *testing.T) {
	t.Helper()
	s.ws.pendingRunsMu.Lock()
	defer s.ws.pendingRunsMu.Unlock()
	rc, ok := s.ws.pendingRuns[s.runID]
	if !ok {
		t.Fatalf("run %d is not pending — nothing to blank", s.runID)
	}
	rc.entryID = ""
	s.ws.pendingRuns[s.runID] = rc
}

// A receipt with no entry is a receipt no renderer can route: the surface
// compares it with the block's own and drops anything else, so sending one is
// a receipt nobody can ever see — which is exactly how nocx-2019q survived
// thirteen tasks of green tests.
//
// So it is not sent. The answer WAS saved, and the person is told so on the
// response's warning, which the prompt shows (agent-approval-decision.ts): a
// soft degrade must be visible in the product, not only in a log.
func TestAgentApprove_StandingAnswer_WithNoTurnEntry_IsReportedAndNotSent(t *testing.T) {
	h := suspendedCommandRun(t, askPolicyStore(t), dfCommand)
	h.forgetTurnEntry(t)

	got := h.approve(t, "always")
	if !strings.Contains(got.Warning, "saved") || !strings.Contains(got.Warning, "Manage permissions") {
		t.Fatalf("warning = %q, want the sentence that says the answer was saved and where to find it", got.Warning)
	}
	h.noReceipt(t)

	// And the save itself is untouched: what failed is the receipt's route,
	// not the write, so the rule must be in the store either way.
	if stored := h.policy.Policy().Rules; len(stored) != 1 {
		t.Fatalf("stored rules = %+v, want the one the answer saved", stored)
	}
}
