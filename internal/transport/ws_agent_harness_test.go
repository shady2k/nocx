package transport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	agentharness "github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/waittest"
)

func answeringScript() protocol.Script {
	return protocol.Script{Steps: []protocol.Step{{Kind: protocol.StepReasoning, Text: "thinking"}, {Kind: protocol.StepAnswer, Text: "answer"}, {Kind: protocol.StepEnd, Model: "actual-model"}}}
}

func TestAgentHarnessStreamsWithoutLegacyClient(t *testing.T) {
	backend := newScriptedAgent(t, answeringScript())
	h := agentHandlers{harness: backend.harness}
	var got []assistant.AskEvent
	err := h.askHarness(t.Context(), askRunContext{question: "why?", sessionID: "pane-session"}, assistant.AskParams{}, func(e assistant.AskEvent) error { got = append(got, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != assistant.AskReasoning || got[1].Text != "answer" {
		t.Fatalf("events: %+v", got)
	}
	prompt := backend.prompt()
	if prompt.ConversationID != "pane-session" || !strings.HasPrefix(prompt.Text, "why?") {
		t.Fatalf("prompt: %+v", prompt)
	}
}

func TestAgentHarnessPersistsAnswerAndActualModel(t *testing.T) {
	svc := &fakeAgentService{}
	h := newGapHandlers(svc, newScriptedAgent(t, answeringScript()).harness, assistant.NewApprovalStore())
	_, h.log = logtest.New(t)
	r := &gapResponder{}
	h.runAskStream(t.Context(), gapRunContext(), r)
	if svc.appendedText() != "answer" {
		t.Fatalf("persisted: %q", svc.appendedText())
	}
	if svc.finishState() != content.RunCompleted || svc.finish.Model != "actual-model" {
		t.Fatalf("finish: %+v", svc.finish)
	}
	if r.runState == nil || r.runState.State != "completed" {
		t.Fatalf("wire: %+v", r.runState)
	}
}

func TestAgentHarnessTerminalCancellationIsNotFailure(t *testing.T) {
	svc := &fakeAgentService{}
	backend := newScriptedAgent(t, protocol.Script{Steps: []protocol.Step{{Kind: protocol.StepHang}}})
	h := newGapHandlers(svc, backend.harness, assistant.NewApprovalStore())
	_, h.log = logtest.New(t)
	rc := gapRunContext()
	ctx, finish := rc.control.beginDrive(t.Context())
	defer finish()
	done := make(chan struct{})
	go func() { defer close(done); h.runAskStream(ctx, rc, &gapResponder{}) }()
	backend.waitHung()
	if !rc.control.beginCancel() {
		t.Fatal("active turn did not accept Stop")
	}
	rc.control.cancelContext(content.TermUserKilled)
	rc.control.finishCancel(content.RunCancelled, content.TermUserKilled, "the person stopped this answer", false)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled turn stayed open")
	}
	if svc.finishState() != content.RunCancelled {
		t.Fatalf("finish: %+v", svc.finish)
	}
}

// closingHarness records the transport's ownership close while every turn and
// process remains owned by the real SessionManager.
type closingHarness struct {
	agentharness.Harness
	closed chan string
}

func (h *closingHarness) CloseConversation(ctx context.Context, id string) error {
	closer, ok := h.Harness.(interface {
		CloseConversation(context.Context, string) error
	})
	if !ok {
		return errors.New("scripted harness cannot close conversation")
	}
	err := closer.CloseConversation(ctx, id)
	h.closed <- id
	return err
}

func TestAgentHarnessDisconnectClosesCompletedConversation(t *testing.T) {
	backend := &closingHarness{Harness: newScriptedAgent(t, answeringScript()).harness, closed: make(chan string, 1)}
	_, logger := logtest.New(t)
	server := NewWSServer(logger, nil, WithAgentHarness("test", backend))
	state := newConnState()
	state.agentConversations = map[string]struct{}{"pane": {}}
	server.closeAgentConversations(t.Context(), state)
	if id := <-backend.closed; id != "pane" {
		t.Fatalf("closed: %q", id)
	}
}

func TestAgentHarnessKeepsPaneFactsOutOfStandingPrompt(t *testing.T) {
	backend := newScriptedAgent(t, answeringScript())
	h := agentHandlers{harness: backend.harness}
	rc := askRunContext{question: "why?", sessionID: "s", promptFacts: assistant.SystemPromptFacts{Cwd: "/first"}}
	emit := func(assistant.AskEvent) error { return nil }
	if err := h.askHarness(t.Context(), rc, assistant.AskParams{}, emit); err != nil {
		t.Fatal(err)
	}
	first := backend.prompt().SystemPrompt
	rc.promptFacts.Cwd = "/second"
	if err := h.askHarness(t.Context(), rc, assistant.AskParams{}, emit); err != nil {
		t.Fatal(err)
	}
	prompt := backend.prompt()
	if first != prompt.SystemPrompt || strings.Contains(first, "/first") {
		t.Fatal("standing prompt changed with pane cwd")
	}
	if !strings.Contains(prompt.Text, "/second") {
		t.Fatalf("message: %q", prompt.Text)
	}
}

func TestAgentHarnessSocketCloseEndsIdleConversation(t *testing.T) {
	backend := &closingHarness{Harness: newScriptedAgent(t, answeringScript()).harness, closed: make(chan string, 1)}
	h := newAskHarnessWithOpts(t, answeringScript(), WithAgentHarness("test", backend))
	sid := openLocalSession(t, h.conn)
	res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "harness-ask", "sessionId": sid, "question": "why?", "cwd": "/repo", "attachedContent": []any{}}, 2)
	if rpcErr != nil {
		t.Fatalf("ask: %+v", rpcErr)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if err := h.conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-backend.closed:
		if id == "" {
			t.Fatal("closed empty conversation")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("conversation for run %d stayed open", res.RunID)
	}
}

func TestAgentHarnessSocketCloseEndsLiveConversation(t *testing.T) {
	script := protocol.Script{Steps: []protocol.Step{{Kind: protocol.StepAnswer, Text: "partial"}, {Kind: protocol.StepHang}}}
	agent := newScriptedAgent(t, script)
	backend := &closingHarness{Harness: agent.harness, closed: make(chan string, 1)}
	h := newAskHarnessWithOpts(t, script, WithAgentHarness("test", backend))
	sid := openLocalSession(t, h.conn)
	res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "live-harness-ask", "sessionId": sid, "question": "why?", "cwd": "/repo", "attachedContent": []any{}}, 2)
	if rpcErr != nil {
		t.Fatalf("ask: %+v", rpcErr)
	}
	agent.waitHung()
	readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	if err := h.conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("live conversation not closed")
	}
	waittest.WaitFor(t, "disconnected run is interrupted", func() bool {
		state, err := h.db.Ledger().RunState(t.Context(), res.RunID)
		return err == nil && state != nil && *state == content.RunInterrupted
	})
	waitChildDead(t, agent.pid())
	if got := proseBodyOf(t, h.db.Ledger(), res.EntryID); got != "partial" {
		t.Fatalf("interrupted prose: %q", got)
	}
}

