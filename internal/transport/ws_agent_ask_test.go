package transport

// agent.ask runs through a real persistent fake-agent process, session actor,
// tool endpoint and MCP bridge. Scripts control agent output; transport,
// authority, notifications and ledger writes stay on the production path.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
	"github.com/shady2k/nocx/internal/waittest"
)

type synchronizedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// askHarness is a full transport env for the ask flow: real content store,
// real vault, real profile store, and a subprocess agent.
type askHarness struct {
	t        *testing.T
	v        *vault.Vault
	db       content.ContentDB
	profiles storage.DocumentStore
	ws       *WSServer
	conn     *websocket.Conn
	logs     *synchronizedBuffer
	agent    *scriptedAgentFixture
	script   protocol.Script
}

func newAskHarness(t *testing.T, script protocol.Script) *askHarness {
	return newAskHarnessWithOpts(t, script)
}

// newAskHarnessWithOpts is newAskHarness with additional WSServerOptions —
// for tests that need a seam named at construction (the policy tests wire
// WithAgentPolicy here, so the control plane registers the methods wired).
// askHarnessParallel asks for this test to run beside its siblings, ONCE per
// test: a test that builds two harnesses calls the constructor twice, and
// t.Parallel twice is a panic. The marker is the test itself.
//
// EVERY TEST THAT USES THIS HARNESS MAY RUN BESIDE THE OTHERS
// (nocx-ouru5.1.16). Each one builds its own temporary directory, vault,
// content store, profile store and WSServer, so they share nothing — and this
// family is the transport package's slowest: 115 call sites at one to three
// seconds each under -race, which is most of why the package's single test
// binary needs the whole default ten-minute go test timeout. (The gated run
// that found this took 599 s of test time against a 600 s bound, and the
// goroutine dump showed no test blocked — the suite simply fills the bound.)
// Asking here rather than at each caller is one decision in one place, and it
// is what keeps the suite inside the bound without moving the bound.
func askHarnessParallel(t *testing.T) {
	t.Helper()
	if _, seen := askHarnessParallelised.LoadOrStore(t, struct{}{}); seen {
		return
	}
	t.Parallel()
}

var askHarnessParallelised sync.Map

