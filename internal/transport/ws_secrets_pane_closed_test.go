package transport

// secrets.paneClosed and the capture scope (nocx-tsajw): a closed pane's pending
// credential dies with it, and only it. Every test here runs TWO panes on ONE
// connection, because that is the arrangement the defect lives in — the old
// connection-keyed destroy could not express "one of two panes closed" (it
// killed both) and could not express a history-record failure in one pane
// without killing the other's offers.
//
// The destruction key is (connection, pane): the pane id is renderer-minted
// and opaque, so a pane id from one connection must never reach another
// connection's captures — asserted over the real socket below, not only in
// the registry unit tests.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/testwait"
)

// recordOnPane records one command from one pane over the socket and decodes
// the ack — the "one pane submits" half of the two-panes-one-connection
// arrangement.
func recordOnPane(t *testing.T, conn *websocket.Conn, line, paneID string, id int) recordAck {
	t.Helper()
	resp := vaultCall(t, conn, "history.record", recordParams(map[string]any{
		"command": line,
		"paneId":  paneID,
	}), id)
	if resp.Error != nil {
		t.Fatalf("history.record (pane %s) error: %+v", paneID, resp.Error)
	}
	var ack recordAck
	if err := json.Unmarshal(resp.Result, &ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	return ack
}

// sendPaneClosed sends the secrets.paneClosed notification the renderer sends when a pane
// closes. The params are marshaled from the transport's own struct so the
// behavior test doubles as the over-the-wire conformance check: the shape the
// Go side declares is the shape the server acts on.
func sendPaneClosed(t *testing.T, conn *websocket.Conn, paneID string) {
	t.Helper()
	payload, err := json.Marshal(paneClosedParams{PaneID: paneID})
	if err != nil {
		t.Fatalf("marshal secrets.paneClosed params: %v", err)
	}
	frame := fmt.Sprintf(`{"jsonrpc":"2.0","method":"secrets.paneClosed","params":%s}`, payload)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write secrets.paneClosed: %v", err)
	}
}

// failOnCommandDB is a captureFakeDB whose RecordCompleted refuses one marker
// command — the history-record failure trigger, scoped to one pane's record so
// the other pane's record still lands. Ledger is overridden because the
// promoted one would answer the EMBEDDED fake, routing every record past this
// override.
type failOnCommandDB struct {
	*captureFakeDB
	failOn string
}

func (f *failOnCommandDB) Ledger() content.LedgerRepository { return f }

func (f *failOnCommandDB) RecordCompleted(ctx context.Context, in content.CompletedCommand) (string, error) {
	if strings.Contains(in.Intent, f.failOn) {
		return "", errors.New("store exploded (test)")
	}
	return f.captureFakeDB.RecordCompleted(ctx, in)
}

// saveCapture is the wire settlement attempt; it returns the JSON-RPC error
// code (0 when the save succeeded).
func saveCapture(t *testing.T, conn *websocket.Conn, captureID string, id int) int {
	t.Helper()
	resp := vaultCall(t, conn, "secrets.captureSave", map[string]any{"captureId": captureID}, id)
	if resp.Error == nil {
		return 0
	}
	return resp.Error.Code
}

// TestPaneClose_DestroysOnlyThatPanesCaptures: closing the first of two panes on
// one connection destroys ITS pending capture and leaves the second's intact —
// and a capture that was never saved or dismissed does not outlive its pane
// (the offer exists at the ack; the save after the close is refused).
func TestPaneClose_DestroysOnlyThatPanesCaptures(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0)
	db := newCaptureFakeDB()
	ws, _, stop := newCaptureWSServer(t, db, &clock)
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	ackA := recordOnPane(t, conn, "TOKEN=aaa-bbb-ccc-ddd-eee-fff-111", "pane-a", 1)
	ackB := recordOnPane(t, conn, "TOKEN=mmm-nnn-ooo-ppp-qqq-rrr-222", "pane-b", 2)
	if len(ackA.Captures) != 1 || len(ackB.Captures) != 1 {
		t.Fatalf("captures = %d/%d, want one offer per pane", len(ackA.Captures), len(ackB.Captures))
	}
	captureA, captureB := ackA.Captures[0].ID, ackB.Captures[0].ID

	// The closing event: pane-a dies, pane-b does not.
	sendPaneClosed(t, conn, "pane-a")

	if code := saveCapture(t, conn, captureA, 3); code != -32010 {
		t.Fatalf("save of pane-a's capture after its pane closed = code %d, want -32010 (capture unknown)", code)
	}
	if code := saveCapture(t, conn, captureB, 4); code != 0 {
		t.Fatalf("save of pane-b's capture after pane-a closed = code %d, want success", code)
	}
}

