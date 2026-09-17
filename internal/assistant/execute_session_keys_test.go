package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// fakePaneKeysExec is PaneKeys for the executor-level tests below: it
// records every request it was given and answers one scripted KeysResult.
// internal/app's real implementation cannot be imported here (app depends
// on assistant, not the reverse — the same layering PaneReader's own tests
// already work around with contractPaneReader in internal/toolendpoint).
type fakePaneKeysExec struct {
	calls  []KeysRequest
	result KeysResult
	err    error
}

func (f *fakePaneKeysExec) Send(_ context.Context, _ any, req KeysRequest) (KeysResult, error) {
	f.calls = append(f.calls, req)
	return f.result, f.err
}

func sessionKeysCapability(keys PaneKeys) *agenttools.SessionDescendantCapability {
	return &agenttools.SessionDescendantCapability{
		PaneAccess:  "fake-pane-access",
		SessionKeys: keys,
	}
}

func TestParseKey_ClosedVocabulary(t *testing.T) {
	valid := []string{
		"Enter", "Esc", "Tab", "BackTab", "Backspace", "Delete",
		"Up", "Down", "Left", "Right", "Home", "End", "PageUp", "PageDown",
		"Insert", "Space", "F1", "F12",
		"Ctrl+C", "Ctrl+a", "Alt+Up", "Alt+5", "Shift+Tab",
	}
	for _, s := range valid {
		if _, err := ParseKey(s); err != nil {
			t.Errorf("ParseKey(%q) = %v, want no error", s, err)
		}
	}
	invalid := []string{
		"", "down", "F13", "Ctrl+Up", "Ctrl+Enter", "Meta+A", "Ctrl+Shift+A", "A",
	}
	for _, s := range invalid {
		if _, err := ParseKey(s); err == nil {
			t.Errorf("ParseKey(%q) succeeded, want an error", s)
		}
	}
}

func TestExecuteSessionKeys_ExactlyOneOfKeyTextOptionRequired(t *testing.T) {
	cases := []string{
		`{"sessionId":"s","tokenId":"t"}`,
		`{"sessionId":"s","tokenId":"t","key":"Enter","text":"hi"}`,
		`{"sessionId":"s","tokenId":"t","key":"Enter","option":"Yes"}`,
	}
	for _, args := range cases {
		keys := &fakePaneKeysExec{}
		cap := sessionKeysCapability(keys)
		if _, err := executeSessionKeys(context.Background(), cap, json.RawMessage(args)); err == nil {
			t.Fatalf("args %s: executeSessionKeys succeeded, want an error", args)
		}
		if len(keys.calls) != 0 {
			t.Fatalf("args %s: PaneKeys.Send was reached before exactly-one validation", args)
		}
	}
}

func TestExecuteSessionKeys_UnknownKeyIsRejectedBeforeSend(t *testing.T) {
	keys := &fakePaneKeysExec{}
	cap := sessionKeysCapability(keys)
	_, err := executeSessionKeys(context.Background(), cap, json.RawMessage(`{"sessionId":"s","tokenId":"t","key":"Ctrl+Up"}`))
	if err == nil {
		t.Fatal("key Ctrl+Up: executeSessionKeys succeeded, want ParseKey to reject it")
	}
	if len(keys.calls) != 0 {
		t.Fatal("PaneKeys.Send was reached with a key outside design §4.2's vocabulary")
	}
}

func TestExecuteSessionKeys_NoDescendantAuthorityRefuses(t *testing.T) {
	cap := &agenttools.SessionDescendantCapability{}
	if _, err := executeSessionKeys(context.Background(), cap, json.RawMessage(`{"sessionId":"s","tokenId":"t","key":"Enter"}`)); err == nil {
		t.Fatal("executeSessionKeys succeeded with no PaneAccess/SessionKeys wired")
	}
}

