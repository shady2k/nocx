package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
)

type grantPromptClient struct {
	seen chan assistant.AskParams
}

func (*grantPromptClient) Probe(context.Context, assistant.ProbeParams) (assistant.ProbeResult, error) {
	return assistant.ProbeResult{OK: true, Model: "test-model"}, nil
}

func (*grantPromptClient) Discard(string) {}

func (c *grantPromptClient) Ask(_ context.Context, p assistant.AskParams, onEvent func(assistant.AskEvent) error) error {
	c.seen <- p
	return onEvent(assistant.AskEvent{Kind: assistant.AskAnswer, Text: "answer"})
}

func TestAgentAsk_GrantsAreNamedWithoutInlining_OverTheWire(t *testing.T) {
	client := &grantPromptClient{seen: make(chan assistant.AskParams, 1)}
	h := newAskHarness(t, client)
	h.createEndpoint()
	askPaneIn(t, h.db)
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
	sid := opened.Result.SessionID
	first := recordBlockWithBody(t, h.db, askPaneID, "git status", "artifact-grant-1", "first block output")
	second := recordBlockWithBody(t, h.db, askPaneID, "npm test", "artifact-grant-2", "second block output")
	third := "cleared-item"
	_, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "ask-grants",
		"sessionId": sid,
		"question":  "what happened?",
		"cwd":       "/repo",
		"attachedContent": []any{
			map[string]any{"itemId": first, "command": "git status", "state": "exited"},
			map[string]any{"itemId": second, "command": "npm test", "state": "running"},
			map[string]any{"itemId": third, "command": "make ci", "state": "exited"},
		},
	}, 1)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}

	params := <-client.seen
	if len(params.Messages) == 0 || params.Messages[0].Role != "system" {
		t.Fatalf("engine messages = %+v, want a standing system prompt", params.Messages)
	}
	prompt := params.Messages[0].Content
	for _, want := range []string{first, second, third, "git status", "npm test", "make ci", "state: exited", "state: running", "session.read"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "first block output") || strings.Contains(prompt, "second block output") {
		t.Fatalf("system prompt inlined marked output:\n%s", prompt)
	}
	if !strings.Contains(prompt, "id: "+third) || !strings.Contains(prompt, "command: make ci") {
		t.Fatalf("system prompt dropped the missing granted item:\n%s", prompt)
	}
	for _, itemID := range []string{first, second} {
		item, readErr := h.ws.ReadSessionItem(t.Context(), sid, itemID, 0, 200)
		if readErr != nil {
			t.Fatalf("session.read %s: %v", itemID, readErr)
		}
		if item.ID != itemID {
			t.Fatalf("session.read returned id %q, want %q", item.ID, itemID)
		}
	}
	if _, readErr := h.ws.ReadSessionItem(t.Context(), sid, third, 0, 200); !errors.Is(readErr, assistant.ErrSessionItemNotFound) {
		t.Fatalf("cleared item error = %v, want assistant.ErrSessionItemNotFound", readErr)
	}
}

func TestAgentAskAttachedContentWindowValidation(t *testing.T) {
	base := func(window string) agentAskParams {
		return agentAskParams{
			AskID:           "ask-window",
			SessionID:       "session-a",
			Question:        "q",
			Cwd:             "/repo",
			AttachedContent: json.RawMessage(`[{"itemId":"item-1","command":"git status","state":"exited",` + window + `}]`),
		}
	}

	valid, attached, reason := validateAgentAsk(base(`"start":2,"count":4`))
	if reason != "" || len(attached) != 1 || attached[0].Start == nil || *attached[0].Start != 2 ||
		attached[0].Count == nil || *attached[0].Count != 4 {
		t.Fatalf("valid row window = %+v, reason %q", attached, reason)
	}
	if valid.ID == "" {
		t.Fatal("valid ask was not returned")
	}
	deep, deepAttached, deepReason := validateAgentAsk(base(`"start":5000,"count":10`))
	if deepReason != "" || deep.ID == "" || len(deepAttached) != 1 {
		t.Fatalf("deep row window = %+v, reason %q", deepAttached, deepReason)
	}
	if deepAttached[0].Start == nil || *deepAttached[0].Start != 5000 ||
		deepAttached[0].Count == nil || *deepAttached[0].Count != 10 {
		t.Fatalf("deep row window metadata = %+v", deepAttached[0])
	}

	for _, tc := range []struct {
		name   string
		window string
	}{
		{name: "negative start", window: `"start":-1,"count":4`},
		{name: "zero count", window: `"start":2,"count":0`},
		{name: "oversized count", window: `"start":2,"count":1000001`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, reason := validateAgentAsk(base(tc.window))
			if reason == "" || !strings.Contains(reason, "attachedContent[0]") {
				t.Fatalf("reason = %q, want attached window refusal", reason)
			}
		})
	}
}