func newAskHarnessWithOpts(t *testing.T, script protocol.Script, extra ...WSServerOption) *askHarness {
	askHarnessParallel(t)
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

	agent := newScriptedAgent(t, script)
	// The real client binds the run kernel; model turns still use the subprocess harness.
	kernel, _, err := assistant.NewClientAndRegistry(nil, nil, content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	opts := []WSServerOption{
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(v), WithVaultUnsealer(v), WithVaultLifecycle(v),
		WithAgentKnownMaterial(adapterKnownMaterial(v)),
		WithContentDB(db),
		WithAgentHarness("fixture", agent.harness),
		WithAssistantClient(kernel),
	}
	opts = append(opts, extra...)
	serverLogs := &synchronizedBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("ask server logs:\n%s", serverLogs.String())
		}
	})
	serverLogger := log.NewSlogAdapter(slog.New(slog.NewJSONHandler(serverLogs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	ws := NewWSServer(serverLogger, newRegWithStub(serverLogger), opts...)
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return &askHarness{t: t, v: v, db: db, profiles: docStore, ws: ws, conn: conn, logs: serverLogs, agent: agent, script: agent.script}
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
	h.seedLegacyRole(e.ID, "qwen3")
}

// seedLegacyRole writes the endpoint-based format left by older installs.
// It is deliberately not sent to roles.assign: new writes require an agentId.
func (h *askHarness) seedLegacyRole(endpointID, model string) {
	h.t.Helper()
	var document map[string]any
	if found, err := h.profiles.Read("p.json", &document); err != nil || !found {
		h.t.Fatalf("read legacy profile document: found %v, err %v", found, err)
	}
	document["roles"] = []map[string]any{{"role": "answering", "endpointId": endpointID, "model": model}}
	if err := h.profiles.Write("p.json", document); err != nil {
		h.t.Fatalf("write legacy role assignment: %v", err)
	}
}

func (h *askHarness) prompt() protocol.Prompt { return h.agent.prompt() }
func (h *askHarness) toolResult() string      { return h.agent.toolResult() }
func (h *askHarness) release()                { h.agent.release() }
func (h *askHarness) waitHung()               { h.agent.waitHung() }
func (h *askHarness) pid() int                { return h.agent.pid() }

// ── the happy path: ask → deltas on the right entry → terminal completed ──

// A person selects a finished block, types a question, and the answer that
// arrives is recorded in the ledger and streamed over the wire in order.
// The wire shape is pinned here exactly: the ask result carries the answer
// entry id; runDelta carries runId + entryId + ascending seq + text;
// runState closes the run.
func TestAgentAsk_StreamsTheAnswerAndTerminalizes(t *testing.T) {
	script := answerScript("hello", " ", "world")
	h := newAskHarness(t, script)
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
	if res.Model != "" {
		t.Errorf("prepared ask model = %q, want unset before the harness answers", res.Model)
	}

	// The deltas arrive as agent.runDelta with ascending seq, routed by
	// runId and entryId.
	wantSeq := 0
	var streamed string
	for range len(script.Steps) - 1 {
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
	prompt := h.prompt()
	messages := []assistant.Message{{Role: "system", Content: prompt.SystemPrompt}, {Role: "user", Content: prompt.Text}}
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

func TestAgentAsk_NoLegacyEndpointStillUsesHarness(t *testing.T) {
	h := newAskHarness(t, answerScript("x"))
	sid := openLocalSession(t, h.conn)
	_, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":           "ask-1",
		"sessionId":       sid,
		"question":        "q",
		"cwd":             "/repo",
		"attachedContent": []any{},
	}, 2)
	if errObj != nil {
		t.Fatalf("ask without legacy endpoint: %+v", errObj)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if got := h.prompt().Text; !strings.HasPrefix(got, "q") {
		t.Fatalf("harness prompt = %q, want question", got)
	}
}

func TestAgentAsk_ModelFailureTerminalizesFailed(t *testing.T) {
	h := newAskHarness(t, protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepAnswer, Text: "partial"},
		{Kind: protocol.StepFail, Reason: "the model returned no text"},
	}})
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

func TestAgentAsk_ConnectionLostMidStreamTerminalizes(t *testing.T) {
	h := newAskHarness(t, protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepAnswer, Text: "first"},
		{Kind: protocol.StepHang},
	}})
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

	// The run terminalizes interrupted with the renderable reason. Poll the
	// ledger — the runState notification goes to a dead socket and is
	// dropped; the record is what a reconnect reads.
	var terminalState *content.RunState
	var entryErr error
	lastState, sawEntry := "<none>", false
	waittest.WaitForTimeoutDetail(t, "the run to terminalize", 5*time.Second,
		func() string {
			return fmt.Sprintf("run never terminalized; state = %s (entry present=%v, err %v)",
				lastState, sawEntry, entryErr)
		},
		func() bool {
			q, err := h.db.Ledger().Entry(context.Background(), res.EntryID)
			if err != nil {
				entryErr = err
				return true
			}
			if q == nil {
				sawEntry = false
				return false
			}
			sawEntry = true
			st := q.Executions[0].State
			if st == nil {
				lastState = "<nil>"
				return false
			}
			lastState = string(*st)
			if *st == content.RunPrepared || *st == content.RunStreaming {
				return false
			}
			terminalState = st
			return true
		})
	if entryErr != nil {
		t.Fatalf("question entry: %v", entryErr)
	}
	if *terminalState != content.RunInterrupted {
		t.Fatalf("run state = %v, want interrupted", *terminalState)
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
	script := answerScript("sure")
	h := newAskHarness(t, script)
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

	for range len(script.Steps) - 1 {
		raw := readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
		_ = raw
	}

	// The engine received the question and NO frame text — nothing was
	// pointed at, so nothing may ride the prompt.
	prompt := h.prompt()
	messages := []assistant.Message{{Role: "system", Content: prompt.SystemPrompt}, {Role: "user", Content: prompt.Text}}
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
		messages[1].Role != "user" || !strings.HasPrefix(messages[1].Content, "what is the capital of France?") {
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

// Legacy endpoint configuration does not select the harness model.
func TestAgentAsk_HarnessDefaultIgnoresLegacyRoleAssignment(t *testing.T) {
	script := answerScript("x")
	h := newAskHarness(t, script)
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
	h.seedLegacyRole(second.ID, "gpt-4o")
	_ = first

	sid := openLocalSession(t, h.conn)
	askRes, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-role", "sessionId": sid, "question": "q", "cwd": "/repo",
		"attachedContent": []any{},
	}, 2)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	if askRes.Model != "" {
		t.Fatalf("prepared model = %q, want harness default deferred", askRes.Model)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if got := h.prompt().Model; got != "" {
		t.Fatalf("harness model override = %q, want default independent of endpoint role", got)
	}
	entry, err := h.db.Ledger().Entry(t.Context(), askRes.EntryID)
	if err != nil || entry == nil || len(entry.Executions) != 1 {
		t.Fatalf("answering entry = %+v, err %v", entry, err)
	}
	var facts content.RunFacts
	if err := json.Unmarshal([]byte(entry.Executions[0].Payload), &facts); err != nil || facts.Model != "fixture-model" {
		t.Fatalf("answering model attribution = %+v, err %v", facts, err)
	}
}

func TestAgentAsk_LegacyReassignmentDoesNotOverrideHarness(t *testing.T) {
	script := answerScript("x")
	h := newAskHarness(t, script)
	h.createEndpoint() // "Local" with model qwen3, answering → qwen3
	otherRaw := jsonrpcCall(t, h.conn, "endpoints.create", map[string]any{
		"name": "Other", "baseUrl": "https://api.example.com/v1", "schema": "openai-compatible",
		"key": "sk", "models": []map[string]any{{"name": "gpt-4o"}},
	})
	other, _ := decodeEndpointResult(t, otherRaw)
	h.seedLegacyRole(other.ID, "gpt-4o")
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-re", "sessionId": sid, "question": "q", "cwd": "/repo",
		"attachedContent": []any{},
	}, 2); errObj != nil {
		t.Fatalf("agent.ask after reassignment: %+v", errObj)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if got := h.prompt().Model; got != "" {
		t.Errorf("harness override after legacy reassignment = %q, want default", got)
	}
}

