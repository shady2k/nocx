package transport

// session.output — the read surface, through the seam a person reaches
// (nocx-22k1c.2).
//
// What these assert is what a user can now do that they could not before:
// open a window on a session that has been running for an hour and see the
// hour, not the last ten screens of it. The recorder's own tests
// (ws_session_record_test.go) say the bytes reach the disk; these say the
// product hands them back.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// ── the shape a test decodes ─────────────────────────────────────────────
//
// Named fields, not an anonymous struct per call site: the contract test
// checks the whole payload against the schema, and these tests check what
// the payload MEANS, so they may name only what they are about.

type wireSessionOutput struct {
	SessionID     string `json:"sessionId"`
	EffectiveSize struct {
		Cols   int `json:"cols"`
		Rows   int `json:"rows"`
		XPixel int `json:"xpixel"`
		YPixel int `json:"ypixel"`
	} `json:"effectiveSize"`
	From uint64 `json:"from"`
	Runs []struct {
		Offset uint64 `json:"offset"`
		Body   string `json:"body"`
	} `json:"runs"`
	Gaps []struct {
		Start  uint64 `json:"start"`
		End    uint64 `json:"end"`
		Reason string `json:"reason"`
	} `json:"gaps"`
	Produced uint64 `json:"produced"`
}

// bodyAt decodes one run's base64 body.
func (w wireSessionOutput) bodyAt(t *testing.T, i int) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(w.Runs[i].Body)
	if err != nil {
		t.Fatalf("run %d body is not base64: %v", i, err)
	}
	return b
}

// readSessionOutput drives the real method over the real socket and returns
// the raw result, so a caller can decode it or assert on the refusal.
func readSessionOutput(t *testing.T, conn *websocket.Conn, id int, params map[string]any) (json.RawMessage, *jsonrpcErrorObj) {
	t.Helper()
	raw := jsonrpcCallWithID(t, conn, "session.output", params, id)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("session.output: unmarshal: %v\nraw: %s", err, raw)
	}
	return env.Result, env.Error
}

// mustReadSessionOutput is readSessionOutput for the cases that must succeed.
func mustReadSessionOutput(t *testing.T, conn *websocket.Conn, id int, params map[string]any) wireSessionOutput {
	t.Helper()
	result, rpcErr := readSessionOutput(t, conn, id, params)
	if rpcErr != nil {
		t.Fatalf("session.output: %+v", rpcErr)
	}
	var got wireSessionOutput
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatalf("session.output: decode result: %v\nraw: %s", err, result)
	}
	return got
}

