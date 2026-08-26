package transport

// The bead's unasserted criteria, now asserted (nocx-dw3.1): a delta the
// wire refuses becomes VISIBLE on the terminal state instead of vanishing
// into a hole that reads as a complete answer; a slow consumer is bounded
// on the delta path specifically; the data plane stays PTY-only (AD-1);
// and two concurrent runs interleave without corrupting either.
//
// The durable side is deliberately NOT under test here beyond "it stays
// whole": AppendRunDelta persisted before the notify is already proven by
// TestAgentAsk_StreamsTheAnswerAndTerminalizes. What this file proves is
// the live-view contract — the gap between the ledger and the socket.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/transport/outbound"
)

// ── fakes ──────────────────────────────────────────────────────────────────

// fakeAgentService records every store touch the stream makes. The ledger
// under test here is a recorder: what matters is WHICH writes happened and
// in what order, not the SQLite behind the real operation.
type fakeAgentService struct {
	mu          sync.Mutex
	transitions []content.RunState
	appended    []string
	finish      *content.FinishAgentRun
	// opened and sealed are the prose-block boundary the stream draws
	// (ADR-0040): which `text` children it asked the store to open, and which
	// it sealed. Recorded in order, because the order IS the assertion — a
	// block opens on the first delta after a call and seals when the next
	// call arrives.
	opened []content.ProseBlock
	sealed []string
	// appendedArtifacts is the artifact each chunk in `appended` landed in,
	// index for index.
	appendedArtifacts []string
	// prose numbers the blocks this fake hands out, so a test can name them.
	prose int
}

func (f *fakeAgentService) CaptureFrame(context.Context, content.CaptureFrame) (content.CaptureFrameResult, error) {
	return content.CaptureFrameResult{}, nil
}

func (f *fakeAgentService) SubmitAsk(context.Context, content.AgentAsk) (content.AgentAskResult, error) {
	return content.AgentAskResult{}, nil
}

func (f *fakeAgentService) TransitionRun(_ context.Context, _ int64, to content.RunState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transitions = append(f.transitions, to)
	return nil
}

func (f *fakeAgentService) FinishAgentRun(_ context.Context, _ int64, in content.FinishAgentRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finish = &in
	return nil
}

func (f *fakeAgentService) PriorTurn(_ context.Context, _, _ string) (*content.PriorTurn, error) {
	return nil, nil
}

func (f *fakeAgentService) OpenProse(_ context.Context, turnID string, _ int64) (content.ProseBlock, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prose++
	b := content.ProseBlock{
		EntryID:    fmt.Sprintf("%s/text-%d", turnID, f.prose),
		ArtifactID: fmt.Sprintf("%s/artifact-%d", turnID, f.prose),
	}
	f.opened = append(f.opened, b)
	return b, nil
}

func (f *fakeAgentService) SealProse(_ context.Context, entryID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sealed = append(f.sealed, entryID)
	return nil
}

// appendedTo records the ARTIFACT each chunk landed in beside the chunk
// itself: which block a delta was persisted into is the fact this bead moved,
// and a recorder that dropped the id could not report it.
func (f *fakeAgentService) AppendRunDelta(_ context.Context, artifactID string, _ int, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appended = append(f.appended, string(body))
	f.appendedArtifacts = append(f.appendedArtifacts, artifactID)
	return nil
}

func (f *fakeAgentService) FrameText(context.Context, string) (string, error) { return "", nil }

func (f *fakeAgentService) appendedText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.appended, "")
}

func (f *fakeAgentService) appendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.appended)
}

func (f *fakeAgentService) finishState() content.RunState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.finish == nil {
		return ""
	}
	return f.finish.State
}

func (f *fakeAgentService) transitionsText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, s := range f.transitions {
		b.WriteString(string(s))
		b.WriteString(",")
	}
	return b.String()
}

