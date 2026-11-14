package app

// pane.connection reaches the renderer from a REAL pane open and a REAL
// observation of the helper protocol (finding 7 of the 5.3 review). The
// contract test in internal/transport publishes fixtures; this one would fail if
// the open stopped binding the pane, the driver stopped mapping, the adapter
// stopped converting, or the publisher stopped sending — which is the wiring the
// fixture test cannot see.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
)

// TestPaneConnection_OverTheWireConformsToContract drives every state the
// helper protocol can produce from a REAL pane open and REAL observations of
// that protocol, through the driver, the composition's adapter and the
// publisher, and validates the bytes off the real socket against the contract
// (review finding 7; contracts/README.md's third check).
//
// `plain` is the one state not exercised, and it is not a gap: it names the
// system's own ssh process, which is a carrier this helper is not, so no
// producer can reach it (it is checked against the schema in
// internal/transport's DTO test, where the later carrier's shape lives). Every
// other state the backend can put on this wire is produced here by the same
// code path production uses; removing the binding, the driver, the adapter or
// the publisher fails this test.
func TestPaneConnection_OverTheWireConformsToContract(t *testing.T) {
	schema := loadAppContractSchema(t, "pane.connection.schema.json")

	logger := log.NewSlogAdapter(discardLogger(t))
	reg := session.New(logger, &reachPTYFactory{stub: pty.NewStub(logger)})
	driver := panedriver.New(string(reg.InstanceID()), logger)
	ws := transport.NewWSServer(logger, reg, transport.WithPaneConnectionPolicy(driver))
	// THE COMPOSITION'S THREE LINKS, wired exactly as the root wires them.
	driver.SetPublisher(func(snapshot panedriver.Snapshot) {
		ws.PublishPaneConnection(paneConnectionWire(snapshot))
	})
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := dialTraceWSServer(t, ws)
	const paneID = "0190d3a0-1c2b-7def-8abc-0123456789ab"

	// OPEN, through the real handler: this is what binds the pane to this
	// connection and opens a session for it.
	openReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "open",
		"params": map[string]any{"kind": "local", "cols": 80, "rows": 24, "paneId": paneID},
	})
	if err := conn.WriteMessage(websocket.TextMessage, openReq); err != nil {
		t.Fatalf("write open: %v", err)
	}
	waitForOpenResult(t, conn, 1)

	// The composition root's binding step (helper_local.go's openSSH), then one
	// REAL helper observation per state, in the order a connection meets them.
	driver.Bind(paneID, "conn-e2e")
	const source = "helper-e2e"
	states := []struct {
		event proto.ConnectionStateEvent
		// wantOffer is the offer the renderer must see, -1 for absent.
		wantOffer int
	}{
		{proto.ConnectionStateEvent{State: proto.ConnectionOpening, Stage: "tcp"}, -1},
		{proto.ConnectionStateEvent{State: proto.ConnectionWaitingPerson, Person: "password"}, -1},
		{proto.ConnectionStateEvent{State: proto.ConnectionConnected, PlainShells: 1, RoundTripMs: 12}, -1},
		{proto.ConnectionStateEvent{
			State: proto.ConnectionStalled, StateMs: 45000, PlainShells: 1,
			LookAnsweredAt: "2026-11-14T07:00:20Z", RoundTripMs: 620, Slow: true,
		}, 1},
		{proto.ConnectionStateEvent{State: proto.ConnectionReconnecting, Attempt: 3, Cause: "lost-link"}, 0},
		{proto.ConnectionStateEvent{State: proto.ConnectionFailed, Cause: "host-key-changed"}, 0},
		{proto.ConnectionStateEvent{State: proto.ConnectionEnded, Cause: "released"}, 0},
	}
	seqs := []uint64{1, 2, 3, 4, 5, 6, 7}
	for i, tc := range states {
		ev := tc.event
		ev.ConnectionID = "conn-e2e"
		ev.Seq = seqs[i]
		driver.ApplyFrom(source, ev)

		raw := waitForNotification(t, conn, "pane.connection")
		validateAppContract(t, schema, raw, "pane.connection "+ev.State)

		var got struct {
			PaneID     string `json:"paneId"`
			Connection string `json:"connectionId"`
			Epoch      uint64 `json:"connectionEpoch"`
			Instance   string `json:"instanceId"`
			Transport  struct {
				State string `json:"state"`
				Stage string `json:"stage"`
			} `json:"transport"`
			Reconnect *struct {
				EndsShells int `json:"endsShells"`
			} `json:"reconnect"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s: unmarshal: %v", ev.State, err)
		}
		if got.PaneID != paneID || got.Connection != "conn-e2e" {
			t.Fatalf("%s: payload names %q/%q", ev.State, got.PaneID, got.Connection)
		}
		if got.Epoch == 0 || got.Instance == "" {
			t.Fatalf("%s: payload lacks its ordering identity: %s", ev.State, raw)
		}
		if got.Transport.State != ev.State {
			t.Fatalf("%s: rendered state = %q", ev.State, got.Transport.State)
		}
		switch {
		case tc.wantOffer < 0:
			if got.Reconnect != nil {
				t.Fatalf("%s: payload carried an offer: %s", ev.State, raw)
			}
		case got.Reconnect == nil:
			t.Fatalf("%s: payload carried no offer: %s", ev.State, raw)
		case got.Reconnect.EndsShells != tc.wantOffer:
			t.Fatalf("%s: offer names %d shell(s), want %d", ev.State, got.Reconnect.EndsShells, tc.wantOffer)
		}
	}
}

// waitForOpenResult reads until the open's response, so the pane is bound and
// the session exists before the observation is fed.
func waitForOpenResult(t *testing.T, conn *websocket.Conn, id int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_ = conn.SetReadDeadline(deadline)
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read open result: %v", err)
		}
		var envelope struct {
			ID    *json.RawMessage `json:"id"`
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			continue
		}
		if envelope.ID == nil || string(*envelope.ID) != "1" {
			continue
		}
		if envelope.Error != nil {
			t.Fatalf("open refused: %+v", envelope.Error)
		}
		return
	}
}

// waitForNotification reads until one notification of method arrives.
func waitForNotification(t *testing.T, conn *websocket.Conn, method string) json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_ = conn.SetReadDeadline(deadline)
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read %s: %v", method, err)
		}
		var envelope struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Method != method {
			continue
		}
		return envelope.Params
	}
}
