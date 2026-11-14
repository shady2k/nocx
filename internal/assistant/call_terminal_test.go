package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

type terminalLedger struct {
	*fakeLedger
	entered chan struct{}
	release chan struct{}
}

func (l *terminalLedger) FinishExecution(ctx context.Context, id int64, end content.FinishExecution) error {
	close(l.entered)
	<-l.release
	return l.fakeLedger.FinishExecution(ctx, id, end)
}

func TestAttemptTerminalCompletionWinsCancellationDuringRecording(t *testing.T) {
	for _, dispatchErr := range []error{nil, errors.New("tool failed")} {
		t.Run(fmtTerminalCase(dispatchErr), func(t *testing.T) {
			ledger := &terminalLedger{fakeLedger: &fakeLedger{}, entered: make(chan struct{}), release: make(chan struct{})}
			reg, err := agenttools.Assemble(toolsDirFS(t))
			if err != nil {
				t.Fatal(err)
			}
			var inner ToolDispatcher = succeedingToolDispatcher{result: "answer"}
			if dispatchErr != nil {
				inner = refusingToolDispatcher{err: dispatchErr}
			}
			dispatcher, err := NewAttemptRecordingDispatcher(reg, inner, ledger)
			if err != nil {
				t.Fatal(err)
			}
			terminal := &CallTerminal{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			invocation := workerInvocationForTest("workers.holdings", workerGrantForTest(), `{}`)
			invocation.Context, invocation.Terminal = ctx, terminal
			done := make(chan struct{})
			var result string
			var got error
			go func() { result, got = dispatcher.Dispatch(invocation); close(done) }()
			<-ledger.entered
			won := terminal.Cancel(context.Canceled)
			// A parent context may still end independently; recording cannot consult it again.
			cancel()
			close(ledger.release)
			<-done
			if won {
				t.Fatal("cancel rewrote a completed call during recording")
			}
			if got != dispatchErr {
				t.Fatalf("delivery=%v, want frozen error %v", got, dispatchErr)
			}
			if terminal.Delivery(ctx, got) != dispatchErr {
				t.Fatal("delivery changed the recorded verdict")
			}
			want := "finish:success"
			if dispatchErr != nil {
				want = "finish:failure"
			} else if result != "answer" {
				t.Fatalf("result=%q", result)
			}
			calls := ledger.calls()
			if calls[len(calls)-1] != want {
				t.Fatalf("ledger=%v, want %s", calls, want)
			}
		})
	}
}

func fmtTerminalCase(err error) string {
	if err != nil {
		return "failure"
	}
	return "success"
}

func TestCallTerminalCancellationWinsCompletion(t *testing.T) {
	terminal := &CallTerminal{}
	cause := TurnEndedCause(content.TermUserKilled)
	if !terminal.Cancel(cause) {
		t.Fatal("pending cancellation refused")
	}
	if got := terminal.Complete(context.Background(), nil); got != cause {
		t.Fatalf("verdict=%v, want %v", got, cause)
	}
	if terminal.Cancel(context.Canceled) {
		t.Fatal("terminal cancellation accepted twice")
	}
}

type failingTerminalLedger struct {
	*fakeLedger
	err error
}

func (l failingTerminalLedger) FinishExecution(context.Context, int64, content.FinishExecution) error {
	return l.err
}

func TestAttemptTerminalDeliveryPreservesRecordingFailure(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatal(err)
	}
	ledgerErr := errors.New("ledger unavailable")
	dispatcher, err := NewAttemptRecordingDispatcher(reg, succeedingToolDispatcher{result: "answer"}, failingTerminalLedger{&fakeLedger{}, ledgerErr})
	if err != nil {
		t.Fatal(err)
	}
	terminal := &CallTerminal{}
	invocation := workerInvocationForTest("workers.holdings", workerGrantForTest(), `{}`)
	invocation.Terminal = terminal
	out, err := dispatcher.Dispatch(invocation)
	if out != "" || !errors.Is(err, ledgerErr) {
		t.Fatalf("dispatch=%q/%v, want recording error", out, err)
	}
	if got := terminal.Delivery(context.Background(), err); !errors.Is(got, ledgerErr) {
		t.Fatalf("delivery=%v, lost recording error %v", got, err)
	}
}

func TestCallTerminalDeliveryWithholdsUncooperativeCancelledDispatcher(t *testing.T) {
	terminal := &CallTerminal{}
	terminal.Cancel(context.Canceled)
	if got := terminal.Delivery(context.Background(), nil); !errors.Is(got, context.Canceled) {
		t.Fatalf("delivery=%v", got)
	}
}
