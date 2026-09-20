package app

// The nocx-xn63t.1 review's blocker 1, held at the layer the product's own
// coordinators actually cross: callerGrant is the grant every admitted
// external coordinator is minted with, and it permits delegate while it
// refuses mutate-reversible. Under that mint a worktree spawn — which creates
// a branch and a linked checkout, a reversible mutation — must be REFUSED
// before anything is created, and a plain spawn must still go through: the
// coordinator's decision was that requiring mutation may not take delegation
// away. The refusal arrives from the dispatch layer's effect gate, which runs
// before any capability is constructed and before any side effect exists, so
// the refusal type itself is the proof of "before side effects".

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/workers"
)

// spawnPolicyRecord answers the worker seam just well enough for one spawn to
// travel the whole dispatch path: Register records what reached it — which is
// the observable the before-side-effects claim is judged on — and everything
// else answers zero, because this test decides policy, not record behaviour.
type spawnPolicyRecord struct {
	registered []workers.RegisterRequest
}

func (r *spawnPolicyRecord) Register(_ context.Context, req workers.RegisterRequest) (workers.Registration, error) {
	r.registered = append(r.registered, req)
	return workers.Registration{Participant: workers.Participant{ID: "p-1", State: workers.StateLive}}, nil
}

func (*spawnPolicyRecord) HeldBy(context.Context, string) ([]workers.Participant, error) {
	return nil, nil
}

func (*spawnPolicyRecord) Say(context.Context, workers.ID, workers.ReaderID, workers.ReaderID, string) (workers.Message, error) {
	return workers.Message{}, nil
}

func (*spawnPolicyRecord) Report(context.Context, workers.ParticipantID, workers.Report) (workers.Message, error) {
	return workers.Message{}, nil
}

func (*spawnPolicyRecord) Inbox(context.Context, workers.ReaderID, workers.ReaderID, int) (workers.Fetch, error) {
	return workers.Fetch{}, nil
}

func (*spawnPolicyRecord) Undelivered(context.Context, workers.ID) ([]workers.Message, error) {
	return nil, nil
}

func (*spawnPolicyRecord) Acknowledge(context.Context, workers.ReaderID, workers.ReaderID, int64) error {
	return nil
}

func (*spawnPolicyRecord) Close(context.Context, string, workers.ParticipantID) (workers.CloseResult, error) {
	return workers.CloseResult{}, nil
}
func (*spawnPolicyRecord) Undispatched() []workers.Fact { return nil }
func (*spawnPolicyRecord) LeftoverCheckouts(context.Context, string) workers.CheckoutSurvey {
	return workers.CheckoutSurvey{Complete: true}
}

func (*spawnPolicyRecord) RemoveCheckouts(context.Context, string, []workers.CheckoutRef) workers.CheckoutRemoval {
	return workers.CheckoutRemoval{}
}

func dispatchSpawnWithGrant(t *testing.T, rec *spawnPolicyRecord, grant content.Grant, params string) error {
	t.Helper()
	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	dispatcher, err := assistant.NewToolDispatcher(registry, rec, "env-local")
	if err != nil {
		t.Fatalf("new tool dispatcher: %v", err)
	}
	_, err = dispatcher.Dispatch(assistant.ToolInvocation{
		Context:    context.Background(),
		Method:     "workers.spawn",
		RunContext: agenttools.RunContext{Session: "sess-coordinator"},
		Grant:      grant,
		RawParams:  []byte(params),
	})
	return err
}

func TestSpawnUnderTheProductMintRefusesTheWorktreeAndAllowsThePlain(t *testing.T) {
	grant := callerGrant(session.ID("sess-coordinator"), "env-local", workerTestWorkspace)

	plainRec := &spawnPolicyRecord{}
	if plainErr := dispatchSpawnWithGrant(t, plainRec, grant, `{"command":"claude","task":"read it"}`); plainErr != nil {
		var plainRefusal *assistant.DispatchRefusalError
		if errors.As(plainErr, &plainRefusal) {
			t.Fatalf("plain spawn refused by the effect gate: %s — requiring mutation took delegation away", plainRefusal.Reason)
		}
		t.Fatalf("plain spawn under the product mint: %v", plainErr)
	}
	if len(plainRec.registered) != 1 {
		t.Fatalf("plain spawn registered %d workers, want 1", len(plainRec.registered))
	}

	worktreeRec := &spawnPolicyRecord{}
	worktreeErr := dispatchSpawnWithGrant(t, worktreeRec, grant, `{"command":"claude","task":"read it","worktree":{"branch":"feat/x"}}`)
	var refusal *assistant.DispatchRefusalError
	if !errors.As(worktreeErr, &refusal) {
		t.Fatalf("worktree spawn under the product mint: err = %v, want the effect gate's refusal", worktreeErr)
	}
	if !strings.Contains(refusal.Reason, "mutate-reversible") {
		t.Fatalf("refusal = %q, want it to name the refused mutate-reversible row", refusal.Reason)
	}
	if len(worktreeRec.registered) != 0 {
		t.Fatalf("the refused spawn reached the record and registered %+v — the checkout would already exist", worktreeRec.registered)
	}
}
