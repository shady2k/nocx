package transport

// session.focus — the backend asking the renderer to bring a session's pane
// to the front (nocx-jiwq.1, plan D1).
//
// The test that matters is the one driving the real push through the real
// socket: a test validating a payload it built itself proves the struct is
// well-formed, not that the server sends it.

import (
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
)

// newFocusWS builds a started WSServer over a stub-backed registry with one
// connected client. No option wires this seam: FocusSession is a push, not a
// method, and it needs nothing but the session's subscriber.
func newFocusWS(t *testing.T) (*WSServer, *websocket.Conn) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return ws, conn
}

func TestSessionFocus_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.focus.schema.json")
	ws, conn := newFocusWS(t)
	sid := openSessionOnConn(t, ws, conn, 1)

	ws.FocusSession(sid)

	params := readNotification(t, conn, "session.focus", wantWithin)
	validateJSON(t, schema, params, "session.focus params")
	var got sessionFocusParams
	if err := json.Unmarshal(params, &got); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if got.SessionID != sid {
		t.Errorf("sessionId = %q, want %q", got.SessionID, sid)
	}
}

// The push carries a session id and NOTHING else (D1). A tab id here would be
// a second addressing identity no part of the backend can own, so the absence
// is asserted rather than assumed — additionalProperties: false makes the
// schema say it, and this says it off the socket.
func TestSessionFocus_CarriesTheSessionIdAndNothingElse(t *testing.T) {
	ws, conn := newFocusWS(t)
	sid := openSessionOnConn(t, ws, conn, 1)

	ws.FocusSession(sid)

	params := readNotification(t, conn, "session.focus", wantWithin)
	var fields map[string]any
	if err := json.Unmarshal(params, &fields); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if len(fields) != 1 {
		t.Fatalf("session.focus carries %v, want sessionId alone", fields)
	}
}

// With no renderer holding the session the push is DROPPED — without error
// and without blocking. A click cannot be honoured by a renderer that is not
// there, and stalling the sink that carries it to pretend otherwise would be
// worse.
//
// Absence is asserted with a sentinel rather than a duration (AGENTS.md: a
// test may not depend on timing): the unknown-session push is followed by a
// real one, and the first frame to arrive proves the first push sent nothing.
func TestSessionFocus_WithNoPaneHoldingTheSessionSendsNothing(t *testing.T) {
	ws, conn := newFocusWS(t)
	sid := openSessionOnConn(t, ws, conn, 1)

	ws.FocusSession("session-nobody-holds")
	ws.FocusSession(sid)

	params := readNotification(t, conn, "session.focus", wantWithin)
	var got sessionFocusParams
	if err := json.Unmarshal(params, &got); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if got.SessionID != sid {
		t.Fatalf("first session.focus named %q, want %q — the unknown session was pushed", got.SessionID, sid)
	}
}

// An empty session id is not addressing at all: it must not be pushed, and
// must not panic. Same sentinel shape as above.
func TestSessionFocus_IgnoresAnEmptySessionId(t *testing.T) {
	ws, conn := newFocusWS(t)
	sid := openSessionOnConn(t, ws, conn, 1)

	ws.FocusSession("")
	ws.FocusSession(sid)

	params := readNotification(t, conn, "session.focus", wantWithin)
	var got sessionFocusParams
	if err := json.Unmarshal(params, &got); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if got.SessionID != sid {
		t.Fatalf("first session.focus named %q, want %q — the empty id was pushed", got.SessionID, sid)
	}
}

func TestSessionFocus_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.focus.schema.json")
	raw, err := json.Marshal(sessionFocusParams{SessionID: "s-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "session.focus DTO")
}
