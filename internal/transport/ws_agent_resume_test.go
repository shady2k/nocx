package transport

// A session-scoped answer refreshes the real kernel's authority: the second
// tool call in the same actor turn executes without another question.
import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

func TestAgentApprove_TheRunCarriesTheFreshGrantAfterAnAnswer(t *testing.T) {
	h := suspendedRunWith(t, askPolicyStore(t), protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepTool, Name: "session.read", Arguments: map[string]any{}},
		{Kind: protocol.StepTool, Name: "session.read", Arguments: map[string]any{}},
		{Kind: protocol.StepAnswer, Text: "both reads completed"},
		{Kind: protocol.StepEnd, Model: "fixture-model"},
	}})
	h.ws.pendingRunsMu.Lock()
	before := h.ws.pendingRuns[h.runID].grant
	h.ws.pendingRunsMu.Unlock()
	if before == nil || before.Policy.DecisionFor(content.EffectObserve) != content.DecisionAsk {
		t.Fatalf("initial grant = %+v, want observe ask", before)
	}
	h.approve(t, "session")
	// An unrefreshed second call would stop here on approvalRequested, not End.
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var state struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &state); err != nil || state.State != "completed" {
		t.Fatalf("run state = %s (%v)", raw, err)
	}
	entries, err := h.db.Ledger().Caused(t.Context(), h.entryID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, entry := range entries {
		if entry.Kind != content.EntryAction || entry.Intent != "session.read" {
			continue
		}
		detail, err := h.db.Ledger().Entry(t.Context(), entry.EntryID)
		if err != nil || detail == nil || detail.Status != content.EntrySuccess {
			t.Fatalf("read action = %+v (%v)", detail, err)
		}
		calls++
	}
	if calls != 2 {
		t.Fatalf("completed reads = %d, want two", calls)
	}
	if got := h.ws.runGrantFor(h.sid).Policy.DecisionFor(content.EffectObserve); got != content.DecisionPermit {
		t.Fatalf("fresh session grant observe = %q", got)
	}
}