func TestExecuteSessionKeys_SendsTheParsedRequestAndRendersTheResult(t *testing.T) {
	keys := &fakePaneKeysExec{result: KeysResult{State: "executed", BytesWritten: 1, Steps: 1}}
	cap := sessionKeysCapability(keys)
	out, err := executeSessionKeys(context.Background(), cap, json.RawMessage(`{"sessionId":"worker-1","tokenId":"tok-1","key":"Down"}`))
	if err != nil {
		t.Fatalf("executeSessionKeys: %v", err)
	}
	if len(keys.calls) != 1 {
		t.Fatalf("PaneKeys.Send calls = %d, want 1", len(keys.calls))
	}
	got := keys.calls[0]
	if got.SessionID != "worker-1" || got.TokenID != "tok-1" || got.Key == nil || *got.Key != "Down" {
		t.Fatalf("PaneKeys.Send request = %+v, want sessionId worker-1, tokenId tok-1, key Down", got)
	}
	var decoded sessionKeysResultWire
	if unmarshalErr := json.Unmarshal([]byte(out), &decoded); unmarshalErr != nil {
		t.Fatalf("decode result: %v", unmarshalErr)
	}
	if decoded.SessionID != "worker-1" || decoded.State != "executed" || decoded.BytesWritten != 1 || decoded.Steps != 1 {
		t.Fatalf("result = %+v, want sessionId worker-1 state executed bytesWritten 1 steps 1", decoded)
	}
}

