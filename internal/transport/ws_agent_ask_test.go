package transport

// agent.ask end to end (nocx-x8s2.2): the run the f4s5 transaction prepares
// is driven by the assistant engine, the answer streams into the ledger and
// over the wire, and the run terminalizes. The engine under test is a
// SCRIPTED stub — the real streaming against a fake OpenAI SSE server is
// proven in internal/assistant; here the orchestration (endpoint resolve,
// transaction, notifications, terminal states, ledger writes) is proven
// without dialling anything.

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
)

// scriptedAssistantClient is the injected engine: Ask plays back a script of
// deltas and an outcome, so the orchestration is deterministic.
type scriptedAssistantClient struct {
	mu             sync.Mutex
	deltas         []string
	err            error // terminal error Ask returns after the deltas
	received       []assistant.Message
	receivedParams assistant.AskParams
	aborted        bool // onDelta returned an error and Ask aborted
	release        <-chan struct{}
	askCalls       int // how many times Ask ran — the "was the model called" fact
}

func (s *scriptedAssistantClient) Probe(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
	return assistant.ProbeResult{OK: true, Model: p.Model}, nil
}

// Discard implements assistant.Client. This fake holds no suspended
// state, so there is nothing to drop.
func (*scriptedAssistantClient) Discard(string) {}

func (s *scriptedAssistantClient) Ask(ctx context.Context, p assistant.AskParams, onEvent func(assistant.AskEvent) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.askCalls++
	s.received = append([]assistant.Message(nil), p.Messages...)
	s.receivedParams = p
	for _, d := range s.deltas {
		if err := onEvent(assistant.AskEvent{Kind: assistant.AskAnswer, Text: d}); err != nil {
			s.aborted = true
			return err
		}
	}
	return s.err
}

// askHarness is a full transport env for the ask flow: real content store,
// real vault, real profile store, scripted engine.
type askHarness struct {
	t    *testing.T
	v    *vault.Vault
	db   content.ContentDB
	ws   *WSServer
	conn *websocket.Conn
	// fakeRequests counts provider requests the readScreen failure-path
	// tests drive (the harness itself never dials a provider).
	fakeRequests atomic.Int64
}

func newAskHarness(t *testing.T, client assistant.Client) *askHarness {
	return newAskHarnessWithOpts(t, client)
}

