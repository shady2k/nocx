package transport

// readScreen end to end (nocx-ljfwz): the broker's first production
// request, over a REAL socket, through the REAL engine. The grant permits
// readScreen on a live session (the harness names the workspace policy
// preset on the server — the run's authority, ADR-0020 decision 5); the model
// calls the tool; the backend asks the renderer through the broker; the
// renderer (this test, standing in for the frontend) answers with a frame;
// and the frame's text lands in the model's tool message on the SECOND
// request the engine sends. The wire is a party to the contract: every
// assertion is on bytes that actually crossed the socket or the fake
// provider, never on a payload the test built and handed to the handler.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/contracts/tools"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/waittest"
)

// autonomousPolicyStore returns a store seeded with the autonomous matrix —
// the composition root's seam (WithAgentPolicy), named at server
// CONSTRUCTION so no field is mutated after the server's goroutines start
// (a post-start write races the run mint's reads). The autonomous matrix,
// minted by runGrantFor with the run's own session as the base scope,
// permits observe on that session: exactly readScreen's scope.
func autonomousPolicyStore(t *testing.T) *assistant.GlobalPolicyStore {
	t.Helper()
	store := assistant.NewGlobalPolicyStore(storage.NewDocumentStore(t.TempDir()), "agent-policy.json")
	if err := store.SetPolicy(autonomousMatrixForTests()); err != nil {
		t.Fatalf("seed global policy: %v", err)
	}
	return store
}

// autonomousMatrixForTests is the autonomous preset expressed as a matrix —
// every row permits — the rows a person would write (the original §7
// presets have no production constructors; clean-only, preset matrices only).
func autonomousMatrixForTests() content.EffectPolicy {
	r := content.EffectRow{Decision: content.DecisionPermit}
	return content.EffectPolicy{
		Observe: r, MutateReversible: r, MutateDestructive: r,
		PrivilegeChange: r, Disclose: r, CrossBoundary: r, Delegate: r,
	}
}

func askEveryTimePolicyStore(t *testing.T) *assistant.GlobalPolicyStore {
	t.Helper()
	store := assistant.NewGlobalPolicyStore(storage.NewDocumentStore(t.TempDir()), "agent-policy.json")
	if err := store.SetPolicy(askEveryTimeMatrixForTests()); err != nil {
		t.Fatalf("seed global policy: %v", err)
	}
	return store
}

func askEveryTimeMatrixForTests() content.EffectPolicy {
	r := content.EffectRow{Decision: content.DecisionAsk}
	return content.EffectPolicy{
		Observe: r, MutateReversible: r, MutateDestructive: r,
		PrivilegeChange: r, Disclose: r, CrossBoundary: r, Delegate: r,
	}
}

// The product-minted grant includes the path, session and one root per
// enabled content family, so the registry can offer each family's tools.
func TestRunGrantFor_OffersPathToolsAndKeepsSessionScope(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	server := NewWSServer(logger, newRegWithStub(logger), WithAgentPolicy(askEveryTimePolicyStore(t)))
	const sid = "session-a"

	grant := server.runGrantFor(sid)
	if grant == nil {
		t.Fatal("runGrantFor returned nil grant")
	}
	if !hasGrantScope(grant.Scopes, content.ResourcePath, "/") {
		t.Fatalf("grant scopes = %+v, want the whole filesystem path scope", grant.Scopes)
	}
	if !hasGrantScope(grant.Scopes, content.ResourceSession, sid) {
		t.Fatalf("grant scopes = %+v, want the run's session scope", grant.Scopes)
	}
	for _, family := range []string{"note", "snippet", "skill"} {
		if !hasGrantScope(grant.Scopes, content.ResourceContent, family) {
			t.Fatalf("grant scopes = %+v, want content family root %q", grant.Scopes, family)
		}
	}
	if !hasGrantScope(grant.Scopes, content.ResourceDestination, "*") {
		t.Fatalf("grant scopes = %+v, want the direct-destination scope", grant.Scopes)
	}
	if len(grant.Scopes) != 6 {
		t.Fatalf("grant scopes = %+v, want path, session, three content and destination scopes", grant.Scopes)
	}

	reg, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble tool registry: %v", err)
	}
	var names []string
	for _, tool := range reg.ForGrant(*grant) {
		names = append(names, tool.Name)
	}
	want := []string{"files.read", "fetch.url", "session.list", "session.read", "session.run", "session.wait", "files.edit", "files.create", "notes.search", "notes.create", "notes.update", "notes.delete", "snippets.list", "snippets.create", "snippets.update", "snippets.delete", "snippets.reorder", "skills.read", "skills.create", "skills.update", "skills.delete", "skills.install"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tools offered by the product-minted grant = %v, want %v", names, want)
	}
}

