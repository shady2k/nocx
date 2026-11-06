package transport

// The credit window (ADR-8eej3e66 §7): the backend advertises how many
// ordinary calls one connection may have outstanding, holds one credit back
// from the window for vault.unseal, never counts rescue traffic, and
// disconnects a peer that sends more than it was told.
//
// These tests drive the REAL socket. The ordinary calls that fill the window
// are real blocked opens through the real session opener, so "in flight" is a
// fact about the server rather than a fixture the test built.

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/diagnostics"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/waittest"
)

// ── the accounting, in isolation ────────────────────────────────────────

func TestCreditWindowCountsOrdinaryCallsAndRefusesTheOneBeyond(t *testing.T) {
	c := newCreditConfig(2, stringSet("ack"), []string{"vault.unseal"}).forConnection()

	p1, over := c.admit("files.list", json.RawMessage("1"))
	if over != nil || p1 == nil {
		t.Fatalf("first ordinary call refused (over=%+v)", over)
	}
	p2, over := c.admit("files.list", json.RawMessage("2"))
	if over != nil || p2 == nil {
		t.Fatalf("second ordinary call refused (over=%+v)", over)
	}
	// InFlight includes the frame being refused: the peer would have had
	// three outstanding against a window of two.
	if _, over := c.admit("files.list", json.RawMessage("3")); over == nil || over.InFlight != 3 || over.Window != 2 {
		t.Fatalf("third ordinary call was admitted, want an overrun at 3/2: %+v", over)
	}

	// A response frees exactly one slot.
	c.releaseByID(json.RawMessage("1"))
	if _, over := c.admit("files.list", json.RawMessage("4")); over != nil {
		t.Fatalf("an admitted call after one release was refused: %+v", over)
	}
}

func TestCreditWindowKeepsAReservedSlotOutsideTheOrdinaryWindow(t *testing.T) {
	c := newCreditConfig(1, nil, []string{"vault.unseal"}).forConnection()
	if _, over := c.admit("files.list", json.RawMessage("1")); over != nil {
		t.Fatalf("ordinary call refused: %+v", over)
	}
	// THE FULL-WINDOW UNLOCK, at the accounting layer: the window is full and
	// the reserved call is still admitted.
	if _, over := c.admit("vault.unseal", json.RawMessage("2")); over != nil {
		t.Fatalf("vault.unseal was refused while the ordinary window was full: %+v", over)
	}
	// The reserved slot is one, not a second window.
	if _, over := c.admit("vault.unseal", json.RawMessage("3")); over == nil || over.Class != "reserved" {
		t.Fatalf("a second vault.unseal was admitted past its one reserved credit: %+v", over)
	}
}

func TestCreditWindowNeverCountsRescueTraffic(t *testing.T) {
	c := newCreditConfig(1, stringSet("ack", "rpc.cancel", "vault.unlockResolved", "transport.ping", "app.diagnostics"), nil).forConnection()
	if _, over := c.admit("open", json.RawMessage("1")); over != nil {
		t.Fatalf("ordinary call refused: %+v", over)
	}
	for i, method := range []string{"ack", "rpc.cancel", "vault.unlockResolved", "transport.ping", "app.diagnostics"} {
		if p, over := c.admit(method, json.RawMessage(string(rune('a'+i)))); over != nil || p != nil {
			t.Fatalf("%s took a credit (permit=%v over=%+v); rescue traffic is exempt", method, p, over)
		}
	}
	// And the rescue traffic did not free the ordinary slot either.
	if _, over := c.admit("open", json.RawMessage("9")); over == nil {
		t.Fatal("the ordinary window was raised by rescue traffic")
	}
}

func TestCreditPermitReleasesOnceHoweverItIsReleased(t *testing.T) {
	c := newCreditConfig(1, nil, nil).forConnection()
	p, _ := c.admit("open", json.RawMessage("1"))
	c.releaseByID(json.RawMessage("1")) // the response path
	p.Release()                         // the task's defer
	if _, over := c.admit("open", json.RawMessage("2")); over != nil {
		t.Fatalf("a double release freed a second credit: %+v", over)
	}
}

// ── the wire ────────────────────────────────────────────────────────────

func newCreditTestServer(t *testing.T, lane, window int, rec *diagnostics.Recorder, opener HelperSessionOpener) (*WSServer, func()) {
	t.Helper()
	ws, _, stop := newCreditTestServerWithLog(t, lane, window, rec, opener)
	return ws, stop
}

