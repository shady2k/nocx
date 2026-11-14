package transport

// The widening answer on the wire (design §5.3, nocx-b453p).
//
// A question raised because a resource fell outside an EDITABLE row scope used
// to reach a person with three answers — once, in this session, always — none
// of which widened the row that excluded the resource. The next identical call
// asked again, for ever. So the notification now carries WHICH bound was
// missed and WHICH resource the row would have to grow to cover, and
// agent.approve grows a fourth answer that widens the row and approves the
// call as ONE act: a store failure leaves neither applied.
//
// Every assertion here is over the REAL socket against the REAL policy store,
// re-read from disk where the point is that something was persisted.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/storage"
)

const wideningPolicyDoc = "agent-policy.json"

// rowScopedPolicyStore is the state §5.3 is about: an observe row a person
// wrote, with a scope narrower than what the model went on to name.
func rowScopedPolicyStore(t *testing.T) (*assistant.GlobalPolicyStore, storage.DocumentStore) {
	t.Helper()
	doc := storage.NewDocumentStore(t.TempDir())
	store := assistant.NewGlobalPolicyStore(doc, wideningPolicyDoc)
	policy := content.EffectPolicy{
		Observe: content.EffectRow{
			Decision: content.DecisionAsk,
			Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: t.TempDir()}},
		},
		MutateReversible:  content.EffectRow{Decision: content.DecisionAsk},
		MutateDestructive: content.EffectRow{Decision: content.DecisionAsk},
		PrivilegeChange:   content.EffectRow{Decision: content.DecisionAsk},
		Disclose:          content.EffectRow{Decision: content.DecisionAsk},
		CrossBoundary:     content.EffectRow{Decision: content.DecisionAsk},
		Delegate:          content.EffectRow{Decision: content.DecisionAsk},
	}
	if err := store.SetPolicy(policy); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	return store, doc
}

// theResourceOutside is the scope the row would have to grow to cover.
var theResourceOutside = content.GrantScope{Kind: content.ResourcePath, ID: "/repo/lib/b.txt"}

func suspendedOutOfScopeRun(t *testing.T, policy assistant.GlobalPolicy) *scopeHarness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(path, []byte("plain document"), 0o600); err != nil {
		t.Fatal(err)
	}
	return suspendedRunWith(t, policy, approvalToolScript("files.read", map[string]any{"path": path}))
}

// ── what the question carries ─────────────────────────────────────────────

// A row-scope ask names the cause and the resource, and offers the widening.
// Without all three the person cannot answer the question usefully.
func TestAgentApprovalRequested_RowScopeAskNamesTheCauseAndOffersTheWidening(t *testing.T) {
	store, _ := rowScopedPolicyStore(t)
	h := suspendedOutOfScopeRun(t, store)

	if h.asked.OutOfScope == nil {
		t.Fatal("the question carries no out-of-scope fact")
	}
	if h.asked.OutOfScope.Cause != string(content.OutOfScopeRowScope) {
		t.Errorf("cause = %q, want %q", h.asked.OutOfScope.Cause, content.OutOfScopeRowScope)
	}
	if h.asked.OutOfScope.Resource.Kind != content.ResourcePath || !strings.HasSuffix(h.asked.OutOfScope.Resource.ID, "/outside.txt") {
		t.Errorf("resource = %+v, want %+v", h.asked.OutOfScope.Resource, theResourceOutside)
	}
	if !h.asked.OutOfScope.Widening.Available {
		t.Errorf("the widening is not offered on an editable row scope (reason %q)",
			h.asked.OutOfScope.Widening.Reason)
	}
}

// A fence ask carries the fact and offers NOTHING. Offering a question whose
// yes cannot be honoured is the lie this whole shape exists to remove.
func TestAgentApprovalRequested_FenceAskOffersNoWidening(t *testing.T) {
	offer := wideningOffer(content.OutOfScopeFence)
	if offer.Available {
		t.Fatal("a fence ask offered to widen a bound no answer can widen")
	}
	if strings.TrimSpace(offer.Reason) == "" {
		t.Fatal("the refused offer says nothing about why")
	}
}

