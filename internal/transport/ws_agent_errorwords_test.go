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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
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
	streamToolCallChunk(w, "session.read", `{"sessionId":"s","notADeclaredProperty":1}`)
}

// unreachableEndpoint is a loopback port nothing listens on. http:// is
// permitted there by the guarded client, so the failure is the dial and
// nothing before it.
const unreachableEndpoint = "http://127.0.0.1:1/v1"

// ── the refusal is an answer, not a failure (nocx-uvac6.1) ──────────────

// outOfScopeReadScreenThenAnswer is the owner's own scenario — the model
// calls readScreen naming a session this run's grant does not cover — and
// then answers once it has seen the refusal as a tool result: a provider
// that stops proposing is every real one.
func outOfScopeReadScreenThenAnswer(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	if strings.Contains(string(body), `"role":"tool"`) {
		streamOKChunks(w)
		return
	}
	streamToolCallChunk(w, "session.read", `{"sessionId":"a-session-this-grant-does-not-cover"}`)
}

// TestAsk_RefusalCompletesTheRun is the transport half of nocx-uvac6.1: a
// policy refusal is the call's result, not a terminal error, so the run
// reaches a terminal state of its own accord — completed, with no error
// sentence on the wire and none of the framework's words anywhere. The
// refusal's own text is asserted where it lives, in the assistant package,
// on the request the engine actually sent; here the wire is the contract.
func TestAsk_RefusalCompletesTheRun(t *testing.T) {
	state, sentence, engineErr := failingRun(t, "", outOfScopeReadScreenThenAnswer)
	if state != "completed" {
		t.Fatalf("runState = %q, want completed — a refusal is an answer, not a failure", state)
	}
	if sentence != "" {
		t.Fatalf("the completed run carries an error sentence %q, want none", sentence)
	}
	if engineErr != nil {
		t.Fatalf("the engine returned an error %v for a run that completed", engineErr)
	}
	assertNoFrameworkWords(t, state+" "+sentence)
}

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