func TestExecuteSessionKeys_TextAndOptionPassThroughUnexamined(t *testing.T) {
	// design §6.5: "decided in the helper at encode, surfaced here" — a
	// newline in text is never filtered client-side; it reaches PaneKeys
	// exactly as given, and whatever the helper answers is what is reported
	// (TestExecuteSessionKeys_RefusalIsRendered covers the would_submit
	// shape that answer takes).
	keys := &fakePaneKeysExec{result: KeysResult{State: "executed", BytesWritten: 5, Steps: 1}}
	cap := sessionKeysCapability(keys)
	raw, err := json.Marshal(map[string]string{
		"sessionId": "worker-1", "tokenId": "tok-1", "text": "hi\n",
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	if _, err := executeSessionKeys(context.Background(), cap, raw); err != nil {
		t.Fatalf("executeSessionKeys: %v", err)
	}
	if len(keys.calls) != 1 || keys.calls[0].Text == nil || *keys.calls[0].Text != "hi\n" {
		t.Fatalf("PaneKeys.Send request = %+v, want text %q untouched", keys.calls, "hi\n")
	}
}

func TestExecuteSessionKeys_RefusalIsRendered(t *testing.T) {
	keys := &fakePaneKeysExec{result: KeysResult{
		State:   "refused",
		Refusal: &proto.IntentRefusal{Cause: "stale_target", RegionNow: "menu text", RegionTruncated: true},
	}}
	cap := sessionKeysCapability(keys)
	out, err := executeSessionKeys(context.Background(), cap, json.RawMessage(`{"sessionId":"worker-1","tokenId":"tok-1","option":"Yes"}`))
	if err != nil {
		t.Fatalf("executeSessionKeys: %v", err)
	}
	var decoded sessionKeysResultWire
	if unmarshalErr := json.Unmarshal([]byte(out), &decoded); unmarshalErr != nil {
		t.Fatalf("decode result: %v", unmarshalErr)
	}
	if decoded.State != "refused" || decoded.Refusal == nil || decoded.Refusal.Cause != "stale_target" ||
		decoded.Refusal.RegionNow != "menu text" || !decoded.Refusal.RegionTruncated {
		t.Fatalf("result = %+v, want a rendered refusal carrying regionNow and regionTruncated", decoded)
	}
	if len(keys.calls) != 1 || keys.calls[0].Option == nil || *keys.calls[0].Option != "Yes" {
		t.Fatalf("PaneKeys.Send request = %+v, want option Yes", keys.calls)
	}
}

// ── contract tests (AGENTS.md rule 5) ──────────────────────────────────────

func sessionKeysResultSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile("../../contracts/tools/session.keys.schema.json")
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
	if addErr := compiler.AddResource("session.keys.result.json", resource); addErr != nil {
		t.Fatalf("add the result schema: %v", addErr)
	}
	schema, err := compiler.Compile("session.keys.result.json")
	if err != nil {
		t.Fatalf("compile the result schema: %v", err)
	}
	return schema
}

// TestSessionKeys_DTOConformsToContract is the Go-struct half of AGENTS.md
// rule 5: sessionKeysResultWire, filled with a refusal, marshals to
// something contracts/tools/session.keys.schema.json's own result shape
// accepts.
func TestSessionKeys_DTOConformsToContract(t *testing.T) {
	schema := sessionKeysResultSchema(t)
	out := sessionKeysResultWire{
		SessionID: "worker-1", State: "refused", BytesWritten: 0, Steps: 2,
		Refusal: &sessionKeysRefusalWire{Cause: "stale_target", RegionNow: "menu text", RegionTruncated: false},
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal DTO: %v", err)
	}
	var value any
	if unmarshalErr := json.Unmarshal(encoded, &value); unmarshalErr != nil {
		t.Fatalf("decode DTO json: %v", unmarshalErr)
	}
	if validateErr := schema.Validate(value); validateErr != nil {
		t.Fatalf("session.keys result does not satisfy its own contract: %v\npayload: %s", validateErr, encoded)
	}
}

// TestSessionKeys_OverTheWireConformsToContract is rule 5's third check —
// the real result off the real dispatcher (NewToolDispatcher, the same
// pipeline the tool endpoint and the kernel both narrow through: schema
// validation, the allowlist, Narrow, the executor map) — not a payload the
// test itself built. internal/toolendpoint's own contract_test.go drives
// the identical shape over the real JSON-RPC socket for session.read; that
// file is nocx-6q1uh.16's (the catalogue task), so this test stays at the
// dispatcher layer this package already owns rather than adding a case
// there.
func TestSessionKeys_OverTheWireConformsToContract(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	dispatcher, err := NewToolDispatcher(reg, &fakeWorkerRecord{}, content.EnvironmentIDFor(content.EnvLocal, ""))
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	keys := &fakePaneKeysExec{result: KeysResult{State: "executed", BytesWritten: 1, Steps: 1}}
	invocation := ToolInvocation{
		Context: context.Background(),
		Method:  "session.keys",
		RunContext: agenttools.RunContext{
			RunID: "run-1", Session: "session-1",
			PaneAccess:  "fake-pane-access",
			SessionKeys: keys,
		},
		Grant:     workerGrantForTest(),
		RawParams: json.RawMessage(`{"sessionId":"worker-session-1","tokenId":"tok-1","key":"Down"}`),
	}
	out, dispatchErr := dispatcher.Dispatch(invocation)
	if dispatchErr != nil {
		t.Fatalf("dispatch session.keys: %v", dispatchErr)
	}
	schema := sessionKeysResultSchema(t)
	var value any
	if unmarshalErr := json.Unmarshal([]byte(out), &value); unmarshalErr != nil {
		t.Fatalf("decode result: %v", unmarshalErr)
	}
	if validateErr := schema.Validate(value); validateErr != nil {
		t.Fatalf("session.keys result off the real dispatcher does not satisfy its contract: %v\npayload: %s", validateErr, out)
	}
}

// TestASequenceIsNotAccepted is design §4.2's own acceptance criterion: a
// key given as a list (`["Down","Enter"]`) is refused at schema validation
// — the schema's own `key` is a bare string, so this never reaches
// PaneKeys.Send at all.
func TestASequenceIsNotAccepted(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	dispatcher, err := NewToolDispatcher(reg, &fakeWorkerRecord{}, content.EnvironmentIDFor(content.EnvLocal, ""))
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	keys := &fakePaneKeysExec{}
	invocation := ToolInvocation{
		Context: context.Background(),
		Method:  "session.keys",
		RunContext: agenttools.RunContext{
			RunID: "run-1", Session: "session-1",
			PaneAccess:  "fake-pane-access",
			SessionKeys: keys,
		},
		Grant:     workerGrantForTest(),
		RawParams: json.RawMessage(`{"sessionId":"worker-session-1","tokenId":"tok-1","key":["Down","Enter"]}`),
	}
	_, dispatchErr := dispatcher.Dispatch(invocation)
	if !errors.Is(dispatchErr, ErrInvalidParams) {
		t.Fatalf("dispatch a sequence for key: err = %v, want ErrInvalidParams", dispatchErr)
	}
	if len(keys.calls) != 0 {
		t.Fatal("PaneKeys.Send was reached with a sequence")
	}
}