// directAgentOp is an AgentOperation that runs its function inline with the
// recorded service — the unit tests exercise the stream's logic, not the
// content gate (whose acquire/release the askHarness tests already cover).
type directAgentOp struct{ svc capability.AgentService }

func (o *directAgentOp) Run(_ context.Context, fn func(context.Context, capability.AgentService) error) error {
	return fn(context.Background(), o.svc)
}

// gapResponder is a Responder that records every notification and refuses
// agent.runDelta on demand with outbound.ErrStalled — the exact error a full
// queue or exhausted budget returns. failDeltaTimes counts DOWN: the first N
// runDelta notifications fail, later ones pass.
type gapResponder struct {
	mu             sync.Mutex
	failDeltaTimes int
	deltas         []agentRunDelta
	runState       *agentRunState
	approvalAsked  bool
}

func (r *gapResponder) TryResult(json.RawMessage, json.RawMessage) error { return nil }
func (r *gapResponder) TryError(json.RawMessage, RPCError) error         { return nil }

func (r *gapResponder) TryNotify(method string, params json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch method {
	case "agent.runDelta":
		var d agentRunDelta
		if err := json.Unmarshal(params, &d); err != nil {
			return fmt.Errorf("runDelta params: %w", err)
		}
		r.deltas = append(r.deltas, d)
		if r.failDeltaTimes > 0 {
			r.failDeltaTimes--
			return outbound.ErrStalled
		}
	case "agent.runState":
		var st agentRunState
		if err := json.Unmarshal(params, &st); err != nil {
			return fmt.Errorf("runState params: %w", err)
		}
		r.runState = &st
	case "agent.approvalRequested":
		r.approvalAsked = true
	}
	return nil
}

// newGapHandlers builds the agentHandlers the unit tests drive directly,
// with the real stream logic and a recording store.
func newGapHandlers(svc *fakeAgentService, client assistant.Client, approvals *assistant.ApprovalStore) agentHandlers {
	return agentHandlers{
		op:            &directAgentOp{svc: svc},
		log:           log.NewSlogAdapter(nil),
		client:        client,
		approvals:     approvals,
		pendingRuns:   make(map[int64]askRunContext),
		pendingRunsMu: &sync.Mutex{},
	}
}

func gapRunContext() askRunContext {
	return askRunContext{
		runID:    7,
		entryID:  "turn-1",
		question: "q",
	}
}

// ── criterion 1: the dropped delta becomes visible ─────────────────────────

// Every delta the wire refuses is counted, and the terminal agent.runState
// names the count — the renderer can mark the gap instead of reading a
// hole as a complete answer. The durable side stays whole: every chunk was
// persisted before the notify refused it.
func TestAgentAsk_DroppedDeltaIsVisibleOnTheTerminalState(t *testing.T) {
	svc := &fakeAgentService{}
	client := &scriptedAssistantClient{deltas: []string{"hello", " ", "world"}}
	h := newGapHandlers(svc, client, assistant.NewApprovalStore())
	r := &gapResponder{failDeltaTimes: 3} // the wire refuses EVERY delta

	h.runAskStream(context.Background(), gapRunContext(), r)

	if r.runState == nil {
		t.Fatal("no agent.runState notification — the drop count never reached the renderer")
	}
	if r.runState.DroppedDeltas != 3 {
		t.Errorf("runState droppedDeltas = %d, want 3 — the gap must be visible, not silent", r.runState.DroppedDeltas)
	}
	if r.runState.State != string(content.RunCompleted) {
		t.Errorf("runState state = %q, want completed — a dropped live delta is a visible bound, never a reason to fail the run", r.runState.State)
	}
	// The durable answer is whole: the ledger persisted every chunk even
	// though the wire refused every frame.
	if got := svc.appendedText(); got != "hello world" {
		t.Errorf("persisted answer = %q, want %q", got, "hello world")
	}
	if got := svc.finishState(); got != content.RunCompleted {
		t.Errorf("persisted run state = %q, want completed", got)
	}
}

