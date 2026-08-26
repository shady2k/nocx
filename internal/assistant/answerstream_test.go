package assistant

// The answer stream (nocx-shxv0, nocx-bshm2, nocx-s92so): what the run EMITS,
// and what it must never emit.
//
// Three defects were measured on 2026-08-22 against a live reasoning model,
// and all three live in one event loop:
//
//  1. a tool RESULT — a message of role `tool` — travelled the delta path as
//     though the model had said it, so the raw JSON of a readScreen return
//     was rendered as the assistant's own prose (nocx-bshm2);
//  2. the model's REASONING was decoded off the wire and dropped on the floor
//     (nocx-s92so);
//  3. a tool CALL was on no wire at all, so the renderer could not know one
//     had happened and the run tool's block landed BELOW the answer written
//     from it (nocx-shxv0).
//
// These tests state the three as one contract: Ask emits ONE ordered stream
// of typed events, and each of the three things is its own kind.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// collectEvents drives one Ask and records every event in arrival order.
// The recorder is the assertion surface of every test here: the ORDER is the
// property under test, so nothing is bucketed by kind before it is asserted.
type eventLog struct {
	mu     sync.Mutex
	events []AskEvent
}

func (l *eventLog) sink() func(AskEvent) error {
	return func(e AskEvent) error {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.events = append(l.events, e)
		return nil
	}
}

func (l *eventLog) all() []AskEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]AskEvent(nil), l.events...)
}

// text concatenates the text of every event of one kind — the answer as the
// renderer would assemble it.
func (l *eventLog) text(kind AskEventKind) string {
	var sb strings.Builder
	for _, e := range l.all() {
		if e.Kind == kind {
			sb.WriteString(e.Text)
		}
	}
	return sb.String()
}

// kinds is the arrival ORDER as a readable sequence.
func (l *eventLog) kinds() []AskEventKind {
	out := make([]AskEventKind, 0, len(l.all()))
	for _, e := range l.all() {
		out = append(out, e.Kind)
	}
	return out
}

// streamReasoningThenAnswer writes one streamed completion whose first two
// chunks carry `reasoning_content` and whose last two carry `content` — the
// shape a reasoning model over an OpenAI-compatible API sends (the delta
// field is `reasoning_content`, decoded by the eino openai adapter into
// schema.Message.ReasoningContent; see the module cache,
// libs/acl/openai/chat_model.go streamMessageBuilder.build).
func streamReasoningThenAnswer(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", reasoningChunkJSON("the user asks about "))
	_, _ = fmt.Fprintf(w, "data: %s\n\n", reasoningChunkJSON("the screen, so I should look"))
	_, _ = fmt.Fprintf(w, "data: %s\n\n", chunkJSON("o", ""))
	_, _ = fmt.Fprintf(w, "data: %s\n\n", chunkJSON("k", "stop"))
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func reasoningChunkJSON(reasoning string) string {
	d := map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "probe-model",
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{"role": "assistant", "reasoning_content": reasoning},
			"finish_reason": "",
		}},
	}
	b, _ := json.Marshal(d)
	return string(b)
}

// ── nocx-bshm2: the tool result is not the answer ─────────────────────────

// TestAsk_TheToolResultNeverReachesTheAnswerText is criterion 2, and it is
// role-checked at the seam rather than string-matched: a tool result is a
// message of role `tool`, and the loop that emitted "any message with
// content" put its raw JSON in front of a person as the assistant's own
// words. The file's body is a distinctive string precisely so its ABSENCE
// from the answer is a fact about the seam and not about the phrasing.
func TestAsk_TheToolResultNeverReachesTheAnswerText(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	const body = "SENTINEL-tool-result-body-must-not-be-spoken"
	writeFile(t, filepath.Join(dir, "a.txt"), body)
	args := fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "a.txt"))

	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	log := &eventLog{}
	if err := cl.Ask(context.Background(), askParams(srv.URL, &grant, realLedger(t), NewApprovalStore()), log.sink()); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	answer := log.text(AskAnswer)
	if answer != "ok" {
		t.Fatalf("answer text = %q, want the model's prose %q", answer, "ok")
	}
	for _, e := range log.all() {
		if strings.Contains(e.Text, body) {
			t.Fatalf("the tool's return value reached a %s event: %q", e.Kind, e.Text)
		}
	}
}

// ── nocx-shxv0: the call is on the wire, and it precedes the answer ───────

