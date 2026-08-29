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
		Subprotocols: []string{"nocx.token." + a.Transport.Token()},
	}).Dial("ws://127.0.0.1:"+strconv.Itoa(a.Transport.Port())+"/session", nil)
	if derr != nil {
		t.Fatalf("dial: %v", derr)
	}
	defer func() { _ = conn.Close() }()

	sid := openSessionForFocus(t, conn)
	// The open RESULT is not the moment this connection can be focused. The
	// open handler answers first and installs the session's subscriber only
	// afterwards, deliberately, because a session-scoped notification may not
	// precede the id that addresses it (AD-7). FocusSession looks that
	// subscriber up and drops the notification when it is not there yet — at
	// Debug, so the drop is silent and the wait below would simply time out.
	// Clicking before the subscriber exists is therefore a test that fails
	// under load and passes on an idle machine, which is what it did.
	awaitSubscriberFor(t, conn, sid)

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

// awaitSubscriberFor blocks until this connection has been published as sid's
// subscriber, and does it by observing the product rather than by waiting out
// a duration.
//
// The observable is any session-scoped notification naming sid. Every one of
// them resolves its destination at emit time from the session's CURRENT
// subscriber — the same lookup FocusSession makes — so one arriving here is
// not a proxy for the state the click needs, it is that state, reported by
// the mechanism that will carry the click.
//
// session.integrationChanged is what makes the wait terminate. A local
// session's integration status is registered by the pty factory, which is the
// only thing that knows which binary it exec'd, and it registers on every
// branch that returns a pty: the enhanced launch reports `starting`, the
// login shell with no local tier reports `conventional`, and a failed
// bootstrap reports `conventional` too. The open handler emits that
// registration through the subscriber it has just installed, unconditionally
// and synchronously. lifecycle.changed usually arrives first and is accepted
// just as happily, but it is NOT the thing waited on: it needs a lifecycle
// lane bound to the session, which only the enhanced branch has, so a session
// that degraded would wait for a fact nobody was going to send.
//
// internal/waittest is not used: it polls a predicate on a timer, and the
// predicate here is a blocking socket read. Polling one would mean a read
// deadline per attempt, and a gorilla connection is permanently failed by any
// read error, timeout included — the first poll that expired would destroy
// the connection the test still has to read session.focus from.
func awaitSubscriberFor(t *testing.T, conn *websocket.Conn, sid string) {
	t.Helper()
	// ONE deadline for the whole wait, for the reason given above.
	if derr := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); derr != nil {
		t.Fatalf("SetReadDeadline: %v — the wait would be unbounded", derr)
	}
	for {
		_, raw, rerr := conn.ReadMessage()
		if rerr != nil {
			t.Fatalf("the session never published a subscriber to click against: %v", rerr)
		}
		var notif struct {
			Method string `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &notif) != nil || notif.Method == "" {
			continue // a response, or a binary data frame
		}
		if notif.Params.SessionID == sid {
			return
		}
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