// A refused delta does not abort the stream and does not disturb the wire's
// seq: a delta that gets through AFTER a dropped one carries the stream's
// own ascending seq, never the delivered count.
func TestAgentAsk_SeqAdvancesPastDroppedDeltas(t *testing.T) {
	svc := &fakeAgentService{}
	client := &scriptedAssistantClient{deltas: []string{"a", "b", "c"}}
	h := newGapHandlers(svc, client, assistant.NewApprovalStore())
	r := &gapResponder{failDeltaTimes: 1} // only the FIRST delta is refused

	h.runAskStream(context.Background(), gapRunContext(), r)

	if len(r.deltas) != 3 {
		t.Fatalf("received %d deltas, want 3 — a refused delta must not abort the stream", len(r.deltas))
	}
	for i, d := range r.deltas {
		if d.Seq != i {
			t.Errorf("delta %d seq = %d, want %d — seq must be the stream's, not the delivered count", i, d.Seq, i)
		}
		if d.EntryID != "turn-1" || d.RunID != 7 {
			t.Errorf("delta %d routed to run %d entry %q, want 7/turn-1", i, d.RunID, d.EntryID)
		}
	}
	if got := r.deltas[1].Text + r.deltas[2].Text; got != "bc" {
		t.Errorf("post-drop text = %q, want %q", got, "bc")
	}
	if r.runState == nil || r.runState.DroppedDeltas != 1 {
		t.Errorf("runState droppedDeltas = %+v, want 1", r.runState)
	}
}

// The count survives a suspension: drops recorded before the question
// reached the person are still part of the run's live-view record when the
// resume's (or the decline's) terminal close arrives.
func TestAgentAsk_DropCountSurvivesASuspension(t *testing.T) {
	svc := &fakeAgentService{}
	approvals := assistant.NewApprovalStore()
	h := newGapHandlers(svc, &scriptedAssistantClient{
		deltas: []string{"first"},
		err: &assistant.ApprovalRequestedError{Request: &assistant.ApprovalRequest{
			RunID: "7", Attempt: 1, Tool: "session.read", CallID: "c1", ArgHash: "h",
			Effect: content.EffectObserve,
		}},
	}, approvals)
	rc := gapRunContext()
	rc.attempt = 1
	h.pendingRuns[rc.runID] = rc // handleAsk stores the context before the stream
	first := &gapResponder{failDeltaTimes: 1}

	h.runAskStream(context.Background(), rc, first)

	if !first.approvalAsked {
		t.Fatal("the run never suspended for approval")
	}
	h.pendingRunsMu.Lock()
	stored, ok := h.pendingRuns[rc.runID]
	h.pendingRunsMu.Unlock()
	if !ok {
		t.Fatal("pending run context lost at suspension")
	}
	if stored.droppedBefore != 1 {
		t.Fatalf("stored droppedBefore = %d, want 1 — the suspension must carry the pre-approval drops", stored.droppedBefore)
	}

	// The person approves; the resume re-streams with the SAME run. Its
	// terminal close must name BOTH drops — the gap describes the whole
	// answer, not just the last Ask invocation.
	h.client = &scriptedAssistantClient{deltas: []string{"second"}}
	second := &gapResponder{failDeltaTimes: 1}
	h.resumeRun(context.Background(), stored, second)

	if second.runState == nil {
		t.Fatal("no terminal runState after the resume")
	}
	if second.runState.DroppedDeltas != 2 {
		t.Errorf("resume runState droppedDeltas = %d, want 2 (1 before + 1 after the approval)", second.runState.DroppedDeltas)
	}
	if got := svc.appendedText(); got != "firstsecond" {
		t.Errorf("persisted answer = %q, want %q", got, "firstsecond")
	}
	if got := svc.finishState(); got != content.RunCompleted {
		t.Errorf("persisted run state = %q, want completed", got)
	}
	// resumeRun moves the run to streaming itself before re-driving the
	// stream (which transitions streaming again), so the full span is
	// streaming → awaiting_approval → streaming → streaming.
	if got := svc.transitionsText(); got != "streaming,awaiting_approval,streaming,streaming," {
		t.Errorf("transitions = %q, want streaming → awaiting_approval → streaming → streaming", got)
	}
}