func TestAgentHarnessActorEndWinsQueuedStop(t *testing.T) {
	control := &agentRunControl{cancelled: true}
	h := agentHandlers{harness: newScriptedAgent(t, answeringScript()).harness}
	err := h.askHarness(t.Context(), askRunContext{control: control}, assistant.AskParams{}, func(assistant.AskEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	control.mu.Lock()
	cancelled := control.cancelled
	control.mu.Unlock()
	if cancelled {
		t.Fatal("Stop rewrote the actor's already accepted End")
	}
}

// settledHarness delays only transport's receipt of Wait. The wrapped actor
// has already chosen and published its own terminal before Stop is submitted.
type settledHarness struct {
	agentharness.Harness
	waiting chan struct{}
}

func (h *settledHarness) Run(ctx context.Context, spec agentharness.TurnSpec) (agentharness.Turn, error) {
	turn, err := h.Harness.Run(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &settledTurn{Turn: turn, ctx: ctx, waiting: h.waiting}, nil
}

type settledTurn struct {
	agentharness.Turn
	ctx     context.Context
	waiting chan struct{}
}

func (t *settledTurn) Wait(ctx context.Context) (agentharness.Outcome, error) {
	out, err := t.Turn.Wait(ctx)
	close(t.waiting)
	<-t.ctx.Done()
	return out, err
}

func TestAgentHarnessSocketActorEndWinsStop(t *testing.T) {
	assertActorTerminalWinsStop(t, protocol.Step{Kind: protocol.StepEnd, Model: "actual"}, content.RunCompleted)
}

func TestAgentHarnessSocketActorFailureWinsStop(t *testing.T) {
	assertActorTerminalWinsStop(t, protocol.Step{Kind: protocol.StepFail, Reason: "actor failed before Stop"}, content.RunFailed)
}

func assertActorTerminalWinsStop(t *testing.T, terminal protocol.Step, want content.RunState) {
	t.Helper()
	script := protocol.Script{Steps: []protocol.Step{terminal}}
	backend := &settledHarness{Harness: newScriptedAgent(t, script).harness, waiting: make(chan struct{})}
	h := newAskHarnessWithOpts(t, script, WithAgentHarness("test", backend))
	sid := openLocalSession(t, h.conn)
	res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "terminal-before-stop", "sessionId": sid, "question": "why?", "cwd": "/repo", "attachedContent": []any{}}, 1)
	if rpcErr != nil {
		t.Fatalf("ask: %+v", rpcErr)
	}
	select {
	case <-backend.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("actor not waiting")
	}
	raw, _ := cancelCallPreservingNotifications(t, h.conn, res.RunID, 2)
	var response struct {
		Result agentCancelResponse `json:"result"`
		Error  *jsonrpcErrorObj    `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil || response.Result.Cancelled || response.Result.State != string(want) {
		t.Fatalf("cancel after actor terminal: %s", raw)
	}
	state, err := h.db.Ledger().RunState(t.Context(), res.RunID)
	if err != nil || state == nil || *state != want {
		t.Fatalf("ledger state: %v, %v", state, err)
	}
}