// TestAsk_TheToolCallPrecedesTheAnswerWrittenFromIt is criterion 1: the
// tool's appearance comes BEFORE the answer text written from its result.
// Asserted on the arrival order of one ordered event stream, never eyeballed
// in a screenshot.
func TestAsk_TheToolCallPrecedesTheAnswerWrittenFromIt(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "the build failed on line 3")
	args := fmt.Sprintf(`{"path":%q}`, path)

	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	log := &eventLog{}
	if err := cl.Ask(context.Background(), askParams(srv.URL, &grant, realLedger(t), NewApprovalStore()), log.sink()); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	events := log.all()
	callAt, answerAt := -1, -1
	for i, e := range events {
		if e.Kind == AskToolCall && callAt < 0 {
			callAt = i
		}
		if e.Kind == AskAnswer && answerAt < 0 {
			answerAt = i
		}
	}
	if callAt < 0 {
		t.Fatalf("no tool-call event in %v — a call the model made left no trace at all", log.kinds())
	}
	if answerAt < 0 {
		t.Fatalf("no answer event in %v", log.kinds())
	}
	if callAt > answerAt {
		t.Fatalf("the tool call arrived AFTER the answer written from it: %v", log.kinds())
	}

	// What the call carries: the tool, the model's call id, the ledger entry
	// the attempt was recorded under, the effect the gate decided on, and the
	// resource the ONE derivation (namedResource) says it touched.
	call := events[callAt].Call
	if call == nil {
		t.Fatal("the tool-call event carries no call")
	}
	if call.Tool != "files.read" {
		t.Fatalf("call.Tool = %q, want files.read", call.Tool)
	}
	if call.CallID == "" {
		t.Fatal("call.CallID is empty — the renderer cannot tell two calls apart")
	}
	if call.EntryID == "" {
		t.Fatal("call.EntryID is empty — the visible call names no ledger entry")
	}
	if call.Effect != content.EffectObserve {
		t.Fatalf("call.Effect = %q, want observe", call.Effect)
	}
	if call.Resource == nil || call.Resource.Kind != content.ResourcePath || call.Resource.ID != path {
		t.Fatalf("call.Resource = %+v, want the path the call named", call.Resource)
	}
	// The raw arguments blob is deliberately NOT what a person reads: the
	// resource is the derived fact, and nothing here restates the JSON.
	if strings.Contains(events[callAt].Text, "{") {
		t.Fatalf("the tool-call event carries a raw arguments blob as text: %q", events[callAt].Text)
	}
}

// ── nocx-s92so: reasoning is its own thing ────────────────────────────────

// TestAsk_ReasoningIsItsOwnEventAndNeverTheAnswerText is criterion 3: a
// model that emits reasoning parts has them reach the caller as reasoning,
// and the answer text is the answer alone. An answer block that concatenates
// thinking with the answer is nocx-bshm2 in another shape.
func TestAsk_ReasoningIsItsOwnEventAndNeverTheAnswerText(t *testing.T) {
	_, srv := newFakeOpenAI(func(w http.ResponseWriter, _ *http.Request) { streamReasoningThenAnswer(w) })
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	log := &eventLog{}
	if err := cl.Ask(context.Background(), testAskParams(srv.URL), log.sink()); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if got, want := log.text(AskReasoning), "the user asks about the screen, so I should look"; got != want {
		t.Fatalf("reasoning text = %q, want %q", got, want)
	}
	if got := log.text(AskAnswer); got != "ok" {
		t.Fatalf("answer text = %q, want the answer alone", got)
	}
}

// ── criterion 4: both ends. Nothing new renders when nothing new exists ───

// TestAsk_NoReasoningNoToolsEmitsAnswerEventsOnly is the paired end of the
// two above (AGENTS.md testing rule 3): the ordinary explain-mode run — a
// model with no reasoning, an ask with no tool calls — emits answer events
// and NOTHING else. No empty reasoning event, no placeholder call.
func TestAsk_NoReasoningNoToolsEmitsAnswerEventsOnly(t *testing.T) {
	_, srv := newFakeOpenAI(nil) // the default: one streamed "ok"
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	log := &eventLog{}
	if err := cl.Ask(context.Background(), testAskParams(srv.URL), log.sink()); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	for _, k := range log.kinds() {
		if k != AskAnswer {
			t.Fatalf("an ordinary run emitted a %s event: %v", k, log.kinds())
		}
	}
	if got := log.text(AskAnswer); got != "ok" {
		t.Fatalf("answer text = %q, want ok", got)
	}
}

// ── the id-less door (w-call-id-order): prose before the call stays before ─