// ── criterion 2: a slow consumer is bounded on the delta path ──────────────

// blockingSocket is the pump's write seam held mid-write: WriteMessage
// blocks until released, so the consumer is genuinely stopped and the
// pump can never complete a frame while the test fills the queue.
type blockingSocket struct {
	released     chan struct{}
	writeStarted chan struct{}
	mu           sync.Mutex
	frames       []outbound.Frame
}

func (s *blockingSocket) ReadMessage() (int, []byte, error) { return 0, nil, fmt.Errorf("no reads") }
func (s *blockingSocket) SetWriteDeadline(time.Time) error  { return nil }
func (s *blockingSocket) Close() error                      { return nil }

func (s *blockingSocket) WriteMessage(msgType int, data []byte) error {
	select {
	case <-s.writeStarted:
	default:
		close(s.writeStarted)
	}
	<-s.released
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, outbound.Frame{MsgType: msgType, Data: append([]byte(nil), data...)})
	return nil
}

func (s *blockingSocket) deltaFrameCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, f := range s.frames {
		if _, ok := notificationParams(f.Data, "agent.runDelta"); ok {
			n++
		}
	}
	return n
}

func (s *blockingSocket) runStateFrame() *agentRunState {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.frames {
		if params, ok := notificationParams(f.Data, "agent.runState"); ok {
			var st agentRunState
			if err := json.Unmarshal(params, &st); err == nil {
				return &st
			}
		}
	}
	return nil
}

// notificationParams returns a text frame's params if it is a JSON-RPC
// notification with the given method.
func notificationParams(data []byte, method string) (json.RawMessage, bool) {
	var env struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(data, &env) != nil || env.Method != method {
		return nil, false
	}
	return env.Params, true
}

// outboundNotifyResponder is wsConn's write surface over a real bounded
// outbound.Conn — the delta path a real renderer sits behind.
type outboundNotifyResponder struct{ c *outbound.Conn }

func (r outboundNotifyResponder) TryResult(id json.RawMessage, result json.RawMessage) error {
	return r.c.TryEnqueueResponse(mustMarshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}))
}

func (r outboundNotifyResponder) TryError(id json.RawMessage, rpcErr RPCError) error {
	return r.c.TryEnqueueResponse(mustMarshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcErr}))
}

func (r outboundNotifyResponder) TryNotify(method string, params json.RawMessage) error {
	return r.c.TryEnqueue(websocket.TextMessage, mustMarshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params}))
}

// barrierAskClient emits N deltas, signals emitted, then blocks until the
// test releases it — the deterministic stand-in for "the stream is still
// producing while the consumer has stopped reading".
type barrierAskClient struct {
	n           int
	emitted     chan struct{}
	release     chan struct{}
	pumpStarted <-chan struct{}
	deltaCnt    int
}

func (b *barrierAskClient) Probe(context.Context, assistant.ProbeParams) (assistant.ProbeResult, error) {
	return assistant.ProbeResult{OK: true}, nil
}

// Discard implements assistant.Client. This fake holds no suspended
// state, so there is nothing to drop.
func (*barrierAskClient) Discard(string) {}

func (b *barrierAskClient) Ask(_ context.Context, _ assistant.AskParams, onEvent func(assistant.AskEvent) error) error {
	for i := 0; i < b.n; i++ {
		if err := onEvent(assistant.AskEvent{Kind: assistant.AskAnswer, Text: fmt.Sprintf("d%d", i)}); err != nil {
			return err
		}
		b.deltaCnt++
		if i == 0 && b.pumpStarted != nil {
			<-b.pumpStarted
		}
	}
	close(b.emitted)
	<-b.release
	return nil
}

