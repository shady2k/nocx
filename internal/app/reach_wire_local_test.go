//go:build nocx_local_ssh

package app

// The wire helpers two surviving app stands share: a WebSocket client for the
// transport server a test starts, and one JSON-RPC call read by id.
//
// They lived beside the launcher-reachability journeys, which drove an ssh pane
// through the coordinator's own dial. Those journeys are gone with that dial
// stack (nocx-oalo1.3.2) and these two helpers are what the remaining stands —
// the typed-`ssh` integration axis above all — still use. The build tag is the
// axis's own: their only callers are the live-typed-ssh stands, which exist
// only where the dial half does.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/transport"
)

func reachConnectWS(t *testing.T, ws *transport.WSServer) *websocket.Conn {
	t.Helper()
	u := url.URL{Scheme: "ws", Host: fmt.Sprintf("127.0.0.1:%d", ws.Port()), Path: "/session"}
	d := websocket.Dialer{Subprotocols: []string{"nocx.token." + ws.Token()}}
	conn, _, err := d.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func reachJSONRPCCall(t *testing.T, conn *websocket.Conn, method string, params any) json.RawMessage {
	t.Helper()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, resp, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var check struct {
			ID *json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(resp, &check)
		if check.ID == nil {
			continue // notification (exit, ack)
		}
		var idCheck struct {
			ID int `json:"id"`
		}
		_ = json.Unmarshal(resp, &idCheck)
		if idCheck.ID != 1 {
			continue
		}
		return resp
	}
}
