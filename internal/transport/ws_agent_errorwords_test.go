package transport

// The product's own words for a refused or failed run (nocx-avogl.3).
//
// The owner photographed this on an answer block:
//
//	the model failed to answer: [NodeRunError] failed to stream tool call
//	call_7947...: agent policy: tool call refused
//	node path: [node_1, ToolNode]
//
// `node_1`, `ToolNode`, `NodeRunError` and the call id are eino's internals.
// A person must be told what was proposed, what happened to it and what they
// can do; the trace belongs in the log.
//
// Every assertion here is over the REAL error the middleware returns: the
// real assistant engine, the real policy middleware, a fake provider that
// emits the tool call, and the sentence read off the REAL socket's
// agent.runState. A test that handed classifyAskFailure an error it invented
// would prove the mapping and not the classification.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
)

// frameworkWords are the identifiers a person must never be shown. The list
// is eino's own, taken from compose/error.go (internalError.Error renders the
// type and the node path) and from the owner's screenshot.
var frameworkWords = []string{"NodeRunError", "GraphRunError", "node path", "ToolNode", "node_1", "call_"}

// assertNoFrameworkWords is the "in our words, not eino's" criterion.
func assertNoFrameworkWords(t *testing.T, sentence string) {
	t.Helper()
	for _, w := range frameworkWords {
		if strings.Contains(sentence, w) {
			t.Errorf("the sentence a person reads contains the framework identifier %q: %q", w, sentence)
		}
	}
}

// recordingClient is the REAL engine with the error it returned kept, so a
// test can assert over the error itself and not only over the sentence it
// became.
type recordingClient struct {
	unauditedClient
	inner assistant.Client
	mu    sync.Mutex
	err   error
}

func (c *recordingClient) Probe(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
	return c.inner.Probe(ctx, p)
}

// Discard implements assistant.Client. This fake holds no suspended
// state, so there is nothing to drop.
func (*recordingClient) Discard(string) {}

func (c *recordingClient) Ask(ctx context.Context, p assistant.AskParams, onEvent func(assistant.AskEvent) error) error {
	err := c.inner.Ask(ctx, p, onEvent)
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
	return err
}

func (c *recordingClient) lastError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// failingRun drives ONE real ask against a provider that answers with the
// given handler (or against baseURL when there is no provider to run at all),
// and returns the terminal agent.runState's state and error sentence plus the
// engine error that produced it.
func failingRun(t *testing.T, baseURL string, handler http.HandlerFunc) (state, sentence string, engineErr error) {
	t.Helper()
	if handler != nil {
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		baseURL = srv.URL
	}
	rec := &recordingClient{inner: mustClient(t)}
	h := newAskHarnessWithOpts(t, rec, WithAgentPolicy(autonomousPolicyStore(t)))
	h.createEndpointAt(baseURL)
	sid := openLocalSession(t, h.conn)

	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "ask-errorwords",
		"sessionId": sid,
		"question":  "what is on the screen?",
		"cwd":       "/repo",
	}, 2); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.runState", 15*time.Second)
	var st struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("runState unmarshal: %v\nraw: %s", err, raw)
	}
	return st.State, st.Error, rec.lastError()
}

// malformedReadScreen names a declared tool — so the call reaches OUR
// middleware rather than the framework's own index lookup — with an argument
// object the schema the model was shown does not allow.
func malformedReadScreen(w http.ResponseWriter, _ *http.Request) {
	streamToolCallChunk(w, "session.read", `{"notADeclaredProperty":1}`)
}

type failingKnownMaterial struct {
	err error
}

func (f failingKnownMaterial) FindKnown(context.Context, string) ([]assistant.KnownMatch, error) {
	return nil, f.err
}