// A consumer that stops reading must not make the server's memory grow
// without bound, asserted on the DELTA path: the real bounded queue accepts
// exactly its depth of runDelta frames and refuses the rest (ErrStalled),
// the producer is never wedged (every delta was emitted while the pump was
// blocked), and once the consumer drains, the terminal runState carries the
// count — the drop surfaces end to end.
func TestAgentAsk_SlowConsumerBoundsTheDeltaPath(t *testing.T) {
	const deltas = outbound.DefaultQueueDepth + 2 // 256 queued + 1 in the pump's hand + 1 refused
	svc := &fakeAgentService{}
	sock := &blockingSocket{
		released:     make(chan struct{}),
		writeStarted: make(chan struct{}),
	}
	conn := outbound.New(sock, outbound.Config{})
	t.Cleanup(conn.Close)
	client := &barrierAskClient{
		n:           deltas,
		emitted:     make(chan struct{}),
		release:     make(chan struct{}),
		pumpStarted: sock.writeStarted,
	}
	h := newGapHandlers(svc, client, assistant.NewApprovalStore())
	t.Cleanup(func() {
		select {
		case <-client.release:
		default:
			close(client.release)
		}
	})

	streamDone := make(chan struct{})
	go func() {
		h.runAskStream(context.Background(), gapRunContext(), outboundNotifyResponder{c: conn})
		close(streamDone)
	}()
	select {
	case <-sock.writeStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("outbound pump did not reach the blocking consumer")
	}

	// The producer emitted every delta while the pump was blocked: the queue
	// refused the overflow (counted, not buffered) and the stream kept going —
	// the producer never waits on the consumer.
	select {
	case <-client.emitted:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream never finished emitting — a full outbound queue wedged the producer")
	}
	if !conn.Stalled() {
		t.Error("connection not marked outbound-stalled after the overflow")
	}
	if got := conn.StallCount(); got != 1 {
		t.Errorf("stall count = %d, want 1", got)
	}
	if sock.deltaFrameCount() != 0 {
		t.Errorf("pump wrote %d frames while the consumer was stopped, want 0", sock.deltaFrameCount())
	}
	if got := svc.appendCount(); got != deltas {
		t.Errorf("persisted %d chunks, want %d — every delta was durable before the wire refused it", got, deltas)
	}
	// The consumer drains: the pump resumes and writes the queue's worth —
	// 257 = 256 queued + the one frame the pump already held when the
	// consumer stopped. The bound held: the refused frame never entered
	// memory.
	close(sock.released)
	const writtenDeltas = outbound.DefaultQueueDepth + 1
	waitFor(t, fmt.Sprintf("%d delta frames written", writtenDeltas), 10*time.Second, func() bool {
		return sock.deltaFrameCount() == writtenDeltas
	})
	if got := sock.deltaFrameCount(); got != writtenDeltas {
		t.Errorf("delta frames written = %d, want %d — the queue bound must hold, not the stream length", got, writtenDeltas)
	}

	// With the queue drained, the terminal runState gets through and carries
	// the count: the drop surfaces (criterion 1).
	close(client.release)
	waitFor(t, "terminal runState on the wire", 10*time.Second, func() bool {
		return sock.runStateFrame() != nil
	})
	st := sock.runStateFrame()
	if st.DroppedDeltas != 1 {
		t.Errorf("runState droppedDeltas = %d, want 1 (258 emitted, 256 buffered + 1 in the pump, 1 refused)", st.DroppedDeltas)
	}
	if st.State != string(content.RunCompleted) {
		t.Errorf("runState state = %q, want completed", st.State)
	}
	select {
	case <-streamDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream never terminalized")
	}
}

// ── criterion 3 (AD-1): the data plane carries no non-PTY payload ─────────

