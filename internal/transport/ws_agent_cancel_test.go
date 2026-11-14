package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// A real assistant run owns the renderer request until the renderer resolves
// it. These tests use the real local session behind that request so cancelling
// the model turn can be proved independent from stopping its foreground child.

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
		Error:         "no local process group to signal — the execution keeps running",
		DroppedDeltas: 2,
		UnarmedBounds: []string{"the inactivity bound is not active because shell integration is unavailable"},
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
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(notifications[len(notifications)-1], &fields); err != nil {
		t.Fatalf("decode cancelled runState fields: %v", err)
	}
	if _, ok := fields["error"]; ok {
		t.Fatalf("ordinary cancelled runState = %s, want no error sentence", notifications[len(notifications)-1])
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

func TestAgentCancel_UnsupportedOwnedRunStillClosesWithPresentation(t *testing.T) {
	client := newCancelBlockingClient()
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "cancel-unsupported-1", "sessionId": sid, "question": "stop remote work", "cwd": "/repo", "attachedContent": []any{},
	}, 1)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	select {
	case <-client.emitted:
	case <-time.After(time.Second):
		t.Fatal("assistant did not emit before cancellation")
	}

	h.ws.pendingRunsMu.Lock()
	rc := h.ws.pendingRuns[res.RunID]
	lease := h.ws.newRunLease(session.ID(sid), RunLeaseConfig{})
	if rc.leaseDegradation == nil {
		h.ws.pendingRunsMu.Unlock()
		t.Fatal("agent run has no lease degradation context")
	}
	rc.leaseDegradation.Add(assistant.RunLeaseBoundInactivity)
	lease.sess = nil
	lease.cancel = func() {} // armed RequestRun: the renderer execution exists
	if !rc.control.attachRunLease(lease) {
		h.ws.pendingRunsMu.Unlock()
		t.Fatal("active run refused its exact lease")
	}
	rc.control.recordDroppedDelta()
	rc.control.recordDroppedDelta()
	h.ws.pendingRunsMu.Unlock()

	resp, notifications := cancelCallPreservingNotifications(t, h.conn, res.RunID, 2)
	var env struct {
		Result agentCancelResponse `json:"result"`
		Error  *jsonrpcErrorObj    `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode agent.cancel: %v\nraw: %s", err, resp)
	}
	warning := "no local process group to signal — the execution keeps running"
	if env.Error != nil || env.Result.Error != warning || env.Result.DroppedDeltas != 2 {
		t.Fatalf("agent.cancel = %+v, want warning and two dropped deltas", env)
	}
	wantUnarmed := []string{"the inactivity bound is not active because shell integration is unavailable"}
	if !reflect.DeepEqual(env.Result.UnarmedBounds, wantUnarmed) {
		t.Fatalf("agent.cancel unarmedBounds = %v, want %v", env.Result.UnarmedBounds, wantUnarmed)
	}
	if len(notifications) == 0 {
		notifications = append(notifications, readNotification(t, h.conn, "agent.runState", time.Second))
	}
	var state agentRunState
	for _, raw := range notifications {
		var candidate agentRunState
		if json.Unmarshal(raw, &candidate) == nil && candidate.RunID == res.RunID {
			state = candidate
		}
	}
	if state.State != string(content.RunCancelled) || state.Error != warning || state.DroppedDeltas != 2 {
		t.Fatalf("runState = %+v, want cancelled with matching presentation", state)
	}
	if !reflect.DeepEqual(state.UnarmedBounds, wantUnarmed) {
		t.Fatalf("runState unarmedBounds = %v, want %v", state.UnarmedBounds, wantUnarmed)
	}
	select {
	case <-client.stopped:
	case <-time.After(time.Second):
		t.Fatal("provider stream did not close when signalling was unsupported")
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

// TestAgentCancel_StopsAssistantRunChildBeforeResponse proves the backend's
// exact ownership contract: agent.cancel stops the child started by this
// turn's RequestRun lease before the reserved response, while the paired
// streaming test proves arrived prose survives the same terminal close.
func TestAgentCancel_StopsAssistantRunChildBeforeResponse(t *testing.T) {
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
	// The reserved response is written only after the synchronous stop ladder.
	// There is no cleanup signal after this assertion: the PID must already be
	// gone as part of agent.cancel itself.
	waitChildDead(t, pid)
	if state.State == "" {
		stateRaw := tapNotify(t, tap, "agent.runState", time.Second)
		if err := json.Unmarshal(stateRaw, &state); err != nil {
			t.Fatalf("decode cancelled runState: %v\nraw: %s", err, stateRaw)
		}
	}
	if state.RunID != res.RunID || state.State != string(content.RunCancelled) {
		t.Fatalf("runState = %+v, want cancelled run %d", state, res.RunID)
	}
	if state.Error != "" {
		t.Fatalf("ordinary cancelled runState error = %q, want empty", state.Error)
	}
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
	if lateEnv.Error == nil || !strings.Contains(lateEnv.Error.Message, "unknown approval") {
		t.Fatalf("late approval = %s, want the answer refused because no question is open", late)
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

// ── a call blocked inside its executor (nocx-a62cl.4.1) ───────────────────

// blockedFetchFetcher is fetch.url's dependency, and it never answers on its
// own: the tool's executor is blocked inside it until the call's context ends.
// It is the seam a REAL executor holds — the pipeline, the narrowed capability
// and executeFetchURL all run — so what the test watches is the call's own
// context and not a fake standing in for the tool.
type blockedFetchFetcher struct {
	started chan struct{}
	stopped chan time.Time
	release chan struct{}
	calls   atomic.Int64
	once    sync.Once
}

// blockedFetchText is what the released fetcher returns, so the paired
// no-cancel journey can prove the same call completes once.
const blockedFetchText = "the page said hello"

func (f *blockedFetchFetcher) FetchText(ctx context.Context, req apifetch.TextRequest) (apifetch.TextDocument, error) {
	f.calls.Add(1)
	f.once.Do(func() { close(f.started) })
	select {
	case <-ctx.Done():
		f.stopped <- time.Now()
		return apifetch.TextDocument{}, ctx.Err()
	case <-f.release:
		return apifetch.TextDocument{URL: req.URL, ContentType: "text/plain", Text: blockedFetchText}, nil
	}
}

// fetchCallingProvider is the model fake: the first request proposes
// fetch.url, every later one answers. It keeps every request body, because
// "no tool result reached the model" is a fact about the bytes it was sent.
type fetchCallingProvider struct {
	mu     sync.Mutex
	bodies []string
}

func (p *fetchCallingProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.bodies = append(p.bodies, string(body))
	n := len(p.bodies)
	p.mu.Unlock()
	if n == 1 {
		streamToolCallChunk(w, "fetch.url", `{"url":"https://example.test/page"}`)
		return
	}
	streamAnswerChunk(w, "The page answered.")
}

func (p *fetchCallingProvider) toolResultBodies() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, body := range p.bodies {
		if strings.Contains(body, `"role":"tool"`) {
			out = append(out, body)
		}
	}
	return out
}

// wireFrame is one frame the tap read: a response (id) or a notification
// (method). Notifications are kept in arrival order, which is the order the
// socket delivered them in.
type wireFrame struct {
	id     string
	method string
	params json.RawMessage
	result json.RawMessage
}

// readFrames reads the tap until done says the window is over.
func readFrames(t *testing.T, tap *socketTap, what string, done func([]wireFrame) bool) []wireFrame {
	t.Helper()
	var frames []wireFrame
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case raw, ok := <-tap.msgs:
			if !ok {
				t.Fatalf("socket closed before %s", what)
			}
			var envelope struct {
				ID     *json.RawMessage `json:"id"`
				Method string           `json:"method"`
				Params json.RawMessage  `json:"params"`
				Result json.RawMessage  `json:"result"`
			}
			if json.Unmarshal(raw, &envelope) != nil {
				continue
			}
			frame := wireFrame{method: envelope.Method, params: envelope.Params, result: envelope.Result}
			if envelope.ID != nil {
				frame.id = string(*envelope.ID)
			}
			frames = append(frames, frame)
			if done(frames) {
				return frames
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("%s never arrived; frames so far: %+v", what, frames)
	return nil
}

func runStateIn(frames []wireFrame, runID int64) (state string, at int) {
	for i, frame := range frames {
		if frame.method != "agent.runState" {
			continue
		}
		var st struct {
			RunID int64  `json:"runId"`
			State string `json:"state"`
		}
		if json.Unmarshal(frame.params, &st) == nil && st.RunID == runID {
			state, at = st.State, i
		}
	}
	return state, at
}

// askRunIDIn is the run id the ask's own response minted.
func askRunIDIn(frames []wireFrame, id string) (int64, bool) {
	for _, frame := range frames {
		if frame.id != id {
			continue
		}
		var res struct {
			RunID int64 `json:"runId"`
		}
		if json.Unmarshal(frame.result, &res) == nil && res.RunID != 0 {
			return res.RunID, true
		}
	}
	return 0, false
}

func answered(frames []wireFrame, id string) bool {
	for _, frame := range frames {
		if frame.id == id {
			return true
		}
	}
	return false
}

// fetchActionAttempt reads fetch.url's action entry back from the ledger.
func fetchActionAttempt(t *testing.T, led content.LedgerRepository) *content.LedgerEntry {
	t.Helper()
	summaries, err := led.ListEntries(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	for _, s := range summaries {
		if s.Kind != content.EntryAction || s.Intent != "fetch.url" {
			continue
		}
		entry, entryErr := led.Entry(context.Background(), s.ID)
		if entryErr != nil {
			t.Fatalf("Entry(%s): %v", s.ID, entryErr)
		}
		return entry
	}
	return nil
}

// TestAgentCancel_StopsACallBlockedInItsExecutor is the stage's Go assertion
// (nocx-a62cl.4.1): the person stops the turn while a tool call is running
// inside its executor. The call's context ends within a second, the attempt
// records the person's stop rather than a tool failure, nothing about the
// call reaches the model afterwards, no agent.run* event follows the cancelled
// state, and the next question in the same block works.
func TestAgentCancel_StopsACallBlockedInItsExecutor(t *testing.T) {
	provider := &fetchCallingProvider{}
	model := httptest.NewServer(provider)
	defer model.Close()
	fetcher := &blockedFetchFetcher{
		started: make(chan struct{}),
		stopped: make(chan time.Time, 1),
		release: make(chan struct{}),
	}
	h := newAskHarnessWithOpts(t, mustClient(t),
		WithAgentPolicy(autonomousPolicyStore(t)), WithAgentFetcher(fetcher))
	h.createEndpointAt(model.URL)
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "cancel-in-executor", "sessionId": sid, "question": "fetch that page", "cwd": "/repo",
	}, 21)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	select {
	case <-fetcher.started:
	case <-time.After(30 * time.Second):
		t.Fatal("fetch.url's executor never started")
	}

	tap := newSocketTap(h.conn)
	stoppedAt := time.Now()
	cancelReq, marshalErr := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 22, "method": "agent.cancel", "params": map[string]any{"runId": res.RunID},
	})
	if marshalErr != nil {
		t.Fatalf("marshal agent.cancel: %v", marshalErr)
	}
	if writeErr := h.conn.WriteMessage(websocket.TextMessage, cancelReq); writeErr != nil {
		t.Fatalf("write agent.cancel: %v", writeErr)
	}

	// THE CALL'S OWN CONTEXT, not the run's state: the executor is blocked in
	// its dependency and only its context can free it.
	select {
	case at := <-fetcher.stopped:
		if elapsed := at.Sub(stoppedAt); elapsed > time.Second {
			t.Fatalf("the call's context ended %s after agent.cancel, want within a second", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the call's context was never cancelled")
	}

	first := readFrames(t, tap, "the cancelled run state", func(frames []wireFrame) bool {
		state, _ := runStateIn(frames, res.RunID)
		return answered(frames, "22") && state == string(content.RunCancelled)
	})
	if _, at := runStateIn(first, res.RunID); at == 0 && first[0].method != "agent.runState" {
		t.Fatalf("no cancelled agent.runState for run %d in %+v", res.RunID, first)
	}

	// THE ATTEMPT RECORDS THE PERSON'S STOP, not a failure of the tool: the
	// call did not fail, it was stopped, and the ledger is the record.
	entry := fetchActionAttempt(t, h.db.Ledger())
	if entry == nil {
		t.Fatal("fetch.url left no action entry")
	}
	waittest.WaitForTimeout(t, "the attempt to close", 10*time.Second, func() bool {
		entry = fetchActionAttempt(t, h.db.Ledger())
		return entry != nil && len(entry.Executions) == 1 && entry.Executions[0].TerminationReason != nil
	})
	if reason := *entry.Executions[0].TerminationReason; reason != content.TermUserKilled {
		t.Fatalf("the attempt recorded %q, want the person's stop %q", reason, content.TermUserKilled)
	}
	if entry.Status != content.EntryInterrupted {
		t.Fatalf("the attempt's entry is %q, want %q — the call was stopped, not failed", entry.Status, content.EntryInterrupted)
	}

	// THE NEXT QUESTION IN THE SAME BLOCK WORKS, and it is the end of the
	// observation window: every frame between the cancelled state and this
	// turn's completion has been read, so "nothing followed" is a statement
	// about a closed interval rather than about a moment.
	askReq, askErr := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 23, "method": "agent.ask", "params": map[string]any{
			"askId": "after-cancel", "sessionId": sid, "question": "now answer me plainly",
			"cwd": "/repo", "attachedContent": []any{},
		},
	})
	if askErr != nil {
		t.Fatalf("marshal the next agent.ask: %v", askErr)
	}
	if writeErr := h.conn.WriteMessage(websocket.TextMessage, askReq); writeErr != nil {
		t.Fatalf("write the next agent.ask: %v", writeErr)
	}
	second := readFrames(t, tap, "the next turn's completion", func(frames []wireFrame) bool {
		nextRun, ok := askRunIDIn(frames, "23")
		if !ok {
			return false
		}
		state, _ := runStateIn(frames, nextRun)
		return state == string(content.RunCompleted)
	})
	after := append(first, second...)
	_, cancelledAt := runStateIn(after, res.RunID)
	for i := cancelledAt + 1; i < len(after); i++ {
		frame := after[i]
		if frame.method == "" {
			continue
		}
		if !strings.HasPrefix(frame.method, "agent.run") {
			continue
		}
		var envelope struct {
			RunID int64 `json:"runId"`
		}
		if json.Unmarshal(frame.params, &envelope) == nil && envelope.RunID == res.RunID {
			t.Fatalf("%s for the cancelled run %d arrived after its cancelled state", frame.method, res.RunID)
		}
	}

	// NOTHING OF THE CALL REACHED THE MODEL: the stopped call's result would be
	// the only thing that could put a tool message in a request.
	if bodies := provider.toolResultBodies(); len(bodies) != 0 {
		t.Fatalf("the model was sent %d tool result(s) after the stop: %s", len(bodies), bodies[0])
	}
}

// TestAgentCancel_TheSameCallWithoutAStopCompletesOnce is the paired half: the
// identical call, released instead of stopped, runs its tool once and its
// result reaches the model. Without it, "the result was withheld" would also
// be true of a call that never produced one.
func TestAgentCancel_TheSameCallWithoutAStopCompletesOnce(t *testing.T) {
	provider := &fetchCallingProvider{}
	model := httptest.NewServer(provider)
	defer model.Close()
	fetcher := &blockedFetchFetcher{
		started: make(chan struct{}),
		stopped: make(chan time.Time, 1),
		release: make(chan struct{}),
	}
	h := newAskHarnessWithOpts(t, mustClient(t),
		WithAgentPolicy(autonomousPolicyStore(t)), WithAgentFetcher(fetcher))
	h.createEndpointAt(model.URL)
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "complete-in-executor", "sessionId": sid, "question": "fetch that page", "cwd": "/repo",
	}, 24)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	select {
	case <-fetcher.started:
	case <-time.After(30 * time.Second):
		t.Fatal("fetch.url's executor never started")
	}
	close(fetcher.release)

	tap := newSocketTap(h.conn)
	readFrames(t, tap, "the run's completion", func(frames []wireFrame) bool {
		state, _ := runStateIn(frames, res.RunID)
		return state == string(content.RunCompleted)
	})
	if calls := fetcher.calls.Load(); calls != 1 {
		t.Fatalf("the tool ran %d times, want exactly 1", calls)
	}
	select {
	case at := <-fetcher.stopped:
		t.Fatalf("the call's context ended at %s with nobody stopping it", at)
	default:
	}
	waittest.WaitForTimeout(t, "the result to reach the model", 10*time.Second, func() bool {
		return len(provider.toolResultBodies()) == 1
	})
	if !strings.Contains(provider.toolResultBodies()[0], blockedFetchText) {
		t.Fatalf("the model's tool result does not carry what the page said")
	}
}
