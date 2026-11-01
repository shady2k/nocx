package transport

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/session"
)

func TestAttachUnavailableStreamDoesNotDeclareLiveSessionAbsent(t *testing.T) {
	ws, _ := newReclaimWS(t)
	first := connectWS(t, ws)
	opened := openInPaneTapped(t, ws, first, newSocketTap(first), reclaimPane, 1)
	sid := session.ID(opened.SessionID)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	awaitDetached(t, ws, sid)

	// A registry entry can precede its transport stream. The refusal must
	// not authorize replacing a shell merely because that stream is absent.
	ws.ringsMu.Lock()
	rx := ws.rx[sid]
	delete(ws.rx, sid)
	ws.ringsMu.Unlock()
	restore := func() {
		ws.ringsMu.Lock()
		ws.rx[sid] = rx
		ws.ringsMu.Unlock()
	}
	t.Cleanup(restore)

	conn := connectWS(t, ws)
	tap := newSocketTap(conn)
	params := map[string]any{
		"sessionId": opened.SessionID, "instanceId": opened.InstanceID,
		"sessionEpoch": opened.SessionEpoch, "offset": 0,
	}
	_, refusal := attachCall(t, conn, tap, 2, params)
	if refusal == nil || refusal.Code != -32000 {
		t.Fatalf("live session without stream must report unavailability, not absence: %+v", refusal)
	}

	restore()
	raw, refusal := attachCall(t, conn, tap, 3, params)
	if refusal != nil {
		t.Fatalf("retrying the same live session after its stream returns: %+v", refusal)
	}
	var result attachResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Resumed || result.Reset {
		t.Fatalf("retry must resume the original stream: %+v", result)
	}
}