// newAskHarnessWithOpts is newAskHarness with additional WSServerOptions —
// for tests that need a seam named at construction (the policy tests wire
// WithAgentPolicy here, so the control plane registers the methods wired).
func newAskHarnessWithOpts(t *testing.T, client assistant.Client, extra ...WSServerOption) *askHarness {
	dir := t.TempDir()
	docStore := storage.NewDocumentStore(dir)
	reg, err := vault.NewRegistry(file.New(docStore, "vault-blob.json"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(nil, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := vault.New(docStore, reg, logger)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	t.Cleanup(v.Close)
	if _, setupErr := v.Setup(t.Context(), vault.SetupRequest{Passphrase: "test"}); setupErr != nil {
		t.Fatalf("Setup: %v", setupErr)
	}
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(t.Context(), content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	askPaneIn(t, db)

	opts := []WSServerOption{
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(v), WithVaultUnsealer(v), WithVaultLifecycle(v),
		WithAgentKnownMaterial(adapterKnownMaterial(v)),
		WithContentDB(db),
		WithAssistantClient(client),
		WithAssistantProbeStore(assistant.NewProbeStore()),
	}
	opts = append(opts, extra...)
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), opts...)
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return &askHarness{t: t, v: v, db: db, ws: ws, conn: conn}
}

// createEndpoint makes one endpoint with a resolvable key.
func (h *askHarness) createEndpoint() {
	h.t.Helper()
	e, code := decodeEndpointResult(h.t, jsonrpcCall(h.t, h.conn, "endpoints.create", map[string]any{
		"name":    "Local",
		"baseUrl": "http://127.0.0.1:11434/v1",
		"schema":  "openai-compatible",
		"key":     "sk-test-123",
		"models":  []map[string]any{{"name": "qwen3"}},
	}))
	if code != 0 {
		h.t.Fatalf("endpoints.create: code %d", code)
	}
	// The ask now resolves through the ANSWERING ROLE (bead nocx-e6kn2),
	// so the ordinary path's fixture assigns the role as well: an endpoint
	// with models but no role assignment is refused, by design.
	assign := jsonrpcCall(h.t, h.conn, "roles.assign", map[string]any{
		"role":       "answering",
		"endpointId": e.ID,
		"model":      "qwen3",
	})
	if isErrorResponse(h.t, assign) {
		h.t.Fatalf("roles.assign: %s", assign)
	}
}

// ── the happy path: ask → deltas on the right entry → terminal completed ──

// A person selects a finished block, types a question, and the answer that
// arrives is recorded in the ledger and streamed over the wire in order.
// The wire shape is pinned here exactly: the ask result carries the answer
// entry id; runDelta carries runId + entryId + ascending seq + text;
// runState closes the run.
func TestAgentAsk_StreamsTheAnswerAndTerminalizes(t *testing.T) {
	client := &scriptedAssistantClient{deltas: []string{"hello", " ", "world"}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":           "ask-1",
		"sessionId":       sid,
		"question":        "what does this screen mean?",
		"cwd":             "/repo",
		"attachedContent": []any{},
	}, 2)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	if res.State != "prepared" {
		t.Fatalf("ask state = %q, want prepared", res.State)
	}
	if res.EntryID == "" {
		t.Fatal("ask result carries no answerEntryId — the renderer cannot place the answer block")
	}
	if res.Model != "qwen3" {
		t.Errorf("ask result model = %q, want qwen3 — the person must be able to tell which model answered (nocx-e6kn2)", res.Model)
	}

	// The deltas arrive as agent.runDelta with ascending seq, routed by
	// runId and entryId.
	wantSeq := 0
	var streamed string
	for range client.deltaCount() {
		raw := readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
		var d struct {
			RunID   int64  `json:"runId"`
			EntryID string `json:"entryId"`
			Seq     int    `json:"seq"`
			Text    string `json:"text"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("runDelta unmarshal: %v\nraw: %s", err, raw)
		}
		if d.RunID != res.RunID {
			t.Errorf("runDelta runId = %d, want %d", d.RunID, res.RunID)
		}
		if d.EntryID != res.EntryID {
			t.Errorf("runDelta entryId = %q, want %q — deltas must land on the answer entry", d.EntryID, res.EntryID)
		}
		if d.Seq != wantSeq {
			t.Errorf("runDelta seq = %d, want %d (ascending from 0)", d.Seq, wantSeq)
		}
		wantSeq++
		streamed += d.Text
	}
	if streamed != "hello world" {
		t.Errorf("streamed answer = %q, want %q", streamed, "hello world")
	}

	// No terminal output is inlined into the prompt. The old frame-reference
	// path is gone; marked items are described by metadata and read through
	// session.read instead.
	messages := client.messages()
	var full string
	for _, m := range messages {
		full += m.Content + "\n"
	}
	if strings.Contains(full, "line one") || strings.Contains(full, "line two") {
		t.Errorf("engine messages inlined terminal output: %q", full)
	}
	if strings.Contains(full, "no-such-block") {
		t.Errorf("engine messages contain text from another block: %q", full)
	}

	// The run terminalizes completed.
	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var st struct {
		RunID int64  `json:"runId"`
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("runState unmarshal: %v\nraw: %s", err, raw)
	}
	if st.RunID != res.RunID || st.State != "completed" {
		t.Fatalf("runState = runId %d state %q, want %d completed", st.RunID, st.State, res.RunID)
	}
	if st.Error != "" {
		t.Errorf("completed runState carries an error: %q", st.Error)
	}

	// The ledger is the record: the turn's prose blocks hold the streamed
	// text, sealed, and the run is terminal.
	led := h.db.Ledger()
	ctx := context.Background()
	ans, err := led.Entry(ctx, res.EntryID)
	if err != nil || ans == nil {
		t.Fatalf("answer entry: %v (err %v)", ans, err)
	}
	if ans.Phase != content.PhaseClosed || ans.Status != content.EntrySuccess {
		t.Errorf("answer entry phase/status = %q/%q, want closed/success", ans.Phase, ans.Status)
	}
	// Nothing contains it: the answer is the turn's own body, so there is no
	// second entry to point at (nocx-4em1z). Containment is a column since
	// ADR-0040, so the question is asked of the row itself.
	if ans.ParentID != nil {
		t.Errorf("the turn is drawn inside %q — the answer is its own body", *ans.ParentID)
	}
	// The turn opens no body of its own: the answer is its `text` children,
	// and with no tool call in this run there is exactly one of them
	// (ADR-0040).
	if len(ans.Executions) != 1 || len(ans.Executions[0].Artifacts) != 0 {
		t.Fatalf("answer entry executions/artifacts = %d/%d, want 1/0",
			len(ans.Executions), len(ans.Executions[0].Artifacts))
	}
	prose := proseUnder(t, led, res.EntryID)
	if len(prose) != 1 {
		t.Fatalf("the run left %+v, want the one run of prose it wrote", prose)
	}
	if prose[0].text != "hello world" {
		t.Errorf("the prose block = %q, want %q", prose[0].text, "hello world")
	}
	if prose[0].state != content.ArtifactSealed {
		t.Errorf("the prose block's body = %q, want sealed", prose[0].state)
	}
	q, err := led.Entry(ctx, res.EntryID)
	if err != nil || q == nil {
		t.Fatalf("question entry: %v (err %v)", q, err)
	}
	if len(q.Executions) != 1 || q.Executions[0].State == nil || *q.Executions[0].State != content.RunCompleted {
		t.Fatalf("question run state = %v, want completed", q.Executions[0].State)
	}
	if q.Phase != content.PhaseClosed || q.Status != content.EntrySuccess {
		t.Errorf("question entry phase/status = %q/%q, want closed/success", q.Phase, q.Status)
	}
}

// ── no endpoint configured: the ask is a refusal the surface can render ──

func TestAgentAsk_NoEndpointIsARefusal(t *testing.T) {
	h := newAskHarness(t, &scriptedAssistantClient{deltas: []string{"x"}})
	sid := openLocalSession(t, h.conn)
	_, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":           "ask-1",
		"sessionId":       sid,
		"question":        "q",
		"cwd":             "/repo",
		"attachedContent": []any{},
	}, 2)
	if errObj == nil {
		t.Fatal("ask with no endpoint succeeded, want a refusal")
	}
	// The refusal names the ANSWERING ROLE and its repair: resolution goes
	// through the role (bead nocx-e6kn2) — a role with no assignment is a
	// visible refusal, never a silent fallback to some other model.
	if !strings.Contains(errObj.Message, "answering role") {
		t.Errorf("refusal message = %q, want it to name the answering role", errObj.Message)
	}
	if !strings.Contains(errObj.Message, "Roles") {
		t.Errorf("refusal message = %q, want it to name the repair (Settings → Roles)", errObj.Message)
	}
}

// ── failure: the model fails mid-stream → failed with a renderable reason ─

func TestAgentAsk_ModelFailureTerminalizesFailed(t *testing.T) {
	h := newAskHarness(t, &scriptedAssistantClient{
		deltas: []string{"partial"},
		err:    &assistant.StreamError{Message: "the model returned no text"},
	})
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":           "ask-1",
		"sessionId":       sid,
		"question":        "q",
		"cwd":             "/repo",
		"attachedContent": []any{},
	}, 2)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	_ = raw
	raw = readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var st struct {
		RunID int64  `json:"runId"`
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("runState unmarshal: %v", err)
	}
	if st.State != "failed" {
		t.Fatalf("runState = %q, want failed", st.State)
	}
	if !strings.Contains(st.Error, "no text") {
		t.Errorf("failed runState error = %q, want the renderable reason", st.Error)
	}
	q, err := h.db.Ledger().Entry(context.Background(), res.EntryID)
	if err != nil || q == nil {
		t.Fatalf("question entry: %v (err %v)", q, err)
	}
	if q.Executions[0].State == nil || *q.Executions[0].State != content.RunFailed {
		t.Errorf("persisted run state = %v, want failed", q.Executions[0].State)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func (s *scriptedAssistantClient) messages() []assistant.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.received
}

func (s *scriptedAssistantClient) askCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.askCalls
}

func (s *scriptedAssistantClient) deltaCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.deltas)
}

var (
	_ = websocket.TextMessage
	_ = credential.NewSecret
)

// blockingAskClient emits ONE delta then blocks until released — the test
// closes the connection mid-stream and watches the run terminalize (a
// refused socket write must not wedge the run: the connection context
// cancels the stream and the run closes failed, "the connection was lost").
type blockingAskClient struct {
	firstDone chan struct{} // closed after the first delta is emitted
	release   chan struct{} // the test closes this to unblock
}

func (b *blockingAskClient) Probe(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
	return assistant.ProbeResult{OK: true}, nil
}

// Discard implements assistant.Client. This fake holds no suspended
// state, so there is nothing to drop.
func (*blockingAskClient) Discard(string) {}

func (b *blockingAskClient) Ask(ctx context.Context, p assistant.AskParams, onEvent func(assistant.AskEvent) error) error {
	if err := onEvent(assistant.AskEvent{Kind: assistant.AskAnswer, Text: "first"}); err != nil {
		return err
	}
	close(b.firstDone)
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// The connection dies mid-stream: the stream's task context cancels, the
// run terminalizes failed with the renderable reason, and the ledger is the
// record — a reconnect reads the terminal state, never waits forever.
func TestAgentAsk_ConnectionLostMidStreamTerminalizes(t *testing.T) {
	client := &blockingAskClient{firstDone: make(chan struct{}), release: make(chan struct{})}
	h := newAskHarness(t, client)
	h.createEndpoint()
	conn := h.conn
	sid := openLocalSession(t, conn)
	res, errObj := askOverWire(t, conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "q", "cwd": "/repo",
		"attachedContent": []any{},
	}, 2)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	// The first delta arrived — the stream is genuinely mid-flight.
	raw := readNotification(t, conn, "agent.runDelta", 5*time.Second)
	_ = raw

	// The connection dies: close the socket. The stream's task context
	// derives from the connection, so the cancellation propagates.
	_ = conn.Close()
	select {
	case <-client.firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stream never started")
	}

	// The run terminalizes failed with the renderable reason. Poll the
	// ledger — the runState notification goes to a dead socket and is
	// dropped; the record is what a reconnect reads.
	deadline := time.Now().Add(5 * time.Second)
	for {
		q, err := h.db.Ledger().Entry(context.Background(), res.EntryID)
		if err != nil || q == nil {
			t.Fatalf("question entry: %v (err %v)", q, err)
		}
		st := q.Executions[0].State
		if st != nil && *st != content.RunPrepared && *st != content.RunStreaming {
			if *st != content.RunFailed {
				t.Fatalf("run state = %v, want failed", *st)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never terminalized; state = %v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The failure sentence is recorded on the run's payload — the ledger is
	// the record, and the reconnect reads the reason there.
	q, err := h.db.Ledger().Entry(context.Background(), res.EntryID)
	if err != nil || q == nil {
		t.Fatalf("question entry: %v", err)
	}
	if !strings.Contains(q.Executions[0].Payload, "connection was lost") {
		t.Errorf("run payload = %q, want the renderable reason", q.Executions[0].Payload)
	}
}

// ── the general question (nocx-4wtlh): ⌘Enter with no chips is a question
//    about nothing pointed at — zero references, and the ask still streams ──

func TestAgentAsk_GeneralQuestionWithNoReferencesStreams(t *testing.T) {
	client := &scriptedAssistantClient{deltas: []string{"sure"}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)

	// No captureFrame at all: the gesture for a general question is type +
	// ⌘Enter, and the payload carries the chips that are in the line — none.
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":           "ask-general-1",
		"sessionId":       sid,
		"question":        "what is the capital of France?",
		"cwd":             "/repo",
		"attachedContent": []any{},
	}, 2)
	if errObj != nil {
		t.Fatalf("general ask refused: %+v", errObj)
	}
	if res.State != "prepared" {
		t.Fatalf("ask state = %q, want prepared", res.State)
	}

	for range client.deltaCount() {
		raw := readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
		_ = raw
	}

	// The engine received the question and NO frame text — nothing was
	// pointed at, so nothing may ride the prompt.
	messages := client.messages()
	var full string
	for _, m := range messages {
		full += m.Content + "\n"
	}
	if !strings.Contains(full, "what is the capital of France?") {
		t.Errorf("engine messages lack the question: %q", full)
	}
	if strings.Contains(full, "Referenced frame") {
		t.Errorf("general ask carried frame text the person never pointed at: %q", full)
	}
	// The engine is handed the standing prompt and the question, and
	// nothing else. NO part of what it is told claims attached content
	// (nocx-6ujux): the "screen content follows" sentence is DERIVED from
	// what is attached, and nothing is attached. Asserted on the message
	// list itself, so a rewrite of the assembly cannot pass by leaving the
	// old constant in place.
	if len(messages) != 2 ||
		messages[0].Role != "system" ||
		messages[1].Role != "user" || messages[1].Content != "what is the capital of France?" {
		t.Errorf("engine received %d message(s), want the standing prompt and the question: %#v", len(messages), messages)
	}
	if strings.Contains(messages[0].Content, "attached to this question") {
		t.Errorf("a zero-reference ask claims attached content:\n%s", messages[0].Content)
	}

	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var st struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("runState unmarshal: %v", err)
	}
	if st.State != "completed" {
		t.Fatalf("runState = %q, want completed", st.State)
	}
}

// ── the answering ROLE is the resolution (bead nocx-e6kn2) ─────────────

// A person assigns a model to the answering role and the ask picks it up —
// WITHOUT the ask ever naming a model id. The engine receives the ASSIGNED
// pair, even when another endpoint's model is first in the list.
func TestAgentAsk_UsesTheAnsweringRoleAssignment(t *testing.T) {
	client := &scriptedAssistantClient{deltas: []string{"x"}}
	h := newAskHarness(t, client)
	// TWO endpoints; the FIRST is the one the old eps[0] path would have
	// picked. The role names the SECOND.
	firstRaw := jsonrpcCall(t, h.conn, "endpoints.create", map[string]any{
		"name": "First", "baseUrl": "http://127.0.0.1:11434/v1", "schema": "openai-compatible",
		"key": "sk-1", "models": []map[string]any{{"name": "qwen3"}},
	})
	first, _ := decodeEndpointResult(t, firstRaw)
	secondRaw := jsonrpcCall(t, h.conn, "endpoints.create", map[string]any{
		"name": "Second", "baseUrl": "https://api.example.com/v1", "schema": "openai-compatible",
		"key": "sk-2", "models": []map[string]any{{"name": "gpt-4o"}, {"name": "gpt-4o-mini"}},
	})
	second, _ := decodeEndpointResult(t, secondRaw)
	if err := isErrorResponse(t, jsonrpcCall(t, h.conn, "roles.assign", map[string]any{
		"role": "answering", "endpointId": second.ID, "model": "gpt-4o",
	})); err {
		t.Fatal("roles.assign refused")
	}
	_ = first

	sid := openLocalSession(t, h.conn)
	askRes, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-role", "sessionId": sid, "question": "q", "cwd": "/repo",
		"attachedContent": []any{},
	}, 2)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	// The answer carries the model attribution on the wire: a person can
	// tell WHICH model answered without reading the assignment.
	if askRes.Model != "gpt-4o" {
		t.Errorf("ask result model = %q, want the assigned gpt-4o", askRes.Model)
	}
	// The stream runs asynchronously; its terminal notification is the
	// synchronization point before the params are asserted.
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if got := client.receivedParams.BaseURL; got != "https://api.example.com/v1" {
		t.Errorf("engine base URL = %q, want the ASSIGNED endpoint's", got)
	}
	if got := client.receivedParams.Model; got != "gpt-4o" {
		t.Errorf("engine model = %q, want the ASSIGNED model, not the first endpoint's first model", got)
	}
}

// A reassignment is picked up by the NEXT ask: the role resolves from the
// store on every ask, so "the feature picks up the change" holds with no
// model id named anywhere but the assignment.
func TestAgentAsk_ReassignmentIsPickedUp(t *testing.T) {
	client := &scriptedAssistantClient{deltas: []string{"x"}}
	h := newAskHarness(t, client)
	h.createEndpoint() // "Local" with model qwen3, answering → qwen3
	otherRaw := jsonrpcCall(t, h.conn, "endpoints.create", map[string]any{
		"name": "Other", "baseUrl": "https://api.example.com/v1", "schema": "openai-compatible",
		"key": "sk", "models": []map[string]any{{"name": "gpt-4o"}},
	})
	other, _ := decodeEndpointResult(t, otherRaw)
	if isErrorResponse(t, jsonrpcCall(t, h.conn, "roles.assign", map[string]any{
		"role": "answering", "endpointId": other.ID, "model": "gpt-4o",
	})) {
		t.Fatal("reassign refused")
	}
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-re", "sessionId": sid, "question": "q", "cwd": "/repo",
		"attachedContent": []any{},
	}, 2); errObj != nil {
		t.Fatalf("agent.ask after reassignment: %+v", errObj)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if got := client.receivedParams.Model; got != "gpt-4o" {
		t.Errorf("engine model after reassignment = %q, want gpt-4o", got)
	}
}

// A deleted endpoint leaves the role unresolvable and the refusal names it
// — never a hop to the OTHER endpoint that still exists.
func TestAgentAsk_DeletedEndpointLeavesTheRoleARefusal(t *testing.T) {
	client := &scriptedAssistantClient{deltas: []string{"x"}}
	h := newAskHarness(t, client)
	raw := jsonrpcCall(t, h.conn, "endpoints.create", map[string]any{
		"name": "Local", "baseUrl": "http://127.0.0.1:11434/v1", "schema": "openai-compatible",
		"key": "sk-test-123", "models": []map[string]any{{"name": "qwen3"}},
	})
	created, code := decodeEndpointResult(t, raw)
	if code != 0 {
		t.Fatalf("endpoints.create: code %d", code)
	}
	if isErrorResponse(t, jsonrpcCall(t, h.conn, "roles.assign", map[string]any{
		"role": "answering", "endpointId": created.ID, "model": "qwen3",
	})) {
		t.Fatal("assign refused")
	}
	if isErrorResponse(t, jsonrpcCall(t, h.conn, "endpoints.delete", map[string]any{"id": created.ID})) {
		t.Fatal("delete refused")
	}
	sid := openLocalSession(t, h.conn)
	_, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-d", "sessionId": sid, "question": "q", "cwd": "/repo",
		"attachedContent": []any{},
	}, 2)
	if errObj == nil {
		t.Fatal("ask after endpoint delete succeeded, want a refusal")
	}
	if !strings.Contains(errObj.Message, "no longer exists") {
		t.Errorf("refusal message = %q, want it to name the deleted endpoint", errObj.Message)
	}
	if !strings.Contains(errObj.Message, "answering") {
		t.Errorf("refusal message = %q, want it to name the role", errObj.Message)
	}
	if client.askCount() != 0 {
		t.Error("the model must never be called when the role cannot resolve")
	}
}

// A model removed from the assigned endpoint leaves the role unresolvable
// and the refusal names the model — never the endpoint's next model.
func TestAgentAsk_RemovedModelLeavesTheRoleARefusal(t *testing.T) {
	client := &scriptedAssistantClient{deltas: []string{"x"}}
	h := newAskHarness(t, client)
	raw := jsonrpcCall(t, h.conn, "endpoints.create", map[string]any{
		"name": "Local", "baseUrl": "http://127.0.0.1:11434/v1", "schema": "openai-compatible",
		"key": "sk-test-123", "models": []map[string]any{{"name": "qwen3"}, {"name": "gpt-4o"}},
	})
	created, _ := decodeEndpointResult(t, raw)
	if isErrorResponse(t, jsonrpcCall(t, h.conn, "roles.assign", map[string]any{
		"role": "answering", "endpointId": created.ID, "model": "gpt-4o",
	})) {
		t.Fatal("assign refused")
	}
	// The update drops gpt-4o from the model list — "qwen3" remains, and
	// must NOT be silently substituted.
	up := jsonrpcCall(t, h.conn, "endpoints.update", map[string]any{
		"id": created.ID, "name": "Local", "baseUrl": "http://127.0.0.1:11434/v1",
		"schema": "openai-compatible", "key": "", "models": []map[string]any{{"name": "qwen3"}},
	})
	if isErrorResponse(t, up) {
		t.Fatalf("endpoints.update: %s", up)
	}
	sid := openLocalSession(t, h.conn)
	_, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-m", "sessionId": sid, "question": "q", "cwd": "/repo",
		"attachedContent": []any{},
	}, 2)
	if errObj == nil {
		t.Fatal("ask with a removed model succeeded, want a refusal")
	}
	if !strings.Contains(errObj.Message, "gpt-4o") {
		t.Errorf("refusal message = %q, want it to name the removed model", errObj.Message)
	}
	if client.askCount() != 0 {
		t.Error("the model must never run after the assigned model was removed")
	}
}

// ── the turn's anchor comes from the SESSION (nocx-4em1z) ─────────────────

// A question asked in a pane is anchored to that pane, and the backend is
// what anchors it — the renderer never sends one.
//
// This is the same rule the shell path states in its own comment
// (ws_ledger.go's open): "a paneId on the envelope would put the same input
// under a second owner, and the renderer's copy would be the one nobody
// checked". The backend already resolved which pane a session is the pipe
// of, so an ask that names its session has named its pane.
//
// It matters because the restore read is BY PANE. Before this the ask wrote
// session_id and nothing else, and a session is gone by the time a restore
// runs (D5) — so every question and every answer vanished from a restored
// tab, which is what the owner reported.
func TestAgentAsk_TheTurnIsAnchoredToTheSessionsPane(t *testing.T) {
	h := newAskHarness(t, &scriptedAssistantClient{deltas: []string{"hi"}})
	h.createEndpoint()

	// A session that IS the pipe of a pane, which is what the product opens.
	resp := jsonrpcCallWithID(t, h.conn, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0, "paneId": askPaneID,
	}, 1)
	var opened struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &opened); err != nil || opened.Result.SessionID == "" {
		t.Fatalf("open with a pane: %v\nraw: %s", err, resp)
	}

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":           "ask-anchored",
		"sessionId":       opened.Result.SessionID,
		"question":        "what is this?",
		"cwd":             "/repo",
		"attachedContent": []any{},
	}, 2)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	entry, err := h.db.Ledger().Entry(context.Background(), res.EntryID)
	if err != nil || entry == nil {
		t.Fatalf("turn entry: %v (err %v)", entry, err)
	}
	if entry.PaneID == nil || *entry.PaneID != askPaneID {
		t.Fatalf("the turn's paneId = %v, want %q — a turn that names no pane cannot be restored",
			entry.PaneID, askPaneID)
	}
}

func TestAgentAsk_SealedVaultUnlocksAndContinuesTheRun(t *testing.T) {
	release := make(chan struct{})
	client := &scriptedAssistantClient{deltas: []string{"after unlock"}, release: release}
	h := newAskHarness(t, client)
	h.createEndpoint()
	h.v.Seal()
	h.v.SetUnlockRequester(unlockRequesterFunc(h.ws.RequestUnlock))
	sid := openLocalSession(t, h.conn)

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "sealed-ask", "sessionId": sid, "question": "answer after unlock",
		"cwd": "/repo", "attachedContent": []any{},
	}, 2)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	frame := readUnlockRequestFrame(t, h.conn)
	if frame.Reason != "answer the ask" {
		t.Fatalf("unlock reason = %q, want %q", frame.Reason, "answer the ask")
	}
	if err := h.v.Unseal(t.Context(), vault.UnsealRequest{Passphrase: "test"}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	answerUnlock(t, h.conn, frame.RequestID, "unsealed")
	close(release)

	raw := readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	var delta agentRunDelta
	if err := json.Unmarshal(raw, &delta); err != nil {
		t.Fatalf("runDelta: %v", err)
	}
	if delta.RunID != res.RunID || delta.Text != "after unlock" {
		t.Fatalf("runDelta = %+v, want run %d after unlock", delta, res.RunID)
	}
	raw = readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var state agentRunState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("runState: %v", err)
	}
	if state.RunID != res.RunID || state.State != string(content.RunCompleted) {
		t.Fatalf("runState = %+v, want run %d completed", state, res.RunID)
	}
}

func TestAgentAsk_CancelledUnlockCancelsTheDurableRun(t *testing.T) {
	client := &scriptedAssistantClient{deltas: []string{"must not run"}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	h.v.Seal()
	h.v.SetUnlockRequester(unlockRequesterFunc(h.ws.RequestUnlock))
	sid := openLocalSession(t, h.conn)

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "cancelled-ask", "sessionId": sid, "question": "cancel this",
		"cwd": "/repo", "attachedContent": []any{},
	}, 2)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	frame := readUnlockRequestFrame(t, h.conn)
	answerUnlock(t, h.conn, frame.RequestID, "cancelled")

	raw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var state agentRunState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("runState: %v", err)
	}
	if state.RunID != res.RunID || state.State != string(content.RunCancelled) {
		t.Fatalf("runState = %+v, want run %d cancelled", state, res.RunID)
	}
	if got := client.messages(); len(got) != 0 {
		t.Fatalf("model received %d messages after unlock cancellation", len(got))
	}

	entry, err := h.db.Ledger().Entry(t.Context(), res.EntryID)
	if err != nil {
		t.Fatalf("ledger entry: %v", err)
	}
	if len(entry.Executions) != 1 || entry.Executions[0].State == nil ||
		*entry.Executions[0].State != content.RunCancelled {
		t.Fatalf("durable run state = %+v, want cancelled", entry.Executions)
	}
}
