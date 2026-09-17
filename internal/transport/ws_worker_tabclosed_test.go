package transport

// workers.tabClosed — the notification a connected renderer learns a
// participant's tab has LEFT the window from (nocx-xn63t.4.6).
//
// WHY IT IS THE SPAWN SIDE'S NOTIFICATION READ THE OTHER WAY. workers.spawn
// mints a tab the renderer never asked for and tells every connected window
// with workers.tabCreated, because the window has no other way to learn of a
// row before its next layout.read. workers.close writes the reverse row change
// for the same reason and with the same audience: the close is a JSON-RPC call
// from a COORDINATOR, not from the window that is drawing the strip, and a
// window that is not told goes on drawing a tab with nothing behind it — the
// defect this notification exists to end.
//
// The tests below drive the real path: the same ws.OpenSession a worker's
// session is opened through, and a separate ordinary renderer connection as
// the audience, because a worker's own session has no subscriber to be one.

import (
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"
)

func TestWorkerTabClosed_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "workers.tabClosed.schema.json")
	validateJSON(t, schema, mustMarshal(workerTabClosedParams{
		TabID: tabID1, InstanceID: "inst-1",
	}), "workers.tabClosed DTO")
}

// TestWorkerTabClosed_OverTheWireConformsToContract reads the real frame off a
// real socket, and pins the instanceId to the server's OWN — the field the
// renderer compares against the session it already holds to drop a fact queued
// before a reconnect (the same check workers.tabCreated's instanceId gets).
func TestWorkerTabClosed_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "workers.tabClosed.schema.json")
	ws := newWorkerTabAnnounceWS(t)
	renderer := connectWS(t, ws)
	t.Cleanup(func() { _ = renderer.Close() })

	ws.AnnounceWorkerTabClosed(tabID1)

	raw := readNotification(t, renderer, "workers.tabClosed", wantWithin)
	validateJSON(t, schema, raw, "workers.tabClosed params (real socket)")

	var got workerTabClosedParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TabID != tabID1 {
		t.Errorf("tabId = %q, want %q", got.TabID, tabID1)
	}
	if want := string(ws.registry.InstanceID()); got.InstanceID != want {
		t.Errorf("instanceId = %q, want %q (the server's own instance)", got.InstanceID, want)
	}
}

// The close is a BROADCAST, on workers.tabCreated's own terms: every connected
// window draws the shared strip, and the coordinator that called the close is
// not necessarily one of them.
func TestWorkerTabClosed_BroadcastsToEveryConnection(t *testing.T) {
	ws := newWorkerTabAnnounceWS(t)
	first := connectWS(t, ws)
	second := connectWS(t, ws)
	t.Cleanup(func() { _ = first.Close() })
	t.Cleanup(func() { _ = second.Close() })

	ws.AnnounceWorkerTabClosed(tabID1)

	for i, conn := range []*websocket.Conn{first, second} {
		raw := readNotification(t, conn, "workers.tabClosed", wantWithin)
		var got workerTabClosedParams
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode connection %d: %v", i, err)
		}
		if got.TabID != tabID1 {
			t.Fatalf("connection %d learned of tab %q, want %q", i, got.TabID, tabID1)
		}
	}
}

// With no connection at all the close must not be threatened by the
// announcement: a coordinator closing a worker while nobody is looking is the
// ordinary case, and a tab close that failed because no window was there to
// hear about it would be the worst possible trade.
func TestWorkerTabClosed_WithNoConnectionDropsSilently(t *testing.T) {
	ws := newWorkerTabAnnounceWS(t)
	ws.AnnounceWorkerTabClosed(tabID1)
}
