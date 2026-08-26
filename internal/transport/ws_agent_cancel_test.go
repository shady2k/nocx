package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// A real assistant run owns the renderer request until the renderer resolves
// it. These tests use the real local session behind that request so stopping
// the parent can be distinguished from merely cancelling the model stream.

type cancelBlockingClient struct {
	started chan struct{}
	emitted chan struct{}
	stopped chan struct{}
	once    sync.Once
	mu      sync.Mutex
	discard []string
}

func newCancelBlockingClient() *cancelBlockingClient {
	return &cancelBlockingClient{
		started: make(chan struct{}),
		emitted: make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

func (c *cancelBlockingClient) Probe(context.Context, assistant.ProbeParams) (assistant.ProbeResult, error) {
	return assistant.ProbeResult{}, nil
}

func (c *cancelBlockingClient) Ask(ctx context.Context, _ assistant.AskParams, onEvent func(assistant.AskEvent) error) error {
	c.once.Do(func() { close(c.started) })
	if err := onEvent(assistant.AskEvent{Kind: assistant.AskAnswer, Text: "received before stop"}); err != nil {
		return err
	}
	close(c.emitted)
	<-ctx.Done()
	close(c.stopped)
	return ctx.Err()
}

func (c *cancelBlockingClient) Discard(runID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.discard = append(c.discard, runID)
}

func TestAgentCancel_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.cancel.schema.json")
	validateJSON(t, schema, mustMarshal(agentCancelResponse{
		RunID: 7, State: string(content.RunCancelled), Cancelled: true,
	}), "agent.cancel DTO")
}

func TestAgentCancel_StopsStreamingRunAndKeepsReceivedProse(t *testing.T) {
	client := newCancelBlockingClient()
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "cancel-ask-1", "sessionId": sid, "question": "stop this", "cwd": "/repo", "attachedContent": []any{},
	}, 1)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("assistant did not start streaming")
	}
	select {
	case <-client.emitted:
	case <-time.After(time.Second):
		t.Fatal("assistant did not emit the prose that must survive cancellation")
	}

	resp, notifications := cancelCallPreservingNotifications(t, h.conn, res.RunID, 2)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode agent.cancel: %v\nraw: %s", err, resp)
	}
	if env.Error != nil {
		t.Fatalf("agent.cancel: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "agent.cancel.schema.json"), env.Result, "agent.cancel result (real socket)")
	var result agentCancelResponse
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("decode agent.cancel result: %v", err)
	}
	if result.RunID != res.RunID || result.State != string(content.RunCancelled) || !result.Cancelled {
		t.Fatalf("agent.cancel result = %+v, want run %d cancelled", result, res.RunID)
	}
	if len(notifications) == 0 {
		notifications = append(notifications, readNotification(t, h.conn, "agent.runState", time.Second))
	}

	var state agentRunState
	for _, notification := range notifications {
		var candidate agentRunState
		if json.Unmarshal(notification, &candidate) == nil && candidate.State == string(content.RunCancelled) {
			state = candidate
			break
		}
	}
	if state.State != string(content.RunCancelled) {
		t.Fatalf("cancel notifications = %s, want cancelled runState", notifications)
	}
	if !strings.Contains(strings.ToLower(state.Error), "person") || !strings.Contains(strings.ToLower(state.Error), "stop") {
		t.Fatalf("runState error = %q, want a sentence saying a person stopped it", state.Error)
	}

	select {
	case <-client.stopped:
	case <-time.After(time.Second):
		t.Fatal("assistant stream did not stop after cancellation")
	}
	if prose := proseUnder(t, h.db.Ledger(), res.EntryID); len(prose) != 1 || prose[0].text != "received before stop" {
		t.Fatalf("persisted prose = %+v, want the text received before cancellation", prose)
	}
	client.mu.Lock()
	discarded := append([]string(nil), client.discard...)
	client.mu.Unlock()
	if len(discarded) != 1 || discarded[0] != fmt.Sprintf("%d", res.RunID) {
		t.Fatalf("discard calls = %v, want run %d", discarded, res.RunID)
	}

	second := jsonrpcCallWithID(t, h.conn, "agent.cancel", map[string]any{"runId": res.RunID}, 3)
	var secondEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(second, &secondEnv); err != nil {
		t.Fatalf("decode repeated cancel: %v\nraw: %s", err, second)
	}
	if secondEnv.Error == nil || !strings.Contains(secondEnv.Error.Message, "not active") {
		t.Fatalf("repeated cancel = %s, want an honest not-active error", second)
	}
}