// TestPaneClose_OtherConnectionsPaneIdIsUntouchable: the pane identity is
// renderer-minted and opaque, so a secrets.paneClosed from ONE connection must not
// destroy the same-named pane's captures on ANOTHER connection — the pair key
// (connection, pane) is the authorization boundary, not the id.
func TestPaneClose_OtherConnectionsPaneIdIsUntouchable(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0)
	db := newCaptureFakeDB()
	ws, _, stop := newCaptureWSServer(t, db, &clock)
	defer stop()

	connA := connectWS(t, ws)
	defer func() { _ = connA.Close() }()
	connB := connectWS(t, ws)
	defer func() { _ = connB.Close() }()

	// Both connections hold a pane that calls itself "pane-1" — each renderer
	// mints its own ids, so this collision is exactly what the pair key
	// exists for.
	ackA := recordOnPane(t, connA, "TOKEN=aaa-bbb-ccc-ddd-eee-fff-333", "pane-1", 1)
	ackB := recordOnPane(t, connB, "TOKEN=mmm-nnn-ooo-ppp-qqq-rrr-444", "pane-1", 2)

	// connA closes ITS pane-1: connB's pane-1 must be untouched.
	sendPaneClosed(t, connA, "pane-1")

	if code := saveCapture(t, connA, ackA.Captures[0].ID, 3); code != -32010 {
		t.Fatalf("connA's capture after its own secrets.paneClosed = code %d, want -32010", code)
	}
	if code := saveCapture(t, connB, ackB.Captures[0].ID, 4); code != 0 {
		t.Fatalf("connB's same-named capture after connA's secrets.paneClosed = code %d, want success", code)
	}
}

// TestHistoryRecordFailure_DestroysOnlyThatPanesCaptures: a history-record
// failure in ONE pane destroys only that pane's pending captures — the other
// pane's offer on the same connection survives (the old connection-keyed
// destroy took both).
func TestHistoryRecordFailure_DestroysOnlyThatPanesCaptures(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0)
	db := &failOnCommandDB{captureFakeDB: newCaptureFakeDB(), failOn: "sudo rm -rf /"}
	ws, _, stop := newCaptureWSServer(t, db, &clock)
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	ackA := recordOnPane(t, conn, "TOKEN=aaa-bbb-ccc-ddd-eee-fff-555", "pane-a", 1)
	ackB := recordOnPane(t, conn, "TOKEN=mmm-nnn-ooo-ppp-qqq-rrr-666", "pane-b", 2)
	captureA, captureB := ackA.Captures[0].ID, ackB.Captures[0].ID

	// pane-a's record fails at the store; pane-a's offer dies with it.
	resp := vaultCall(t, conn, "history.record", recordParams(map[string]any{
		"command": "sudo rm -rf /", // the marker the failing store refuses
		"paneId":  "pane-a",
	}), 3)
	if resp.Error == nil || resp.Error.Code != -32603 {
		t.Fatalf("failing record error = %+v, want -32603", resp.Error)
	}

	if code := saveCapture(t, conn, captureA, 4); code != -32010 {
		t.Fatalf("pane-a's capture after its record failed = code %d, want -32010", code)
	}
	if code := saveCapture(t, conn, captureB, 5); code != 0 {
		t.Fatalf("pane-b's capture after pane-a's record failed = code %d, want success", code)
	}
}

