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
	"sync"
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

// THE OVER-THE-WIRE CONFORMANCE TEST IS NOT HERE. It lives in internal/app
// (`TestPaneConnection_OverTheWireConformsToContract`) because the producer is
// the whole chain — helper observation, driver, composition adapter, publisher —
// and a test that builds this package's DTOs and calls PublishPaneConnection
// directly would keep passing with that chain disconnected (review finding 7).
// What stays in this package is the shape each state must have (the DTO test
// above, including `plain`, which no producer reaches yet) and the ordering
// guard below, which is the publisher's own contract.

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

// TestTheEpochGuardSerializesPublication is finding 5 of the 5.3 review: two
// publishers that both pass the epoch check must reach the socket in epoch
// order, or a stale observation (including one from a connection the pane has
// left) can arrive after a newer one.
func TestTheEpochGuardSerializesPublication(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	bindTestPane(t, ws, wc, "pane-1")

	var workers sync.WaitGroup
	for epoch := uint64(1); epoch <= 50; epoch++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			state := paneConnectionStates()["opening"]
			state.ConnectionEpoch = epoch
			ws.PublishPaneConnection(state)
		}()
	}
	workers.Wait()
	final := paneConnectionStates()["opening"]
	final.ConnectionEpoch = 51
	ws.PublishPaneConnection(final)

	var previous uint64
	for previous < 51 {
		raw := readNotification(t, conn, "pane.connection", wantWithin)
		var got struct {
			Epoch uint64 `json:"connectionEpoch"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Epoch <= previous {
			t.Fatalf("epoch %d followed %d", got.Epoch, previous)
		}
		previous = got.Epoch
	}
}

// panePolicySpy records the policy notices the transport sends.
type panePolicySpy struct {
	mu      sync.Mutex
	started []string
	closed  []string
}

func (p *panePolicySpy) ReconnectStarted(paneID string) {
	p.mu.Lock()
	p.started = append(p.started, paneID)
	p.mu.Unlock()
}
func (p *panePolicySpy) ReconnectFinished(string) {}
func (p *panePolicySpy) PaneClosed(paneID string) {
	p.mu.Lock()
	p.closed = append(p.closed, paneID)
	p.mu.Unlock()
}

func (p *panePolicySpy) closedPanes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.closed...)
}

// TestPaneCloseRemovesTheBinding is finding 4 of the 5.3 review: a pane that
// left the layout must stop producing notifications for a connection other
// panes keep alive.
func TestPaneCloseRemovesTheBinding(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	spy := &panePolicySpy{}
	ws.paneConnectionPolicy = spy
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	bindTestPane(t, ws, wc, "pane-1")

	publishTestConnection(ws, "pane-1", 1, "connected", nil)
	readNotification(t, conn, "pane.connection", wantWithin)

	ws.closeSessionsForPanes(map[string]struct{}{"pane-1": {}})
	if got := spy.closedPanes(); len(got) != 1 || got[0] != "pane-1" {
		t.Fatalf("policy saw closures %v, want [pane-1]", got)
	}

	// Epoch 2 is published while the pane is gone: it must reach nobody. A
	// rebind then publishes 3, and reading 3 — not 2 — is the proof.
	publishTestConnection(ws, "pane-1", 2, "connected", nil)
	ws.bindPane("pane-1", wc, newConnState(), openParams{PaneID: "pane-1", Cols: 80, Rows: 24})
	publishTestConnection(ws, "pane-1", 3, "connected", nil)
	raw := readNotification(t, conn, "pane.connection", wantWithin)
	var got struct {
		Epoch uint64 `json:"connectionEpoch"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Epoch != 3 {
		t.Fatalf("read epoch %d, want 3: an observation was published for a closed pane", got.Epoch)
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
