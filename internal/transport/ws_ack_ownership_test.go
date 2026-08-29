package transport

import (
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/session"
)

// ── who may move the ring's cursor (nocx-7ih2d) ───────────────────────────

// ackOn sends one ack notification over conn. A notification has no answer of
// its own; the caller's barrier is the next REQUEST on the same connection.
func ackOn(t *testing.T, conn *websocket.Conn, sid string, offset uint64) {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "ack",
		"params":  map[string]any{"sessionId": sid, "offset": offset},
	})
	if err != nil {
		t.Fatalf("marshal ack: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write ack: %v", err)
	}
}

// TestAck_FromADisplacedConnection_LeavesTheRingWhereItIs: the ack cursor is
// what `trim` frees bytes against, so only the connection that HOLDS the
// session may move it. A displacement takes the claim and leaves the socket
// up — the loser goes on being read — and an ack it had already queued must
// not reclaim bytes out from under the pump of the connection that took over.
func TestAck_FromADisplacedConnection_LeavesTheRingWhereItIs(t *testing.T) {
	ws, term := newReclaimWS(t)

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	opened := openInPaneTapped(t, ws, connA, tapA, reclaimPane, 1)
	term.emit(t, "some output\n")
	tapDataFor(t, tapA, opened.SessionID, "some output", wantWithin)

	// connB takes the session. connA's SOCKET is deliberately left open.
	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)
	if _, rpcErr := attachCall(t, connB, tapB, 2, map[string]any{
		"sessionId":    opened.SessionID,
		"instanceId":   opened.InstanceID,
		"sessionEpoch": opened.SessionEpoch,
		"offset":       0,
	}); rpcErr != nil {
		t.Fatalf("attach: %+v", rpcErr)
	}

	rx := ws.getRx(session.ID(opened.SessionID))
	if rx == nil {
		t.Fatal("the session has no ring")
	}
	before := rx.ring.oldestLocked()

	// The loser acks everything it received before the take.
	ackOn(t, connA, opened.SessionID, rx.ring.writtenLocked())

	// The barrier, and it is an observable state rather than a duration: an
	// ack is an IMMEDIATE submission, so it runs inline on connA's read loop
	// before that loop dispatches the next frame at all. A request answered
	// on connA therefore proves the ack has already run.
	liveSessions(t, connA, tapA, 3)

	if got := rx.ring.oldestLocked(); got != before {
		t.Fatalf("the displaced connection's ack moved the ring's base from %d to %d: a connection that no longer holds the session reclaimed the subscriber's stream", before, got)
	}
}
