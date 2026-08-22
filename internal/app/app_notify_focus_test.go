package app

// A banner click reaches the renderer through production wiring
// (nocx-jiwq.1, plan D1).
//
// The click's own half is the adapter's and is tested there
// (internal/notify/wailsadapter: the OS hands back a click, the adapter
// decodes the session id the banner carried and calls Focus). What could not
// be tested until now is the half after it: that the composition root has a
// channel to ask the renderer at all. Before this the seam existed and every
// click was discarded, which is exactly the shape AGENTS.md rule 2 names —
// each unit correct, the user's task impossible.

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// TestBannerClickReachesTheRendererAsSessionFocus drives the real composition
// root: boot it, open a session over the REAL WebSocket, hand the backend the
// session id a banner click carries, and watch session.focus arrive on the
// socket naming that session.
func TestBannerClickReachesTheRendererAsSessionFocus(t *testing.T) {
	storagetest.IsolateWithHome(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	if serr := a.Start(context.Background()); serr != nil {
		t.Fatalf("Start: %v", serr)
	}
	defer a.Shutdown(context.Background())

	conn, _, derr := (&websocket.Dialer{
		Subprotocols: []string{"nocx.token." + a.WSToken()},
	}).Dial("ws://127.0.0.1:"+strconv.Itoa(a.WSPort())+"/session", nil)
	if derr != nil {
		t.Fatalf("dial: %v", derr)
	}
	defer func() { _ = conn.Close() }()

	sid := openSessionForFocus(t, conn)

	// What the OS hands back when the user clicks the banner, as the adapter
	// decodes it: a session id and nothing else.
	a.FocusSession(sid)

	// ONE deadline for the whole wait, set once: a gorilla connection is
	// permanently failed by ANY read error, and reading it again panics.
	if derr := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); derr != nil {
		t.Fatalf("SetReadDeadline: %v — the wait would be unbounded", derr)
	}
	for {
		_, raw, rerr := conn.ReadMessage()
		if rerr != nil {
			t.Fatalf("a banner click never reached the renderer as session.focus: %v", rerr)
		}
		var notif struct {
			Method string `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &notif) != nil || notif.Method != "session.focus" {
			continue
		}
		if notif.Params.SessionID != sid {
			t.Fatalf("session.focus named %q, want the clicked session %q", notif.Params.SessionID, sid)
		}
		return
	}
}

// openSessionForFocus opens a session over the wire and returns its id. It
// reads past the notifications an open produces, the same way the launcher
// tests do.
func openSessionForFocus(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "open",
		"params": map[string]any{"cols": 80, "rows": 24},
	})
	if err != nil {
		t.Fatalf("marshal open: %v", err)
	}
	if werr := conn.WriteMessage(websocket.TextMessage, req); werr != nil {
		t.Fatalf("write open: %v", werr)
	}
	if derr := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); derr != nil {
		t.Fatalf("SetReadDeadline: %v", derr)
	}
	for {
		_, raw, rerr := conn.ReadMessage()
		if rerr != nil {
			t.Fatalf("read open response: %v", rerr)
		}
		var envelope struct {
			ID     *json.RawMessage `json:"id"`
			Result struct {
				SessionID string `json:"sessionId"`
			} `json:"result"`
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.ID == nil {
			continue // a notification arriving before the response
		}
		if envelope.Error != nil {
			t.Fatalf("open: %+v", envelope.Error)
		}
		if envelope.Result.SessionID == "" {
			t.Fatalf("open returned no session id: %s", raw)
		}
		return envelope.Result.SessionID
	}
}