// An ordinary ask carries no fact at all — no offer may appear on a question
// about nothing.
func TestAgentApprovalRequested_AnAskWithNothingOutsideCarriesNoFact(t *testing.T) {
	store, _ := rowScopedPolicyStore(t)
	h := suspendedRun(t, store)

	if h.asked.OutOfScope != nil {
		t.Fatalf("an ordinary ask carries %+v, want no out-of-scope fact", h.asked.OutOfScope)
	}
}

// An EGRESS question is untouched: two answers, once only, and no widening.
func TestAgentApprovalRequested_EgressCarriesNoWideningOffer(t *testing.T) {
	store, _ := rowScopedPolicyStore(t)
	h := suspendedEgressRun(t, store)

	if h.asked.OutOfScope != nil {
		t.Fatalf("an egress question carries %+v, want no out-of-scope fact", h.asked.OutOfScope)
	}
	_, errObj := approveOverWire(t, h.conn, h.answer(true, approveScopeExpand), 2)
	if errObj == nil {
		t.Fatal("agent.approve widened a row on an egress question")
	}
}

// ── the answer ────────────────────────────────────────────────────────────

// The acceptance criterion: the widening answer widens the row AND resumes the
// call, and a FRESH READ of the store — not the in-memory value the write
// returned — shows the row grown.
func TestAgentApprove_ScopeExpand_WidensTheRowAndResumesTheCall(t *testing.T) {
	store, doc := rowScopedPolicyStore(t)
	h := suspendedOutOfScopeRun(t, store)

	got := h.approve(t, approveScopeExpand)
	if got.State != string(content.RunStreaming) {
		t.Fatalf("state = %q, want the run resumed", got.State)
	}
	if got.Warning != "" {
		t.Fatalf("warning = %q, want none: the widening either applied or the answer was refused", got.Warning)
	}
	// The call resumed and the run finished.
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if !strings.Contains(string(raw), "completed") {
		t.Fatalf("runState = %s, want completed", raw)
	}
	// And the row grew, read back off disk.
	reloaded := assistant.NewGlobalPolicyStore(doc, wideningPolicyDoc)
	scopes := reloaded.Policy().RowScopes(content.EffectObserve)
	if len(scopes) != 2 || scopes[1] != h.asked.OutOfScope.Resource {
		t.Fatalf("observe scopes after the widening = %+v, want the row grown by %+v", scopes, theResourceOutside)
	}
}

// A store failure leaves NEITHER applied. The person must not get an approval
// whose standing half silently vanished, nor a widened row for a call that did
// not run — so this is the one answer that is REFUSED rather than warned
// about, and the question stays open for them to answer differently.
func TestAgentApprove_ScopeExpand_AStoreFailureAppliesNeither(t *testing.T) {
	failing := failingPolicyStore{err: errors.New("disk is full")}
	h := suspendedOutOfScopeRun(t, scopedFailingPolicy{failingPolicyStore: failing, root: t.TempDir()})

	_, errObj := approveOverWire(t, h.conn, h.answer(true, approveScopeExpand), 2)
	if errObj == nil {
		t.Fatal("the widening answer succeeded against a store that refuses every write")
	}
	if !strings.Contains(errObj.Message, "disk is full") {
		t.Fatalf("error = %q, want it to name the store failure", errObj.Message)
	}
	// The approval half did not apply either: the call was not woken, and no
	// second drive was started.
	h.ws.pendingRunsMu.Lock()
	waiting := h.ws.pendingRuns[h.runID].approvalWait != nil
	h.ws.pendingRunsMu.Unlock()
	if !waiting {
		t.Fatal("refused widening woke the waiting call")
	}
	// And the question is still open, which is what "answer it again,
	// differently if you like" means in the product.
	if got := h.approve(t, approveScopeOnce); got.State != string(content.RunStreaming) {
		t.Fatalf("answering the same question again = %q, want it still pending and answerable", got.State)
	}
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
}