// streamProseThenToolCalls writes ONE completion whose first chunk carries
// content and whose final frame carries the tool calls — the exact shape the
// e2e fake scripts for a response that both speaks and proposes
// (e2e/fake-openai.ts: content chunks with finish_reason "", then the
// tool_calls frame with finish_reason "tool_calls", then [DONE]). The call
// carries NO id, which is the provider shape this bead is about.
func streamProseThenToolCalls(prose string, calls ...toolCallSpec) func(w http.ResponseWriter, r *http.Request) {
	var n int
	return func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunkJSON(prose, ""))
			tcs := make([]map[string]any, 0, len(calls))
			for i, c := range calls {
				id := c.id
				if id == "" {
					id = fmt.Sprintf("call_%d", i+1)
				}
				tcs = append(tcs, map[string]any{
					"id":   id,
					"type": "function",
					"function": map[string]any{
						"name":      c.name,
						"arguments": c.args,
					},
				})
			}
			d := map[string]any{
				"id":      "chatcmpl-test",
				"object":  "chat.completion.chunk",
				"created": 0,
				"model":   "probe-model",
				"choices": []map[string]any{{
					"index":         0,
					"delta":         map[string]any{"role": "assistant", "tool_calls": tcs},
					"finish_reason": "tool_calls",
				}},
			}
			b, _ := json.Marshal(d)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		streamOK(w)
	}
}

// TestAsk_ProseBeforeTheCallStaysBeforeTheCall is the seam assertion for the
// id-less door: a response that carries BOTH prose and a tool call in one
// completion must emit the prose BEFORE the call event. The sentence written
// before the command explains why the command was run; the transport seals
// the open prose block on the call event, so a call that arrives before the
// prose would leave the prose to open a block AFTER the command — the
// inversion this bead exists to make impossible.
func TestAsk_ProseBeforeTheCallStaysBeforeTheCall(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "the build failed on line 3")
	args := fmt.Sprintf(`{"path":%q}`, path)

	_, srv := newFakeOpenAI(streamProseThenToolCalls("Let me check.", toolCallSpec{name: "files.read", args: args}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	log := &eventLog{}
	if err := cl.Ask(context.Background(), askParams(srv.URL, &grant, realLedger(t), NewApprovalStore()), log.sink()); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	events := log.all()
	firstAnswer, callAt, lastAnswer := -1, -1, -1
	for i, e := range events {
		switch e.Kind {
		case AskAnswer:
			if firstAnswer < 0 {
				firstAnswer = i
			}
			lastAnswer = i
		case AskToolCall:
			if callAt < 0 {
				callAt = i
			}
		}
	}
	if firstAnswer < 0 || callAt < 0 || lastAnswer < 0 {
		t.Fatalf("expected answer, call, answer in %v", log.kinds())
	}
	if firstAnswer > callAt {
		t.Fatalf("the prose written before the call arrived AFTER the call event: %v", log.kinds())
	}
	if callAt > lastAnswer {
		t.Fatalf("the call arrived after the answer written from its result: %v", log.kinds())
	}
	if got := events[firstAnswer].Text; got != "Let me check." {
		t.Fatalf("the pre-call prose = %q, want %q", got, "Let me check.")
	}
}

// TestAsk_ProseBeforeTheCallStaysBeforeTheCall_WithId is the paired end
// (AGENTS.md testing rule 3): the same response with the call carrying the
// model's own id must behave identically — a real provider sends ids, and
// the id-less fix must not regress that path.
func TestAsk_ProseBeforeTheCallStaysBeforeTheCall_WithId(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "the build failed on line 3")
	args := fmt.Sprintf(`{"path":%q}`, path)

	_, srv := newFakeOpenAI(streamProseThenToolCalls("Let me check.", toolCallSpec{name: "files.read", args: args, id: "call_diag"}))
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	log := &eventLog{}
	if err := cl.Ask(context.Background(), askParams(srv.URL, &grant, realLedger(t), NewApprovalStore()), log.sink()); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	events := log.all()
	firstAnswer, callAt, lastAnswer := -1, -1, -1
	for i, e := range events {
		switch e.Kind {
		case AskAnswer:
			if firstAnswer < 0 {
				firstAnswer = i
			}
			lastAnswer = i
		case AskToolCall:
			if callAt < 0 {
				callAt = i
			}
		}
	}
	if firstAnswer < 0 || callAt < 0 || lastAnswer < 0 {
		t.Fatalf("expected answer, call, answer in %v", log.kinds())
	}
	if firstAnswer > callAt {
		t.Fatalf("the prose written before the call arrived AFTER the call event: %v", log.kinds())
	}
	if callAt > lastAnswer {
		t.Fatalf("the call arrived after the answer written from its result: %v", log.kinds())
	}
	if got := events[callAt].Call.CallID; got != "call_diag" {
		t.Fatalf("the call's id = %q, want the model's own %q", got, "call_diag")
	}
}
