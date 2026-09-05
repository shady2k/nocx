package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/wave"
)

func waveDispatcherForTest(t *testing.T, rec WaveRecord) WaveDispatcher {
	t.Helper()
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	dispatcher, err := NewWaveDispatcher(reg, rec, content.EnvironmentIDFor(content.EnvLocal, ""))
	if err != nil {
		t.Fatalf("new wave dispatcher: %v", err)
	}
	return dispatcher
}

func waveInvocationForTest(method string, grant content.Grant, params string) WaveInvocation {
	return WaveInvocation{
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

func waveGrantForTest() content.Grant {
	return autonomousMatrix().AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-1"},
		{Kind: content.ResourceEnvironment, ID: content.EnvironmentIDFor(content.EnvLocal, "")},
	})
}

func TestWaveDispatcher_GrantWithoutEnvironmentLeavesSpawnUnreachable(t *testing.T) {
	rec := &fakeWaveRecord{}
	dispatcher := waveDispatcherForTest(t, rec)
	grant := autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-1"}})

	_, err := dispatcher.Dispatch(waveInvocationForTest(
		"wave.spawn", grant, `{"command":"claude","task":"read it"}`,
	))
	if !errors.Is(err, ErrUnreachableMethod) {
		t.Fatalf("spawn without environment scope: %v, want ErrUnreachableMethod", err)
	}
	if len(rec.registered) != 0 {
		t.Fatalf("spawn reached executor with an unreachable grant: %+v", rec.registered)
	}
}

func TestWaveDispatcher_RejectsMalformedParamsBeforeExecutor(t *testing.T) {
	cases := []struct {
		name   string
		method string
		params string
	}{
		{name: "unknown field", method: "wave.spawn", params: `{"command":"claude","task":"read it","extra":true}`},
		{name: "missing required", method: "wave.spawn", params: `{"command":"claude"}`},
		{name: "wrong type", method: "wave.spawn", params: `{"command":7,"task":"read it"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &fakeWaveRecord{}
			dispatcher := waveDispatcherForTest(t, rec)
			_, err := dispatcher.Dispatch(waveInvocationForTest(tc.method, waveGrantForTest(), tc.params))
			if !errors.Is(err, ErrInvalidParams) {
				t.Fatalf("dispatch error = %v, want ErrInvalidParams", err)
			}
			if len(rec.registered) != 0 {
				t.Fatalf("malformed params reached executor: %+v", rec.registered)
			}
		})
	}
}

func TestWaveDispatcher_UnknownMethodIsNamed(t *testing.T) {
	dispatcher := waveDispatcherForTest(t, &fakeWaveRecord{})
	_, err := dispatcher.Dispatch(waveInvocationForTest("wave.unknown", waveGrantForTest(), `{}`))
	if !errors.Is(err, ErrUnknownMethod) {
		t.Fatalf("unknown method error = %v, want ErrUnknownMethod", err)
	}
}

func TestDispatchAbortedOutcomeRetainsTypedReason(t *testing.T) {
	reason := "wave.spawn refused: approval required"
	err := dispatchAbortedError(WaveInvocation{Method: "wave.spawn"}, &modelResult{text: reason})
	var refusal *DispatchRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %T %v, want DispatchRefusalError", err, err)
	}
	if refusal.Method != "wave.spawn" || refusal.Reason != reason {
		t.Fatalf("refusal = %+v, want method and reason retained", refusal)
	}
}

func TestWaveDispatcher_ReachesEveryWaveExecutor(t *testing.T) {
	cases := []struct {
		name   string
		method string
		params string
	}{
		{name: "holdings", method: "wave.holdings", params: `{}`},
		{name: "spawn", method: "wave.spawn", params: `{"command":"claude","task":"read it"}`},
		{name: "say", method: "wave.say", params: `{"worker":"p-1","message":"start"}`},
		{name: "wait", method: "wave.wait", params: `{"seconds":1}`},
		{name: "close", method: "wave.close", params: `{"worker":"p-1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &fakeWaveRecord{held: []wave.Participant{{ID: "p-1", Wave: "session-1", State: wave.StateLive}}}
			dispatcher := waveDispatcherForTest(t, rec)
			if _, err := dispatcher.Dispatch(waveInvocationForTest(tc.method, waveGrantForTest(), tc.params)); err != nil {
				t.Fatalf("dispatch %s: %v", tc.method, err)
			}
			switch tc.method {
			case "wave.holdings":
				if len(rec.heldFor) != 1 {
					t.Fatalf("holdings executor was not reached: %+v", rec)
				}
			case "wave.spawn":
				if len(rec.registered) != 1 {
					t.Fatalf("spawn executor was not reached: %+v", rec)
				}
			case "wave.say":
				if len(rec.sent) != 1 {
					t.Fatalf("say executor was not reached: %+v", rec)
				}
			case "wave.wait":
				if len(rec.waitedFor) != 1 {
					t.Fatalf("wait executor was not reached: %+v", rec)
				}
			case "wave.close":
				if len(rec.closed) != 1 {
					t.Fatalf("close executor was not reached: %+v", rec)
				}
			}
		})
	}
}
