package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// Exercise the model-facing kernel entry, not the spelling of its dispatch
// call. The real session.list executor reaches this source only if authority
// permits execution.
func TestKernelEntryPreservesAuthorityOutcomes(t *testing.T) {
	for _, decision := range []content.Decision{
		content.DecisionRefuse, content.DecisionPermit, content.DecisionAsk,
	} {
		t.Run(string(decision), func(t *testing.T) {
			policy := autonomousMatrix()
			policy.Observe.Decision = decision
			grant := policy.AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: "pane-1"}})
			kernel := kernelFor(t, grant, &fakeLedger{})
			kernel.runCtx.Session = "pane-1"
			source := &kernelEntrySessionSource{}
			kernel.runSeams.sessions = source

			result, err := kernel.invokeClassified(context.Background(), "session.list", "call-1", `{}`)
			switch decision {
			case content.DecisionRefuse:
				if err != nil {
					t.Fatalf("refusal became a run error: %v", err)
				}
				want := modelResult{text: refusalResult("session.list", RefusedByDecision, ""), kind: modelNocxMessage}
				if result != want {
					t.Errorf("refusal = %+v, want unchanged nocx result %+v", result, want)
				}
			case content.DecisionPermit:
				if err != nil {
					t.Fatalf("permitted invocation: %v", err)
				}
				want := modelResult{text: `{"sessionId":"pane-1","items":[]}`, kind: modelToolOutput}
				if result != want {
					t.Errorf("permitted result = %+v, want %+v", result, want)
				}
			case content.DecisionAsk:
				var suspended *ApprovalRequestedError
				if !errors.As(err, &suspended) {
					t.Fatalf("ask error = %v, want ApprovalRequestedError", err)
				}
				if suspended.Request == nil {
					t.Fatal("suspension lost its approval request")
				}
				req := suspended.Request
				if req.Tool != "session.list" || req.CallID != "call-1" || req.Arguments != `{}` || req.Effect != content.EffectObserve {
					t.Errorf("suspension request lost the proposed call: %+v", req)
				}
				if result != (modelResult{}) {
					t.Errorf("suspended invocation returned a model result: %+v", result)
				}
			}
			wantCalls := 0
			if decision == content.DecisionPermit {
				wantCalls = 1
			}
			if source.calls != wantCalls {
				t.Errorf("session.list executed %d times, want %d", source.calls, wantCalls)
			}
		})
	}
}

type kernelEntrySessionSource struct {
	calls int
}

func (s *kernelEntrySessionSource) ListSessionItems(_ context.Context, _ string, _ int) (SessionItems, error) {
	s.calls++
	return SessionItems{}, nil
}

func (*kernelEntrySessionSource) ReadSessionItem(context.Context, string, string, int, int) (SessionItemRead, error) {
	return SessionItemRead{}, errors.New("unexpected session.read call")
}
