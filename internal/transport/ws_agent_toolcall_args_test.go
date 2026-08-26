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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// twoReadsProvider is the fake model: it reads one finished command, then
// another, then answers. One tool, one session, two different blocks — the
// exact shape the owner could not tell apart.
type twoReadsProvider struct {
	session  string
	first    string
	second   string
	requests int
	bodies   []string
	ready    chan struct{}
}

func (p *twoReadsProvider) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.bodies = append(p.bodies, string(body))
	p.requests++
	if p.requests == 3 {
		close(p.ready)
	}
	switch p.requests {
	case 1:
		streamToolCallChunk(w, "session.read", fmt.Sprintf(
			`{"sessionId":%q,"id":%q,"start":0,"count":1}`, p.session, p.first))
	case 2:
		streamToolCallChunk(w, "session.read", fmt.Sprintf(
			`{"sessionId":%q,"id":%q,"start":0,"count":1}`, p.session, p.second))
	default:
		streamAnswerChunk(w, "both of them")
	}
}

func TestAgentRunToolCall_ArgumentsOverTheWireConformToContract(t *testing.T) {
	schema := loadSchema(t, "agent.runToolCall.schema.json")

	prov := &twoReadsProvider{ready: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(prov.serve))
	defer srv.Close()

	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(autonomousPolicyStore(t)))
	h.createEndpointAt(srv.URL)

	seedPaneChain(t, h.db, blockPaneThis, blockPaneOther)
	sid, openErr := openSessionInPane(t, h.conn, blockPaneThis, 1)
	if openErr != nil {
		t.Fatalf("open in pane: %+v", openErr)
	}
	prov.session = sid
	waitPastMilli(sessionOpenedAt(t, h.ws, sid))
	prov.first = recordBlockWithBody(t, h.db, blockPaneThis, "df -h",
		"0198f2b0-0000-7000-8000-0000000000f1", "Filesystem  Size")
	prov.second = recordBlockWithBody(t, h.db, blockPaneThis, "du -sh .",
		"0198f2b0-0000-7000-8000-0000000000f2", "12G\t.")
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "0198f2b0-0000-7000-8000-0000000000fa",
		"sessionId": sid,
		"question":  "what did those two commands say?",
		"cwd":       "/repo",
		"attachedContent": []any{
			map[string]any{"itemId": prov.first, "command": "df -h", "state": "exited"},
			map[string]any{"itemId": prov.second, "command": "du -sh .", "state": "exited"},
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
	<-prov.ready
	if len(prov.bodies) < 3 {
		t.Fatalf("provider requests = %d, want initial prompt plus two tool-result requests", len(prov.bodies))
	}
	if !strings.Contains(prov.bodies[0], prov.first) || !strings.Contains(prov.bodies[0], prov.second) {
		t.Fatalf("initial model request omitted granted item ids: %s", prov.bodies[0])
	}
	if !strings.Contains(prov.bodies[1], "Filesystem  Size") || !strings.Contains(prov.bodies[2], "12G") {
		t.Fatalf("session.read tool results did not reach the model: %q / %q", prov.bodies[1], prov.bodies[2])
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
	if calls[0].Args["id"] != prov.first {
		t.Fatalf("first call args = %v, want id %q", calls[0].Args, prov.first)
	}
	if calls[1].Args["id"] != prov.second {
		t.Fatalf("second call args = %v, want id %q", calls[1].Args, prov.second)
	}
	if calls[0].Args["sessionId"] != sid {
		t.Fatalf("first call args = %v, want sessionId %q", calls[0].Args, sid)
	}
}

// A tool that takes no arguments is announced with `{}` and never with null:
// the wire declares an object and requires it, and absent and empty are the
// same sentence about a call. The paired end of the case above — a field
// asserted only in its populated state is a field nobody has checked
// (AGENTS.md testing rule 3).
func TestAgentRunToolCall_NoArgumentsIsAnEmptyObjectOverTheWire(t *testing.T) {
	schema := loadSchema(t, "agent.runToolCall.schema.json")
	client := &scriptedEventClient{events: []assistant.AskEvent{
		{Kind: assistant.AskToolCall, Call: &assistant.ToolCall{
			Tool: "git.status", CallID: "call_9", EntryID: "entry-action-9",
			Effect: content.EffectObserve,
		}},
		{Kind: assistant.AskAnswer, Text: "clean"},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
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
