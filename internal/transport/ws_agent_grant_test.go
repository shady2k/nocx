package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/note"
	"github.com/shady2k/nocx/internal/snippet"
)

func TestAgentAsk_GrantsAreNamedWithoutInlining_OverTheWire(t *testing.T) {
	h := newAskHarness(t, answerScript("answer"))
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

	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	p := h.prompt()
	prompt := p.SystemPrompt + p.Text
	for _, want := range []string{first, second, third, "git status", "npm test", "make ci", "state: exited", "state: running", "session.read"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "first block output") || strings.Contains(prompt, "second block output") {
		t.Fatalf("system prompt inlined marked output:\n%s", prompt)
	}
	if !strings.Contains(prompt, "id: "+third) || !strings.Contains(prompt, `command: "make ci"`) {
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

func TestRunGrantForOffersContentTools(t *testing.T) {
	server := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithAgentPolicy(autonomousPolicyStore(t)))
	grant := server.runGrantFor("session-a")
	if grant == nil {
		t.Fatal("runGrantFor returned no grant")
	}
	reg, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	tools := reg.ForGrant(*grant)
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"notes.search", "snippets.list"} {
		if !names[want] {
			t.Fatalf("run grant offered tools %v, missing %q", names, want)
		}
	}
}

func TestAgentAskReceivesContentOperationsFromCompositionRoot(t *testing.T) {
	script := protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepTool, Name: "notes.create", Arguments: map[string]any{"body": "created through assistant operation"}},
		{Kind: protocol.StepTool, Name: "snippets.create", Arguments: map[string]any{"title": "Assistant", "body": "created through assistant operation"}},
		{Kind: protocol.StepAnswer, Text: "answer"},
		{Kind: protocol.StepEnd, Model: "test-model"},
	}}

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 9)
	}
	noteStore, err := note.Open(t.Context(), note.Config{
		Path: t.TempDir() + "/notes.db",
		Key:  key,
	})
	if err != nil {
		t.Fatalf("note.Open: %v", err)
	}
	t.Cleanup(func() { _ = noteStore.Close() })
	noteID := 0
	noteSvc := note.NewService(noteStore, func() string {
		noteID++
		return fmt.Sprintf("note-%d", noteID)
	}, nil)
	snippetID := 0
	snippetSvc := snippet.NewService(&memSnippetStore{existed: true}, func() string {
		snippetID++
		return fmt.Sprintf("snippet-%d", snippetID)
	})

	h := newAskHarnessWithOpts(t, script,
		WithAgentPolicy(autonomousPolicyStore(t)),
		WithNotes(noteSvc),
		WithSnippets(snippetSvc),
	)
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "ask-content-operations",
		"sessionId": sid,
		"question":  "what is here?",
		"cwd":       "/repo",
	}, 1); errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}

	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	notes, err := noteSvc.Search(t.Context(), "created through assistant operation")
	if err != nil || len(notes) != 1 {
		t.Fatalf("notes created via real bridge = %+v (%v)", notes, err)
	}
	snippets, err := snippetSvc.List()
	if err != nil || len(snippets) != 1 || snippets[0].Title != "Assistant" || snippets[0].Body != "created through assistant operation" {
		t.Fatalf("snippets via real bridge = %+v (%v)", snippets, err)
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

func TestAgentAsk_AcceptsAutomaticFrozenFrameProvenance(t *testing.T) {
	p := agentAskParams{
		AskID:           "ask-frame",
		SessionID:       "session-a",
		Question:        "what is on screen?",
		Cwd:             "/repo",
		AttachedContent: json.RawMessage(`[{"itemId":"attempt-top","command":"top","state":"running","automatic":true}]`),
	}
	_, attached, reason := validateAgentAsk(p)
	if reason != "" {
		t.Fatalf("automatic frozen frame refused: %q", reason)
	}
	if len(attached) != 1 || !attached[0].Automatic {
		t.Fatalf("attached = %+v, want one automatic frame", attached)
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
			h := newAskHarness(t, answerScript("answer"))
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
	h := newAskHarness(t, answerScript("answer"))
	sid := openLocalSession(t, h.conn)
	_, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "legacy-references", "sessionId": sid, "question": "q", "cwd": "/repo",
		"references": []any{}, "attachedContent": []any{},
	}, 1)
	if errObj == nil || errObj.Code != -32602 || !strings.Contains(errObj.Message, "references") {
		t.Fatalf("error = %+v, want legacy references refusal", errObj)
	}
}

func TestAgentAsk_MissingGrantedItemTerminalizesWithReason(t *testing.T) {
	const missingID = "0198f2b0-0000-7000-8000-0000000000ff"
	h := newAskHarnessWithOpts(t, approvalToolScript("session.read", map[string]any{"id": missingID, "start": 0, "count": 20}), WithAgentPolicy(autonomousPolicyStore(t)))
	seedPaneChain(t, h.db, blockPaneThis, blockPaneOther)
	sid, err := openSessionInPane(t, h.conn, blockPaneThis, 1)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	waitPastMilli(sessionOpenedAt(t, h.ws, sid))

	askResult, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "0198f2b0-0000-7000-8000-0000000000fa",
		"sessionId": sid,
		"question":  "what did the cleared command do?",
		"cwd":       "/repo",
		"attachedContent": []any{
			map[string]any{"itemId": missingID, "command": "git status", "state": "exited"},
		},
	}, 2)
	if errObj != nil {
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
	if !strings.Contains(state.Error, "tools/call") {
		t.Fatalf("runState error = %q, want safe bridge failure", state.Error)
	}
	entries, ledgerErr := h.db.Ledger().Caused(t.Context(), askResult.EntryID)
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	failedRead := false
	for _, caused := range entries {
		entry, err := h.db.Ledger().Entry(t.Context(), caused.EntryID)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Kind == content.EntryAction && entry.Intent == "session.read" && entry.Status == content.EntryFailure {
			failedRead = true
		}
	}
	if !failedRead {
		t.Fatal("missing-item read attempt was not recorded as failed")
	}
	if logs := h.agent.endpointLogs.String(); !strings.Contains(logs, "session.read: no such item") {
		t.Fatalf("missing-item reason absent from endpoint diagnostics: %s", logs)
	}
}