// TestTransportDisconnect_DestroysEverythingOnTheConnection: the one
// destruction event that is genuinely connection-scoped. Both panes' captures
// die on the disconnect — asserted deliberately, not left to omission — and
// the assertion is on the registry (the socket is gone, so there is no wire
// to ask).
func TestTransportDisconnect_DestroysEverythingOnTheConnection(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0)
	db := newCaptureFakeDB()
	ws, caps, stop := newCaptureWSServerWithRegistry(t, db, &clock)
	defer stop()
	conn := connectWS(t, ws)
	ackA := recordOnPane(t, conn, "TOKEN=aaa-bbb-ccc-ddd-eee-fff-777", "pane-a", 1)
	ackB := recordOnPane(t, conn, "TOKEN=mmm-nnn-ooo-ppp-qqq-rrr-888", "pane-b", 2)
	captureA, captureB := ackA.Captures[0].ID, ackB.Captures[0].ID

	// The offers existed (the opening end of the invariant); the disconnect
	// is the closing end. The destroy is the statement after the
	// broadcast-set removal inside unregisterConn, so poll the set, then
	// poll the registry until the destroy is OBSERVABLE — Dismiss is the
	// probe because it never blocks: unknown means the capture is gone,
	// and a live probe dismisses the capture (a loud failure below, never
	// a hang on a saving capture).
	_ = conn.Close()
	var dismissErr error
	liveConns := func() int {
		ws.connsMu.Lock()
		defer ws.connsMu.Unlock()
		return len(ws.conns)
	}
	testwait.WaitForTimeoutDetail(t, "disconnect destroy to become observable", wantWithin,
		func() string {
			return fmt.Sprintf("not observable within %s: %d connection(s) still registered", wantWithin, liveConns())
		},
		func() bool {
			if liveConns() != 0 {
				return false
			}
			dismissErr = caps.Dismiss(credential.CaptureID(captureA))
			return true
		})
	if !errors.Is(dismissErr, credential.ErrCaptureUnknown) {
		t.Fatalf("disconnect destroy error = %v, want capture unknown", dismissErr)
	}

	for _, c := range []struct {
		id   string
		name string
	}{
		{captureA, "pane-a's capture"},
		{captureB, "pane-b's capture"},
	} {
		if _, err := caps.Reserve(credential.CaptureID(c.id)); !errors.Is(err, credential.ErrCaptureUnknown) {
			t.Fatalf("%s after transport disconnect = %v, want unknown", c.name, err)
		}
	}
}

// TestPaneClose_RejectsMalformedNotification: a secrets.paneClosed with no paneId or a
// non-object payload is refused by the validator before the handler; the
// capture stays pending (a notification has no response, so the assertion is
// that nothing was destroyed).
func TestPaneClose_RejectsMalformedNotification(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0)
	db := newCaptureFakeDB()
	ws, _, stop := newCaptureWSServer(t, db, &clock)
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	ack := recordOnPane(t, conn, "TOKEN=aaa-bbb-ccc-ddd-eee-fff-999", "pane-a", 1)
	capture := ack.Captures[0].ID

	for _, frame := range []string{
		`{"jsonrpc":"2.0","method":"secrets.paneClosed","params":{}}`,
		`{"jsonrpc":"2.0","method":"secrets.paneClosed","params":"not-an-object"}`,
	} {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatalf("write malformed secrets.paneClosed: %v", err)
		}
	}

	if code := saveCapture(t, conn, capture, 2); code != 0 {
		t.Fatalf("capture after malformed secrets.paneClosed = code %d, want success", code)
	}
}

// TestPaneClose_DTOConformsToContract pins the Go side of the wire shape: the
// struct the handler parses marshals to exactly what contracts/secrets.paneClosed
// declares (additionalProperties false, paneId required).
func TestPaneClose_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "secrets.paneClosed.schema.json")
	cases := map[string]paneClosedParams{
		"typical pane": {PaneID: "3f2a5c1e-8b0d-4e6a-9f2c-1d0b3e4a5f6a"},
		"minimal pane": {PaneID: "1"},
		"long pane id": {PaneID: strings.Repeat("x", 128)},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "secrets.paneClosed params DTO")
		})
	}
}
