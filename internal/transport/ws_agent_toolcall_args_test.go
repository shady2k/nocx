package transport

// The arguments reach the renderer, and they are what tells two calls apart
// (ADR-0040).
//
// THE DEFECT THIS FILE EXISTS FOR. A turn announced four calls and three of
// them read as the same three words, because the announcement carried the
// tool and the backend's DERIVED RESOURCE — and for every session-scoped
// tool the resource is the pane. Two `blocks.read` of two different finished
// commands were indistinguishable. So the wire now carries `args`, and the
// check that matters is not that the field exists: it is that two calls of
// ONE tool, on ONE session, differ.
//
// Off the real socket and through the real engine, deliberately (AGENTS.md
// testing rule 5): the fake here is the PROVIDER, so the arguments travel the
// path they travel in production — the model emits them, the policy
// middleware validates them against the tool's own schema, and what is
// announced is the validated object the tool ran on. A test that built the
// notification itself would prove the struct is well formed and nothing about
// what the server sends.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

func TestAgentRunToolCall_ArgumentsOverTheWireConformToContract(t *testing.T) {
	schema := loadSchema(t, "agent.runToolCall.schema.json")

	firstResult, secondResult := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "second")
	// The connector serializes the script only when the ask starts, after the ledger IDs are known.
	var first, second string
	script := protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepTool, Name: "session.read", Arguments: map[string]any{"id": first, "start": 0, "count": 1}, ResultFile: firstResult},
		{Kind: protocol.StepTool, Name: "session.read", Arguments: map[string]any{"id": second, "start": 0, "count": 1}, ResultFile: secondResult},
		{Kind: protocol.StepAnswer, Text: "both of them"},
		{Kind: protocol.StepEnd, Model: "fixture-model"},
	}}
	h := newAskHarnessWithOpts(t, script, WithAgentPolicy(autonomousPolicyStore(t)))
	seedPaneChain(t, h.db, blockPaneThis, blockPaneOther)
	sid, openErr := openSessionInPane(t, h.conn, blockPaneThis, 1)
	if openErr != nil {
		t.Fatalf("open in pane: %+v", openErr)
	}
	waitPastMilli(sessionOpenedAt(t, h.ws, sid))
	first = recordBlockWithBody(t, h.db, blockPaneThis, "df -h",
		"0198f2b0-0000-7000-8000-0000000000f1", "Filesystem  Size")
	second = recordBlockWithBody(t, h.db, blockPaneThis, "du -sh .",
		"0198f2b0-0000-7000-8000-0000000000f2", "12G\t.")
	script.Steps[0].Arguments["id"] = first
	script.Steps[1].Arguments["id"] = second
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "0198f2b0-0000-7000-8000-0000000000fa",
		"sessionId": sid,
		"question":  "what did those two commands say?",
		"cwd":       "/repo",
		"attachedContent": []any{
			map[string]any{"itemId": first, "command": "df -h", "state": "exited"},
			map[string]any{"itemId": second, "command": "du -sh .", "state": "exited"},
		},
	}, 2); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	// One announcement per call, in the order the calls happened.
	type announced struct {
		Tool     string         `json:"tool"`
		Args     map[string]any `json:"args"`
		Resource *struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"resource"`
	}
	var calls []announced
	for i := 0; i < 2; i++ {
		raw := readNotification(t, h.conn, "agent.runToolCall", 15*time.Second)
		validateJSON(t, schema, raw, "agent.runToolCall params (real socket)")
		var got announced
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v\nraw: %s", err, raw)
		}
		calls = append(calls, got)
	}
	if !strings.Contains(h.prompt().Text, first) || !strings.Contains(h.prompt().Text, second) {
		t.Fatalf("initial prompt omitted granted item ids: %+v", h.prompt())
	}
	firstBody := string(h.agent.read(firstResult))
	secondBody := string(h.agent.read(secondResult))
	if !strings.Contains(firstBody, "Filesystem  Size") || !strings.Contains(secondBody, "12G") {
		t.Fatalf("session.read results did not reach agent: %q / %q", firstBody, secondBody)
	}
	// The tool and the resource are IDENTICAL — which is the whole reason
	// the announcement had to grow the arguments. Asserted rather than
	// assumed: if these ever differ, this test stops being the case the
	// epic was written about.
	if calls[0].Tool != "session.read" || calls[1].Tool != "session.read" {
		t.Fatalf("tools = %q and %q, want two session.read calls", calls[0].Tool, calls[1].Tool)
	}
	if calls[0].Resource == nil || calls[1].Resource == nil {
		t.Fatalf("a call named no resource: %+v / %+v", calls[0].Resource, calls[1].Resource)
	}
	if *calls[0].Resource != *calls[1].Resource {
		t.Fatalf("the two calls named different resources (%+v / %+v); this test needs the case where they do not",
			calls[0].Resource, calls[1].Resource)
	}

	// …and the arguments tell them apart, with the value the model really
	// sent and the tool really ran on.
	if calls[0].Args["id"] != first {
		t.Fatalf("first call args = %v, want id %q", calls[0].Args, first)
	}
	if calls[1].Args["id"] != second {
		t.Fatalf("second call args = %v, want id %q", calls[1].Args, second)
	}
	if calls[0].Args["sessionId"] != nil || calls[1].Args["sessionId"] != nil {
		t.Fatalf("session.read calls carried model sessionId arguments: %v / %v", calls[0].Args, calls[1].Args)
	}
}

// A tool that takes no arguments is announced with `{}` and never with null:
// the wire declares an object and requires it, and absent and empty are the
// same sentence about a call. The paired end of the case above — a field
// asserted only in its populated state is a field nobody has checked
// (AGENTS.md testing rule 3).
func TestAgentRunToolCall_NoArgumentsIsAnEmptyObjectOverTheWire(t *testing.T) {
	schema := loadSchema(t, "agent.runToolCall.schema.json")
	source := &recordingScreenCapture{body: screenCaptureBody(t, 0, "clean")}
	h := newAskHarnessWithOpts(t, bridgeToolScript("session.read", map[string]any{}, "clean"), WithAgentPolicy(autonomousPolicyStore(t)), WithScreenCaptureSource(source))
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "is the tree clean?", "cwd": "/repo",
	}, 1); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.runToolCall", 5*time.Second)
	validateJSON(t, schema, raw, "agent.runToolCall params, no arguments (real socket)")
	if !strings.Contains(string(raw), `"args":{}`) {
		t.Fatalf("a call with no arguments was not announced as {}: %s", raw)
	}
}
