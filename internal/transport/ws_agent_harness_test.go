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
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/waittest"
)

type answeringHarness struct{ spec agentharness.TurnSpec }

func (*answeringHarness) Facts() agentharness.Facts { return agentharness.Facts{} }
func (*answeringHarness) Readiness(context.Context) agentharness.Readiness {
	return agentharness.Readiness{State: agentharness.Ready}
}

func (h *answeringHarness) Run(_ context.Context, spec agentharness.TurnSpec) (agentharness.Turn, error) {
	h.spec = spec
	events := make(chan agentharness.Event, 5)
	events <- agentharness.Event{Kind: agentharness.EventReasoningDelta, Text: "thinking"}
	events <- agentharness.Event{Kind: agentharness.EventToolStarted, Tool: &agentharness.ToolCall{Name: "nocx", ID: "call"}}
	events <- agentharness.Event{Kind: agentharness.EventToolFinished, Tool: &agentharness.ToolCall{Name: "nocx", ID: "call"}}
	events <- agentharness.Event{Kind: agentharness.EventAnswerDelta, Text: "answer"}
	events <- agentharness.Event{Kind: agentharness.EventEnd, End: &agentharness.End{Model: "actual-model"}}
	close(events)
	return &answeringTurn{events: events}, nil
}

type answeringTurn struct{ events chan agentharness.Event }

func (t *answeringTurn) Events() <-chan agentharness.Event { return t.events }
func (*answeringTurn) Cancel(context.Context) error        { return nil }
func (*answeringTurn) Wait(context.Context) (agentharness.Outcome, error) {
	return agentharness.Outcome{Model: "actual-model"}, nil
}

