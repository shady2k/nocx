package transport

import (
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/log"
)

func TestSandboxErrorsExposeStableReason(t *testing.T) {
	control := NewSandboxCoordinator(SandboxCoordinatorOptions{Context: t.Context()})
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithSandboxControl(control))
	if err := ws.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Stop(t.Context()) })
	conn, _, err := (&websocket.Dialer{Subprotocols: []string{"nocx.token." + ws.Token()}}).Dial(wsURL(ws), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	tap := newSocketTap(conn)
	raw := tapCall(t, conn, tap, 3, "sandbox.profile.get", map[string]any{})
	var env struct {
		Error struct {
			Data struct {
				Reason string `json:"reason"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Data.Reason != "store_unavailable" {
		t.Fatalf("data.reason = %q", env.Error.Data.Reason)
	}
}