// strictTap is socketTap plus the AD-1 clause: a binary frame that does not
// decode as a session data frame is reported on bad instead of silently
// skipped — the binary plane's entire vocabulary is session frames.
type strictTap struct {
	data chan Frame
	msgs chan json.RawMessage
	bad  chan []byte
}

func newStrictTap(conn *websocket.Conn) *strictTap {
	t := &strictTap{
		data: make(chan Frame, 8192),
		msgs: make(chan json.RawMessage, 4096),
		bad:  make(chan []byte, 64),
	}
	go func() {
		defer close(t.data)
		defer close(t.msgs)
		defer close(t.bad)
		for {
			mt, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.BinaryMessage {
				if f, derr := DecodeFrame(payload); derr == nil {
					t.data <- f
				} else {
					t.bad <- payload
				}
				continue
			}
			t.msgs <- payload
		}
	}()
	return t
}

// strictCall sends one JSON-RPC request over the strict tap and waits for
// the response carrying the same id (notifications pass through untouched)
// — the strictTap twin of tapCall.
func strictCall(t *testing.T, conn *websocket.Conn, tap *strictTap, id int, method string, params any) json.RawMessage {
	t.Helper()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal %s: %v", method, err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
	want := fmt.Sprintf("%d", id)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case msg, ok := <-tap.msgs:
			if !ok {
				t.Fatalf("socket closed before %s answered", method)
			}
			var env struct {
				ID *json.RawMessage `json:"id"`
			}
			if json.Unmarshal(msg, &env) != nil || env.ID == nil || string(*env.ID) != want {
				continue // a notification, or another call's response
			}
			return msg
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("%s never answered", method)
	return nil
}

// strictNotify waits for the next notification with the method on the
// strict tap — the strictTap twin of tapNotify.
func strictNotify(t *testing.T, tap *strictTap, method string, timeout time.Duration) json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case msg, ok := <-tap.msgs:
			if !ok {
				t.Fatalf("socket closed before %s arrived", method)
			}
			var n struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(msg, &n) != nil || n.Method != method {
				continue
			}
			return n.Params
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("no %s notification within %v", method, timeout)
	return nil
}

// During an ask, every agent notification rides the TEXT control plane, and
// the binary plane carries nothing that is not a session data frame — and
// nothing that contains the model's answer. AD-1 asserted, not assumed.
func TestAgentAsk_DataPlaneCarriesNoNonPTYPayload(t *testing.T) {
	client := &scriptedAssistantClient{deltas: []string{"hello", " ", "world"}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)

	// The strict reader owns the socket from here on; the wire calls go
	// through it (strictCall/strictNotify), never the conn directly.
	tap := newStrictTap(h.conn)

	askRaw := strictCall(t, h.conn, tap, 2, "agent.ask", map[string]any{
		"askId": "ask-ad1", "sessionId": sid, "question": "q", "cwd": "/repo",
		"attachedContent": []any{},
	})
	var ar struct {
		Result struct {
			RunID int64  `json:"runId"`
			State string `json:"state"`
		} `json:"result"`
	}
	if err := json.Unmarshal(askRaw, &ar); err != nil || ar.Result.State != "prepared" {
		t.Fatalf("ask over the tap: %v (%s)", err, askRaw)
	}

	// The whole stream flows over the text plane.
	for range client.deltaCount() {
		if raw := strictNotify(t, tap, "agent.runDelta", 5*time.Second); len(raw) == 0 {
			t.Fatal("empty runDelta notification")
		}
	}
	if raw := strictNotify(t, tap, "agent.runState", 5*time.Second); len(raw) == 0 {
		t.Fatal("empty runState notification")
	}

	// Every binary frame during the ask was a session data frame (none on
	// bad), and no binary payload smuggled the model's answer onto the
	// PTY plane.
	var binText strings.Builder
	binFrames := 0
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case f, ok := <-tap.data:
			if !ok {
				deadline = time.Now()
				continue
			}
			binFrames++
			binText.Write(f.Payload)
		case b, ok := <-tap.bad:
			if !ok {
				deadline = time.Now()
				continue
			}
			t.Errorf("binary frame that is not a session data frame (AD-1): %q", b)
		case <-time.After(100 * time.Millisecond):
			deadline = time.Now()
		}
	}
	if got := binText.String(); strings.Contains(got, "hello") || strings.Contains(got, "world") {
		t.Errorf("model answer text found in a binary frame payload (AD-1): %q", got)
	}
	t.Logf("binary frames during the ask: %d (all session data frames, none carrying model text)", binFrames)
}