func TestAgentAsk_RejectsMalformedAttachedContent(t *testing.T) {
	cases := []struct {
		name string
		list any
		want string
	}{
		{name: "wrong element type", list: []any{"item-1"}, want: "attachedContent"},
		{name: "missing item id", list: []any{map[string]any{"command": "git status", "state": "exited"}}, want: "itemId"},
		{name: "missing command", list: []any{map[string]any{"itemId": "item-1", "state": "exited"}}, want: "command"},
		{name: "invalid state", list: []any{map[string]any{"itemId": "item-1", "command": "git status", "state": "done"}}, want: "state"},
		{name: "unknown field", list: []any{map[string]any{"itemId": "item-1", "command": "git status", "state": "exited", "extra": true}}, want: "attachedContent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newAskHarness(t, &grantPromptClient{seen: make(chan assistant.AskParams, 1)})
			sid := openLocalSession(t, h.conn)
			_, errObj := askOverWire(t, h.conn, map[string]any{
				"askId": "malformed-" + tc.name, "sessionId": sid, "question": "q", "cwd": "/repo",
				"attachedContent": tc.list,
			}, 1)
			if errObj == nil || errObj.Code != -32602 || !strings.Contains(errObj.Message, tc.want) {
				t.Fatalf("error = %+v, want -32602 message containing %q", errObj, tc.want)
			}
		})
	}
}

func TestAgentAsk_RejectsLegacyReferencesField(t *testing.T) {
	h := newAskHarness(t, &grantPromptClient{seen: make(chan assistant.AskParams, 1)})
	sid := openLocalSession(t, h.conn)
	_, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "legacy-references", "sessionId": sid, "question": "q", "cwd": "/repo",
		"references": []any{}, "attachedContent": []any{},
	}, 1)
	if errObj == nil || errObj.Code != -32602 || !strings.Contains(errObj.Message, "references") {
		t.Fatalf("error = %+v, want legacy references refusal", errObj)
	}
}

type missingGrantProvider struct {
	session   string
	missingID string
}

func (p *missingGrantProvider) serve(w http.ResponseWriter, _ *http.Request) {
	streamToolCallChunk(w, "session.read", fmt.Sprintf(
		`{"sessionId":%q,"id":%q,"start":0,"count":20}`, p.session, p.missingID))
}

func TestAgentAsk_MissingGrantedItemTerminalizesWithReason(t *testing.T) {
	const missingID = "0198f2b0-0000-7000-8000-0000000000ff"
	provider := &missingGrantProvider{missingID: missingID}
	server := httptest.NewServer(http.HandlerFunc(provider.serve))
	defer server.Close()

	h := newAskHarnessWithOpts(t, mustClient(t), WithAgentPolicy(autonomousPolicyStore(t)))
	h.createEndpointAt(server.URL)
	seedPaneChain(t, h.db, blockPaneThis, blockPaneOther)
	sid, err := openSessionInPane(t, h.conn, blockPaneThis, 1)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	provider.session = sid
	waitPastMilli(sessionOpenedAt(t, h.ws, sid))

	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "0198f2b0-0000-7000-8000-0000000000fa",
		"sessionId": sid,
		"question":  "what did the cleared command do?",
		"cwd":       "/repo",
		"attachedContent": []any{
			map[string]any{"itemId": missingID, "command": "git status", "state": "exited"},
		},
	}, 2); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	readNotification(t, h.conn, "agent.runToolCall", 15*time.Second)
	raw := readNotification(t, h.conn, "agent.runState", 15*time.Second)
	var state struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("runState: %v\nraw: %s", err, raw)
	}
	if state.State != "failed" {
		t.Fatalf("runState = %#v, want failed after missing session item", state)
	}
	if !strings.Contains(state.Error, "session.read") || !strings.Contains(state.Error, "no such item") {
		t.Fatalf("runState error = %q, want the deliberate missing-item reason", state.Error)
	}
}
