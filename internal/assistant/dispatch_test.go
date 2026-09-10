package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/workers"
)

func toolDispatcherForTest(t *testing.T, rec WorkerRecord) ToolDispatcher {
	t.Helper()
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	dispatcher, err := NewToolDispatcher(reg, rec, content.EnvironmentIDFor(content.EnvLocal, ""))
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	return dispatcher
}

func workerInvocationForTest(method string, grant content.Grant, params string) ToolInvocation {
	return ToolInvocation{
		Context: context.Background(),
		Method:  method,
		RunContext: agenttools.RunContext{
			RunID:   "run-1",
			Session: "session-1",
		},
		Grant:     grant,
		RawParams: json.RawMessage(params),
	}
}

func workerGrantForTest() content.Grant {
	return autonomousMatrix().AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-1"},
		{Kind: content.ResourceEnvironment, ID: content.EnvironmentIDFor(content.EnvLocal, "")},
	})
}

func TestToolDispatcher_GrantWithoutEnvironmentLeavesSpawnUnreachable(t *testing.T) {
	rec := &fakeWorkerRecord{}
	dispatcher := toolDispatcherForTest(t, rec)
	grant := autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-1"}})

	_, err := dispatcher.Dispatch(workerInvocationForTest(
		"workers.spawn", grant, `{"command":"claude","task":"read it"}`,
	))
	if !errors.Is(err, ErrUnreachableMethod) {
		t.Fatalf("spawn without environment scope: %v, want ErrUnreachableMethod", err)
	}
	if len(rec.registered) != 0 {
		t.Fatalf("spawn reached executor with an unreachable grant: %+v", rec.registered)
	}
}

func TestToolDispatcher_RejectsMalformedParamsBeforeExecutor(t *testing.T) {
	cases := []struct {
		name   string
		method string
		params string
	}{
		{name: "unknown field", method: "workers.spawn", params: `{"command":"claude","task":"read it","extra":true}`},
		{name: "missing required", method: "workers.spawn", params: `{"command":"claude"}`},
		{name: "wrong type", method: "workers.spawn", params: `{"command":7,"task":"read it"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &fakeWorkerRecord{}
			dispatcher := toolDispatcherForTest(t, rec)
			_, err := dispatcher.Dispatch(workerInvocationForTest(tc.method, workerGrantForTest(), tc.params))
			if !errors.Is(err, ErrInvalidParams) {
				t.Fatalf("dispatch error = %v, want ErrInvalidParams", err)
			}
			if len(rec.registered) != 0 {
				t.Fatalf("malformed params reached executor: %+v", rec.registered)
			}
		})
	}
}

func TestToolDispatcher_UnknownMethodIsNamed(t *testing.T) {
	dispatcher := toolDispatcherForTest(t, &fakeWorkerRecord{})
	_, err := dispatcher.Dispatch(workerInvocationForTest("workers.unknown", workerGrantForTest(), `{}`))
	if !errors.Is(err, ErrUnknownMethod) {
		t.Fatalf("unknown method error = %v, want ErrUnknownMethod", err)
	}
}

func TestDispatchAbortedOutcomeRetainsTypedReason(t *testing.T) {
	reason := "workers.spawn refused: approval required"
	err := dispatchAbortedError(ToolInvocation{Method: "workers.spawn"}, &modelResult{text: reason})
	var refusal *DispatchRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %T %v, want DispatchRefusalError", err, err)
	}
	if refusal.Method != "workers.spawn" || refusal.Reason != reason {
		t.Fatalf("refusal = %+v, want method and reason retained", refusal)
	}
}

func TestToolDispatcher_ReachesEveryWorkerExecutor(t *testing.T) {
	cases := []struct {
		name   string
		method string
		params string
	}{
		{name: "holdings", method: "workers.holdings", params: `{}`},
		{name: "spawn", method: "workers.spawn", params: `{"command":"claude","task":"read it"}`},
		{name: "say", method: "workers.say", params: `{"worker":"p-1","message":"start"}`},
		{name: "wait", method: "workers.wait", params: `{"seconds":1}`},
		{name: "close", method: "workers.close", params: `{"worker":"p-1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &fakeWorkerRecord{held: []workers.Participant{{ID: "p-1", Group: "session-1", State: workers.StateLive}}}
			dispatcher := toolDispatcherForTest(t, rec)
			if _, err := dispatcher.Dispatch(workerInvocationForTest(tc.method, workerGrantForTest(), tc.params)); err != nil {
				t.Fatalf("dispatch %s: %v", tc.method, err)
			}
			switch tc.method {
			case "workers.holdings":
				if len(rec.heldFor) != 1 {
					t.Fatalf("holdings executor was not reached: %+v", rec)
				}
			case "workers.spawn":
				if len(rec.registered) != 1 {
					t.Fatalf("spawn executor was not reached: %+v", rec)
				}
			case "workers.say":
				if len(rec.sent) != 1 {
					t.Fatalf("say executor was not reached: %+v", rec)
				}
			case "workers.wait":
				if len(rec.waitedFor) != 1 {
					t.Fatalf("wait executor was not reached: %+v", rec)
				}
			case "workers.close":
				if len(rec.closed) != 1 {
					t.Fatalf("close executor was not reached: %+v", rec)
				}
			}
		})
	}
}