func newCreditTestServerWithLog(t *testing.T, lane, window int, rec *diagnostics.Recorder, opener HelperSessionOpener) (*WSServer, *slog.Logger, func()) {
	t.Helper()
	sl := logtest.Slog(t)
	logger := log.NewSlogAdapter(sl)
	opts := []WSServerOption{
		WithControlLaneCapacity(lane),
		WithCreditWindow(window),
		WithProfileResolver(&openProfileResolver{host: "host.example:22"}),
	}
	if rec != nil {
		opts = append(opts, WithDiagnostics(rec, stubDiagnosticsProvider{}))
	}
	if opener != nil {
		opts = append(opts, WithHelperSessionOpener(opener))
	}
	ws := NewWSServer(logger, newRegWithStub(logger), opts...)
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, sl, func() { _ = ws.Stop(ctx) }
}

func TestTheCreditWindowIsAdvertisedPerConnection(t *testing.T) {
	ws, stop := newCreditTestServer(t, 4, 2, nil, nil)
	defer stop()

	for i, conn := range []*websocket.Conn{connectWS(t, ws), connectWS(t, ws)} {
		resp := snippetCall(t, conn, "transport.credit", map[string]any{}, 1)
		if resp.Error != nil {
			t.Fatalf("connection %d: transport.credit answered an error: %+v", i, resp.Error)
		}
		var got transportCreditResult
		if err := json.Unmarshal(resp.Result, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Window != 2 {
			t.Errorf("window = %d, want 2", got.Window)
		}
		for _, want := range []string{"ack", "rpc.cancel", "vault.unlockResolved", "connections.passwordResolved", "agent.runResolved", "host.resolved", "agent.laneInteractivity", "transport.ping", "app.diagnostics"} {
			if !containsString(got.Exempt, want) {
				t.Errorf("exempt = %v, want %q", got.Exempt, want)
			}
		}
		if len(got.Reserved) != 1 || got.Reserved[0] != "vault.unseal" {
			t.Errorf("reserved = %v, want [vault.unseal]", got.Reserved)
		}
	}
}

// THE FULL-WINDOW UNLOCK: a window of calls is in flight, and vault.unseal is
// still admitted — and then the peer that goes one past its window is told by
// being disconnected, with the overrun in the log and in Copy diagnostics.
func TestTheReservedUnsealGetsThroughAFullWindowAndAnOverrunIsDisconnected(t *testing.T) {
	blocker := newBlockingHelperOpener()
	rec := diagnostics.NewRecorder()
	ws, sl, stop := newCreditTestServerWithLog(t, 4, 2, rec, blocker)
	defer stop()
	t.Cleanup(func() { close(blocker.release) })

	conn := connectWS(t, ws)
	// Two ordinary calls, admitted and never answered: the window is full.
	sendCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "kind": "ssh", "profileId": "ssh:one",
	}, 1)
	sendCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "kind": "ssh", "profileId": "ssh:two",
	}, 2)
	waittest.WaitFor(t, "both opens to be in flight", func() bool {
		return len(rec.Snapshot().Live) == 2
	})

	// The window is full of ordinary work and the unlock still gets through.
	unseal := snippetCall(t, conn, "vault.unseal", map[string]any{"means": "passphrase", "secret": "x"}, 3)
	if unseal.Error != nil && unseal.Error.Code == SaturationErrorCode {
		t.Fatalf("vault.unseal was refused for capacity while the ordinary window was full: %+v", unseal.Error)
	}

	// One ordinary call past the window: the connection is gone and the
	// overrun is recorded. The frame is deliberately not answered.
	sendCall(t, conn, "profiles.list", map[string]any{}, 4)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}

	waittest.WaitFor(t, "the overrun to reach the record", func() bool {
		return len(rec.Snapshot().CreditOverruns) == 1
	})
	over := rec.Snapshot().CreditOverruns[0]
	if over.Method != "profiles.list" || over.Class != "ordinary" || over.InFlight != 3 || over.Window != 2 {
		t.Fatalf("overrun = %+v, want profiles.list ordinary 3/2", over)
	}
	// AND THE LINE. A shipped build's log is where a reader who never opened
	// Copy diagnostics finds the same fact.
	if !logtest.WaitForSlog(sl, 2*time.Second, func(r logtest.Record) bool {
		if r.Message != "control credit window exceeded" {
			return false
		}
		class, _ := recordAttr(r, "class")
		window, _ := recordAttr(r, "window")
		return class == "ordinary" && window == "2"
	}) {
		t.Fatal("no shipped-level line recorded the credit overrun")
	}

	// Copy diagnostics says the same thing, from another connection.
	out := snippetCall(t, connectWS(t, ws), "app.diagnostics", map[string]any{}, 9)
	if out.Error != nil {
		t.Fatalf("app.diagnostics: %+v", out.Error)
	}
	var got diagnosticsResult
	if err := json.Unmarshal(out.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, want := range []string{"credit overruns: 1", "method=profiles.list", "class=ordinary", "in_flight=3", "window=2"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("the text lacks %q:\n%s", want, got.Text)
		}
	}
}

