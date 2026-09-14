package assistant

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// fakePaneMessagesExec is PaneMessages for the executor-level tests below:
// it records every request it was given and answers one scripted result.
// internal/app's real implementation cannot be imported here (app depends
// on assistant, not the reverse — the same layering session_keys_test.go's
// fakePaneKeysExec already works around).
type fakePaneMessagesExec struct {
	sendCalls    []sendCall
	sendResult   MessageView
	sendErr      error
	cancelCalls  []cancelCall
	cancelResult CancelResult
	cancelErr    error
}

type sendCall struct {
	sessionID, text, when, id, tokenID string
}

type cancelCall struct {
	sessionID, id string
}

func (f *fakePaneMessagesExec) Send(_ context.Context, _ any, sessionID, text, when, id, tokenID string) (MessageView, error) {
	f.sendCalls = append(f.sendCalls, sendCall{sessionID, text, when, id, tokenID})
	return f.sendResult, f.sendErr
}

func (f *fakePaneMessagesExec) Cancel(_ context.Context, _ any, sessionID, id string) (CancelResult, error) {
	f.cancelCalls = append(f.cancelCalls, cancelCall{sessionID, id})
	return f.cancelResult, f.cancelErr
}

func sessionMessageCapability(messages PaneMessages) *agenttools.SessionDescendantCapability {
	return &agenttools.SessionDescendantCapability{
		PaneAccess:      "fake-pane-access",
		SessionMessages: messages,
	}
}

func TestExecuteSessionMessage_NoDescendantAuthorityRefuses(t *testing.T) {
	cap := &agenttools.SessionDescendantCapability{}
	if _, err := executeSessionMessage(context.Background(), cap, json.RawMessage(`{"sessionId":"s","text":"hi","when":"free","id":"m1"}`)); err == nil {
		t.Fatal("executeSessionMessage succeeded with no PaneAccess/SessionMessages wired")
	}
}

func TestExecuteSessionMessage_SendRequiresTextWhenAndID(t *testing.T) {
	cases := []string{
		`{"sessionId":"s","when":"free","id":"m1"}`,
		`{"sessionId":"s","text":"hi","id":"m1"}`,
		`{"sessionId":"s","text":"hi","when":"soon","id":"m1"}`,
		`{"sessionId":"s","text":"hi","when":"free"}`,
	}
	for _, args := range cases {
		messages := &fakePaneMessagesExec{}
		cap := sessionMessageCapability(messages)
		if _, err := executeSessionMessage(context.Background(), cap, json.RawMessage(args)); err == nil {
			t.Fatalf("args %s: executeSessionMessage succeeded, want an error", args)
		}
		if len(messages.sendCalls) != 0 {
			t.Fatalf("args %s: PaneMessages.Send was reached before validation", args)
		}
	}
}

func TestExecuteSessionMessage_WhenNowRequiresTokenID(t *testing.T) {
	messages := &fakePaneMessagesExec{}
	cap := sessionMessageCapability(messages)
	args := json.RawMessage(`{"sessionId":"s","text":"hi","when":"now","id":"m1"}`)
	if _, err := executeSessionMessage(context.Background(), cap, args); err == nil {
		t.Fatal(`executeSessionMessage succeeded with when "now" and no tokenId`)
	}
	if len(messages.sendCalls) != 0 {
		t.Fatal("PaneMessages.Send was reached with when \"now\" and no tokenId")
	}
}

func TestExecuteSessionMessage_SendsTheParsedRequestAndRendersTheResult(t *testing.T) {
	messages := &fakePaneMessagesExec{sendResult: MessageView{ID: "m1", Namespace: "caller", Phase: PhaseSubmitted}}
	cap := sessionMessageCapability(messages)
	out, err := executeSessionMessage(context.Background(), cap, json.RawMessage(`{"sessionId":"worker-1","text":"hello","when":"free","id":"m1"}`))
	if err != nil {
		t.Fatalf("executeSessionMessage: %v", err)
	}
	if len(messages.sendCalls) != 1 {
		t.Fatalf("PaneMessages.Send calls = %d, want 1", len(messages.sendCalls))
	}
	got := messages.sendCalls[0]
	if got.sessionID != "worker-1" || got.text != "hello" || got.when != "free" || got.id != "m1" || got.tokenID != "" {
		t.Fatalf("PaneMessages.Send request = %+v, want sessionId worker-1 text hello when free id m1 tokenId \"\"", got)
	}
	var decoded sessionMessageResultWire
	if unmarshalErr := json.Unmarshal([]byte(out), &decoded); unmarshalErr != nil {
		t.Fatalf("decode result: %v", unmarshalErr)
	}
	if decoded.SessionID != "worker-1" || decoded.ID != "m1" || decoded.Namespace != "caller" || decoded.Phase != "submitted" {
		t.Fatalf("result = %+v, want sessionId worker-1 id m1 namespace caller phase submitted", decoded)
	}
}