type refusingToolDispatcher struct {
	err error
}

func (d refusingToolDispatcher) Dispatch(ToolInvocation) (string, error) {
	return "", d.err
}

func (d refusingToolDispatcher) Catalogue(content.Grant) []agenttools.Tool {
	return nil
}

func TestAttemptRecordingDispatcherRecordsRefusedDispatch(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	ledger := &fakeLedger{}
	dispatcher, err := NewAttemptRecordingDispatcher(
		reg,
		refusingToolDispatcher{err: ErrUnreachableMethod},
		ledger,
	)
	if err != nil {
		t.Fatalf("new attempt-recording dispatcher: %v", err)
	}

	invocation := workerInvocationForTest(
		"workers.holdings",
		workerGrantForTest(),
		`{}`,
	)
	invocation.RunContext.RunID = ""
	_, err = dispatcher.Dispatch(invocation)
	if !errors.Is(err, ErrUnreachableMethod) {
		t.Fatalf("dispatch error = %v, want ErrUnreachableMethod", err)
	}
	if got := ledger.started(); got != 1 {
		t.Fatalf("started attempts = %d, want 1 for the refused call", got)
	}
	submissions := ledger.recordedSubmissions()
	if len(submissions) != 1 {
		t.Fatalf("submissions = %+v, want one attempt entry", submissions)
	}
	var payload struct {
		Tool   string         `json:"tool"`
		Effect content.Effect `json:"effect"`
		RunID  string         `json:"runId"`
	}
	if err := json.Unmarshal([]byte(submissions[0].payload), &payload); err != nil {
		t.Fatalf("attempt payload = %q: %v", submissions[0].payload, err)
	}
	if payload.Tool != "workers.holdings" || payload.Effect != content.EffectObserve || payload.RunID != "" {
		t.Fatalf("attempt payload = %+v, want holdings/observe with no run id", payload)
	}

	calls := ledger.calls()
	want := []string{
		"ensure-env",
		"observe",
		"submit:workers.holdings",
		"start:entry-workers.holdings",
		"finish:failure",
	}
	if len(calls) != len(want) {
		t.Fatalf("ledger calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("ledger call %d = %q, want %q; all calls = %v", i, calls[i], want[i], calls)
		}
	}
}

// succeedingToolDispatcher is the inner dispatcher for a call that already
// produced its answer by the time the invocation context is examined again —
// the shape nocx-uhii1's detachment tests need.
type succeedingToolDispatcher struct {
	result string
}

func (d succeedingToolDispatcher) Dispatch(ToolInvocation) (string, error) {
	return d.result, nil
}

func (d succeedingToolDispatcher) Catalogue(content.Grant) []agenttools.Tool {
	return nil
}