// TestAskFailure_EgressScreeningNamesSealedVault drives a real tool call
// through the policy middleware, makes its known-material gate fail, and
// reads the resulting sentence from the real runState notification.
func TestAskFailure_EgressScreeningNamesSealedVault(t *testing.T) {
	var sessionID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		streamToolCallChunk(w, "session.list", `{}`)
	}))
	defer srv.Close()

	rec := &recordingClient{inner: mustClient(t)}
	h := newAskHarnessWithOpts(t, rec,
		WithAgentPolicy(autonomousPolicyStore(t)),
		WithAgentKnownMaterial(failingKnownMaterial{err: vault.ErrVaultSealed}),
	)
	h.createEndpointAt(srv.URL)
	sessionID = openLocalSession(t, h.conn)
	if err := h.conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear read deadline: %v", err)
	}
	tap := newSocketTap(h.conn)
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "agent.ask",
		"params": map[string]any{
			"askId":           "ask-sealed-egress",
			"sessionId":       sessionID,
			"question":        "what is on the screen?",
			"cwd":             "/repo",
			"attachedContent": []any{},
		},
	})
	if err != nil {
		t.Fatalf("marshal ask: %v", err)
	}
	if err := h.conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write ask: %v", err)
	}
	_, sentence := waitForRunState(t, tap, "failed")
	var screeningErr *assistant.EgressScreeningError
	if !errors.As(rec.lastError(), &screeningErr) {
		t.Fatalf("engine error = %v, want assistant.EgressScreeningError", rec.lastError())
	}
	if screeningErr.Gate != "egress" {
		t.Fatalf("screening gate = %q, want %q", screeningErr.Gate, "egress")
	}
	if screeningErr.Tool != "session.list" {
		t.Fatalf("screening tool = %q, want %q", screeningErr.Tool, "session.list")
	}
	if !strings.Contains(screeningErr.Error(), `agent tool "session.list": egress screening failed`) {
		t.Fatalf("screening error = %q, want the tool name in its log text", screeningErr.Error())
	}
	if !errors.Is(screeningErr, vault.ErrVaultSealed) {
		t.Fatalf("screening error = %v, want vault.ErrVaultSealed in its chain", screeningErr)
	}
	assertNoFrameworkWords(t, sentence)
	const wantSentence = "the vault is sealed. Unlock it in Settings → Vault, then ask again."
	if sentence != wantSentence {
		t.Fatalf("sentence = %q, want %q", sentence, wantSentence)
	}
	if strings.Contains(sentence, screeningErr.Tool) {
		t.Fatalf("wire sentence contains tool name %q: %q", screeningErr.Tool, sentence)
	}
}