func TestAgentHarnessStreamsWithoutLegacyClient(t *testing.T) {
	backend := &answeringHarness{}
	h := agentHandlers{harness: backend}
	var got []assistant.AskEvent
	err := h.askHarness(context.Background(), askRunContext{question: "why?", sessionID: "pane-session"}, assistant.AskParams{}, func(e assistant.AskEvent) error { got = append(got, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != assistant.AskReasoning || got[1].Text != "answer" {
		t.Fatalf("events: %+v", got)
	}
	if backend.spec.ConversationID != "pane-session" || !strings.HasPrefix(backend.spec.Text, "why?") {
		t.Fatalf("spec: %+v", backend.spec)
	}
}

func TestAgentHarnessPersistsAnswerAndActualModel(t *testing.T) {
	svc := &fakeAgentService{}
	h := newGapHandlers(svc, nil, assistant.NewApprovalStore())
	_, h.log = logtest.New(t)
	h.harness = &answeringHarness{}
	rc := gapRunContext()
	r := &gapResponder{}
	h.runAskStream(context.Background(), rc, r)
	if svc.appendedText() != "answer" {
		t.Fatalf("persisted: %q", svc.appendedText())
	}
	if svc.finishState() != content.RunCompleted {
		t.Fatalf("finish: %+v", svc.finish)
	}
	if svc.finish.Model != "actual-model" {
		t.Fatalf("model: %q", svc.finish.Model)
	}
	if r.runState == nil || r.runState.State != "completed" {
		t.Fatalf("wire: %+v", r.runState)
	}
}

func TestAgentHarnessTerminalCancellationIsNotFailure(t *testing.T) {
	svc := &fakeAgentService{}
	h := newGapHandlers(svc, nil, assistant.NewApprovalStore())
	_, h.log = logtest.New(t)
	h.harness = &cancelledHarness{}
	h.runAskStream(context.Background(), gapRunContext(), &gapResponder{})
	if svc.finishState() != content.RunCancelled {
		t.Fatalf("finish: %+v", svc.finish)
	}
}

type cancelledHarness struct{ answeringHarness }

func (*cancelledHarness) Run(context.Context, agentharness.TurnSpec) (agentharness.Turn, error) {
	events := make(chan agentharness.Event, 1)
	events <- agentharness.Event{Kind: agentharness.EventFailure, Failure: &agentharness.Failure{Reason: "cancelled"}}
	close(events)
	return &cancelledTurn{answeringTurn{events: events}}, nil
}

type cancelledTurn struct{ answeringTurn }

func (*cancelledTurn) Wait(context.Context) (agentharness.Outcome, error) {
	return agentharness.Outcome{}, agentharness.ErrCancelled
}

func TestAgentHarnessDisconnectClosesCompletedConversation(t *testing.T) {
	backend := &closingHarness{}
	_, logger := logtest.New(t)
	server := NewWSServer(logger, nil, WithAgentHarness("test", backend))
	state := newConnState()
	state.agentConversations = map[string]struct{}{"pane": {}}
	server.closeAgentConversations(t.Context(), state)
	if backend.closed != "pane" {
		t.Fatalf("closed: %q", backend.closed)
	}
}

type closingHarness struct {
	answeringHarness
	closed string
}

func (h *closingHarness) CloseConversation(_ context.Context, id string) error {
	h.closed = id
	return nil
}

func TestAgentHarnessKeepsPaneFactsOutOfStandingPrompt(t *testing.T) {
	backend := &answeringHarness{}
	h := agentHandlers{harness: backend}
	rc := askRunContext{question: "why?", sessionID: "s", promptFacts: assistant.SystemPromptFacts{Cwd: "/first"}}
	emit := func(assistant.AskEvent) error { return nil }
	if err := h.askHarness(context.Background(), rc, assistant.AskParams{}, emit); err != nil {
		t.Fatal(err)
	}
	first := backend.spec.SystemPrompt
	rc.promptFacts.Cwd = "/second"
	if err := h.askHarness(context.Background(), rc, assistant.AskParams{}, emit); err != nil {
		t.Fatal(err)
	}
	if first != backend.spec.SystemPrompt || strings.Contains(first, "/first") {
		t.Fatal("standing prompt changed with pane cwd")
	}
	if !strings.Contains(backend.spec.Text, "/second") {
		t.Fatalf("message: %q", backend.spec.Text)
	}
}

func TestAgentHarnessSocketCloseEndsIdleConversation(t *testing.T) {
	backend := &socketClosingHarness{closed: make(chan string, 1)}
	h := newAskHarnessWithOpts(t, nil, WithAgentHarness("test", backend))
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

type socketClosingHarness struct {
	answeringHarness
	closed chan string
}

func (h *socketClosingHarness) CloseConversation(_ context.Context, id string) error {
	h.closed <- id
	return nil
}

// legacyStreamHarness keeps existing stream/ledger regression scripts on the
// harness boundary. It never enters the production assistant ask path.
type legacyStreamHarness struct{ client assistant.Client }

func (*legacyStreamHarness) Facts() agentharness.Facts { return agentharness.Facts{} }
func (*legacyStreamHarness) Readiness(context.Context) agentharness.Readiness {
	return agentharness.Readiness{State: agentharness.Ready}
}

func (h *legacyStreamHarness) Run(ctx context.Context, spec agentharness.TurnSpec) (agentharness.Turn, error) {
	events := make(chan agentharness.Event)
	t := &legacyStreamTurn{events: events, done: make(chan struct{})}
	go func() {
		defer close(t.done)
		defer close(events)
		t.err = h.client.Ask(ctx, assistant.AskParams{}, func(ev assistant.AskEvent) error {
			kind := agentharness.EventAnswerDelta
			if ev.Kind == assistant.AskReasoning {
				kind = agentharness.EventReasoningDelta
			}
			select {
			case events <- agentharness.Event{Kind: kind, Text: ev.Text}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	return t, nil
}

type legacyStreamTurn struct {
	events chan agentharness.Event
	done   chan struct{}
	err    error
}

func (t *legacyStreamTurn) Events() <-chan agentharness.Event { return t.events }
func (t *legacyStreamTurn) Cancel(context.Context) error      { return nil }
func (t *legacyStreamTurn) Wait(context.Context) (agentharness.Outcome, error) {
	<-t.done
	return agentharness.Outcome{}, t.err
}

func TestAgentHarnessSocketCloseEndsLiveConversation(t *testing.T) {
	backend := &liveSocketHarness{started: make(chan struct{}), cancelled: make(chan struct{}), closed: make(chan string, 1)}
	h := newAskHarnessWithOpts(t, nil, WithAgentHarness("test", backend))
	sid := openLocalSession(t, h.conn)
	res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "live-harness-ask", "sessionId": sid, "question": "why?", "cwd": "/repo", "attachedContent": []any{}}, 2)
	if rpcErr != nil {
		t.Fatalf("ask: %+v", rpcErr)
	}
	select {
	case <-backend.started:
	case <-time.After(5 * time.Second):
		t.Fatal("turn not started")
	}
	if err := h.conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("turn context not cancelled")
	}
	select {
	case <-backend.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("live conversation not closed")
	}
	waittest.WaitFor(t, "disconnected run is interrupted", func() bool {
		state, err := h.db.Ledger().RunState(context.Background(), res.RunID)
		return err == nil && state != nil && *state == content.RunInterrupted
	})
}

type liveSocketHarness struct {
	answeringHarness
	started, cancelled chan struct{}
	closed             chan string
}

func (h *liveSocketHarness) Run(ctx context.Context, _ agentharness.TurnSpec) (agentharness.Turn, error) {
	events := make(chan agentharness.Event, 1)
	done := make(chan struct{})
	close(h.started)
	go func() {
		<-ctx.Done()
		close(h.cancelled)
		events <- agentharness.Event{Kind: agentharness.EventFailure, Failure: &agentharness.Failure{Reason: "cancelled"}}
		close(events)
		close(done)
	}()
	return &liveSocketTurn{events: events, done: done}, nil
}

func (h *liveSocketHarness) CloseConversation(_ context.Context, id string) error {
	h.closed <- id
	return nil
}

type liveSocketTurn struct {
	events chan agentharness.Event
	done   chan struct{}
}

func (t *liveSocketTurn) Events() <-chan agentharness.Event { return t.events }
func (*liveSocketTurn) Cancel(context.Context) error        { return nil }
func (t *liveSocketTurn) Wait(context.Context) (agentharness.Outcome, error) {
	<-t.done
	return agentharness.Outcome{}, context.Canceled
}

func TestAgentHarnessActorEndWinsQueuedStop(t *testing.T) {
	control := &agentRunControl{cancelled: true}
	h := agentHandlers{harness: &answeringHarness{}}
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

func TestAgentHarnessSocketActorEndWinsStop(t *testing.T) {
	backend := &endBeforeStopHarness{waiting: make(chan struct{})}
	h := newAskHarnessWithOpts(t, nil, WithAgentHarness("test", backend))
	sid := openLocalSession(t, h.conn)
	res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "end-before-stop", "sessionId": sid, "question": "why?", "cwd": "/repo", "attachedContent": []any{}}, 1)
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
	if response.Error != nil || response.Result.Cancelled || response.Result.State != string(content.RunCompleted) {
		t.Fatalf("cancel after End: %s", raw)
	}
	state, err := h.db.Ledger().RunState(t.Context(), res.RunID)
	if err != nil || state == nil || *state != content.RunCompleted {
		t.Fatalf("ledger state: %v, %v", state, err)
	}
}

type endBeforeStopHarness struct {
	err error
	answeringHarness
	waiting chan struct{}
}

func (h *endBeforeStopHarness) Run(ctx context.Context, _ agentharness.TurnSpec) (agentharness.Turn, error) {
	events := make(chan agentharness.Event, 1)
	if h.err != nil {
		events <- agentharness.Event{Kind: agentharness.EventFailure, Failure: &agentharness.Failure{Reason: h.err.Error()}}
	} else {
		events <- agentharness.Event{Kind: agentharness.EventEnd, End: &agentharness.End{Model: "actual"}}
	}
	close(events)
	return &endBeforeStopTurn{ctx: ctx, waiting: h.waiting, events: events, err: h.err}, nil
}

type endBeforeStopTurn struct {
	err     error
	ctx     context.Context
	waiting chan struct{}
	events  chan agentharness.Event
}

func (t *endBeforeStopTurn) Events() <-chan agentharness.Event { return t.events }
func (*endBeforeStopTurn) Cancel(context.Context) error        { return nil }
func (t *endBeforeStopTurn) Wait(context.Context) (agentharness.Outcome, error) {
	close(t.waiting)
	<-t.ctx.Done()
	return agentharness.Outcome{Model: "actual"}, t.err
}

func TestAgentHarnessSocketActorFailureWinsStop(t *testing.T) {
	backend := &endBeforeStopHarness{waiting: make(chan struct{}), err: errors.New("actor failed before Stop")}
	h := newAskHarnessWithOpts(t, nil, WithAgentHarness("test", backend))
	sid := openLocalSession(t, h.conn)
	res, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "end-before-stop", "sessionId": sid, "question": "why?", "cwd": "/repo", "attachedContent": []any{}}, 1)
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
	if response.Error != nil || response.Result.Cancelled || response.Result.State != string(content.RunFailed) {
		t.Fatalf("cancel after End: %s", raw)
	}
	state, err := h.db.Ledger().RunState(t.Context(), res.RunID)
	if err != nil || state == nil || *state != content.RunFailed {
		t.Fatalf("ledger state: %v, %v", state, err)
	}
}