// A completed call must survive a caller who has already stopped waiting for
// it (nocx-uhii1): a person can interrupt the session holding a call, which
// cancels the invocation's context, after the inner dispatcher already
// produced its answer. Recording that outcome is bookkeeping, not the
// caller's business, and must not be able to turn the completed call into a
// reported failure.
func TestAttemptRecordingDispatcherSuccessSurvivesAnAbandonedInvocationContext(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	ledger := &fakeLedger{}
	dispatcher, err := NewAttemptRecordingDispatcher(
		reg,
		succeedingToolDispatcher{result: "the answer"},
		ledger,
	)
	if err != nil {
		t.Fatalf("new attempt-recording dispatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the caller has already stopped waiting by the time this runs
	invocation := workerInvocationForTest("workers.holdings", workerGrantForTest(), `{}`)
	invocation.Context = ctx

	result, err := dispatcher.Dispatch(invocation)
	if err != nil {
		t.Fatalf("dispatch error = %v, want the completed call's own result, not an error manufactured by the caller having stopped waiting for it", err)
	}
	if result != "the answer" {
		t.Fatalf("result = %q, want the completed call's own result", result)
	}

	calls := ledger.calls()
	if len(calls) == 0 || calls[len(calls)-1] != "finish:success" {
		t.Fatalf("ledger calls = %v, want the outcome recorded as a success", calls)
	}

	records := ledger.finishCallRecords()
	if len(records) != 1 {
		t.Fatalf("finish calls recorded = %d, want exactly one", len(records))
	}
	if records[0].errAtCall != nil {
		t.Fatalf("outcome recorded on a context with Err()=%v at call time — it inherited the caller's cancellation instead of detaching from it", records[0].errAtCall)
	}
	if !records[0].hasDeadline {
		t.Fatalf("outcome recorded on a context with no deadline of its own — a wedged ledger could hang the dispatcher")
	}
}

// The failure twin of the test above: the inner dispatcher's own failure must
// still be recorded (not silently dropped) when the invocation context is
// already cancelled, and the original error must reach the caller unchanged.
func TestAttemptRecordingDispatcherFailureRecordedWithAnAbandonedInvocationContext(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	ledger := &fakeLedger{}
	innerErr := errors.New("dispatch: the tool itself failed")
	dispatcher, err := NewAttemptRecordingDispatcher(
		reg,
		refusingToolDispatcher{err: innerErr},
		ledger,
	)
	if err != nil {
		t.Fatalf("new attempt-recording dispatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	invocation := workerInvocationForTest("workers.holdings", workerGrantForTest(), `{}`)
	invocation.Context = ctx

	_, err = dispatcher.Dispatch(invocation)
	if !errors.Is(err, innerErr) {
		t.Fatalf("dispatch error = %v, want the inner dispatcher's own error preserved", err)
	}

	calls := ledger.calls()
	if len(calls) == 0 || calls[len(calls)-1] != "finish:failure" {
		t.Fatalf("ledger calls = %v, want the failure outcome recorded despite the abandoned context", calls)
	}
	records := ledger.finishCallRecords()
	if len(records) != 1 || records[0].errAtCall != nil {
		t.Fatalf("outcome recorded on the caller's own cancelled context instead of a detached one: %+v", records)
	}
}

// AGENTS.md testing rule 3: for the external call detachedFinishContext
// makes, there is a test where that call fails. A ledger write that never
// returns must not be able to hang the dispatcher forever just because the
// caller who wanted the answer is gone — the detached context's own bound
// has to apply regardless.
func TestAttemptRecordingDispatcherFinishTimeoutBoundsAWedgedLedger(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	ledger := &fakeLedger{finishHang: true}
	dispatcher, err := NewAttemptRecordingDispatcher(
		reg,
		succeedingToolDispatcher{result: "the answer"},
		ledger,
	)
	if err != nil {
		t.Fatalf("new attempt-recording dispatcher: %v", err)
	}

	previous := finishAttemptTimeout
	finishAttemptTimeout = 50 * time.Millisecond
	t.Cleanup(func() { finishAttemptTimeout = previous })

	invocation := workerInvocationForTest("workers.holdings", workerGrantForTest(), `{}`)

	done := make(chan struct{})
	var dispatchErr error
	go func() {
		_, dispatchErr = dispatcher.Dispatch(invocation)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not return — a wedged ledger write hung the dispatcher instead of being bounded")
	}
	if dispatchErr == nil {
		t.Fatal("dispatch error = nil, want the bound to surface as an error since the ledger write never actually finished")
	}
}
