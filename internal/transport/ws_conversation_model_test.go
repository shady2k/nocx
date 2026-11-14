package transport

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	agentharness "github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

func (*fakeAgentService) ConversationModel(context.Context, string, string) (string, error) {
	return "", nil
}

type modelPinHarness struct {
	agentharness.Harness
	mu     sync.Mutex
	specs  []agentharness.TurnSpec
	models []agentharness.Model
}

func newModelPinHarness(t *testing.T, models ...string) *modelPinHarness {
	t.Helper()
	askHarnessParallel(t)
	actor := newScriptedAgent(t, protocol.Script{Steps: []protocol.Step{{Kind: protocol.StepEnd, Model: models[0]}}})
	h := &modelPinHarness{Harness: actor.harness}
	for _, id := range models {
		h.models = append(h.models, agentharness.Model{ID: id})
	}
	return h
}

func (h *modelPinHarness) Models(context.Context) ([]agentharness.Model, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]agentharness.Model(nil), h.models...), nil
}

func (h *modelPinHarness) Run(ctx context.Context, spec agentharness.TurnSpec) (agentharness.Turn, error) {
	h.mu.Lock()
	h.specs = append(h.specs, spec)
	h.mu.Unlock()
	return h.Harness.Run(ctx, spec)
}

func (h *modelPinHarness) modelAt(i int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.specs[i].Model
}

func TestConversationRolePinsActualDefaultOverTheWire(t *testing.T) {
	backend := newModelPinHarness(t, "actual-model", "changed-role")
	h := newAskHarnessWithOpts(t, answerScript("unused"), WithAgentHarness("test", backend))
	sid := openLocalSession(t, h.conn)
	ask := func(id string) {
		t.Helper()
		_, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": id, "sessionId": sid, "question": "why?", "cwd": "/repo", "attachedContent": []any{}}, 2)
		if rpcErr != nil {
			t.Fatalf("ask: %+v", rpcErr)
		}
		readNotification(t, h.conn, "agent.runState", 5*time.Second)
	}
	ask("pin-first")
	if got := backend.modelAt(0); got != "" {
		t.Fatalf("unassigned = %q", got)
	}
	raw := jsonrpcCall(t, h.conn, "roles.assign", map[string]any{"agentId": "test", "role": "answering", "model": "changed-role"})
	if isErrorResponse(t, raw) {
		t.Fatalf("assign: %s", raw)
	}
	ask("pin-second")
	if got := backend.modelAt(1); got != "actual-model" {
		t.Fatalf("next model = %q", got)
	}
	backend.mu.Lock()
	backend.models = []agentharness.Model{{ID: "changed-role"}}
	backend.mu.Unlock()
	_, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "pin-third", "sessionId": sid, "question": "why?", "cwd": "/repo", "attachedContent": []any{}}, 3)
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "no longer") || !strings.Contains(rpcErr.Message, "actual-model") {
		t.Fatalf("removed pinned model: %+v", rpcErr)
	}
}

func TestConversationRoleChosenAndRemovedModelOverTheWire(t *testing.T) {
	backend := newModelPinHarness(t, "chosen")
	h := newAskHarnessWithOpts(t, answerScript("unused"), WithAgentHarness("test", backend))
	sid := openLocalSession(t, h.conn)
	raw := jsonrpcCall(t, h.conn, "roles.assign", map[string]any{"agentId": "test", "role": "answering", "model": "chosen"})
	if isErrorResponse(t, raw) {
		t.Fatalf("assign: %s", raw)
	}
	res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "chosen-first", "sessionId": sid, "question": "why?", "cwd": "/repo", "attachedContent": []any{}}, 2)
	if rpcErr != nil {
		t.Fatalf("ask: %+v", rpcErr)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if res.Model != "chosen" || backend.modelAt(0) != "chosen" {
		t.Fatalf("chosen response %+v model %q", res, backend.modelAt(0))
	}
	// A different pane/session has no conversation pin and resolves the role anew.
	sid2 := openLocalSession(t, h.conn)
	backend.mu.Lock()
	backend.models = nil
	backend.mu.Unlock()
	_, rpcErr = askOverWire(t, h.conn, map[string]any{"askId": "chosen-removed", "sessionId": sid2, "question": "why?", "cwd": "/repo", "attachedContent": []any{}}, 3)
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "chosen") {
		t.Fatalf("removed choice: %+v", rpcErr)
	}
}

type modelReadyHarness struct{ *modelPinHarness }

func (*modelReadyHarness) Readiness(context.Context) agentharness.Readiness {
	return agentharness.Readiness{State: agentharness.NotConfigured, Message: "native default unavailable"}
}

func (*modelReadyHarness) ReadinessForModel(_ context.Context, model string) agentharness.Readiness {
	if model == "chosen" {
		return agentharness.Readiness{State: agentharness.Ready}
	}
	return agentharness.Readiness{State: agentharness.NotConfigured, Message: "chosen model unavailable"}
}

func TestConversationRoleChosenDoesNotRequireNativeDefault(t *testing.T) {
	backend := &modelReadyHarness{modelPinHarness: newModelPinHarness(t, "chosen")}
	h := newAskHarnessWithOpts(t, answerScript("unused"), WithAgentHarness("test", backend))
	raw := jsonrpcCall(t, h.conn, "roles.assign", map[string]any{"agentId": "test", "role": "answering", "model": "chosen"})
	if isErrorResponse(t, raw) {
		t.Fatalf("assign: %s", raw)
	}
	sid := openLocalSession(t, h.conn)
	_, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "chosen-ready", "sessionId": sid, "question": "why?", "cwd": "/repo", "attachedContent": []any{}}, 2)
	if rpcErr != nil {
		t.Fatalf("chosen model refused: %+v", rpcErr)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
}

func TestConversationRoleMissingReportedModelIsVisible(t *testing.T) {
	svc := &fakeAgentService{}
	h := newGapHandlers(svc, nil, nil)
	h.harness = newScriptedAgent(t, answerScript("unused")).harness
	rc := gapRunContext()
	r := &gapResponder{}
	h.terminalize(t.Context(), rc, "completed", "completed", "", r)
	if r.runState == nil || len(r.runState.Notices) != 1 || !strings.Contains(r.runState.Notices[0], "cannot promise") {
		t.Fatalf("missing-model warning: %+v", r.runState)
	}
}
