package toolendpoint

// A CALL STOPPED THROUGH THE ENDPOINT IS RECORDED AS STOPPED, NOT AS FAILED
// (nocx-a62cl.4.2, closing nocx-a62cl.4.1's finding 2).
//
// The endpoint's attempt record was the last place that called every
// unsuccessful ending "failed": a call whose caller sent calls.cancel and whose
// executor therefore returned its cancelled context was written down as a tool
// that broke. The in-process path had already been taught the difference
// (effectKernel.closeFailedAttempt, nocx-a62cl.4.1); this is the same verdict
// through the same predicate, and a genuine failure still reads as one.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/waittest"
)

// endingDispatcher is the tool surface these tests need: one call only its
// context can end, and one that fails for its own reason. It reports each
// ending, because the attempt's verdict must follow the ending the executor
// actually had.
type endingDispatcher struct {
	started chan string
	ended   chan error
}

func (d *endingDispatcher) Dispatch(inv assistant.ToolInvocation) (string, error) {
	d.started <- inv.Method
	if inv.Method == "alpha.fail" {
		err := errors.New("the tool broke")
		d.ended <- err
		return "", err
	}
	<-inv.Context.Done()
	d.ended <- inv.Context.Err()
	return "", inv.Context.Err()
}

func (d *endingDispatcher) Catalogue(content.Grant) []agenttools.Tool { return nil }

// startEndingEndpoint wires the real attempt-recording dispatcher over the
// real socket: the attempt is recorded by the product's own adapter, and the
// only doubles are the ledger and the tool surface.
func startEndingEndpoint(t *testing.T, ledger *endpointAttemptLedger) (*Endpoint, *endingDispatcher) {
	t.Helper()
	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble worker tools: %v", err)
	}
	dispatch := &endingDispatcher{started: make(chan string, 4), ended: make(chan error, 4)}
	dispatcher, err := assistant.NewAttemptRecordingDispatcher(registry, dispatch, ledger)
	if err != nil {
		t.Fatalf("new attempt-recording dispatcher: %v", err)
	}
	endpoint := startEndpoint(t, Config{
		Dir:     filepath.Join(t.TempDir(), "runtime"),
		Peers:   testPeers{uid: 1000, pid: 1234},
		Owner:   testOwner{uid: 1000},
		SelfUID: 1000,
		Auth: &testAuthorizer{inv: assistant.ToolInvocation{
			Context:    context.Background(),
			RunContext: agenttools.RunContext{Session: "session-1"},
			Grant:      contractGrant(),
		}},
		Dispatch: dispatcher,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return endpoint, dispatch
}

// endedAttempt waits for the executor's own report and then for the ledger's
// record of it, returning that record.
func endedAttempt(t *testing.T, dispatch *endingDispatcher, ledger *endpointAttemptLedger) content.FinishExecution {
	t.Helper()
	select {
	case <-dispatch.ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the call never ended")
	}
	var finished content.FinishExecution
	waittest.WaitFor(t, "the attempt to close", func() bool {
		ledger.mu.Lock()
		defer ledger.mu.Unlock()
		if len(ledger.finished) == 0 {
			return false
		}
		finished = ledger.finished[len(ledger.finished)-1]
		return true
	})
	return finished
}

// TestACancelledEndpointCallRecordsItsAttemptAsInterrupted is the finding:
// the cancel reaches the call's context, and the attempt says the call was
// stopped rather than that the tool failed.
func TestACancelledEndpointCallRecordsItsAttemptAsInterrupted(t *testing.T) {
	ledger := &endpointAttemptLedger{}
	endpoint, dispatch := startEndingEndpoint(t, ledger)
	client := dialClient(t, endpoint)

	client.request(1, "alpha.hold", `{}`)
	select {
	case method := <-dispatch.started:
		if method != "alpha.hold" {
			t.Fatalf("the call started as %q", method)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call never started")
	}
	client.notify("calls.cancel", `{"requestId":1}`)

	finished := endedAttempt(t, dispatch, ledger)
	if finished.TerminationReason != content.TermInterrupted || finished.Status != content.EntryInterrupted {
		t.Fatalf("the cancelled call's attempt reads %s/%s, want interrupted/interrupted",
			finished.TerminationReason, finished.Status)
	}
}

// TestAFailedEndpointCallStillRecordsItsAttemptAsFailed is the paired half:
// the same record, for a call that really failed, still says so — the change
// above must not turn every unsuccessful ending into a stop.
func TestAFailedEndpointCallStillRecordsItsAttemptAsFailed(t *testing.T) {
	ledger := &endpointAttemptLedger{}
	endpoint, dispatch := startEndingEndpoint(t, ledger)
	client := dialClient(t, endpoint)

	client.request(1, "alpha.fail", `{}`)
	select {
	case <-dispatch.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the call never started")
	}

	finished := endedAttempt(t, dispatch, ledger)
	if finished.TerminationReason != content.TermFailed || finished.Status != content.EntryFailure {
		t.Fatalf("the failed call's attempt reads %s/%s, want failed/failure",
			finished.TerminationReason, finished.Status)
	}
}