func TestAgentAsk_DeletedLegacyEndpointDoesNotBlockHarness(t *testing.T) {
	script := answerScript("x")
	h := newAskHarness(t, script)
	raw := jsonrpcCall(t, h.conn, "endpoints.create", map[string]any{
		"name": "Local", "baseUrl": "http://127.0.0.1:11434/v1", "schema": "openai-compatible",
		"key": "sk-test-123", "models": []map[string]any{{"name": "qwen3"}},
	})
	created, code := decodeEndpointResult(t, raw)
	if code != 0 {
		t.Fatalf("endpoints.create: code %d", code)
	}
	h.seedLegacyRole(created.ID, "qwen3")
	if isErrorResponse(t, jsonrpcCall(t, h.conn, "endpoints.delete", map[string]any{"id": created.ID})) {
		t.Fatal("delete refused")
	}
	sid := openLocalSession(t, h.conn)
	_, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-d", "sessionId": sid, "question": "q", "cwd": "/repo",
		"attachedContent": []any{},
	}, 2)
	if errObj != nil {
		t.Fatalf("legacy endpoint change must not refuse a harness ask: %+v", errObj)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if got := h.prompt().Model; got != "" {
		t.Fatalf("harness model = %q, want its default", got)
	}
}

func TestAgentAsk_RemovedLegacyModelDoesNotBlockHarness(t *testing.T) {
	script := answerScript("x")
	h := newAskHarness(t, script)
	raw := jsonrpcCall(t, h.conn, "endpoints.create", map[string]any{
		"name": "Local", "baseUrl": "http://127.0.0.1:11434/v1", "schema": "openai-compatible",
		"key": "sk-test-123", "models": []map[string]any{{"name": "qwen3"}, {"name": "gpt-4o"}},
	})
	created, _ := decodeEndpointResult(t, raw)
	h.seedLegacyRole(created.ID, "gpt-4o")
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
	if errObj != nil {
		t.Fatalf("legacy endpoint change must not refuse a harness ask: %+v", errObj)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if got := h.prompt().Model; got != "" {
		t.Fatalf("harness model = %q, want its default", got)
	}
}

func TestAgentAsk_TheTurnIsAnchoredToTheSessionsPane(t *testing.T) {
	h := newAskHarness(t, answerScript("hi"))
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

func TestAgentAsk_SealedLegacyEndpointDoesNotBlockHarness(t *testing.T) {
	h := newAskHarness(t, answerScript("without endpoint unlock"))
	h.createEndpoint()
	h.v.Seal()
	h.v.SetUnlockRequester(unlockRequesterFunc(h.ws.RequestUnlock))
	sid := openLocalSession(t, h.conn)

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "sealed-ask", "sessionId": sid, "question": "answer without endpoint unlock",
		"cwd": "/repo", "attachedContent": []any{},
	}, 2)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	raw := readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	var delta agentRunDelta
	if err := json.Unmarshal(raw, &delta); err != nil {
		t.Fatalf("runDelta: %v", err)
	}
	if delta.RunID != res.RunID || delta.Text != "without endpoint unlock" {
		t.Fatalf("runDelta = %+v, want run %d without endpoint unlock", delta, res.RunID)
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