// The offer is what makes the answer legitimate. A question that offered no
// widening — a fence, or an ask with nothing outside — refuses it.
func TestAgentApprove_ScopeExpand_RefusedWhenTheQuestionOfferedNoWidening(t *testing.T) {
	for name, suspend := range map[string]func(t *testing.T) *scopeHarness{
		"an ask with nothing outside": func(t *testing.T) *scopeHarness {
			store, _ := rowScopedPolicyStore(t)
			return suspendedRun(t, store)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := suspend(t)
			_, errObj := approveOverWire(t, h.conn, h.answer(true, approveScopeExpand), 2)
			if errObj == nil {
				t.Fatal("agent.approve widened a row for a question that offered no widening")
			}
		})
	}
}

// A widening is an approval. "No, and widen it" is not an answer anybody can
// mean, so it is refused rather than silently read as a plain decline.
func TestAgentApprove_ScopeExpand_RefusedOnADecline(t *testing.T) {
	store, _ := rowScopedPolicyStore(t)
	h := suspendedOutOfScopeRun(t, store)

	_, errObj := approveOverWire(t, h.conn, h.answer(false, approveScopeExpand), 2)
	if errObj == nil {
		t.Fatal("agent.approve accepted a decline that widens a row")
	}
}

// Declining a widening question refuses that (effect, resource) for the rest
// of the run — recorded where the kernel will read it on the next proposal.
func TestAgentApprove_DecliningAWideningRefusesThatResourceForTheRun(t *testing.T) {
	store, _ := rowScopedPolicyStore(t)
	h := suspendedOutOfScopeRun(t, store)

	if got := h.deny(t, approveScopeOnce); got.State != string(content.RunStreaming) {
		t.Fatalf("decline state = %q, want the run resumed with the refusal as the result", got.State)
	}
	outcome := h.ws.agentApprovals.BeginWideningAsk(h.asked.RunID, content.EffectObserve, h.asked.OutOfScope.Resource)
	if outcome != assistant.WideningAskDeclined {
		t.Fatalf("the next proposal of the same resource gets %v, want it refused by the person's no", outcome)
	}
}

// The cap is stated in the run: the person is TOLD the assistant stopped
// asking rather than left to infer it from questions that no longer arrive.
func TestAgentRunState_SaysTheRunStoppedAskingToWiden(t *testing.T) {
	store, _ := rowScopedPolicyStore(t)
	h := suspendedOutOfScopeRun(t, store)

	// Spend the run's whole budget of widening questions, then one more.
	for i := 0; i <= assistant.MaxWideningAsksPerRun; i++ {
		h.ws.agentApprovals.BeginWideningAsk(h.asked.RunID, content.EffectObserve,
			content.GrantScope{Kind: content.ResourcePath, ID: "/repo/lib/" + string(rune('a'+i))})
	}

	h.approve(t, approveScopeOnce)
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var st agentRunState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("runState: %v", err)
	}
	if len(st.Notices) != 1 || st.Notices[0] != assistant.WideningCapSentence() {
		t.Fatalf("runState notices = %q, want exactly %q", st.Notices, assistant.WideningCapSentence())
	}
}

// The ordinary run says nothing: a notice on every run is a notice nobody
// reads.
func TestAgentRunState_SaysNothingWhenTheRunNeverStoppedAsking(t *testing.T) {
	store, _ := rowScopedPolicyStore(t)
	h := suspendedOutOfScopeRun(t, store)

	h.approve(t, approveScopeOnce)
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var st agentRunState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("runState: %v", err)
	}
	if len(st.Notices) != 0 {
		t.Fatalf("runState notices = %q, want none", st.Notices)
	}
}

// ── the wire contracts (contracts/README row 3) ───────────────────────────