// openRecordingStore opens a real encrypted content store with a chosen
// per-command cap — the bound whose semantics acceptance 3 is about.
func openRecordingStore(t *testing.T, capBytes int) content.ContentDB {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	policy := content.NewPolicy()
	policy.SetOutputCapBytes(capBytes)
	db, err := content.Open(context.Background(), content.Config{
		Path:   filepath.Join(t.TempDir(), "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Policy: policy,
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// awaitProduced waits until the recording's end offset has reached n — the
// observable that says the recorder has caught up, never a duration.
func awaitProduced(t *testing.T, db content.ContentDB, sid string, n uint64) {
	t.Helper()
	waittest.WaitForTimeout(t, "the recording to reach what was produced", wantWithin, func() bool {
		rec, err := db.SessionOutput().Read(context.Background(), sid)
		return err == nil && rec.Produced >= n
	})
}

// ── acceptance 1: output from BEFORE the ring window ─────────────────────

// A client that arrives an hour into a run recovers what the ring cannot
// hold. The session produces four ring-fulls with nobody attached; a fresh
// connection then reads the whole of it back, byte for byte, at offsets the
// ring discarded long before.
//
// It also exercises the per-answer budget: a recording larger than one
// answer arrives over several calls, and the client pages by asking again
// from where the last run ended. A page that silently stopped without
// saying there was more would look exactly like a shorter session.
func TestSessionOutput_RecoversWhatTheRingCouldNotHold(t *testing.T) {
	const total = 4 * RingCapacity
	db := openRecordingStore(t, total) // no cap-drop: this test is about the ring, not the bound

	term := newFeedablePTY()
	ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(db.SessionOutput()))
	defer stop()

	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	// The window closes. The hour of work happens with nobody watching.
	_ = conn.Close()
	awaitNoSubscriber(t, ws, session.ID(sid))

	stream := recordingStream(total)
	awaitPush(t, "four ring-fulls with nobody attached", pushOutput(term, stream))
	awaitProduced(t, db, sid, total)

	// The ring has moved on: this is the window a reattaching client used to
	// be limited to, and it must not start at zero, or the test would prove
	// nothing about recovering anything older.
	oldest := ws.getRx(session.ID(sid)).ring.oldestLocked()
	if oldest == 0 {
		t.Fatal("the ring still holds the first byte; nothing older than the window exists to recover")
	}

	fresh := connectWS(t, ws)
	got := make([]byte, 0, total)
	var produced uint64
	for id, at := 100, uint64(0); ; id++ {
		page := mustReadSessionOutput(t, fresh, id, map[string]any{"sessionId": sid, "from": at})
		if page.From != at {
			t.Fatalf("page answers from %d, asked from %d", page.From, at)
		}
		if len(page.Gaps) != 0 {
			t.Fatalf("a recording inside its cap reports %d gaps", len(page.Gaps))
		}
		produced = page.Produced
		if len(page.Runs) == 0 {
			break
		}
		for i, run := range page.Runs {
			if run.Offset != at {
				t.Fatalf("run %d starts at %d, want %d — an unbroken recording is one run per page", i, run.Offset, at)
			}
			body := page.bodyAt(t, i)
			got = append(got, body...)
			at += uint64(len(body))
		}
		if at >= produced {
			break
		}
	}

	if produced != total {
		t.Fatalf("produced = %d, want %d", produced, total)
	}
	if len(got) != total {
		t.Fatalf("recovered %d bytes of %d", len(got), total)
	}
	if !bytes.Equal(got, stream) {
		t.Fatal("what came back is not what the session produced")
	}
	// The point of the bead, stated as the assertion: the whole span the
	// ring had already discarded — [0, oldest) — came back anyway, and came
	// back as the bytes the session actually printed there.
	if !bytes.Equal(got[:oldest], stream[:oldest]) {
		t.Fatalf("the %d bytes the ring had discarded did not come back as the session printed them", oldest)
	}
	// And the two halves join with nothing between them: the recording's end
	// is an offset the ring can still replay from, so a client reads to here
	// and attaches here.
	if produced < oldest {
		t.Fatalf("the recording ends at %d and the ring's window starts at %d — a hole between the read and the attach", produced, oldest)
	}
}

// ── acceptance 2: two clients agree on the content ───────────────────────

// The session is attached to one client and read by another, and the two
// see the same thing: the same bytes at the same offsets, and the same
// effectiveSize to render them at. That is the whole of "without a grid on
// the backend" — the backend hands out bytes and a size, and agreement
// follows from the two being equal, not from anything it decided about a
// screen.
func TestSessionOutput_TwoClientsSeeTheSameContent(t *testing.T) {
	const total = 48 << 10
	db := openRecordingStore(t, 1<<20)

	term := newFeedablePTY()
	ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(db.SessionOutput()))
	defer stop()

	attached := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, attached, 1)
	sidBytes, _ := session.IDToBytes(session.ID(sid))

	stream := recordingStream(total)
	awaitPush(t, "the session's output", pushOutput(term, stream))

	// What the ATTACHED client received off the data plane, acking as a real
	// client does so the credit window keeps reopening (AD-10).
	received := make([]byte, 0, total)
	deadline := time.Now().Add(wantWithin)
	for len(received) < total && time.Now().Before(deadline) {
		_ = attached.SetReadDeadline(time.Now().Add(5 * time.Second))
		typ, payload, err := attached.ReadMessage()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if typ != websocket.BinaryMessage {
			continue
		}
		frame, decErr := DecodeFrame(payload)
		if decErr != nil || frame.MsgType != MsgTypeData || frame.SessionID != sidBytes {
			continue
		}
		received = append(received, frame.Payload...)
		if err := attached.WriteJSON(map[string]any{
			"jsonrpc": "2.0", "method": "ack",
			"params": map[string]any{"sessionId": sid, "offset": len(received)},
		}); err != nil {
			t.Fatalf("ack: %v", err)
		}
	}
	if len(received) < total {
		t.Fatalf("the attached client received %d of %d bytes", len(received), total)
	}
	awaitProduced(t, db, sid, total)

	// A SECOND client, holding nothing, reads the same session.
	observer := connectWS(t, ws)
	byObserver := mustReadSessionOutput(t, observer, 100, map[string]any{"sessionId": sid})
	if len(byObserver.Runs) != 1 {
		t.Fatalf("the recording is %d runs, want one unbroken stretch", len(byObserver.Runs))
	}
	if byObserver.Runs[0].Offset != 0 {
		t.Fatalf("the observer's run starts at %d, want the first byte", byObserver.Runs[0].Offset)
	}
	// By offset and in pieces, so a recording holding the right bytes at the
	// wrong coordinates cannot pass.
	body := byObserver.bodyAt(t, 0)
	if len(body) < len(received) {
		t.Fatalf("the observer sees %d bytes and the attached client received %d", len(body), len(received))
	}
	for at := 0; at < len(received); at += 4096 {
		end := at + 4096
		if end > len(received) {
			end = len(received)
		}
		if !bytes.Equal(body[at:end], received[at:end]) {
			t.Fatalf("the two clients disagree over bytes [%d,%d)", at, end)
		}
	}

	// The same answer to the same question, whoever asks: the attached
	// client reads too, and gets a payload identical to the observer's,
	// effectiveSize included.
	byAttachedRaw, rpcErr := readSessionOutput(t, attached, 101, map[string]any{"sessionId": sid})
	if rpcErr != nil {
		t.Fatalf("the attached client's read: %+v", rpcErr)
	}
	var byAttached wireSessionOutput
	if err := json.Unmarshal(byAttachedRaw, &byAttached); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if byAttached.EffectiveSize != byObserver.EffectiveSize {
		t.Fatalf("the two clients were given different sizes: %+v vs %+v",
			byAttached.EffectiveSize, byObserver.EffectiveSize)
	}
	if byAttached.EffectiveSize.Cols != 80 || byAttached.EffectiveSize.Rows != 24 {
		t.Fatalf("effectiveSize = %+v, want the size the session is running at", byAttached.EffectiveSize)
	}
	if !bytes.Equal(byAttached.bodyAt(t, 0), body) {
		t.Fatal("the two clients were given different bytes for the same session")
	}
}

// ── acceptance 3: what the retention bound dropped is SAID ───────────────

// The bound keeps the head and the tail and drops the middle
// (internal/content/policy.go). A read across that hole answers with what
// remains AND names the range that is gone — and every byte of the span
// asked for is accounted for as either a run or a gap, so "shorter" can
// never be mistaken for "that is all there was".
func TestSessionOutput_SaysWhatTheRetentionBoundDropped(t *testing.T) {
	const capBytes = 64 << 10
	const total = 4 * capBytes
	db := openRecordingStore(t, capBytes)

	term := newFeedablePTY()
	ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(db.SessionOutput()))
	defer stop()

	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	_ = conn.Close()
	awaitNoSubscriber(t, ws, session.ID(sid))

	stream := recordingStream(total)
	awaitPush(t, "far more than the bound keeps", pushOutput(term, stream))
	awaitProduced(t, db, sid, total)

	fresh := connectWS(t, ws)
	got := mustReadSessionOutput(t, fresh, 100, map[string]any{"sessionId": sid, "from": 0})

	if len(got.Runs) != 2 {
		t.Fatalf("the recording is %d runs, want a head and a tail with a hole between them", len(got.Runs))
	}
	if len(got.Gaps) != 1 {
		t.Fatalf("%d gaps reported, want the one the bound made", len(got.Gaps))
	}
	gap := got.Gaps[0]
	head, tail := got.Runs[0], got.Runs[1]
	if head.Offset != 0 {
		t.Errorf("the head starts at %d, want the first byte the session produced", head.Offset)
	}
	headEnd := head.Offset + uint64(len(got.bodyAt(t, 0)))
	if gap.Start != headEnd {
		t.Errorf("the gap starts at %d, want the end of the head (%d)", gap.Start, headEnd)
	}
	if gap.End != tail.Offset {
		t.Errorf("the gap ends at %d, want the start of the tail (%d)", gap.End, tail.Offset)
	}
	if gap.Reason != "cap" {
		t.Errorf("gap reason = %q, want the bound's own word", gap.Reason)
	}
	// The bytes that ARE there are the session's own, at their real offsets.
	if !bytes.Equal(got.bodyAt(t, 0), stream[:headEnd]) {
		t.Error("the head is not what the session produced")
	}
	tailBody := got.bodyAt(t, 1)
	if !bytes.Equal(tailBody, stream[tail.Offset:tail.Offset+uint64(len(tailBody))]) {
		t.Error("the tail is not what the session produced")
	}
	// Nothing is unstated: run + gap accounts for the whole span.
	var accounted uint64
	for i := range got.Runs {
		accounted += uint64(len(got.bodyAt(t, i)))
	}
	for _, g := range got.Gaps {
		accounted += g.End - g.Start
	}
	if accounted != got.Produced-got.From {
		t.Fatalf("runs and gaps account for %d bytes of the %d in [%d,%d)",
			accounted, got.Produced-got.From, got.From, got.Produced)
	}

	// And asking from INSIDE the hole is answered the same way: what remains,
	// and the range that is gone — never a silently shorter answer.
	inside := gap.Start + 1
	from := mustReadSessionOutput(t, fresh, 101, map[string]any{"sessionId": sid, "from": inside})
	if from.From != inside {
		t.Fatalf("answered from %d, asked from %d", from.From, inside)
	}
	if len(from.Gaps) != 1 || from.Gaps[0].Start != inside || from.Gaps[0].End != tail.Offset {
		t.Fatalf("gaps = %+v, want the remainder of the hole from %d to %d", from.Gaps, inside, tail.Offset)
	}
	if len(from.Runs) != 1 || from.Runs[0].Offset != tail.Offset {
		t.Fatalf("runs = %d starting at %d, want the tail at %d", len(from.Runs), from.Runs[0].Offset, tail.Offset)
	}
}

