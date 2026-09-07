package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

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