// TestAsk_EgressSealedVaultUnlocksAndContinues drives a real run tool result
// through the real policy middleware and the real vault adapter. The egress
// gate must raise the unlock, then continue screening the same result rather
// than discard it and fail the run.
func TestAsk_EgressSealedVaultUnlocksAndContinues(t *testing.T) {
	const secret = "egress-secret-value"
	fake, srv := newRunToolCallingServer("")
	defer srv.Close()

	client := mustClient(t)
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(autonomousPolicyStore(t)))
	created, code := decodeEndpointResult(t, jsonrpcCall(t, h.conn, "endpoints.create", map[string]any{
		"name":    "No-key provider",
		"baseUrl": srv.URL,
		"noKey":   true,
		"models":  []map[string]any{{"name": "qwen3"}},
	}))
	if code != 0 {
		t.Fatalf("endpoints.create: code %d", code)
	}
	if isErrorResponse(t, jsonrpcCall(t, h.conn, "roles.assign", map[string]any{
		"role": "answering", "endpointId": created.ID, "model": "qwen3",
	})) {
		t.Fatal("roles.assign refused the no-key endpoint")
	}
	createSecret(t, h.v, "egress-secret", secret)
	h.v.Seal()
	h.v.SetUnlockRequester(unlockRequesterFunc(h.ws.RequestUnlock))
	sid := openLocalSession(t, h.conn)
	fake.args = `{"command":"printf output"}`

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "sealed-egress", "sessionId": sid, "question": "show the result",
		"cwd": "/repo", "attachedContent": []any{},
	}, 2)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.runRequest", 10*time.Second)
	var runReq struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(raw, &runReq); err != nil {
		t.Fatalf("runRequest: %v", err)
	}
	if runReq.RequestID == "" {
		t.Fatal("runRequest has no requestId")
	}
	if response := jsonrpcCall(t, h.conn, "agent.runResolved", runResolvedWire(
		runReq.RequestID, "egress-entry", 0, "success", 1, 0, 1, secret,
	)); isErrorResponse(t, response) {
		t.Fatalf("agent.runResolved refused: %s", response)
	}

	frame := readUnlockRequestFrame(t, h.conn)
	if frame.Reason != "screen the tool result" {
		t.Fatalf("unlock reason = %q, want %q", frame.Reason, "screen the tool result")
	}
	unseal := jsonrpcCallWithID(t, h.conn, "vault.unseal", map[string]any{
		"means": "passphrase", "secret": "test",
	}, 3)
	if isErrorResponse(t, unseal) {
		t.Fatalf("the unseal that answers the raised unlock was refused: %s", unseal)
	}
	answerUnlock(t, h.conn, frame.RequestID, "unsealed")
	raw = readNotification(t, h.conn, "agent.approvalRequested", 10*time.Second)
	if strings.Contains(string(raw), secret) {
		t.Fatalf("egress approval carried secret material: %s", raw)
	}
	var approval agentApprovalRequested
	if err := json.Unmarshal(raw, &approval); err != nil {
		t.Fatalf("approvalRequested: %v", err)
	}
	if approval.Reason != "egress" || len(approval.Findings) != 1 ||
		approval.Findings[0].SecretName != "egress-secret" {
		t.Fatalf("approvalRequested = %+v, want one named egress finding", approval)
	}
	if _, errObj := approveOverWire(t, h.conn, map[string]any{
		"runId": approval.RunID, "attempt": approval.Attempt,
		"tool": approval.Tool, "callId": approval.CallID,
		"argHash": approval.ArgHash, "approved": true, "scope": "once",
	}, 4); errObj != nil {
		t.Fatalf("agent.approve: %+v", errObj)
	}

	var answer string
	for range 2 {
		raw = readNotification(t, h.conn, "agent.runDelta", 10*time.Second)
		var delta agentRunDelta
		if err := json.Unmarshal(raw, &delta); err != nil {
			t.Fatalf("runDelta: %v", err)
		}
		if delta.RunID != res.RunID {
			t.Fatalf("runDelta runId = %d, want %d", delta.RunID, res.RunID)
		}
		answer += delta.Text
	}
	if answer != "ok" {
		t.Fatalf("answer = %q, want final answer %q", answer, "ok")
	}
	raw = readNotification(t, h.conn, "agent.runState", 10*time.Second)
	var state agentRunState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("runState: %v", err)
	}
	if state.RunID != res.RunID || state.State != string(content.RunCompleted) || state.Error != "" {
		t.Fatalf("runState = %+v, want completed without an error", state)
	}
}

// unreachableEndpoint is a loopback port nothing listens on. http:// is
// permitted there by the guarded client, so the failure is the dial and
// nothing before it.
const unreachableEndpoint = "http://127.0.0.1:1/v1"

// unknownToolThenAnswer is a provider that first proposes one tool call and
// then answers after receiving the model-visible result. The atomic flag is
// set only when the expected result crossed the real model boundary.
func unknownToolThenAnswer(name, args, expected string, firstBody, toolBody *atomic.Value, saw *atomic.Bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if firstBody.Load() == nil {
			firstBody.Store(string(body))
		}
		if strings.Contains(string(body), `"role":"tool"`) {
			toolBody.Store(string(body))
			if strings.Contains(string(body), expected) {
				saw.Store(true)
			}
			streamOKChunks(w)
			return
		}
		streamToolCallChunk(w, name, args)
	}
}

func unknownToolRun(t *testing.T, policy *assistant.GlobalPolicyStore, name, args, expected string) (string, string, error, bool, string, string, bool) {
	t.Helper()
	saw := &atomic.Bool{}
	var firstBody, toolBody atomic.Value
	srv := httptest.NewServer(unknownToolThenAnswer(name, args, expected, &firstBody, &toolBody, saw))
	t.Cleanup(srv.Close)

	rec := &recordingClient{inner: mustClient(t)}
	h := newAskHarnessWithOpts(t, rec, WithAgentPolicy(policy))
	h.createEndpointAt(srv.URL)
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "ask-unknown-tool",
		"sessionId": sid,
		"question":  "what is on the screen?",
		"cwd":       "/repo",
	}, 2); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.runState", 15*time.Second)
	var st struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("runState unmarshal: %v\nraw: %s", err, raw)
	}
	_, approvalRequested := inboxOf(h.conn).take(isNotification("agent.approvalRequested"))
	first, _ := firstBody.Load().(string)
	tool, _ := toolBody.Load().(string)
	return st.State, st.Error, rec.lastError(), saw.Load(), first, tool, approvalRequested
}

