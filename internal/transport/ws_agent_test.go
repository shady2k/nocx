package transport

// agent.captureFrame / agent.ask over the real socket (nocx-f4s5): the
// frame is ingested first and the ask transaction references it; the
// backend mints every id; params are validated and bounded at the wire;
// ownership of every id is checked. These tests drive the real methods
// through a REAL content store (the ledger is the store).

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// frameParams is one live 2x2 frame on the wire.
func frameParams(sessionID, captureID string) map[string]any {
	attrs := map[string]any{"fg": nil, "bg": nil}
	return map[string]any{
		"captureId": captureID,
		"sessionId": sessionID,
		"source":    "live",
		"rows": []any{
			map[string]any{"kind": "cells", "cells": []any{
				map[string]any{"char": "a", "attrs": attrs},
				map[string]any{"char": "b", "attrs": attrs},
			}},
			map[string]any{"kind": "cells", "cells": []any{
				map[string]any{"char": "c", "attrs": attrs},
				map[string]any{"char": "d", "attrs": attrs},
			}},
		},
		"cursor":   map[string]any{"line": 5, "col": 1},
		"identity": map[string]any{"buffer": map[string]any{"kind": "normal"}, "cols": 2, "rows": 24, "generation": 3},
		"range":    map[string]any{"start": 10, "end": 12},
		"cwd":      "/repo",
	}
}

