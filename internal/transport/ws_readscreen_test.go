package transport

// Backend-owned session.read tests: the assistant runs through the real
// engine and socket, while a narrow capture source stands in for the helper
// runtime. The separate app acceptance test exercises the real helper-owned
// runtime without a renderer.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/contracts/tools"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
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

// The production mint states the grant's DEADLINE, and this is the pin that
// keeps content.Grant.ExpiresAt's "zero means nobody minted it" honest
// (nocx-1z1r1). Before this, runGrantFor's grant carried expires_at = 0 into
// every authority_grants row and ADR-0020 §5's "expiring" was a word in a
// document. The paired half — a grant PAST its deadline yields no capability
// — lives where it is enforced, in internal/agenttools.
func TestRunGrantFor_StatesTheDeadline(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	server := NewWSServer(logger, newRegWithStub(logger), WithAgentPolicy(askEveryTimePolicyStore(t)))

	before := time.Now()
	grant := server.runGrantFor("session-a")
	if grant == nil {
		t.Fatal("runGrantFor returned nil grant")
	}
	if grant.ExpiresAt == 0 {
		t.Fatal("the run mint stated no deadline: every recorded grant would carry expires_at = 0")
	}
	if grant.Expired(before) {
		t.Fatal("a grant minted a moment ago is already expired")
	}
	if got, want := grant.ExpiresAt, before.Add(content.GrantLifetime).UnixMilli(); got < want {
		t.Fatalf("ExpiresAt = %d, want at least mint + GrantLifetime (%d)", got, want)
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
	// The environment a worker may be started in (nocx-dkawo.8). It is the
	// local machine and only that: the spawner opens a local session, so a
	// fence naming any other environment would offer an authority nothing
	// could deliver.
	if !hasGrantScope(grant.Scopes, content.ResourceEnvironment, content.EnvironmentIDFor(content.EnvLocal, "")) {
		t.Fatalf("grant scopes = %+v, want the local environment scope", grant.Scopes)
	}
	if len(grant.Scopes) != 7 {
		t.Fatalf("grant scopes = %+v, want path, session, three content, destination and environment scopes", grant.Scopes)
	}

	reg, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble tool registry: %v", err)
	}
	var names []string
	for _, tool := range reg.ForGrant(*grant) {
		names = append(names, tool.Name)
	}
	want := []string{"files.read", "fetch.url", "session.list", "session.read", "session.keys", "session.message", "session.run", "session.wait", "files.edit", "files.create", "notes.search", "notes.create", "notes.update", "notes.delete", "snippets.list", "snippets.create", "snippets.update", "snippets.delete", "snippets.reorder", "skills.read", "skills.create", "skills.update", "skills.delete", "skills.resolve", "skills.install", "workers.holdings", "workers.spawn", "workers.say", "workers.close", "workers.removeCheckout"}
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

type screenCaptureCall struct {
	sessionID string
	region    *assistant.FrameRegion
}

type recordingScreenCapture struct {
	mu    sync.Mutex
	body  json.RawMessage
	err   error
	calls []screenCaptureCall
}

func (s *recordingScreenCapture) CaptureScreen(_ context.Context, sessionID string, region *assistant.FrameRegion) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call := screenCaptureCall{sessionID: sessionID}
	if region != nil {
		copy := *region
		call.region = &copy
	}
	s.calls = append(s.calls, call)
	if s.err != nil {
		return nil, s.err
	}
	return append(json.RawMessage(nil), s.body...), nil
}

func (s *recordingScreenCapture) snapshotCalls() []screenCaptureCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]screenCaptureCall(nil), s.calls...)
}

func screenCaptureBody(t *testing.T, floor int, lines ...string) json.RawMessage {
	t.Helper()
	rows := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, map[string]any{"kind": "text", "text": line})
	}
	body, err := json.Marshal(map[string]any{
		"floor":  floor,
		"total":  floor + len(lines),
		"rows":   rows,
		"cursor": map[string]any{"line": 0, "col": 0},
		"identity": map[string]any{
			"buffer": map[string]any{"kind": "normal", "instance": 7},
			"cols":   80, "rows": 24, "generation": 1,
		},
		"range": map[string]any{"start": floor, "end": floor + len(lines)},
	})
	if err != nil {
		t.Fatalf("marshal screen capture fixture: %v", err)
	}
	return body
}