// TestAgentRun_StopReachesTheForegroundCommand proves the renderer's Stop
// gesture against a real assistant run: the run request names the pane,
// the renderer submits the command into that same session, and session.signal
// must kill the command's foreground process group rather than the shell.
func TestAgentRun_StopReachesTheForegroundCommand(t *testing.T) {
	h := newRunLeaseHarness(t, RunLeaseConfig{
		WallClock:   time.Minute,
		Inactivity:  time.Minute,
		SignalGrace: 100 * time.Millisecond,
	})
	h.createEndpointAt()
	sid := openLocalSession(t, h.conn)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	pidTmp := pidFile + ".tmp"
	cmd := "sh -c 'printf %s $$ > " + pidTmp + "; mv " + pidTmp + " " + pidFile + "; exec sleep 100'"
	h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)
	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	requestID, requestSession, requestCommand := decodeRunRequest(t, raw)
	if requestSession != sid || requestCommand != cmd {
		t.Fatalf("run request = session %q command %q, want session %q command %q", requestSession, requestCommand, sid, cmd)
	}
	submitCommand(t, h.conn, sid, cmd)
	pid := readPidFile(t, pidFile)
	waitChildAlive(t, pid)

	raw = tapCall(t, h.conn, tap, 2, "session.signal", map[string]any{
		"sessionId": sid,
		"signal":    signalStop,
	})
	var env struct {
		Result signalResult     `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode session.signal: %v\nraw: %s", err, raw)
	}
	if env.Error != nil || env.Result.Outcome != string(foregroundDelivered) {
		t.Fatalf("session.signal = %s, want stop/delivered", raw)
	}
	waitChildDead(t, pid)

	reply := tapCall(t, h.conn, tap, 3, "agent.runResolved",
		runResolvedWire(requestID, "entry-stop", 130, "failure", 0, 0, 0, ""))
	var replyEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &replyEnv); err != nil {
		t.Fatalf("decode agent.runResolved: %v\nraw: %s", err, reply)
	}
	if replyEnv.Error != nil {
		t.Fatalf("agent.runResolved: %+v", replyEnv.Error)
	}
	waitForRunState(t, tap, "completed")
}

func tapCancelCall(t *testing.T, conn *websocket.Conn, tap *socketTap, id int, runID int64, params any) (json.RawMessage, agentRunState) {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "agent.cancel", "params": params,
	})
	if err != nil {
		t.Fatalf("marshal agent.cancel: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write agent.cancel: %v", err)
	}
	var state agentRunState
	wantID := fmt.Sprint(id)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case msg, ok := <-tap.msgs:
			if !ok {
				t.Fatal("socket closed before agent.cancel answered")
			}
			var envelope struct {
				ID     *json.RawMessage `json:"id"`
				Method string           `json:"method"`
				Params json.RawMessage  `json:"params"`
			}
			if json.Unmarshal(msg, &envelope) != nil {
				continue
			}
			if envelope.ID == nil {
				if envelope.Method == "agent.runState" {
					var candidate agentRunState
					if json.Unmarshal(envelope.Params, &candidate) == nil && candidate.RunID == runID {
						state = candidate
					}
				}
				continue
			}
			if string(*envelope.ID) == wantID {
				return msg, state
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("agent.cancel never answered")
	return nil, state
}

// TestAgentCancel_StopsRendererRunCommand proves parent cancellation reaches
// the renderer-executed command before the turn is terminalized.
func TestAgentCancel_StopsRendererRunCommand(t *testing.T) {
	h := newRunLeaseHarness(t, RunLeaseConfig{
		WallClock:   time.Minute,
		Inactivity:  time.Minute,
		SignalGrace: 100 * time.Millisecond,
	})
	h.createEndpointAt()
	sid := openLocalSession(t, h.conn)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	pidTmp := pidFile + ".tmp"
	cmd := "sh -c 'printf %s $$ > " + pidTmp + "; mv " + pidTmp + " " + pidFile + "; exec sleep 100'"
	res := h.askRunsTool(sid, cmd)
	tap := newSocketTap(h.conn)
	raw := tapNotify(t, tap, "agent.runRequest", 10*time.Second)
	_, requestSession, requestCommand := decodeRunRequest(t, raw)
	if requestSession != sid || requestCommand != cmd {
		t.Fatalf("run request = session %q command %q, want session %q command %q", requestSession, requestCommand, sid, cmd)
	}
	submitCommand(t, h.conn, sid, cmd)
	pid := readPidFile(t, pidFile)
	waitChildAlive(t, pid)

	raw, state := tapCancelCall(t, h.conn, tap, 2, res.RunID, map[string]any{"runId": res.RunID})
	var env struct {
		Result agentCancelResponse `json:"result"`
		Error  *jsonrpcErrorObj    `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode agent.cancel: %v\nraw: %s", err, raw)
	}
	if env.Error != nil || !env.Result.Cancelled || env.Result.State != string(content.RunCancelled) {
		t.Fatalf("agent.cancel = %s, want cancelled result", raw)
	}
	if state.State == "" {
		stateRaw := tapNotify(t, tap, "agent.runState", time.Second)
		if err := json.Unmarshal(stateRaw, &state); err != nil {
			t.Fatalf("decode cancelled runState: %v\nraw: %s", err, stateRaw)
		}
	}
	if state.RunID != res.RunID || state.State != string(content.RunCancelled) {
		t.Fatalf("runState = %+v, want cancelled run %d", state, res.RunID)
	}
	if !strings.Contains(strings.ToLower(state.Error), "stop") {
		t.Fatalf("runState error = %q, want a stop sentence", state.Error)
	}
	waitChildDead(t, pid)
}