// captureFrameOverWire sends agent.captureFrame and decodes the result.
func captureFrameOverWire(t *testing.T, conn *websocket.Conn, params map[string]any, id int) (string, *jsonrpcErrorObj) {
	t.Helper()
	resp := jsonrpcCallWithID(t, conn, "agent.captureFrame", params, id)
	var env struct {
		Result struct {
			FrameID string `json:"frameId"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode captureFrame response: %v\nraw: %s", err, resp)
	}
	return env.Result.FrameID, env.Error
}

type askWireResult struct {
	RunID         int64  `json:"runId"`
	QuestionID    string `json:"questionId"`
	AnswerEntryID string `json:"answerEntryId"`
	State         string `json:"state"`
	IngestSeq     int64  `json:"ingestSeq"`
	Replayed      bool   `json:"replayed"`
}

// askOverWire sends agent.ask and decodes the result.
func askOverWire(t *testing.T, conn *websocket.Conn, params map[string]any, id int) (askWireResult, *jsonrpcErrorObj) {
	t.Helper()
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

// ── the happy path ────────────────────────────────────────────────────────

// The renderer ingests the frame FIRST and gets a backend-minted id back;
// the ask references it and gets a backend-minted run id with the run in
// state prepared. The full loop works end to end off the real socket.
func TestAgentCaptureFrameAndAsk_OverTheWire(t *testing.T) {
	h := newAskHarness(t, &scriptedAssistantClient{deltas: []string{"hi"}})
	h.createEndpoint()
	conn := h.conn
	sid := openLocalSession(t, conn)
	db := h.db

	frameID, errObj := captureFrameOverWire(t, conn, frameParams(sid, "cap-1"), 1)
	if errObj != nil {
		t.Fatalf("captureFrame error: %+v", errObj)
	}
	if frameID == "" {
		t.Fatal("captureFrame returned an empty frame id")
	}

	res, errObj := askOverWire(t, conn, map[string]any{
		"askId":     "ask-1",
		"sessionId": sid,
		"question":  "what does this screen mean?",
		"cwd":       "/repo",
		"references": []any{
			map[string]any{"frameId": frameID, "region": map[string]any{"rowStart": 0, "rowEnd": 2}},
		},
	}, 2)
	if errObj != nil {
		t.Fatalf("ask error: %+v", errObj)
	}
	if res.RunID == 0 || res.QuestionID == "" || res.AnswerEntryID == "" {
		t.Fatalf("ask result = %+v, want run/question/answer ids", res)
	}
	if res.State != "prepared" {
		t.Errorf("run state = %q, want prepared at the response", res.State)
	}
	if res.IngestSeq == 0 {
		t.Errorf("ingest_seq = 0, want the backend-assigned order")
	}
	if res.Replayed {
		t.Errorf("first ask reported replayed")
	}

	// The engine streamed the stub's answer and the run terminalized.
	raw := readNotification(t, conn, "agent.runState", 5*time.Second)
	var st struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("runState unmarshal: %v", err)
	}
	if st.State != "completed" {
		t.Errorf("runState = %q, want completed", st.State)
	}

	// The frame, the question and the answer landed in the one ledger,
	// joined by the references and caused-by edges (read the store directly
	// — the wire answer is not evidence of what was stored).
	led := db.Ledger()
	ctx := context.Background()
	q, err := led.Entry(ctx, res.QuestionID)
	if err != nil || q == nil {
		t.Fatalf("question entry: %v (err %v)", q, err)
	}
	if len(q.Executions) != 1 {
		t.Fatalf("question executions = %d, want exactly one run", len(q.Executions))
	}
	if q.Executions[0].State == nil || *q.Executions[0].State != content.RunCompleted {
		t.Errorf("stored run state = %v, want completed", q.Executions[0].State)
	}
	edges, err := led.Edges(ctx, res.QuestionID)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	foundRef := false
	for _, e := range edges {
		if e.To == frameID && e.Rel == content.RelReferences {
			foundRef = true
		}
	}
	if !foundRef {
		t.Errorf("edges = %+v, want a references edge to %s", edges, frameID)
	}
}

// Idempotency off the wire: a lost response retries with the same
// captureId / askId and gets the ORIGINAL ids back — nothing duplicated.
func TestAgentCaptureFrameAndAsk_ReplayOffTheWire(t *testing.T) {
	h := newAskHarness(t, &scriptedAssistantClient{deltas: []string{"hi"}})
	h.createEndpoint()
	conn := h.conn
	sid := openLocalSession(t, conn)
	db := h.db

	f1, errObj := captureFrameOverWire(t, conn, frameParams(sid, "cap-1"), 1)
	if errObj != nil {
		t.Fatalf("first captureFrame: %+v", errObj)
	}
	f2, errObj := captureFrameOverWire(t, conn, frameParams(sid, "cap-1"), 2)
	if errObj != nil {
		t.Fatalf("replay captureFrame: %+v", errObj)
	}
	if f2 != f1 {
		t.Errorf("replay frame id = %q, want %q", f2, f1)
	}

	ask := map[string]any{
		"askId":     "ask-1",
		"sessionId": sid,
		"question":  "what does this screen mean?",
		"cwd":       "/repo",
		"references": []any{
			map[string]any{"frameId": f1, "region": map[string]any{"rowStart": 0, "rowEnd": 2}},
		},
	}
	r1, errObj := askOverWire(t, conn, ask, 3)
	if errObj != nil {
		t.Fatalf("first ask: %+v", errObj)
	}
	r2, errObj := askOverWire(t, conn, ask, 4)
	if errObj != nil {
		t.Fatalf("replay ask: %+v", errObj)
	}
	if r2.RunID != r1.RunID {
		t.Errorf("replay run id = %d, want %d", r2.RunID, r1.RunID)
	}
	if !r2.Replayed {
		t.Errorf("replay ask did not report replayed")
	}

	// Exactly one question entry exists.
	entries, err := db.Ledger().ListEntries(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	var questions int
	for _, e := range entries {
		if e.Intent == "what does this screen mean?" {
			questions++
		}
	}
	if questions != 1 {
		t.Errorf("question entries = %d, want exactly 1 after a replay", questions)
	}
}

// ── validation at the wire ────────────────────────────────────────────────

func TestAgentCaptureFrame_RejectsBadParams(t *testing.T) {
	ws, _, stop := newAgentWSServer(t)
	defer stop()
	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck
	sid := openLocalSession(t, conn)

	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown session", func(p map[string]any) { p["sessionId"] = "deadbeef" }},
		{"bad source", func(p map[string]any) { p["source"] = "hologram" }},
		{"missing cursor on live", func(p map[string]any) { p["cursor"] = nil }},
		{"frozen with cursor", func(p map[string]any) { p["source"] = "frozen"; p["serializerVersion"] = 1 }},
		{"missing identity on live", func(p map[string]any) { p["identity"] = nil }},
		{"missing range on live", func(p map[string]any) { p["range"] = nil }},
		{"range not spanning rows", func(p map[string]any) { p["range"] = map[string]any{"start": 10, "end": 13} }},
		{"negative identity cols", func(p map[string]any) {
			p["identity"] = map[string]any{"buffer": map[string]any{"kind": "normal"}, "cols": -1, "rows": 24, "generation": 1}
		}},
		{"oversized cols", func(p map[string]any) {
			p["identity"] = map[string]any{"buffer": map[string]any{"kind": "normal"}, "cols": maxFrameCols + 1, "rows": 24, "generation": 1}
		}},
		{"cell count not matching cols", func(p map[string]any) {
			p["rows"] = []any{map[string]any{"kind": "cells", "cells": []any{map[string]any{"char": "a", "attrs": map[string]any{}}}}}
		}},
		{"empty captureId", func(p map[string]any) { p["captureId"] = "" }},
		{"empty cwd", func(p map[string]any) { p["cwd"] = "" }},
		{"alternate buffer without altSession", func(p map[string]any) {
			p["identity"] = map[string]any{"buffer": map[string]any{"kind": "alternate"}, "cols": 2, "rows": 24, "generation": 1}
		}},
		{"cursor line beyond the buffer ceiling", func(p map[string]any) {
			p["cursor"] = map[string]any{"line": maxFrameRows + 24, "col": 1}
		}},
	}
	for i, tc := range cases {
		p := frameParams(sid, "cap-"+fmt.Sprint(i))
		tc.mutate(p)
		_, errObj := captureFrameOverWire(t, conn, p, i+1)
		if errObj == nil {
			t.Errorf("%s: accepted, want -32602", tc.name)
			continue
		}
		if errObj.Code != -32602 {
			t.Errorf("%s: error code = %d, want -32602 (message %s)", tc.name, errObj.Code, errObj.Message)
		}
	}
}

// An oversized frame (over the character budget) is refused at the wire,
// never truncated.
func TestAgentCaptureFrame_RejectsOversizedFrame(t *testing.T) {
	ws, _, stop := newAgentWSServer(t)
	defer stop()
	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck
	sid := openLocalSession(t, conn)

	// maxFrameRows+1 rows of one cell each: over the row bound.
	rows := make([]any, 0, maxFrameRows+1)
	cell := map[string]any{"char": "x", "attrs": map[string]any{"fg": nil, "bg": nil}}
	for i := 0; i < maxFrameRows+1; i++ {
		rows = append(rows, map[string]any{"kind": "cells", "cells": []any{cell}})
	}
	p := frameParams(sid, "cap-big")
	p["rows"] = rows
	p["range"] = map[string]any{"start": 0, "end": maxFrameRows + 1}
	p["identity"] = map[string]any{"buffer": map[string]any{"kind": "normal"}, "cols": 1, "rows": maxFrameRows + 1, "generation": 1}
	_, errObj := captureFrameOverWire(t, conn, p, 1)
	if errObj == nil || errObj.Code != -32602 {
		t.Fatalf("oversized frame: err = %+v, want -32602", errObj)
	}
}

func TestAgentAsk_RejectsBadParams(t *testing.T) {
	h := newAskHarness(t, &scriptedAssistantClient{deltas: []string{"x"}})
	h.createEndpoint()
	conn := h.conn
	sid := openLocalSession(t, conn)
	frameID, errObj := captureFrameOverWire(t, conn, frameParams(sid, "cap-1"), 1)
	if errObj != nil {
		t.Fatalf("captureFrame: %+v", errObj)
	}
	ref := map[string]any{"frameId": frameID, "region": map[string]any{"rowStart": 0, "rowEnd": 2}}

	base := map[string]any{
		"askId":      "ask-1",
		"sessionId":  sid,
		"question":   "what does this screen mean?",
		"cwd":        "/repo",
		"references": []any{ref},
	}

	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown session", func(p map[string]any) { p["sessionId"] = "deadbeef" }},
		{"empty question", func(p map[string]any) { p["question"] = "" }},
		{"empty askId", func(p map[string]any) { p["askId"] = "" }},
		{"empty cwd", func(p map[string]any) { p["cwd"] = "" }},
		{"region rows out of bounds", func(p map[string]any) {
			p["references"] = []any{map[string]any{"frameId": frameID, "region": map[string]any{"rowStart": 0, "rowEnd": 5000}}}
		}},
		{"negative region row", func(p map[string]any) {
			p["references"] = []any{map[string]any{"frameId": frameID, "region": map[string]any{"rowStart": -1, "rowEnd": 2}}}
		}},
		{"column pair without start", func(p map[string]any) {
			p["references"] = []any{map[string]any{"frameId": frameID, "region": map[string]any{"rowStart": 0, "rowEnd": 1, "colEnd": 2}}}
		}},
		{"unknown frame id", func(p map[string]any) {
			p["references"] = []any{map[string]any{"frameId": "no-such-frame", "region": map[string]any{"rowStart": 0, "rowEnd": 2}}}
		}},
	}
	for i, tc := range cases {
		p := make(map[string]any)
		for k, v := range base {
			p[k] = v
		}
		p["askId"] = fmt.Sprintf("ask-%d", i+1)
		tc.mutate(p)
		_, errObj := askOverWire(t, conn, p, i+2)
		if errObj == nil {
			t.Errorf("%s: accepted, want -32602", tc.name)
			continue
		}
		if errObj.Code != -32602 {
			t.Errorf("%s: error code = %d, want -32602 (message %s)", tc.name, errObj.Code, errObj.Message)
		}
	}
}

// A frame captured from another session is rejected — the ownership check
// is against the stored session, not against what the renderer claims.
func TestAgentAsk_RejectsFrameFromAnotherSession(t *testing.T) {
	h := newAskHarness(t, &scriptedAssistantClient{deltas: []string{"x"}})
	h.createEndpoint()
	conn := h.conn
	sidA := openLocalSession(t, conn)
	sidB := openLocalSession(t, conn)

	frameID, errObj := captureFrameOverWire(t, conn, frameParams(sidA, "cap-1"), 1)
	if errObj != nil {
		t.Fatalf("captureFrame: %+v", errObj)
	}
	_, errObj = askOverWire(t, conn, map[string]any{
		"askId":     "ask-1",
		"sessionId": sidB,
		"question":  "what does this screen mean?",
		"cwd":       "/repo",
		"references": []any{
			map[string]any{"frameId": frameID, "region": map[string]any{"rowStart": 0, "rowEnd": 2}},
		},
	}, 2)
	if errObj == nil || errObj.Code != -32602 {
		t.Fatalf("cross-session ask: err = %+v, want -32602", errObj)
	}
	if !strings.Contains(errObj.Message, "another session") {
		t.Errorf("error message = %q, want it to name the session ownership", errObj.Message)
	}
}

// A region beyond the STORED frame's rows is refused by the store — the
// wire pre-check bounds it, the ledger checks it against what it holds.
func TestAgentAsk_RejectsRegionBeyondStoredFrame(t *testing.T) {
	h := newAskHarness(t, &scriptedAssistantClient{deltas: []string{"x"}})
	h.createEndpoint()
	conn := h.conn
	sid := openLocalSession(t, conn)
	frameID, errObj := captureFrameOverWire(t, conn, frameParams(sid, "cap-1"), 1)
	if errObj != nil {
		t.Fatalf("captureFrame: %+v", errObj)
	}

	// The frame holds 2 rows; a region over row 2 must be refused (the
	// wire bound allows up to maxFrameRows, so only the store can catch it).
	_, errObj = askOverWire(t, conn, map[string]any{
		"askId":     "ask-1",
		"sessionId": sid,
		"question":  "what does this screen mean?",
		"cwd":       "/repo",
		"references": []any{
			map[string]any{"frameId": frameID, "region": map[string]any{"rowStart": 1, "rowEnd": 5}},
		},
	}, 2)
	if errObj == nil || errObj.Code != -32602 {
		t.Fatalf("out-of-bounds region: err = %+v, want -32602", errObj)
	}
}

// With no content store wired, the methods answer -32601 — a dead surface
// never pretends to work.
func TestAgentMethods_UnwiredAnswerMethodNotFound(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCallWithID(t, conn, "agent.captureFrame", map[string]any{}, 1)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error == nil || env.Error.Code != -32601 {
		t.Fatalf("unwired captureFrame: err = %+v, want -32601", env.Error)
	}
}

// An EXTERNAL ledger failure (the store is closed underneath the server)
// surfaces as an internal error — the wire never pretends a capture or an
// ask succeeded.
func TestAgentMethods_StoreFailureIsAnInternalError(t *testing.T) {
	ws, db, stop := newAgentWSServer(t)
	defer stop()
	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck
	sid := openLocalSession(t, conn)

	// Close the store under the server: every ledger write now fails.
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	_, errObj := captureFrameOverWire(t, conn, frameParams(sid, "cap-1"), 1)
	if errObj == nil || errObj.Code != -32603 {
		t.Fatalf("captureFrame against a closed store: err = %+v, want -32603", errObj)
	}
	_, errObj = askOverWire(t, conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "q", "cwd": "/repo",
		"references": []any{map[string]any{"frameId": "x", "region": map[string]any{"rowStart": 0, "rowEnd": 1}}},
	}, 2)
	if errObj == nil || errObj.Code != -32603 {
		t.Fatalf("ask against a closed store: err = %+v, want -32603", errObj)
	}
}
