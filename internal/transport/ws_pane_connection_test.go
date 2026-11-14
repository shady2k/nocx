package transport

// pane.connection on the wire (design §5.6, nocx-oalo1.5.3).
//
// Two checks and the third one, the same shape every contract in this directory
// gets: the DTO against the schema for every state, the same objects read off
// the REAL socket, and the per-pane epoch guard read as the third frame rather
// than as an absence (a test that waits for silence is a test that depends on a
// duration).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/waittest"
)

// paneConnectionStates is one payload per state the schema admits, with the
// fields that state makes true. `plain` has no producer in this stage and is
// still exercised: the contract is what the later carrier will fill.
func paneConnectionStates() map[string]PaneConnection {
	shells := func(n int) *int { return &n }
	base := func(epoch uint64, state string) PaneConnection {
		return PaneConnection{
			PaneID:          "pane-1",
			ConnectionID:    "conn-1",
			ConnectionEpoch: epoch,
			InstanceID:      "instance-1",
			Transport:       PaneConnectionTransport{State: state, StateMs: 10},
			Attempt:         PaneConnectionAttempt{Shown: "none", Candidate: "none"},
			ObservedAt:      "2026-11-14T07:00:00Z",
		}
	}
	opening := base(1, "opening")
	opening.Transport.Stage = "tcp"

	waiting := base(2, "waiting_person")
	waiting.Transport.Person = "key-signature"

	connected := base(3, "connected")
	connected.Transport.PlainShells = shells(0)
	connected.Quality = &PaneConnectionQuality{Slow: false, RoundTripMS: 12}
	connected.SessionID = "sess-1"

	stalled := base(4, "stalled")
	stalled.Transport.PlainShells = shells(2)
	stalled.Transport.StateMs = 45000
	stalled.Transport.LookAnsweredAt = "2026-11-14T07:00:20Z"
	stalled.Quality = &PaneConnectionQuality{Slow: true, RoundTripMS: 620}
	// The offer, naming the two shells a press would end (nocx-oalo1.5.4).
	stalled.Reconnect = &PaneConnectionReconnect{EndsShells: 2}

	reconnecting := base(5, "reconnecting")
	reconnecting.Transport.Attempt = 3

	failed := base(6, "failed")
	failed.Transport.Cause = "host-key-changed"
	// A dropped connection whose shells already ended: the button is there and
	// ENDS NOTHING, which is a real answer and not an absent one.
	failed.Reconnect = &PaneConnectionReconnect{EndsShells: 0}

	ended := base(7, "ended")
	ended.Transport.Cause = "released"

	plain := base(8, "plain")

	return map[string]PaneConnection{
		"opening": opening, "waiting_person": waiting, "connected": connected,
		"stalled": stalled, "reconnecting": reconnecting, "failed": failed,
		"ended": ended, "plain": plain,
	}
}

// TestPaneConnectionDTOConformsToContract checks every state's shape, and the
// two impossible combinations the schema's branches exist to refuse.
func TestPaneConnectionDTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "pane.connection.schema.json")
	for name, payload := range paneConnectionStates() {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "pane.connection DTO "+name)
		})
	}

	t.Run("an opening may not carry a cause", func(t *testing.T) {
		p := paneConnectionStates()["opening"]
		p.Transport.Cause = "auth-rejected"
		raw, _ := json.Marshal(p)
		if err := validateJSONErr(schema, raw); err == nil {
			t.Fatal("the schema admitted an opening with a cause")
		}
	})
	t.Run("a connected connection must say how many shells it carries", func(t *testing.T) {
		p := paneConnectionStates()["connected"]
		p.Transport.PlainShells = nil
		raw, _ := json.Marshal(p)
		if err := validateJSONErr(schema, raw); err == nil {
			t.Fatal("the schema admitted connected without plainShells")
		}
	})
}

// TestPaneConnectionOverTheWireConformsToContract drives the real publisher on a
// real socket, one notification per state, and validates the bytes the renderer
// actually receives.
func TestPaneConnectionOverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "pane.connection.schema.json")
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	ws.bindPane("pane-1", wc, newConnState(), openParams{PaneID: "pane-1", Cols: 80, Rows: 24})

	// ONE EPOCH PER OBSERVATION, in a fixed order: the publisher refuses a
	// sequence that is not newer, and a map's order would make the first
	// observation a different state on every run.
	states := paneConnectionStates()
	order := []string{"opening", "waiting_person", "connected", "stalled", "reconnecting", "failed", "ended", "plain"}
	epochs := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	for i, state := range order {
		payload := states[state]
		payload.ConnectionEpoch = epochs[i]
		ws.PublishPaneConnection(payload)
		raw := readNotification(t, conn, "pane.connection", wantWithin)
		validateJSON(t, schema, raw, "pane.connection over the wire: "+state)
		var got struct {
			PaneID string `json:"paneId"`
			Epoch  uint64 `json:"connectionEpoch"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s: unmarshal: %v", state, err)
		}
		if got.PaneID != payload.PaneID || got.Epoch != payload.ConnectionEpoch {
			t.Fatalf("%s: read %s/%d, sent %s/%d", state, got.PaneID, got.Epoch, payload.PaneID, payload.ConnectionEpoch)
		}
	}
}

// TestPaneConnectionRefusesAnEpochItHasAlreadyPassed is the guard read as a
// frame: a stale epoch published between two fresh ones must not appear, which
// the third frame proves without any wait for silence.
func TestPaneConnectionRefusesAnEpochItHasAlreadyPassed(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	ws.bindPane("pane-1", wc, newConnState(), openParams{PaneID: "pane-1", Cols: 80, Rows: 24})

	state := paneConnectionStates()["opening"]
	for _, epoch := range []uint64{1, 2, 1, 3} {
		state.ConnectionEpoch = epoch
		ws.PublishPaneConnection(state)
	}

	var epochs []uint64
	for i := 0; i < 3; i++ {
		raw := readNotification(t, conn, "pane.connection", wantWithin)
		var got struct {
			Epoch uint64 `json:"connectionEpoch"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		epochs = append(epochs, got.Epoch)
	}
	want := []uint64{1, 2, 3}
	for i := range want {
		if epochs[i] != want[i] {
			t.Fatalf("epochs over the wire = %v, want %v: a stale epoch was sent", epochs, want)
		}
	}
}

// newPaneConnectionServer is a real server with a real registry: the publisher
// needs a connection to address and nothing else, and this is the same stand
// the notification tests beside it use.
func newPaneConnectionServer(t *testing.T) (*WSServer, func()) {
	t.Helper()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, func() { _ = ws.Stop(ctx) }
}

// soleWSConn answers the one connection the server has accepted. The publisher
// addresses a *wsConn, and a test drives the same path the open handler does by
// binding that connection to a pane.
func soleWSConn(t *testing.T, ws *WSServer) *wsConn {
	t.Helper()
	var only *wsConn
	waittest.WaitFor(t, "the server's one connection", func() bool {
		ws.connsMu.Lock()
		defer ws.connsMu.Unlock()
		if len(ws.conns) != 1 {
			return false
		}
		for wc := range ws.conns {
			only = wc
		}
		return true
	})
	return only
}
