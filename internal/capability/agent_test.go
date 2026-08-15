package capability_test

// The agent operation (nocx-f4s5): one operation, one gate — the content
// domain, because the ask transaction is the ledger. A handler constructed
// with an AgentOperation can reach exactly the ledger's ask seam and nothing
// else; an escaped service dies with its operation.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

func newAgentOp(t *testing.T) capability.AgentOperation {
	t.Helper()
	configGate, _, contentGate, _, _, _ := testGates()
	_ = configGate
	op := capability.NewAgentOperation(contentGate, testLane(), content.NewStub(log.NewSlogAdapter(nil)))
	return op
}

// Inside Run the service reaches the ledger seam (the stub answers
// ErrNotImplemented — the point is that the call ARRIVES, gate held).
func TestAgentOperation_RunsAgainstTheLedgerSeam(t *testing.T) {
	op := newAgentOp(t)
	err := op.Run(context.Background(), func(ctx context.Context, svc capability.AgentService) error {
		if _, err := svc.CaptureFrame(ctx, content.CaptureFrame{CaptureID: "c", Client: "x"}); !errors.Is(err, content.ErrNotImplemented) {
			t.Errorf("CaptureFrame error = %v, want ErrNotImplemented (the ledger seam)", err)
		}
		if _, err := svc.SubmitAsk(ctx, content.AgentAsk{ID: "a", Client: "x", Question: "q"}); !errors.Is(err, content.ErrNotImplemented) {
			t.Errorf("SubmitAsk error = %v, want ErrNotImplemented (the ledger seam)", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// The escaped-handle property: a service carried out of its Run can no
// longer touch the ledger.
func TestAgentServiceCannotEscapeCallback(t *testing.T) {
	op := newAgentOp(t)
	var leaked capability.AgentService
	err := op.Run(context.Background(), func(ctx context.Context, svc capability.AgentService) error {
		leaked = svc
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := leaked.CaptureFrame(context.Background(), content.CaptureFrame{CaptureID: "c", Client: "x"}); !errors.Is(err, capability.ErrOperationInactive) {
		t.Fatalf("escaped agent service still usable after Run: err=%v", err)
	}
	if _, err := leaked.SubmitAsk(context.Background(), content.AgentAsk{ID: "a", Client: "x", Question: "q"}); !errors.Is(err, capability.ErrOperationInactive) {
		t.Fatalf("escaped agent service still usable after Run: err=%v", err)
	}
}