func TestAgentCancel_SuspendedApprovalClosesQuestion(t *testing.T) {
	h := suspendedRun(t, askPolicyStore(t))
	raw, notifications := cancelCallPreservingNotifications(t, h.conn, h.runID, 2)
	var env struct {
		Result agentCancelResponse `json:"result"`
		Error  *jsonrpcErrorObj    `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode suspended cancel: %v\nraw: %s", err, raw)
	}
	if env.Error != nil || env.Result.State != string(content.RunCancelled) || !env.Result.Cancelled {
		t.Fatalf("suspended cancel = %s, want cancelled result", raw)
	}
	if len(notifications) == 0 {
		notifications = append(notifications, readNotification(t, h.conn, "agent.runState", time.Second))
	}
	var state agentRunState
	for _, notification := range notifications {
		var candidate agentRunState
		if json.Unmarshal(notification, &candidate) == nil && candidate.State == string(content.RunCancelled) {
			state = candidate
			break
		}
	}
	if state.State != string(content.RunCancelled) {
		t.Fatalf("suspended cancel notifications = %s, want cancelled runState", notifications)
	}

	late := jsonrpcCallWithID(t, h.conn, "agent.approve", h.answer(true, "once"), 3)
	var lateEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(late, &lateEnv); err != nil {
		t.Fatalf("decode late approval: %v\nraw: %s", err, late)
	}
	if lateEnv.Error == nil || !strings.Contains(lateEnv.Error.Message, "no pending question") {
		t.Fatalf("late approval = %s, want no pending question", late)
	}
}

func TestAgentCancel_RejectsInvalidAndUnknownRuns(t *testing.T) {
	h := newAskHarness(t, newCancelBlockingClient())
	for _, tc := range []struct {
		name   string
		runID  any
		needle string
	}{
		{name: "zero", runID: 0, needle: "backend-minted"},
		{name: "unknown", runID: 999999, needle: "not active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := jsonrpcCallWithID(t, h.conn, "agent.cancel", map[string]any{"runId": tc.runID}, 10)
			var env struct {
				Error *jsonrpcErrorObj `json:"error"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("decode invalid cancel: %v\nraw: %s", err, raw)
			}
			if env.Error == nil || !strings.Contains(env.Error.Message, tc.needle) {
				t.Fatalf("cancel(%v) = %s, want error containing %q", tc.runID, raw, tc.needle)
			}
		})
	}
}

func cancelCallPreservingNotifications(t *testing.T, conn *websocket.Conn, runID int64, id int) (json.RawMessage, []json.RawMessage) {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "agent.cancel", "params": map[string]any{"runId": runID},
	})
	if err != nil {
		t.Fatalf("marshal cancel: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write cancel: %v", err)
	}
	var notifications []json.RawMessage
	for {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read cancel response: %v", err)
		}
		var envelope struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("decode cancel envelope: %v\nraw: %s", err, raw)
		}
		if envelope.ID == nil {
			if envelope.Method == "agent.runState" {
				notifications = append(notifications, append(json.RawMessage(nil), envelope.Params...))
			}
			continue
		}
		if *envelope.ID == id {
			_ = conn.SetReadDeadline(time.Time{})
			return raw, notifications
		}
	}
}