// ── criterion 1: two concurrent runs interleave without corrupting ────────

// interleavedAskClient streams two concurrent Ask calls with strictly
// alternating deltas: the wire order genuinely interleaves the runs, which
// is the point the acceptance criterion drives.
type interleavedAskClient struct {
	mu          sync.Mutex
	scripts     [][]string
	nextScript  int
	started     int
	bothStarted chan struct{}
	turn        chan struct{}
}

func newInterleavedAskClient(a, b []string) *interleavedAskClient {
	turn := make(chan struct{}, 1)
	turn <- struct{}{}
	return &interleavedAskClient{
		scripts:     [][]string{a, b},
		bothStarted: make(chan struct{}),
		turn:        turn,
	}
}

func (c *interleavedAskClient) Probe(context.Context, assistant.ProbeParams) (assistant.ProbeResult, error) {
	return assistant.ProbeResult{OK: true}, nil
}

// Discard implements assistant.Client. This fake holds no suspended
// state, so there is nothing to drop.
func (*interleavedAskClient) Discard(string) {}

func (c *interleavedAskClient) Ask(_ context.Context, _ assistant.AskParams, onEvent func(assistant.AskEvent) error) error {
	c.mu.Lock()
	script := c.scripts[c.nextScript]
	c.nextScript++
	c.started++
	if c.started == 2 {
		close(c.bothStarted)
	}
	c.mu.Unlock()
	<-c.bothStarted // both runs are streaming before either emits
	for _, d := range script {
		<-c.turn
		if err := onEvent(assistant.AskEvent{Kind: assistant.AskAnswer, Text: d}); err != nil {
			c.turn <- struct{}{}
			return err
		}
		c.turn <- struct{}{}
	}
	return nil
}

