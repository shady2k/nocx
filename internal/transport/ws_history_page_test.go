package transport

// session.historyPage's contract tests (nocx-zg3k3.10.3), in the house's
// three shapes:
//
//   - the $defs guard: the carrier document's row vocabulary is the frame
//     contract's, COPIED — and the copy is kept identical to the original by
//     this test rather than by trusting either hand (one rows vocabulary,
//     one encoder, one cell model).
//   - the DTO conformance: what the handler builds against the schema.
//   - the over-the-wire conformance: the REAL method through the REAL
//     socket, with the carrier document read off the wire BEFORE the result
//     that names it, and a client paging backwards across two calls with
//     every row delivered exactly once.
//
// The pager in the wire test is a scripted stand behaving exactly as the
// seam's contract states (the helper's own page read is proven against the
// real emulator in internal/sessionruntime and internal/helper/session):
// what this file proves is the transport half — registration, validation,
// the carrier's ordering, the id correlation, the paging shape a renderer
// consumes — and none of it against a payload the test built.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// TestSessionHistoryPageRows_DefsMatchTheFrameContract keeps the copied
// $defs identical to contracts/session.frame.schema.json's.
func TestSessionHistoryPageRows_DefsMatchTheFrameContract(t *testing.T) {
	readDefs := func(name string) map[string]json.RawMessage {
		raw, err := os.ReadFile(filepath.Join(contractDir, name)) //nolint:gosec // the path is contractDir, the directory every schema reader here uses
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var doc struct {
			Defs map[string]json.RawMessage `json:"$defs"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return doc.Defs
	}
	frame := readDefs("session.frame.schema.json")
	rows := readDefs("session.historyPageRows.schema.json")
	// The rows document needs exactly the frame's vocabulary: row, mark,
	// run, style, color — one rows vocabulary on both planes.
	for _, name := range []string{"row", "mark", "run", "style", "color"} {
		f, ok := frame[name]
		if !ok {
			t.Fatalf("session.frame.schema.json has no $defs/%s", name)
		}
		got, ok := rows[name]
		if !ok {
			t.Fatalf("session.historyPageRows.schema.json lost $defs/%s", name)
		}
		if compactJSON(f) != compactJSON(got) {
			t.Fatalf("$defs/%s drifted from the frame contract's:\n frame: %s\n  page: %s", name, compactJSON(f), compactJSON(got))
		}
	}
	if len(rows) != len(frame) {
		t.Fatalf("the rows document declares %d defs, want the frame's %d", len(rows), len(frame))
	}
}

func compactJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// TestSessionHistoryPage_DTOConformsToContract: the result DTO as the
// handler builds it — a full page, and the empty page below a floor.
func TestSessionHistoryPage_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.historyPage.schema.json")
	raw, err := json.Marshal(sessionHistoryPage{
		PageID: "0123456789abcdef0123456789abcdef",
		Start:  10, End: 40, Floor: 0, More: true,
		DurableThrough: nil,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "session.historyPage DTO")

	rawEmpty, err := json.Marshal(sessionHistoryPage{
		PageID: "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, rawEmpty, "session.historyPage empty DTO")
}

// TestSessionHistoryPageRows_DTOConformsToContract: the carrier document as
// the handler builds it.
func TestSessionHistoryPageRows_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.historyPageRows.schema.json")
	rows, err := sessionruntime.EncodeRows(testHistoryPageRows("L000010", "L000011"))
	if err != nil {
		t.Fatalf("encode rows: %v", err)
	}
	raw, err := json.Marshal(sessionHistoryPageRows{PageID: "0123456789abcdef0123456789abcdef", Rows: rows})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "session.historyPageRows DTO")
}

// testHistoryPageRows builds rows whose text is the given line, one cell
// each — the shape EncodeRows trims and encodes.
func testHistoryPageRows(lines ...string) []emulator.Row {
	rows := make([]emulator.Row, 0, len(lines))
	for _, line := range lines {
		cells := make([]emulator.Cell, 0, len(line))
		for _, r := range line {
			cells = append(cells, emulator.Cell{Grapheme: string(r), Width: emulator.WidthNarrow, HasText: true})
		}
		rows = append(rows, emulator.Row{Cells: cells})
	}
	return rows
}

// scriptedHistoryPager is the stand the wire test pages through: a history
// of forty rows, numbered 0..39, none pruned — the seam's own arithmetic,
// so the paging assertions below mean what they say.
type scriptedHistoryPager struct {
	sessionID string
	limits    []int
}

func (p *scriptedHistoryPager) HistoryPage(_ context.Context, sessionID string, before *uint64, limit int) (HistoryPage, error) {
	const head = 40
	p.sessionID = sessionID
	p.limits = append(p.limits, limit)
	end := uint64(head)
	if before != nil && *before < end {
		end = *before
	}
	start := uint64(0)
	if room := end - start; room > uint64(limit) { //nolint:gosec // a row count
		start = end - uint64(limit) //nolint:gosec // a row count
	}
	rows := make([]emulator.Row, 0, end-start)
	for i := start; i < end; i++ {
		rows = append(rows, testHistoryPageRows(fmt.Sprintf("L%06d", i))...)
	}
	encoded, err := sessionruntime.EncodeRows(rows)
	if err != nil {
		return HistoryPage{}, err
	}
	return HistoryPage{Start: start, End: end, Floor: 0, More: start > 0, Rows: encoded}, nil
}

// historyPageExchange writes one request and reads BOTH answers off the
// socket: the binary carrier document FIRST, then the result that names it.
// It is its own reader rather than jsonrpcCall because the carrier frame is
// binary, and the control plane's reader retains text and lets binary pass —
// the two planes' frames must be read in the order this test asserts.
func historyPageExchange(t *testing.T, conn *websocket.Conn, id int, params map[string]any) (docRaw []byte, resultRaw []byte) {
	t.Helper()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "session.historyPage", "params": params})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if werr := conn.WriteMessage(websocket.TextMessage, req); werr != nil {
		t.Fatalf("write request: %v", werr)
	}
	deadline := time.Now().Add(wantWithin)
	for docRaw == nil || resultRaw == nil {
		if !time.Now().Before(deadline) {
			t.Fatalf("the exchange never completed: doc=%v result=%v", docRaw != nil, resultRaw != nil)
		}
		_ = conn.SetReadDeadline(deadline)
		mt, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read the exchange: %v", err)
		}
		switch mt {
		case websocket.BinaryMessage:
			if resultRaw != nil {
				t.Fatal("a carrier document arrived AFTER the result that names it")
			}
			frame, err := DecodeFrame(msg)
			if err != nil {
				t.Fatalf("decode the carrier frame: %v", err)
			}
			if frame.MsgType != MsgTypeMetadata {
				t.Fatalf("the carrier rode msg-type %#x, want the screen plane's %#x", frame.MsgType, MsgTypeMetadata)
			}
			docRaw = frame.Payload
		case websocket.TextMessage:
			if isResponseTo(id)(msg) {
				resultRaw = msg
			}
			// A notification this exchange caused is not this test's to
			// consume; the test makes no further reads after its calls.
		}
	}
	return docRaw, resultRaw
}

// The real method through the real socket: a session opened over the stub
// registry, a page read, and then the page below it. The rows ride the
// carrier keyed by the id the result carries; the walk collects every row
// exactly once.
func TestSessionHistoryPage_OverTheWireConformsToContract(t *testing.T) {
	resultSchema := loadSchema(t, "session.historyPage.schema.json")
	rowsSchema := loadSchema(t, "session.historyPageRows.schema.json")

	pager := &scriptedHistoryPager{}
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithHistoryPager(pager))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	sid := openSessionOnConn(t, ws, conn, 1)

	decodeResult := func(t *testing.T, resp []byte, what string) sessionHistoryPage {
		t.Helper()
		var envelope struct {
			Result json.RawMessage  `json:"result"`
			Error  *jsonrpcErrorObj `json:"error"`
		}
		if err := json.Unmarshal(resp, &envelope); err != nil {
			t.Fatalf("unmarshal %s: %v\nraw: %s", what, err, resp)
		}
		if envelope.Error != nil {
			t.Fatalf("%s: %+v", what, envelope.Error)
		}
		validateJSON(t, resultSchema, envelope.Result, what)
		var got sessionHistoryPage
		if err := json.Unmarshal(envelope.Result, &got); err != nil {
			t.Fatalf("decode %s: %v", what, err)
		}
		return got
	}

	// The head page, over the wire.
	docRaw, resp := historyPageExchange(t, conn, 2, map[string]any{"sessionId": sid, "before": nil, "limit": 30})
	first := decodeResult(t, resp, "session.historyPage result (real socket)")
	validateJSON(t, rowsSchema, docRaw, "session.historyPageRows document (real socket)")
	var doc struct {
		PageID string `json:"pageId"`
		Rows   []struct {
			Text string `json:"text"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(docRaw, &doc); err != nil {
		t.Fatalf("decode the carrier document: %v\n%s", err, docRaw)
	}
	if doc.PageID != first.PageID {
		t.Fatalf("the carrier names page %q, the result names %q — the id is the correlation", doc.PageID, first.PageID)
	}
	if first.Start != 10 || first.End != 40 {
		t.Fatalf("the head page is [%d,%d), want [10,40)", first.Start, first.End)
	}
	if len(doc.Rows) != 30 {
		t.Fatalf("the carrier holds %d rows, want 30", len(doc.Rows))
	}
	for i, row := range doc.Rows {
		if want := fmt.Sprintf("L%06d", 10+i); row.Text != want {
			t.Fatalf("carrier row %d reads %q, want %q", i, row.Text, want)
		}
	}
	if !first.More {
		t.Fatal("the head page says no more, want true")
	}

	// The page below it, at the cursor the first answer named.
	docRaw2, resp2 := historyPageExchange(t, conn, 3, map[string]any{"sessionId": sid, "before": first.Start, "limit": 30})
	second := decodeResult(t, resp2, "session.historyPage result (real socket, second page)")
	validateJSON(t, rowsSchema, docRaw2, "session.historyPageRows document (real socket, second page)")
	var doc2 struct {
		PageID string `json:"pageId"`
		Rows   []struct {
			Text string `json:"text"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(docRaw2, &doc2); err != nil {
		t.Fatalf("decode the second carrier document: %v", err)
	}
	if second.End != first.Start {
		t.Fatalf("the second page ends at %d, want %d — no gap, no duplicate at the seam", second.End, first.Start)
	}
	if doc2.PageID == doc.PageID {
		t.Fatal("two pages carried one page id")
	}
	seen := map[string]int{}
	for _, row := range doc.Rows {
		seen[row.Text]++
	}
	for _, row := range doc2.Rows {
		seen[row.Text]++
	}
	if len(seen) != 40 {
		t.Fatalf("the walk collected %d distinct rows, want the stand's 40", len(seen))
	}
	for text, n := range seen {
		if n != 1 {
			t.Fatalf("row %q was delivered %d times, want exactly once", text, n)
		}
	}
	if pager.sessionID != sid {
		t.Fatalf("the pager was asked for %q, want the opened session %q", pager.sessionID, sid)
	}
	for _, limit := range pager.limits {
		if limit != 30 {
			t.Fatalf("the pager was asked for limit %d, want the params' 30", limit)
		}
	}
}

// The unwired pager leaves the method unregistered: -32601, the answer
// registration.go names for "the caller's next move is to stop asking".
func TestSessionHistoryPage_UnwiredPagerIsMethodNotFound(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "session.historyPage", map[string]any{
		"sessionId": strings.Repeat("0", 32), "before": nil, "limit": 10,
	})
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error == nil || envelope.Error.Code != -32601 {
		t.Fatalf("want -32601 for an unwired pager, got %+v", envelope.Error)
	}
}