func TestRequestScreen_BackendSourceReceivesSessionAndRange(t *testing.T) {
	body := screenCaptureBody(t, 12, "hello", "world")
	source := &recordingScreenCapture{body: body}
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger), WithScreenCaptureSource(source))
	region := &assistant.FrameRegion{Start: 12, End: 14}

	got, err := ws.RequestScreen(t.Context(), "session-1", region)
	if err != nil {
		t.Fatalf("RequestScreen: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("RequestScreen body = %s, want %s", got, body)
	}
	calls := source.snapshotCalls()
	if len(calls) != 1 || calls[0].sessionID != "session-1" || calls[0].region == nil || *calls[0].region != *region {
		t.Fatalf("capture calls = %+v, want session-1 and region [%d,%d)", calls, region.Start, region.End)
	}
}

func TestRequestScreen_ReportsMissingBackendSource(t *testing.T) {
	_, err := (&WSServer{}).RequestScreen(t.Context(), "sid", nil)
	want := "session.read: no backend screen capture source is wired"
	if err == nil || err.Error() != want {
		t.Fatalf("unwired RequestScreen error = %v, want %q", err, want)
	}
}

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
func TestSessionRead_BackendCaptureOverTheRealSocket(t *testing.T) {
	fake, srv := newToolCallingServer()
	defer srv.Close()

	client, _, err := newEinoTestClient(t)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	source := &recordingScreenCapture{body: screenCaptureBody(t, 12, "hello", "world")}
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(autonomousPolicyStore(t)), WithScreenCaptureSource(source))
	h.createEndpointAt(srv.URL)
	sid := openLocalSession(t, h.conn)

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-backend-screen-1", "sessionId": sid,
		"question": "what is on the screen?", "cwd": "/repo",
	}, 2)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	if res.State != "prepared" {
		t.Fatalf("ask state = %q, want prepared", res.State)
	}

	var answer string
	var state struct {
		RunID int64  `json:"runId"`
		State string `json:"state"`
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		raw := readNotification(t, h.conn, "agent.runDelta", 10*time.Second)
		var delta struct {
			RunID int64  `json:"runId"`
			Text  string `json:"text"`
		}
		if err := json.Unmarshal(raw, &delta); err != nil {
			t.Fatalf("runDelta unmarshal: %v\nraw: %s", err, raw)
		}
		if delta.RunID != res.RunID {
			t.Fatalf("runDelta runId = %d, want %d", delta.RunID, res.RunID)
		}
		answer += delta.Text
		if strings.Contains(answer, "ok") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("answer never arrived; collected %q", answer)
		}
	}
	raw := readNotification(t, h.conn, "agent.runState", 10*time.Second)
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("runState unmarshal: %v\nraw: %s", err, raw)
	}
	if state.RunID != res.RunID || state.State != "completed" {
		t.Fatalf("runState = runId %d state %q, want %d completed", state.RunID, state.State, res.RunID)
	}
	if fake.requests.Load() < 2 {
		t.Fatalf("provider received %d requests, want at least two", fake.requests.Load())
	}
	if len(fake.bodies) < 2 {
		t.Fatalf("provider bodies = %d, want tool result request", len(fake.bodies))
	}
	toolResult := sessionReadToolResult(t, fake.bodies[1])
	schema := loadSchema(t, "tools/session.read.schema.json#/$defs/result")
	if err := schema.Validate(toolResult); err != nil {
		t.Fatalf("actual session.read tool result does not satisfy its schema: %v\nresult: %+v", err, toolResult)
	}
	if toolResult["sessionId"] != sid || toolResult["source"] != "backend" || toolResult["text"] != "hello\nworld" {
		t.Fatalf("session.read tool result = %+v, want backend capture for %q with hello/world", toolResult, sid)
	}
	if toolResult["window"] == nil || toolResult["returned"] == nil || toolResult["identity"] == nil {
		t.Fatalf("session.read tool result omitted range or buffer identity: %+v", toolResult)
	}
	calls := source.snapshotCalls()
	if len(calls) != 1 || calls[0].sessionID != sid || calls[0].region != nil {
		t.Fatalf("backend capture calls = %+v, want one whole-screen capture for %q", calls, sid)
	}
}

func sessionReadToolResult(t *testing.T, requestBody string) map[string]any {
	t.Helper()
	content := lifecycleBlockReadToolResult(requestBody)
	const openTag = "<tool-output>\n"
	const closeTag = "\n</tool-output>"
	start := strings.Index(content, openTag)
	end := strings.LastIndex(content, closeTag)
	if start >= 0 && end > start+len(openTag) {
		var result map[string]any
		if err := json.Unmarshal([]byte(content[start+len(openTag):end]), &result); err == nil && result["source"] == "backend" {
			return result
		}
	}
	t.Fatalf("second provider request has no backend session.read tool result: %s", requestBody)
	return nil
}