func TestExecuteSessionMessage_WhenNowPassesTokenIDThrough(t *testing.T) {
	messages := &fakePaneMessagesExec{sendResult: MessageView{ID: "m1", Namespace: "caller", Phase: PhaseWritten}}
	cap := sessionMessageCapability(messages)
	args := json.RawMessage(`{"sessionId":"worker-1","text":"hello","when":"now","id":"m1","tokenId":"tok-1"}`)
	if _, err := executeSessionMessage(context.Background(), cap, args); err != nil {
		t.Fatalf("executeSessionMessage: %v", err)
	}
	if len(messages.sendCalls) != 1 || messages.sendCalls[0].tokenID != "tok-1" {
		t.Fatalf("PaneMessages.Send request = %+v, want tokenId tok-1", messages.sendCalls)
	}
}

func TestExecuteSessionMessage_CancelFormRoutesToCancelNotSend(t *testing.T) {
	messages := &fakePaneMessagesExec{cancelResult: CancelResult{Result: "cancelled", Phase: PhaseCancelled}}
	cap := sessionMessageCapability(messages)
	out, err := executeSessionMessage(context.Background(), cap, json.RawMessage(`{"sessionId":"worker-1","cancel":"m1"}`))
	if err != nil {
		t.Fatalf("executeSessionMessage: %v", err)
	}
	if len(messages.sendCalls) != 0 {
		t.Fatal("the cancel form reached PaneMessages.Send")
	}
	if len(messages.cancelCalls) != 1 || messages.cancelCalls[0].sessionID != "worker-1" || messages.cancelCalls[0].id != "m1" {
		t.Fatalf("PaneMessages.Cancel calls = %+v, want one for worker-1/m1", messages.cancelCalls)
	}
	var decoded sessionMessageCancelResultWire
	if unmarshalErr := json.Unmarshal([]byte(out), &decoded); unmarshalErr != nil {
		t.Fatalf("decode result: %v", unmarshalErr)
	}
	if decoded.Result != "cancelled" || decoded.Phase != "cancelled" {
		t.Fatalf("result = %+v, want {cancelled, cancelled}", decoded)
	}
}

func TestExecuteSessionMessage_CancelNoSuchMessageCarriesNoPhase(t *testing.T) {
	messages := &fakePaneMessagesExec{cancelResult: CancelResult{Result: "no_such_message"}}
	cap := sessionMessageCapability(messages)
	out, err := executeSessionMessage(context.Background(), cap, json.RawMessage(`{"sessionId":"worker-1","cancel":"never-sent"}`))
	if err != nil {
		t.Fatalf("executeSessionMessage: %v", err)
	}
	var decoded map[string]json.RawMessage
	if unmarshalErr := json.Unmarshal([]byte(out), &decoded); unmarshalErr != nil {
		t.Fatalf("decode result: %v", unmarshalErr)
	}
	if _, hasPhase := decoded["phase"]; hasPhase {
		t.Fatalf("result %s carries a phase for no_such_message, want none", out)
	}
	if string(decoded["result"]) != `"no_such_message"` {
		t.Fatalf("result = %s, want \"no_such_message\"", decoded["result"])
	}
}

// ── contract tests (AGENTS.md rule 5) ──────────────────────────────────────

func sessionMessageResultSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile("../../contracts/tools/session.message.schema.json")
	if err != nil {
		t.Fatalf("read the contract: %v", err)
	}
	var doc struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if unmarshalErr := json.Unmarshal(raw, &doc); unmarshalErr != nil {
		t.Fatalf("parse the contract: %v", unmarshalErr)
	}
	compiler := jsonschema.NewCompiler()
	resource, err := jsonschema.UnmarshalJSON(strings.NewReader(string(doc.Defs["result"])))
	if err != nil {
		t.Fatalf("read $defs/result: %v", err)
	}
	if addErr := compiler.AddResource("session.message.result.json", resource); addErr != nil {
		t.Fatalf("add the result schema: %v", addErr)
	}
	schema, err := compiler.Compile("session.message.result.json")
	if err != nil {
		t.Fatalf("compile the result schema: %v", err)
	}
	return schema
}