// ── refusals: a read names a session COMPLETELY ──────────────────────────

// The same three verdicts attach gives, because the same claim is being
// judged (judgeClaim). A read that answered "here is an empty recording" to
// an id this backend never held would be indistinguishable from a session
// that printed nothing.
func TestSessionOutput_RefusesAClaimItCannotResolve(t *testing.T) {
	term := newFeedablePTY()
	rec := newFakeRecorder()
	ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(rec))
	defer stop()

	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	for _, tc := range []struct {
		name   string
		params map[string]any
		reason string
	}{
		{
			name:   "a session this backend does not hold",
			params: map[string]any{"sessionId": "ffffffffffffffffffffffffffffffff"},
			reason: reasonUnknownSession,
		},
		{
			name:   "a binding written against another backend",
			params: map[string]any{"sessionId": sid, "instanceId": "0123456789abcdef0123456789abcdef"},
			reason: reasonForeignInstance,
		},
		{
			name:   "another incarnation of this session id",
			params: map[string]any{"sessionId": sid, "sessionEpoch": 99},
			reason: reasonForeignIncarnation,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, rpcErr := readSessionOutput(t, conn, 50, tc.params)
			if rpcErr == nil {
				t.Fatal("the claim was answered rather than refused")
			}
			if rpcErr.Code != -32602 {
				t.Errorf("code = %d, want -32602", rpcErr.Code)
			}
			data, _ := rpcErr.Data.(map[string]any)
			if data == nil || data["reason"] != tc.reason {
				t.Errorf("reason = %v, want %q", rpcErr.Data, tc.reason)
			}
		})
	}
}