func hasGrantScope(scopes []content.GrantScope, kind content.ResourceKind, id string) bool {
	for _, scope := range scopes {
		if scope.Kind == kind && scope.ID == id {
			return true
		}
	}
	return false
}

type toolCallingServer struct {
	requests atomic.Int64
	bodies   []string
}

func newToolCallingServer() (*toolCallingServer, *httptest.Server) {
	s := &toolCallingServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := s.requests.Add(1)
		s.bodies = append(s.bodies, string(body))
		if n == 1 {
			// The exact SSE shape the assistant engine tests use: one chunk
			// carrying the tool call and finish_reason tool_calls, then the
			// end-of-stream marker (policy_test.go's streamToolCalls).
			streamToolCallChunk(w, "session.read", `{}`)
			return
		}
		streamOKChunks(w)
	}))
	return s, srv
}

// streamToolCallChunk writes one SSE completion whose single chunk carries
// the given tool call and finish_reason tool_calls — the shape the openai
// adapter's streaming builder consumes (mirrors the assistant tests).
func streamToolCallChunk(w http.ResponseWriter, name, args string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	d := map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "probe-model",
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{
				"role": "assistant",
				"tool_calls": []map[string]any{{
					"id":   "call_readscreen",
					"type": "function",
					"function": map[string]any{
						"name":      name,
						"arguments": args,
					},
				}},
			},
			"finish_reason": "tool_calls",
		}},
	}
	b, _ := json.Marshal(d)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

// streamOKChunks writes one streamed "ok" in two chunks, the way a real
// streaming completion arrives.
func streamOKChunks(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	chunk := func(content, finish string) {
		d := map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion.chunk", "created": 0,
			"model": "probe-model",
			"choices": []map[string]any{{
				"index": 0, "delta": map[string]any{"role": "assistant", "content": content},
				"finish_reason": finish,
			}},
		}
		b, _ := json.Marshal(d)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	}
	chunk("o", "")
	chunk("k", "stop")
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

// readScreenFrameWire is the renderer's answer to a readScreen request: the
// live frame shape — text rows, cursor, identity, range — under the closed
// "frame" outcome. Each row is padded to identity.cols, the range
// spans exactly the rows, and the cursor is inside the geometry: the wire
// validation of the broker (validateReadScreenResolvedRaw) is what the test
// is crossing.
func readScreenFrameWire(t *testing.T, rid string, texts ...string) map[string]any {
	t.Helper()
	cols := 0
	for _, text := range texts {
		if len(text) > cols {
			cols = len(text)
		}
	}
	rows := make([]any, 0, len(texts))
	for _, text := range texts {
		// Padded to the width, the way a mint pads a short line with blank
		// cells: the row's width IS the screen's.
		rows = append(rows, map[string]any{
			"kind": "text",
			"text": text + strings.Repeat(" ", cols-len(text)),
		})
	}
	return map[string]any{
		"requestId": rid,
		"outcome":   "frame",
		"rows":      rows,
		"cursor":    map[string]any{"line": 0, "col": 0},
		"identity":  map[string]any{"buffer": map[string]any{"kind": "normal"}, "cols": cols, "rows": len(texts), "generation": 1},
		"range":     map[string]any{"start": 0, "end": len(texts)},
	}
}

