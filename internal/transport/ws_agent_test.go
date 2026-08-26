package transport

// agent.ask over the real socket (nocx-p4f7r): the question carries
// renderer-supplied terminal-item grants, and the backend records the turn
// through a REAL content store.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

// ── harness ───────────────────────────────────────────────────────────────

// newAgentWSServer wires a WSServer over a REAL content store (the ledger
// methods are what is under test — a fake cannot answer them).
func newAgentWSServer(t *testing.T) (*WSServer, content.ContentDB, func()) {
	t.Helper()
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	ctx := context.Background()
	db, err := content.Open(ctx, content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithContentDB(db))
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, db, func() {
		_ = ws.Stop(ctx)
		_ = db.Close()
	}
}

type askWireResult struct {
	RunID     int64  `json:"runId"`
	EntryID   string `json:"entryId"`
	State     string `json:"state"`
	IngestSeq int64  `json:"ingestSeq"`
	Replayed  bool   `json:"replayed"`
	Model     string `json:"model"`
}

// askPaneID is the pane every ask in these tests is asked in — the harness
// creates it, because a turn is a block and a block names its pane
// (nocx-4em1z).
const askPaneID = "01930000-0000-7000-8000-0000000000a1"

// askPaneIn creates the layout chain a turn hangs on. A turn IS a block
// (nocx-4em1z), so agent.ask names the pane it was asked in and the FK is
// what makes that anchor real — an ask naming a pane nobody created is
// refused, in a harness exactly as in the product. One owner, because every
// ask harness needs the same chain.
func askPaneIn(t *testing.T, db content.ContentDB) {
	t.Helper()
	if _, err := db.Layout().CreateWorkspace(t.Context(),
		content.Workspace{ID: "ws-ask", Name: "ask"},
		content.Tab{ID: "tab-ask", WorkspaceID: "ws-ask", Position: 0, Layout: content.LayoutRow},
		content.Pane{ID: askPaneID, TabID: "tab-ask", Cwd: "/repo", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateWorkspace for the ask pane: %v", err)
	}
}

// askOverWire sends agent.ask and decodes the result.
func askOverWire(t *testing.T, conn *websocket.Conn, params map[string]any, id int) (askWireResult, *jsonrpcErrorObj) {
	t.Helper()
	if _, ok := params["attachedContent"]; !ok {
		params["attachedContent"] = []any{}
	}
	resp := jsonrpcCallWithID(t, conn, "agent.ask", params, id)
	var env struct {
		Result askWireResult    `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode ask response: %v\nraw: %s", err, resp)
	}
	return env.Result, env.Error
}

// ── unwired transport ─────────────────────────────────────────────────────
func TestAgentMethods_UnwiredAnswerMethodNotFound(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCallWithID(t, conn, "agent.ask", map[string]any{}, 1)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error == nil || env.Error.Code != -32601 {
		t.Fatalf("unwired agent.ask: err = %+v, want -32601", env.Error)
	}
}

func TestAgentAsk_RejectsBadParams(t *testing.T) {
	h := newAskHarness(t, &scriptedAssistantClient{deltas: []string{"x"}})
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	base := map[string]any{
		"askId":           "ask-1",
		"sessionId":       sid,
		"question":        "what does this mean?",
		"cwd":             "/repo",
		"attachedContent": []any{},
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unknown session", mutate: func(p map[string]any) { p["sessionId"] = "deadbeef" }},
		{name: "empty question", mutate: func(p map[string]any) { p["question"] = "" }},
		{name: "empty askId", mutate: func(p map[string]any) { p["askId"] = "" }},
		{name: "empty cwd", mutate: func(p map[string]any) { p["cwd"] = "" }},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := make(map[string]any, len(base))
			for key, value := range base {
				params[key] = value
			}
			params["askId"] = fmt.Sprintf("bad-ask-%d", i)
			tc.mutate(params)
			_, errObj := askOverWire(t, h.conn, params, i+2)
			if errObj == nil || errObj.Code != -32602 {
				t.Fatalf("error = %+v, want -32602", errObj)
			}
		})
	}
}

func TestAgentMethods_StoreFailureIsAnInternalError(t *testing.T) {
	ws, db, stop := newAgentWSServer(t)
	defer stop()
	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck
	sid := openLocalSession(t, conn)

	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}
	_, errObj := askOverWire(t, conn, map[string]any{
		"askId": "ask-closed-store", "sessionId": sid, "question": "q", "cwd": "/repo",
		"attachedContent": []any{},
	}, 2)
	if errObj == nil || errObj.Code != -32603 {
		t.Fatalf("ask against a closed store: err = %+v, want -32603", errObj)
	}
}
