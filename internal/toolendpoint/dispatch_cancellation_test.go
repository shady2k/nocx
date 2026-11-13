package toolendpoint

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tools "github.com/shady2k/nocx/contracts/tools"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/workers"
)

type cancellationWorkerRecord struct {
	contractWorkerRecord
	calls atomic.Int32
}

func (r *cancellationWorkerRecord) HeldBy(ctx context.Context, session string) ([]workers.Participant, error) {
	r.calls.Add(1)
	return r.contractWorkerRecord.HeldBy(ctx, session)
}

func TestEndpointRealDispatcherPreservesAbandonment(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name, cause := "canceled", "context was canceled"
		if expired {
			name, cause = "expired", "context expired"
		}
		t.Run(name, func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			if expired {
				ctx, cancel = context.WithDeadline(context.Background(), time.Unix(0, 0))
			} else {
				ctx, cancel = context.WithCancel(context.Background())
				cancel()
			}
			defer cancel()
			registry, err := agenttools.Assemble(tools.Schemas)
			if err != nil {
				t.Fatalf("assemble tools: %v", err)
			}
			record := &cancellationWorkerRecord{}
			dispatcher, err := assistant.NewToolDispatcher(registry, record, content.EnvironmentIDFor(content.EnvLocal, ""))
			if err != nil {
				t.Fatalf("new dispatcher: %v", err)
			}
			auth := &testAuthorizer{inv: assistant.ToolInvocation{
				Context:    ctx,
				RunContext: agenttools.RunContext{RunID: "run-1", Session: "session-1"},
				Grant:      contractGrant(),
			}}
			endpoint := startEndpoint(t, Config{
				Dir: t.TempDir(), Peers: testPeers{uid: 1000, pid: 1234},
				Owner: testOwner{uid: 1000}, SelfUID: 1000,
				Auth: auth, Dispatch: dispatcher, Logger: logtest.Slog(t),
			})
			conn := dialEndpoint(t, endpoint)
			t.Cleanup(func() { _ = conn.Close() })
			if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"workers.holdings","params":{}}`+"\n"); err != nil {
				t.Fatalf("write request: %v", err)
			}
			response := readResponse(t, conn)
			if response.Error == nil || response.Error.Code != rpcDomainError || response.Error.Message != "call abandoned" {
				t.Fatalf("response error = %+v, want call abandoned", response.Error)
			}
			if !strings.Contains(response.Error.Data.Reason, cause) || !strings.Contains(response.Error.Data.Reason, "Calling it again is legitimate") {
				t.Errorf("response reason = %q, want %q and retry guidance", response.Error.Data.Reason, cause)
			}
			if got := record.calls.Load(); got != 0 {
				t.Errorf("worker calls = %d, want zero after context ended", got)
			}
		})
	}
}