// TestSessionMessage_DTOConformsToContract is the Go-struct half of
// AGENTS.md rule 5, for both disjoint forms.
func TestSessionMessage_DTOConformsToContract(t *testing.T) {
	schema := sessionMessageResultSchema(t)
	cases := []any{
		sessionMessageResultWire{SessionID: "worker-1", ID: "m1", Namespace: "caller", Phase: "submitted"},
		sessionMessageResultWire{SessionID: "worker-1", ID: "m1", Namespace: "caller", Phase: "partial", BoxContents: "half a message"},
		sessionMessageCancelResultWire{Result: "cancelled", Phase: "cancelled"},
		sessionMessageCancelResultWire{Result: "too_late", Phase: "pasting"},
		sessionMessageCancelResultWire{Result: "no_such_message"},
	}
	for _, out := range cases {
		encoded, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("marshal DTO %+v: %v", out, err)
		}
		var value any
		if unmarshalErr := json.Unmarshal(encoded, &value); unmarshalErr != nil {
			t.Fatalf("decode DTO json: %v", unmarshalErr)
		}
		if validateErr := schema.Validate(value); validateErr != nil {
			t.Fatalf("session.message result %s does not satisfy its own contract: %v", encoded, validateErr)
		}
	}
}

// TestSessionMessage_OverTheWireConformsToContract is rule 5's third check
// — the real result off the real dispatcher, for both the send and the
// cancel form — not a payload the test itself built.
func TestSessionMessage_OverTheWireConformsToContract(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	dispatcher, err := NewToolDispatcher(reg, &fakeWorkerRecord{}, content.EnvironmentIDFor(content.EnvLocal, ""))
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	schema := sessionMessageResultSchema(t)

	sendMessages := &fakePaneMessagesExec{sendResult: MessageView{ID: "m1", Namespace: "caller", Phase: PhaseQueued}}
	sendInvocation := ToolInvocation{
		Context: context.Background(),
		Method:  "session.message",
		RunContext: agenttools.RunContext{
			RunID: "run-1", Session: "session-1",
			PaneAccess:      "fake-pane-access",
			SessionMessages: sendMessages,
		},
		Grant:     workerGrantForTest(),
		RawParams: json.RawMessage(`{"sessionId":"worker-session-1","text":"hello","when":"free","id":"m1"}`),
	}
	sendOut, dispatchErr := dispatcher.Dispatch(sendInvocation)
	if dispatchErr != nil {
		t.Fatalf("dispatch session.message (send): %v", dispatchErr)
	}
	var sendValue any
	if unmarshalErr := json.Unmarshal([]byte(sendOut), &sendValue); unmarshalErr != nil {
		t.Fatalf("decode send result: %v", unmarshalErr)
	}
	if validateErr := schema.Validate(sendValue); validateErr != nil {
		t.Fatalf("session.message send result off the real dispatcher does not satisfy its contract: %v\npayload: %s", validateErr, sendOut)
	}

	cancelMessages := &fakePaneMessagesExec{cancelResult: CancelResult{Result: "cancelled", Phase: PhaseCancelled}}
	cancelInvocation := ToolInvocation{
		Context: context.Background(),
		Method:  "session.message",
		RunContext: agenttools.RunContext{
			RunID: "run-1", Session: "session-1",
			PaneAccess:      "fake-pane-access",
			SessionMessages: cancelMessages,
		},
		Grant:     workerGrantForTest(),
		RawParams: json.RawMessage(`{"sessionId":"worker-session-1","cancel":"m1"}`),
	}
	cancelOut, dispatchErr := dispatcher.Dispatch(cancelInvocation)
	if dispatchErr != nil {
		t.Fatalf("dispatch session.message (cancel): %v", dispatchErr)
	}
	var cancelValue any
	if unmarshalErr := json.Unmarshal([]byte(cancelOut), &cancelValue); unmarshalErr != nil {
		t.Fatalf("decode cancel result: %v", unmarshalErr)
	}
	if validateErr := schema.Validate(cancelValue); validateErr != nil {
		t.Fatalf("session.message cancel result off the real dispatcher does not satisfy its contract: %v\npayload: %s", validateErr, cancelOut)
	}
}