// Two runs in flight at once, both streaming: each entry's seq ascends
// independently and no text lands on the wrong entryId. The whole flow is
// read through ONE tap: a terminal runState can arrive while the deltas
// are still being read, and a reader that consumes it (readNotification)
// would make the test race the streams.
func TestAgentAsk_TwoConcurrentStreamsInterleaveWithoutCorrupting(t *testing.T) {
	client := newInterleavedAskClient(
		[]string{"aaa1", "aaa2", "aaa3"},
		[]string{"bbb1", "bbb2", "bbb3"},
	)
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	tap := newSocketTap(h.conn)

	askARaw := tapCall(t, h.conn, tap, 11, "agent.ask", map[string]any{
		"askId": "ask-a", "sessionId": sid, "question": "first?", "cwd": "/repo",
		"attachedContent": []any{},
	})
	var askARes struct {
		Result agentAskResponse `json:"result"`
	}
	if err := json.Unmarshal(askARaw, &askARes); err != nil {
		t.Fatalf("first ask response: %v (%s)", err, askARaw)
	}
	resA := askARes.Result
	askBRaw := tapCall(t, h.conn, tap, 12, "agent.ask", map[string]any{
		"askId": "ask-b", "sessionId": sid, "question": "second?", "cwd": "/repo",
		"attachedContent": []any{},
	})
	var askBRes struct {
		Result agentAskResponse `json:"result"`
	}
	if err := json.Unmarshal(askBRaw, &askBRes); err != nil {
		t.Fatalf("second ask response: %v (%s)", err, askBRaw)
	}
	resB := askBRes.Result
	if resA.State != "prepared" || resB.State != "prepared" {
		t.Fatalf("ask states = %q/%q, want prepared/prepared", resA.State, resB.State)
	}
	if resA.RunID == resB.RunID {
		t.Fatalf("both asks minted run %d — two concurrent runs need distinct identities", resA.RunID)
	}

	// Classify every notification until both runs have streamed three
	// deltas and terminalized — the terminal states may arrive while the
	// deltas are still in flight.
	seqs := map[int64][]int{}
	texts := map[int64]string{}
	states := map[int64]string{}
	deadline := time.Now().Add(15 * time.Second)
	for (len(seqs[resA.RunID]) < 3 || len(seqs[resB.RunID]) < 3 || states[resA.RunID] == "" || states[resB.RunID] == "") && time.Now().Before(deadline) {
		select {
		case msg, ok := <-tap.msgs:
			if !ok {
				t.Fatal("socket closed before both runs finished")
			}
			var n struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(msg, &n) != nil {
				continue
			}
			switch n.Method {
			case "agent.runDelta":
				var d agentRunDelta
				if json.Unmarshal(n.Params, &d) != nil {
					continue
				}
				seqs[d.RunID] = append(seqs[d.RunID], d.Seq)
				texts[d.RunID] += d.Text
			case "agent.runState":
				var st struct {
					RunID int64  `json:"runId"`
					State string `json:"state"`
				}
				if json.Unmarshal(n.Params, &st) != nil {
					continue
				}
				states[st.RunID] = st.State
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if len(seqs[resA.RunID]) != 3 || len(seqs[resB.RunID]) != 3 {
		t.Fatalf("delta counts = %d/%d, want 3/3 — a run never finished streaming", len(seqs[resA.RunID]), len(seqs[resB.RunID]))
	}
	if states[resA.RunID] != "completed" || states[resB.RunID] != "completed" {
		t.Fatalf("terminal states = %v, want both completed", states)
	}

	for run, got := range seqs {
		if run != resA.RunID && run != resB.RunID {
			t.Errorf("delta routed to run %d, want %d or %d", run, resA.RunID, resB.RunID)
		}
		for i, s := range got {
			if s != i {
				t.Errorf("run %d seqs = %v, want [0 1 2] ascending independently", run, got)
				break
			}
		}
	}
	// No text landed on the wrong run: run A carries only aaa*, run B only
	// bbb* — a cross-contaminated entry would mix the families.
	for run, txt := range texts {
		if run == resA.RunID && strings.Contains(txt, "bbb") {
			t.Errorf("run %d (first ask) received %q — text from the other run", run, txt)
		}
		if run == resB.RunID && strings.Contains(txt, "aaa") {
			t.Errorf("run %d (second ask) received %q — text from the other run", run, txt)
		}
	}

	// And each run's own prose holds exactly its own text: two runs, two
	// turns, two sets of `text` children, and nothing crossed over.
	led := h.db.Ledger()
	for run, want := range map[int64]string{resA.RunID: "aaa1aaa2aaa3", resB.RunID: "bbb1bbb2bbb3"} {
		var entryID string
		if run == resA.RunID {
			entryID = resA.EntryID
		} else {
			entryID = resB.EntryID
		}
		ans, err := led.Entry(context.Background(), entryID)
		if err != nil || ans == nil {
			t.Fatalf("run %d answer entry: %v (err %v)", run, ans, err)
		}
		if len(ans.Executions) != 1 || len(ans.Executions[0].Artifacts) != 0 {
			t.Fatalf("run %d executions/artifacts = %d/%d, want 1/0 — the answer is its prose children",
				run, len(ans.Executions), len(ans.Executions[0].Artifacts))
		}
		if body := proseBodyOf(t, led, entryID); body != want {
			t.Errorf("run %d prose = %q, want %q", run, body, want)
		}
	}
}