func refusingPolicyStore(t *testing.T) *assistant.GlobalPolicyStore {
	t.Helper()
	refuse := content.EffectRow{Decision: content.DecisionRefuse}
	policy := content.EffectPolicy{
		Observe: refuse, MutateReversible: refuse, MutateDestructive: refuse,
		PrivilegeChange: refuse, Disclose: refuse, CrossBoundary: refuse,
		Delegate: refuse,
	}
	store := assistant.NewGlobalPolicyStore(storage.NewDocumentStore(t.TempDir()), "agent-policy.json")
	if err := store.SetPolicy(policy); err != nil {
		t.Fatalf("seed refusing policy: %v", err)
	}
	return store
}

// TestAsk_StandingNeverUnknownCallCompletes proves the all-refused shape:
// ForGrant still declares no tools, but a model call for a known tool is
// intercepted before eino's no-tools path can fail the turn. The tool is
// neither advertised nor executed; its standing policy refusal is returned
// to the model, which answers in words.
func TestAsk_StandingNeverUnknownCallCompletes(t *testing.T) {
	const expected = "this kind of action is refused by the policy"
	state, sentence, engineErr, saw, firstBody, _, approvalRequested := unknownToolRun(
		t, refusingPolicyStore(t), "session.read", `{"sessionId":"not-this-run"}`, expected,
	)
	if state != "completed" || sentence != "" || engineErr != nil {
		t.Fatalf("run = state %q, sentence %q, error %v; want completed refusal answer", state, sentence, engineErr)
	}
	if !saw {
		t.Fatal("the standing-policy refusal never reached the model as a tool result")
	}
	if strings.Contains(firstBody, `"name":"session.read"`) ||
		strings.Contains(firstBody, unknownToolAnchorNameForTest) {
		t.Fatalf("Never policy advertised a refused or internal tool: %s", firstBody)
	}
	if approvalRequested {
		t.Fatal("Never policy raised an approval request after the run completed")
	}
}

func TestAsk_WithheldToolNamesEveryRefusedEffect(t *testing.T) {
	state, sentence, engineErr, saw, _, toolBody, approvalRequested := unknownToolRun(
		t, refusingPolicyStore(t), "session.run", `{}`, "every effect it can reach is refused by policy",
	)
	if state != "completed" || sentence != "" || engineErr != nil {
		t.Fatalf("run = state %q, sentence %q, error %v; want completed withheld-tool answer", state, sentence, engineErr)
	}
	if !saw {
		t.Fatal("the withheld-tool explanation never reached the model as a tool result")
	}
	if toolBody == "" {
		t.Fatal("the tool-result request body was not captured")
	}
	const wantRows = "every effect it can reach is refused by policy (observe, mutate-reversible, mutate-destructive, delegate, cross-boundary)."
	if !strings.Contains(toolBody, wantRows) {
		t.Fatalf("withheld-tool explanation %q does not name every refusing row", toolBody)
	}
	for _, effect := range []string{
		"observe", "mutate-reversible", "mutate-destructive", "cross-boundary", "delegate",
	} {
		if !strings.Contains(toolBody, effect) {
			t.Errorf("withheld-tool explanation %q does not name refusing row %q", toolBody, effect)
		}
	}
	if approvalRequested {
		t.Fatal("withheld tool raised an approval request")
	}
}

// TestAsk_InventedUnknownCallCompletes proves a wholly invented name follows
// the same model-visible result path while permitted tools remain advertised.
func TestAsk_InventedUnknownCallCompletes(t *testing.T) {
	const expected = "There is no such tool"
	state, sentence, engineErr, saw, _, _, approvalRequested := unknownToolRun(
		t, autonomousPolicyStore(t), "model.invented.tool", `{}`, expected,
	)
	if state != "completed" || sentence != "" || engineErr != nil {
		t.Fatalf("run = state %q, sentence %q, error %v; want completed unknown-tool answer", state, sentence, engineErr)
	}
	if !saw {
		t.Fatal("the invented-tool explanation never reached the model as a tool result")
	}
	if approvalRequested {
		t.Fatal("invented tool raised an approval request")
	}
}

