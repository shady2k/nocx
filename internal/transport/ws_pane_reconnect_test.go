package transport

// pane.reconnect's refusals and its registration (design §5.3, §5.6;
// nocx-oalo1.5.4).
//
// The success path is one open run again and is covered where the open is; what
// this file pins is the GATE: a press is answered only for a pane this
// connection opened, at the epoch that offered the button, while the offer
// stands. Each refusal is a different sentence because each is a different thing
// for a surface to do — a stale frame is dropped, a withdrawn offer is not
// pressed, and an unknown pane belongs to another window.

import (
	"encoding/json"
	"testing"
)

// bindTestPane gives the server a pane record for the one connection it has.
func bindTestPane(t *testing.T, ws *WSServer, conn *wsConn, paneID string) {
	t.Helper()
	ws.bindPane(paneID, conn, newConnState(), openParams{PaneID: paneID, Cols: 80, Rows: 24})
}

// publishTestConnection sends one pane.connection the driver would have sent.
func publishTestConnection(ws *WSServer, paneID string, epoch uint64, state string, offer *PaneConnectionReconnect) {
	ws.PublishPaneConnection(PaneConnection{
		PaneID:          paneID,
		ConnectionID:    "conn-1",
		ConnectionEpoch: epoch,
		InstanceID:      "instance-1",
		Transport:       PaneConnectionTransport{State: state, StateMs: 1000},
		Attempt:         PaneConnectionAttempt{Shown: "none", Candidate: "none"},
		Reconnect:       offer,
		ObservedAt:      "2026-11-14T07:00:00Z",
	})
}

// TestPaneReconnectRefusesAPaneThisConnectionNeverOpened: a pane is addressed by
// the window that opened it.
func TestPaneReconnectRefusesAPaneThisConnectionNeverOpened(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "pane.reconnect", map[string]any{
		"paneId": "pane-1", "connectionEpoch": 1,
	}, 1)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("answer = %+v, want -32602", resp)
	}
	if resp.Error.Message != "Invalid params: unknown paneId" {
		t.Fatalf("message = %q", resp.Error.Message)
	}
}

// TestPaneReconnectRefusesWhenNoOfferStands: a healthy connection has no
// button, so a press is about a state the backend never offered.
func TestPaneReconnectRefusesWhenNoOfferStands(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	bindTestPane(t, ws, wc, "pane-1")
	publishTestConnection(ws, "pane-1", 1, "connected", nil)

	resp := vaultCall(t, conn, "pane.reconnect", map[string]any{
		"paneId": "pane-1", "connectionEpoch": 1,
	}, 1)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("answer = %+v, want -32602", resp)
	}
	if resp.Error.Message != "Invalid params: no reconnect is offered for this pane" {
		t.Fatalf("message = %q", resp.Error.Message)
	}
}

// TestPaneReconnectRefusesAStaleEpoch: a second press racing a state change is
// about an observation the person is no longer looking at.
func TestPaneReconnectRefusesAStaleEpoch(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	bindTestPane(t, ws, wc, "pane-1")
	publishTestConnection(ws, "pane-1", 1, "stalled", nil)
	publishTestConnection(ws, "pane-1", 2, "stalled", &PaneConnectionReconnect{EndsShells: 1})

	resp := vaultCall(t, conn, "pane.reconnect", map[string]any{
		"paneId": "pane-1", "connectionEpoch": 1,
	}, 1)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("answer = %+v, want -32602", resp)
	}
	if resp.Error.Message != "Invalid params: stale connectionEpoch" {
		t.Fatalf("message = %q", resp.Error.Message)
	}
}

// TestPaneReconnectPassesTheGateAndRunsTheOneOpenPath: with the offer standing
// at the epoch, the press is carried out by the SAME open path `open` uses, and
// answers with the same result shape — a new session, not a second declaration
// of one.
func TestPaneReconnectPassesTheGateAndRunsTheOneOpenPath(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	bindTestPane(t, ws, wc, "pane-1")
	publishTestConnection(ws, "pane-1", 1, "stalled", &PaneConnectionReconnect{EndsShells: 2})

	resp := vaultCall(t, conn, "pane.reconnect", map[string]any{
		"paneId": "pane-1", "connectionEpoch": 1,
	}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	// The result is `open`'s, over the real socket.
	validateJSON(t, loadSchema(t, "open.schema.json"), resp.Result, "pane.reconnect result")
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.SessionID == "" {
		t.Fatal("the reconnect answered no session: the open path did not run")
	}
}

// TestPaneReconnectRefusesABadEpochShape is the params gate: both fields are
// required and the epoch names an observation, so zero is not one.
func TestPaneReconnectRefusesABadEpochShape(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	conn := connectWS(t, ws)

	for name, params := range map[string]map[string]any{
		"no epoch":   {"paneId": "pane-1"},
		"zero epoch": {"paneId": "pane-1", "connectionEpoch": 0},
		"no pane":    {"connectionEpoch": 1},
	} {
		t.Run(name, func(t *testing.T) {
			resp := vaultCall(t, conn, "pane.reconnect", params, 7)
			if resp.Error == nil || resp.Error.Code != -32602 {
				t.Fatalf("answer = %+v, want -32602", resp)
			}
		})
	}
}