// A well-behaved client is never told no: it may make any NUMBER of calls,
// as long as it does not send the next before the last was answered. The
// window bounds what is outstanding, never what is sent over the
// connection's life.
func TestAClientWithinItsWindowReceivesNoCapacityAnswer(t *testing.T) {
	ws, stop := newCreditTestServer(t, 4, 2, nil, nil)
	defer stop()
	conn := connectWS(t, ws)
	for i := 0; i < 6; i++ {
		resp := snippetCall(t, conn, "profiles.list", map[string]any{}, 100+i)
		if resp.Error != nil && resp.Error.Code == SaturationErrorCode {
			t.Fatalf("call %d of a one-at-a-time client was refused for capacity: %+v", i+1, resp.Error)
		}
	}
}

func TestRescueTrafficDoesNotSpendTheWindow(t *testing.T) {
	blocker := newBlockingHelperOpener()
	ws, stop := newCreditTestServer(t, 4, 1, nil, blocker)
	defer stop()
	t.Cleanup(func() { close(blocker.release) })

	conn := connectWS(t, ws)
	sendCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "kind": "ssh", "profileId": "ssh:one",
	}, 1)
	waittest.WaitFor(t, "the open to be in flight", func() bool {
		select {
		case <-blocker.entered:
			return true
		default:
			return false
		}
	})

	for i, method := range []string{"transport.ping", "app.diagnostics"} {
		resp := snippetCall(t, conn, method, map[string]any{}, 10+i)
		if resp.Error != nil {
			t.Fatalf("%s was refused while the ordinary window was full: %+v", method, resp.Error)
		}
	}
	// rpc.cancel is a notification in production, and answers an empty result
	// as a request. It must reach the read loop without a credit either.
	cancel := snippetCall(t, conn, "rpc.cancel", map[string]any{"id": 999}, 20)
	if cancel.Error != nil {
		t.Fatalf("rpc.cancel was refused while the ordinary window was full: %+v", cancel.Error)
	}
}

func TestACancelledCallReturnsItsCreditBeforeTheNextFrame(t *testing.T) {
	blocker := newBlockingHelperOpener()
	ws, stop := newCreditTestServer(t, 4, 1, nil, blocker)
	defer stop()
	t.Cleanup(func() { close(blocker.release) })

	conn := connectWS(t, ws)
	sendCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "kind": "ssh", "profileId": "ssh:one",
	}, 1)
	waittest.WaitFor(t, "the open to be in flight", func() bool {
		select {
		case <-blocker.entered:
			return true
		default:
			return false
		}
	})

	// The cancellation is immediate on the read loop, so the next ordinary
	// call is admitted even though the cancelled handler has not returned.
	snippetCall(t, conn, "rpc.cancel", map[string]any{"id": 1}, 2)
	resp := snippetCall(t, conn, "profiles.list", map[string]any{}, 3)
	if resp.Error != nil && resp.Error.Code == SaturationErrorCode {
		t.Fatalf("the call after a cancellation was refused for capacity: %+v", resp.Error)
	}
}

// stringSet is the exempt set as the server builds it: the ingress-critical
// method names as a map.
func stringSet(methods ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(methods))
	for _, m := range methods {
		set[m] = struct{}{}
	}
	return set
}

// The wire shape is declared once (contracts/transport.credit.schema.json):
// one test marshals the DTO, one drives the REAL socket. The second is the
// one that matters — it proves the server sends what the renderer decodes.
func TestTransportCredit_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "transport.credit.schema.json")
	window := newCreditConfig(3, stringSet("ack", "rpc.cancel"), reservedCreditMethods)
	raw, err := json.Marshal(window.advertisement())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "transport.credit result DTO")
}

func TestTransportCredit_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "transport.credit.schema.json")
	ws, stop := newCreditTestServer(t, 4, 2, nil, nil)
	defer stop()
	resp := snippetCall(t, connectWS(t, ws), "transport.credit", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("transport.credit answered an error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "transport.credit result off the socket")
}