const unknownToolAnchorNameForTest = "nocx.internal.unknown-tool-anchor"

// ── cause 2: the model's tool call was malformed ─────────────────────────

// TestAskFailure_MalformedModelOutputKeepsItsOwnSentence is the bead's third
// criterion, first half: arguments that do not match the schema the model was
// shown are NOT a refusal — there was nothing to refuse — and they must not
// share the refusal's sentence.
func TestAskFailure_MalformedModelOutputKeepsItsOwnSentence(t *testing.T) {
	state, sentence, engineErr := failingRun(t, "", malformedReadScreen)
	if state != "failed" {
		t.Fatalf("runState = %q, want failed", state)
	}
	if !errors.Is(engineErr, assistant.ErrMalformedModelOutput) {
		t.Fatalf("the engine error is not ErrMalformedModelOutput: %v", engineErr)
	}
	assertNoFrameworkWords(t, sentence)
	if strings.Contains(strings.ToLower(sentence), "polic") {
		t.Errorf("a malformed tool call is reported as a policy refusal — one message for two causes: %q", sentence)
	}
	if !strings.Contains(strings.ToLower(sentence), "tool call") {
		t.Errorf("the malformed-output sentence does not say what could not be acted on: %q", sentence)
	}
	// The schema validator's own text — the contract URL and the jsonschema
	// vocabulary — is machinery, not something a person reads.
	for _, w := range []string{"jsonschema", "https://nocx.local", "additional properties"} {
		if strings.Contains(sentence, w) {
			t.Errorf("the sentence carries the validator's own text %q: %q", w, sentence)
		}
	}
}

// ── cause 3: the endpoint could not be reached ───────────────────────────

// TestAskFailure_AnUnreachableEndpointKeepsItsOwnSentence is the bead's third
// criterion, second half. The Go error is a *url.Error naming the URL and the
// syscall; the person is told the endpoint could not be reached.
func TestAskFailure_AnUnreachableEndpointKeepsItsOwnSentence(t *testing.T) {
	state, sentence, _ := failingRun(t, unreachableEndpoint, nil)
	if state != "failed" {
		t.Fatalf("runState = %q, want failed", state)
	}
	assertNoFrameworkWords(t, sentence)
	if !strings.Contains(strings.ToLower(sentence), "reach") {
		t.Errorf("the transport-failure sentence does not say the endpoint could not be reached: %q", sentence)
	}
	for _, w := range []string{"127.0.0.1", "dial tcp", "connection refused", "chat/completions"} {
		if strings.Contains(sentence, w) {
			t.Errorf("the sentence carries the Go transport error's text %q: %q", w, sentence)
		}
	}
	if strings.Contains(strings.ToLower(sentence), "polic") {
		t.Errorf("a transport failure is reported as a policy refusal: %q", sentence)
	}
}

// ── the causes stay distinct ─────────────────────────────────────────────

// TestAskFailure_CausesKeepTheirOwnSentences is the bead's third criterion
// stated directly: one message for two causes is how a cause stops being
// findable. The sentences come from two REAL runs and must all differ. A
// policy refusal is absent by design: since nocx-uvac6.1 it is the call's
// result, not a run-ending error, so it has no sentence here (the refusal's
// own text is asserted in the assistant package).
func TestAskFailure_CausesKeepTheirOwnSentences(t *testing.T) {
	cases := []struct {
		cause   string
		baseURL string
		handler http.HandlerFunc
	}{
		{cause: "malformed model output", handler: malformedReadScreen},
		{cause: "unreachable endpoint", baseURL: unreachableEndpoint},
	}
	seen := map[string]string{}
	for _, tc := range cases {
		_, sentence, _ := failingRun(t, tc.baseURL, tc.handler)
		if sentence == "" {
			t.Fatalf("%s produced no sentence at all", tc.cause)
		}
		if other, dup := seen[sentence]; dup {
			t.Errorf("%q and %q share one sentence %q — a cause that cannot be told apart cannot be found", tc.cause, other, sentence)
		}
		assertNoFrameworkWords(t, sentence)
		seen[sentence] = tc.cause
		t.Logf("%-24s → %s", tc.cause, sentence)
	}
}