// ── the failure path of the one external call this handler makes ─────────

// The store refuses the read: the caller is told, in the store's own words,
// and never handed an empty recording that reads as "this session printed
// nothing". Its paired success is every test above, each against a real
// encrypted store on a real file.
func TestSessionOutput_AStoreThatRefusesTheReadIsReported(t *testing.T) {
	term := newFeedablePTY()
	rec := newFakeRecorder()
	rec.setReadFailure(errors.New("the recording could not be decrypted"))
	ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(rec))
	defer stop()

	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	_, rpcErr := readSessionOutput(t, conn, 50, map[string]any{"sessionId": sid})
	if rpcErr == nil {
		t.Fatal("a refused read was answered as a recording")
	}
	if rpcErr.Code != -32603 {
		t.Errorf("code = %d, want -32603", rpcErr.Code)
	}
	if !bytes.Contains([]byte(rpcErr.Message), []byte("could not be decrypted")) {
		t.Errorf("message = %q, want the store's own words", rpcErr.Message)
	}
}

// With no store wired there is no recording to hand back, and the method
// says so as "method not found" — the caller's next move is to stop calling
// it, not to fix its arguments (registration.go's availability rule).
func TestSessionOutput_WithoutAStoreAnswersMethodNotFound(t *testing.T) {
	term := newFeedablePTY()
	ws, stop := newRecordingWSServer(t, term)
	defer stop()

	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	_, rpcErr := readSessionOutput(t, conn, 50, map[string]any{"sessionId": sid})
	if rpcErr == nil {
		t.Fatal("a method with no store behind it answered a recording")
	}
	if rpcErr.Code != -32601 {
		t.Errorf("code = %d, want -32601", rpcErr.Code)
	}
}

// A malformed session id never reaches the store: the shape is the honest
// check on a server-minted id (session.IDToBytes owns it), and it is refused
// before the handler runs.
func TestSessionOutput_RefusesAMalformedSessionID(t *testing.T) {
	term := newFeedablePTY()
	rec := newFakeRecorder()
	ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(rec))
	defer stop()

	conn := connectWS(t, ws)
	_ = openSessionOnConn(t, ws, conn, 1)

	_, rpcErr := readSessionOutput(t, conn, 50, map[string]any{"sessionId": "not-a-session"})
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("error = %+v, want -32602 for a malformed id", rpcErr)
	}
	if rec.readCount() != 0 {
		t.Errorf("the store was read %d times for a request that never had a valid id", rec.readCount())
	}
}
