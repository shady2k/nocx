package transport

// session.output against contracts/session.output.schema.json (nocx-22k1c.2).
//
// Two checks and the second is the point: the DTO says the struct is
// well-formed, and the socket says the SERVER SENDS IT. A test validating a
// payload it built itself proves neither the handler nor the wire.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// The DTO's own conformance: field tags, how a []byte renders (base64), and
// the two list fields that must never marshal as null — the exact defect
// this directory's first run found in vault.status's providers, and the one
// that would throw on the renderer's first .map.
func TestSessionOutput_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.output.schema.json")

	full, err := json.Marshal(sessionOutputResult{
		SessionID:     "0123456789abcdef0123456789abcdef",
		EffectiveSize: sizeResult{Cols: 80, Rows: 24},
		From:          0,
		Runs: []sessionOutputRunResult{
			{Offset: 0, Body: []byte("hello")},
			{Offset: 900, Body: []byte("world")},
		},
		Gaps:     []sessionOutputGapResult{{Start: 5, End: 900, Reason: "cap"}},
		Produced: 905,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, full, "session.output DTO with a hole in it")

	// A whole recording of nothing: both lists empty, neither null.
	empty, err := json.Marshal(sessionOutputResult{
		SessionID:     "0123456789abcdef0123456789abcdef",
		EffectiveSize: sizeResult{Cols: 80, Rows: 24},
		Runs:          []sessionOutputRunResult{},
		Gaps:          []sessionOutputGapResult{},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(empty), `"runs":null`) || strings.Contains(string(empty), `"gaps":null`) {
		t.Errorf("a list serialised as null: %s", empty)
	}
	validateJSON(t, schema, empty, "session.output DTO for a session that printed nothing")
}

// The real method through the real socket, on a real encrypted store, with
// a real hole in the recording — so the payload under test is the one the
// server actually sends and not one this test could have shaped.
func TestSessionOutput_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.output.schema.json")

	const capBytes = 64 << 10
	db := openRecordingStore(t, capBytes)

	term := newFeedablePTY()
	ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(db.SessionOutput()))
	defer stop()

	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	_ = conn.Close()
	awaitNoSubscriber(t, ws, session.ID(sid))
	awaitPush(t, "more output than the bound keeps", pushOutput(term, recordingStream(4*capBytes)))
	waittest.WaitForTimeout(t, "the bound to drop the middle", wantWithin, func() bool {
		rec, err := db.SessionOutput().Read(t.Context(), sid)
		return err == nil && len(rec.Gaps) > 0
	})

	fresh := connectWS(t, ws)
	result, rpcErr := readSessionOutput(t, fresh, 100, map[string]any{"sessionId": sid, "from": 0})
	if rpcErr != nil {
		t.Fatalf("session.output: %+v", rpcErr)
	}
	validateJSON(t, schema, result, "session.output result off the socket")
}