func TestReadScreen_ErrorSentences(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "resolution decode",
			raw:  `{`,
			want: "resolution: unexpected end of JSON input",
		},
		{
			name: "failed capture",
			raw:  `{"outcome":"failed","error":"no such session: gone-session"}`,
			want: "the renderer could not capture the screen: no such session: gone-session",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveReadScreen(json.RawMessage(tt.raw))
			if err == nil {
				t.Fatal("resolveReadScreen returned nil error")
			}
			if err.Error() != tt.want {
				t.Fatalf("resolveReadScreen error = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestRequestScreen_ErrorSentences(t *testing.T) {
	_, err := (&WSServer{}).RequestScreen(context.Background(), "sid", nil)
	if err == nil || err.Error() != "no renderer request broker is wired" {
		t.Fatalf("unwired RequestScreen error = %v, want %q", err, "no renderer request broker is wired")
	}

	ws, _, stop := newAgentWSServer(t)
	defer stop()
	_, err = ws.RequestScreen(context.Background(), "sid", nil)
	if err == nil || err.Error() != "no renderer connected to read the screen" {
		t.Fatalf("no-client RequestScreen error = %v, want %q", err, "no renderer connected to read the screen")
	}
}

// createEndpointAt points the ask run at the given provider URL (the
// harness's own createEndpoint hardcodes the loopback URL; the readScreen
// test needs the fake provider).
func (h *askHarness) createEndpointAt(baseURL string) {
	h.t.Helper()
	created, code := decodeEndpointResult(h.t, jsonrpcCall(h.t, h.conn, "endpoints.create", map[string]any{
		"name":    "Local",
		"baseUrl": baseURL,
		"schema":  "openai-compatible",
		"key":     "sk-test-123",
		"models":  []map[string]any{{"name": "qwen3"}},
	}))
	if code != 0 {
		h.t.Fatalf("endpoints.create: code %d", code)
	}
	// The ask resolves through the ANSWERING ROLE (bead nocx-e6kn2).
	if isErrorResponse(h.t, jsonrpcCall(h.t, h.conn, "roles.assign", map[string]any{
		"role": "answering", "endpointId": created.ID, "model": "qwen3",
	})) {
		h.t.Fatalf("roles.assign refused")
	}
}

// TestReadScreen_EndToEndOverTheRealSocket is criterion 3: a run whose grant
// permits readScreen on a live session produces a frame in the model's tool
// message. The engine is real, the socket is real, the provider is fake; the
// renderer half is THIS test — it reads the broker's request notification
// off the socket and answers it with a frame. Everything asserted crossed a
// wire.
func TestReadScreen_EndToEndOverTheRealSocket(t *testing.T) {
	fake, srv := newToolCallingServer()
	defer srv.Close()

	// The REAL engine: the embedded schemas include readScreen.
	client, _, err := assistant.NewClientAndRegistry(nil, nil, content.Floor{}, nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	// The composition-root seam, named at construction: the autonomous
	// matrix minted with the run's own session permits observe on its own
	// session — exactly readScreen's scope.
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(autonomousPolicyStore(t)))
	h.createEndpointAt(srv.URL)

	sid := openLocalSession(t, h.conn)

	// The ask that starts a grant-bearing run: the harness names the
	// workspace policy preset (autonomous within the routine local
	// environment), so the run's grant permits observe on its own session —
	// exactly readScreen's scope.
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "ask-readscreen-1",
		"sessionId": sid,
		"question":  "what is on the screen?",
		"cwd":       "/repo",
	}, 2)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	if res.State != "prepared" {
		t.Fatalf("ask state = %q, want prepared", res.State)
	}

	// The renderer half: the broker's request arrives on the socket, naming
	// the session the grant permitted.
	raw := readNotification(t, h.conn, "agent.readScreenRequest", 10*time.Second)
	if raw == nil {
		t.Fatalf("no readScreenRequest within 10s; provider requests=%d bodies=%v", fake.requests.Load(), fake.bodies)
	}

	var req struct {
		RequestID string `json:"requestId"`
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("readScreenRequest unmarshal: %v\nraw: %s", err, raw)
	}
	if req.RequestID == "" {
		t.Fatal("readScreenRequest carries no requestId")
	}
	if req.SessionID != sid {
		t.Fatalf("readScreenRequest sessionId = %q, want %q", req.SessionID, sid)
	}

	// Answer with a frame, over the same socket: the resolution RPC the
	// broker correlates.
	reply := jsonrpcCall(t, h.conn, "agent.readScreenResolved", readScreenFrameWire(t, req.RequestID, "hello", "world"))
	var rerr struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &rerr); err != nil {
		t.Fatalf("resolution response unmarshal: %v", err)
	}
	if rerr.Error != nil {
		t.Fatalf("readScreenResolved refused: %+v", rerr.Error)
	}

	// The engine streams the tool message (the frame window) as a delta —
	// the same behavior the assistant tests tolerate — then re-asks the
	// model, whose streamed "ok" is the answer. Collect deltas until the
	// run terminalizes and assert the answer arrived.
	var answer string
	var st struct {
		RunID int64  `json:"runId"`
		State string `json:"state"`
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		raw = readNotification(t, h.conn, "agent.runDelta", 10*time.Second)
		var d struct {
			RunID int64  `json:"runId"`
			Text  string `json:"text"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("runDelta unmarshal: %v\nraw: %s", err, raw)
		}
		if d.RunID != res.RunID {
			t.Fatalf("runDelta runId = %d, want %d", d.RunID, res.RunID)
		}
		answer += d.Text
		if strings.Contains(answer, "ok") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("answer never arrived; collected %q", answer)
		}
	}
	raw = readNotification(t, h.conn, "agent.runState", 10*time.Second)
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("runState unmarshal: %v\nraw: %s", err, raw)
	}
	if st.RunID != res.RunID || st.State != "completed" {
		t.Fatalf("runState = runId %d state %q, want %d completed", st.RunID, st.State, res.RunID)
	}

	// The wire evidence: the engine's SECOND request carries the tool
	// message with the frame's text — a frame crossed the socket and landed
	// in the model's tool message.
	if fake.requests.Load() < 2 {
		t.Fatalf("provider received %d requests, want >= 2; bodies=%v", fake.requests.Load(), fake.bodies)
	}
	second := fake.bodies[1]
	if !strings.Contains(second, "hello") || !strings.Contains(second, "world") {
		t.Fatalf("the tool message lacks the frame's text; second body: %s", second)
	}
}

// TestReadScreen_FailedCaptureAnswersHonestly is criterion 6's first
// direction: a renderer that cannot produce the frame (a session it does
// not know) answers "failed" and the run is not left hanging — the failure
func TestReadScreen_FailedCaptureAnswersHonestly(t *testing.T) {
	client, _, err := assistant.NewClientAndRegistry(nil, nil, content.Floor{}, nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	// The model names the GRANTED session — a call naming any other session
	// is refused by policy before the broker is ever asked (the grant tests
	// own that direction). The RENDERER is what fails here: it answers the
	// request honestly with outcome failed, and the failure sentence must
	// cross as a tool error the run proceeds past — never a hang.
	// The same composition-root seam, at construction: the autonomous
	// matrix, run-scoped to the harness session.
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(autonomousPolicyStore(t)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if h.fakeRequests.Add(1) == 1 {
			streamToolCallChunk(w, "session.read", `{}`)
			return
		}
		streamOKChunks(w)
	}))
	defer srv.Close()
	h.createEndpointAt(srv.URL)

	sid := openLocalSession(t, h.conn)

	askOverWire(t, h.conn, map[string]any{
		"askId":     "ask-readscreen-2",
		"sessionId": sid,
		"question":  "read it",
		"cwd":       "/repo",
	}, 3)

	raw := readNotification(t, h.conn, "agent.readScreenRequest", 10*time.Second)
	var req struct {
		RequestID string `json:"requestId"`
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("readScreenRequest unmarshal: %v", err)
	}
	if req.SessionID != sid {
		t.Fatalf("readScreenRequest sessionId = %q, want %q", req.SessionID, sid)
	}
	// The renderer answers honestly: the capture failed (a session it does
	// not know, a capture aborted by disposal — the renderer's own honest
	// terminal answer, never a hang).
	reply := jsonrpcCall(t, h.conn, "agent.readScreenResolved", map[string]any{
		"requestId": req.RequestID,
		"outcome":   "failed",
		"error":     "no such session: gone-session",
	})
	var rerr struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &rerr); err != nil {
		t.Fatalf("resolution response unmarshal: %v", err)
	}
	if rerr.Error != nil {
		t.Fatalf("failed resolution refused: %+v", rerr.Error)
	}
	// The run is NOT left hanging: a failed capture is a terminal tool
	// error, and eino surfaces a tool error as a run failure — the renderer
	// answered honestly, the run reached a terminal state with the
	// renderer's sentence on it, and nothing leaked.
	raw = readNotification(t, h.conn, "agent.runState", 10*time.Second)
	var st struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("runState unmarshal: %v\nraw: %s", err, raw)
	}
	if st.State != "failed" {
		t.Fatalf("runState = %q, want failed (the renderer's failed outcome is a terminal tool error)", st.State)
	}
	want := "the assistant's session.read call did not finish: the renderer could not capture the screen: no such session: gone-session"
	if st.Error != want {
		t.Fatalf("runState error = %q, want %q", st.Error, want)
	}
}

// TestReadScreen_DisconnectedRendererTerminalizes is criterion 6's second
// direction: a renderer that never answers — here, a renderer that DIES —
// terminalizes the run through the broker's terminalization rather than
// leaking a pending request. The ledger is the record: the run reaches a
// terminal state.
func TestReadScreen_DisconnectedRendererTerminalizes(t *testing.T) {
	client, _, err := assistant.NewClientAndRegistry(nil, nil, content.Floor{}, nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	// The tool call names the ASK's session (the grant's scope): a call
	// naming any other session would be refused by policy before the broker
	// is ever asked, which is the test the grant tests own.
	// The composition-root seam (WithAgentPolicy), named at construction:
	// the autonomous matrix minted with the run's own session permits
	// observe on its own session — exactly readScreen's scope.
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(autonomousPolicyStore(t)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if h.fakeRequests.Add(1) == 1 {
			streamToolCallChunk(w, "session.read", `{}`)
			return
		}
		streamOKChunks(w)
	}))
	defer srv.Close()
	h.createEndpointAt(srv.URL)

	sid := openLocalSession(t, h.conn)

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "ask-readscreen-3",
		"sessionId": sid,
		"question":  "read it",
		"cwd":       "/repo",
	}, 4)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	// The broker's request reaches us; we never answer — we close the
	// socket instead, which is the renderer's death.
	raw := readNotification(t, h.conn, "agent.readScreenRequest", 10*time.Second)
	var req struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("readScreenRequest unmarshal: %v", err)
	}
	if err := h.conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The run reaches a terminal state in the ledger — the broker
	// terminalized the request (ErrRequestDisconnected) or the connection
	// death cancelled the stream; either way nothing hangs and nothing
	// leaks. Poll the ledger (an observable state change, never a sleep).
	led := h.db.Ledger()
	ctx := context.Background()
	var entryErr error
	lastState, lastExecs := "<none>", -1
	waittest.WaitForTimeoutDetail(t, "the run to terminalize", 15*time.Second,
		func() string {
			return fmt.Sprintf("run %d never terminalized; last state %s (executions=%d, entry err %v)",
				res.RunID, lastState, lastExecs, entryErr)
		},
		func() bool {
			entry, err := led.Entry(ctx, res.EntryID)
			if err != nil {
				entryErr = err
				return true
			}
			if entry == nil {
				lastExecs = -1
				return false
			}
			lastExecs = len(entry.Executions)
			if lastExecs == 0 {
				return false
			}
			st := entry.Executions[0].State
			if st == nil {
				lastState = "<nil>"
				return false
			}
			lastState = string(*st)
			return *st != content.RunPrepared && *st != content.RunStreaming
		})
	if entryErr != nil {
		t.Fatalf("question entry: %v", entryErr)
	}
}