// The DTO: both causes, and the absent field on a question with nothing
// outside. Field tags, the nested offer, and the enum spelling of the cause.
func TestAgentApprovalRequested_OutOfScopeDTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.approvalRequested.schema.json")
	base := agentApprovalRequested{
		RunID: "7", Attempt: 1, Tool: "files.read", CallID: "call_1",
		ArgHash: "hash-a", Arguments: `{"path":"/repo/lib/b.txt"}`,
		Reason: "policy", Effect: "observe",
		Standing: agentApprovalStanding{Reason: "the call has no invocation to show"},
		Resource: &theResourceOutside,
	}
	cases := map[string]agentApprovalRequested{}
	for name, cause := range map[string]content.OutOfScopeCause{
		"row-scope offers the widening": content.OutOfScopeRowScope,
		"fence offers nothing":          content.OutOfScopeFence,
	} {
		dto := base
		dto.OutOfScope = &agentApprovalOutOfScope{
			Cause: string(cause), Resource: theResourceOutside, Widening: wideningOffer(cause),
		}
		cases[name] = dto
	}
	cases["nothing fell outside"] = base
	for name, dto := range cases {
		raw, err := json.Marshal(dto)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		validateJSON(t, schema, raw, "agent.approvalRequested DTO ("+name+")")
	}
}

// The real notification off the real socket — the assertion that would catch
// a field the handler never sends.
func TestAgentApprovalRequested_OutOfScopeOverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.approvalRequested.schema.json")
	store, _ := rowScopedPolicyStore(t)
	h := suspendedOutOfScopeRun(t, store)
	raw := h.rawAsked
	validateJSON(t, schema, raw, "agent.approvalRequested params with outOfScope (real socket)")
	if !strings.Contains(string(raw), `"outOfScope"`) {
		t.Fatalf("the notification off the socket carries no outOfScope: %s", raw)
	}
}

// agent.approve's widening answer, as the renderer's LITERAL bytes: the
// schema accepts them and the real socket acts on them.
func TestAgentApprove_ExpandParamsOverTheSocketConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.approve.schema.json")
	store, doc := rowScopedPolicyStore(t)
	h := suspendedOutOfScopeRun(t, store)

	params := `{"runId":` + strconv.Quote(h.asked.RunID) +
		`,"attempt":1,"tool":"files.read","callId":` + strconv.Quote(h.asked.CallID) + `,"argHash":` + strconv.Quote(h.asked.ArgHash) + `,"approved":true,"scope":"expand"}`
	validateJSON(t, schema, []byte(params), "agent.approve params (renderer's literal widening payload)")

	got, errObj := approveOverWireRaw(t, h.conn, []byte(params), 2)
	if errObj != nil {
		t.Fatalf("agent.approve with the literal widening payload: %+v", errObj)
	}
	if got.State != string(content.RunStreaming) {
		t.Fatalf("state = %q, want streaming", got.State)
	}
	reloaded := assistant.NewGlobalPolicyStore(doc, wideningPolicyDoc)
	if len(reloaded.Policy().RowScopes(content.EffectObserve)) != 2 {
		t.Fatalf("the row was not widened by the literal payload: %+v",
			reloaded.Policy().RowScopes(content.EffectObserve))
	}
}

// agent.runState carrying the notice, off the real socket.
func TestAgentRunState_NoticesOverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.runState.schema.json")
	store, _ := rowScopedPolicyStore(t)
	h := suspendedOutOfScopeRun(t, store)
	for i := 0; i <= assistant.MaxWideningAsksPerRun; i++ {
		h.ws.agentApprovals.BeginWideningAsk(h.asked.RunID, content.EffectObserve,
			content.GrantScope{Kind: content.ResourcePath, ID: "/repo/lib/" + string(rune('a'+i))})
	}
	h.approve(t, approveScopeOnce)
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	validateJSON(t, schema, raw, "agent.runState params with notices (real socket)")
	if !strings.Contains(string(raw), "stopped asking") {
		t.Fatalf("the terminal notification does not say the run stopped asking: %s", raw)
	}
}

// A real scoped policy gate with a store that refuses the widening write.
type scopedFailingPolicy struct {
	failingPolicyStore
	root string
}

func (p scopedFailingPolicy) Policy() content.EffectPolicy {
	m := p.failingPolicyStore.Policy()
	m.Observe.Scopes = []content.GrantScope{{Kind: content.ResourcePath, ID: p.root}}
	return m
}
