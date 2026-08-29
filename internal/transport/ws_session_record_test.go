package transport

// The recorder, through the seam a person reaches (nocx-22k1c.1).
//
// What these assert is what a user can now do that they could not before:
// close the window on a session that is still working, and come back to find
// it still working and its output kept. The unit tests in ring_test.go say
// the ring frees itself; these say the product does the thing.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// ── pushing output through the package's feedable terminal ──────────────

// pushOutput writes bytes as if the program in the pane had printed them,
// from a goroutine, and reports when the last of them has been TAKEN.
//
// That is the observable this whole bead is about. feedablePTY is an
// io.Pipe: a write does not finish until the session's output pump reads it,
// and the pump does not read while ring.write is blocked. So "this channel
// closed" is exactly "the session is still consuming its terminal", and a
// stalled session shows up as a push that never returns rather than as a
// wrong value somewhere.
func pushOutput(term *feedablePTY, b []byte) <-chan error {
	done := make(chan error, 1)
	go func() {
		term.mu.Lock()
		defer term.mu.Unlock()
		_, err := term.pw.Write(b)
		done <- err
	}()
	return done
}

// awaitPush fails the test if the session stopped taking what it was given.
func awaitPush(t *testing.T, what string, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-time.After(wantWithin):
		t.Fatalf("%s: the session stopped reading its terminal", what)
	}
}

// ── a recorder a test can watch and break ────────────────────────────────

// fakeRecorder is the durable sink, in memory, with a switch for each way the
// real one can answer: it keeps, it refuses (retention off), or it fails.
type fakeRecorder struct {
	mu       sync.Mutex
	stream   []byte
	firstAt  uint64
	appends  int
	calls    int
	reads    int
	stance   content.SessionOutputStance
	failWith error
	readErr  error
}

func newFakeRecorder() *fakeRecorder {
	return &fakeRecorder{stance: content.SessionOutputKept}
}

func (r *fakeRecorder) Append(_ context.Context, in content.SessionOutputAppend) (content.SessionOutputResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.failWith != nil {
		return content.SessionOutputResult{}, r.failWith
	}
	if r.stance != content.SessionOutputKept {
		return content.SessionOutputResult{}, nil
	}
	if len(r.stream) == 0 {
		r.firstAt = in.Offset
	}
	r.stream = append(r.stream, in.Body...)
	r.appends++
	return content.SessionOutputResult{Kept: true}, nil
}

// Read hands back what this fake kept, as ONE run: the fake never drops, so
// a hole here would be a fiction. readErr is separate from failWith because
// a store that cannot be read and a store that cannot be written are
// different failures with different answers, and a test that could only set
// both together could not tell which one it had provoked.
func (r *fakeRecorder) Read(_ context.Context, _ string) (content.SessionOutputRecording, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if r.readErr != nil {
		return content.SessionOutputRecording{}, r.readErr
	}
	out := content.SessionOutputRecording{
		Bytes:    uint64(len(r.stream)),
		Produced: r.firstAt + uint64(len(r.stream)),
	}
	if len(r.stream) > 0 {
		out.Runs = []content.SessionOutputRun{{
			Offset: r.firstAt,
			Body:   append([]byte(nil), r.stream...),
		}}
	}
	return out, nil
}

func (r *fakeRecorder) setReadFailure(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readErr = err
}

func (r *fakeRecorder) readCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

func (r *fakeRecorder) Stance() content.SessionOutputStance {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stance
}

func (r *fakeRecorder) setStance(s content.SessionOutputStance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stance = s
}

func (r *fakeRecorder) setFailure(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failWith = err
}

func (r *fakeRecorder) appendCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *fakeRecorder) recorded() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.stream...)
}

// ── the harness ──────────────────────────────────────────────────────────

func newRecordingWSServer(t *testing.T, term *feedablePTY, extra ...WSServerOption) (*WSServer, func()) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, session.New(logger, &feedableFactory{p: term}), extra...)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, func() { _ = ws.Stop(ctx) }
}

// awaitNoSubscriber is the moment the interval OPENS: the last client has
// detached and the ring's consumer is the recorder from here on.
func awaitNoSubscriber(t *testing.T, ws *WSServer, sid session.ID) {
	t.Helper()
	waittest.WaitForTimeout(t, "the last client to detach", wantWithin, func() bool {
		rx := ws.getRx(sid)
		if rx == nil {
			return false
		}
		wconn, _ := rx.getSubscriber()
		return wconn == nil
	})
}

func awaitRecorded(t *testing.T, rec *fakeRecorder, n int) {
	t.Helper()
	waittest.WaitForTimeout(t, "the recorder to keep what was produced", wantWithin, func() bool {
		return len(rec.recorded()) >= n
	})
}

// deterministic, so an off-by-one in the offsets is visible.
func recordingStream(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i*13 + i/97)
	}
	return out
}

// ── acceptance ───────────────────────────────────────────────────────────

// 1. Output produced while NO client is attached is on disk afterwards — a
// real encrypted store, read back off the file, not a fake.
func TestSessionRecording_DetachedOutputIsOnDiskAfterwards(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	db, err := content.Open(context.Background(), content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	term := newFeedablePTY()
	ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(db.SessionOutput()))
	defer stop()

	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	// The window closes. Everything below happens with nobody watching.
	_ = conn.Close()
	awaitNoSubscriber(t, ws, session.ID(sid))

	stream := recordingStream(40 << 10)
	awaitPush(t, "the detached session's output", pushOutput(term, stream))

	waittest.WaitForTimeout(t, "the store to hold what was produced while detached", wantWithin, func() bool {
		rec, readErr := db.SessionOutput().Read(context.Background(), sid)
		return readErr == nil && rec.Bytes >= uint64(len(stream))
	})

	rec, err := db.SessionOutput().Read(context.Background(), sid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rec.Runs) != 1 {
		t.Fatalf("the recording is %d runs, want one unbroken stretch", len(rec.Runs))
	}
	if rec.Runs[0].Offset != 0 {
		t.Errorf("the recording starts at %d, want the first byte the session produced", rec.Runs[0].Offset)
	}
	if !bytes.Equal(rec.Runs[0].Body[:len(stream)], stream) {
		t.Error("what is on disk is not what the session produced")
	}
}

// 2. The recorded stream is the same bytes the client received, CHECKED BY
// OFFSET: every data frame the socket delivered is compared against the
// recording at the coordinate it arrived on.
func TestSessionRecording_ReplaysToTheBytesTheClientReceived(t *testing.T) {
	term := newFeedablePTY()
	rec := newFakeRecorder()
	ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(rec))
	defer stop()

	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	sidBytes, _ := session.IDToBytes(session.ID(sid))

	stream := recordingStream(96 << 10)
	awaitPush(t, "the session's output", pushOutput(term, stream))

	// Collect the data frames for this session until the client has as many
	// bytes as were produced. The frames arrive in order and the offsets are
	// implicit in that order — which is exactly the coordinate the recorder
	// is keying on, so the comparison is meaningful.
	received := make([]byte, 0, len(stream))
	deadline := time.Now().Add(wantWithin)
	for len(received) < len(stream) && time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		typ, payload, readErr := conn.ReadMessage()
		if readErr != nil {
			t.Fatalf("read frame: %v", readErr)
		}
		if typ != websocket.BinaryMessage {
			continue
		}
		frame, decErr := DecodeFrame(payload)
		if decErr != nil || frame.MsgType != MsgTypeData || frame.SessionID != sidBytes {
			continue
		}
		received = append(received, frame.Payload...)
		// Ack as a real client does. Without it the credit window (AD-10)
		// shuts after CreditLimit bytes and the rest never arrives — which
		// is the flow control working, not a defect, and the test has to
		// play the client's part rather than route around it.
		if err := conn.WriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"method":  "ack",
			"params":  map[string]any{"sessionId": sid, "offset": len(received)},
		}); err != nil {
			t.Fatalf("ack: %v", err)
		}
	}
	if len(received) < len(stream) {
		t.Fatalf("the client received %d of %d bytes", len(received), len(stream))
	}
	awaitRecorded(t, rec, len(stream))

	got := rec.recorded()
	// By offset, and in pieces, so a recording that held the right bytes at
	// the wrong coordinates cannot pass.
	for at := 0; at < len(received); at += 4096 {
		end := at + 4096
		if end > len(received) {
			end = len(received)
		}
		if !bytes.Equal(got[at:end], received[at:end]) {
			t.Fatalf("the recording and the client disagree over bytes [%d,%d)", at, end)
		}
	}
}

// 3. THE STALL, end to end: a detached session produces far more than the
// ring can hold, and is still alive and still writing at the end.
func TestSessionRecording_DetachedSessionOutlivesTheRing(t *testing.T) {
	term := newFeedablePTY()
	rec := newFakeRecorder()
	ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(rec))
	defer stop()

	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	_ = conn.Close()
	awaitNoSubscriber(t, ws, session.ID(sid))

	const total = 4 * RingCapacity
	stream := recordingStream(total)
	// THE STALL: without a consumer this push stops after RingCapacity bytes
	// and never returns.
	awaitPush(t, "four ring-fulls with nobody attached", pushOutput(term, stream))
	awaitRecorded(t, rec, total)

	// Still alive and still writing at the end — the acceptance says the
	// session keeps going, not merely that it got there.
	tail := recordingStream(4096)
	awaitPush(t, "the session's output after the burst", pushOutput(term, tail))
	waittest.WaitForTimeout(t, "the tail to be recorded too", wantWithin, func() bool {
		return len(rec.recorded()) >= total+len(tail)
	})
	if got := rec.recorded(); !bytes.Equal(got[:total], stream) {
		t.Error("the recording is not what the session produced")
	}
}

// 6. The failure path for the store call the recorder makes, paired with the
// success: a refusing store is stated in the product, through the one surface
// that already says durable history is not working — and the statement closes
// when writes succeed again.
func TestSessionRecording_StoreFailureIsStatedInTheProduct(t *testing.T) {
	term := newFeedablePTY()
	rec := newFakeRecorder()
	rec.setFailure(errors.New("disk is full"))
	st := NewHistoryStatus()
	ws, stop := newRecordingWSServer(t, term,
		WithSessionOutputRecorder(rec), WithHistoryStatus(st))
	defer stop()

	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	_ = conn.Close()
	awaitNoSubscriber(t, ws, session.ID(sid))
	awaitPush(t, "output into a refusing store", pushOutput(term, recordingStream(4096)))

	waittest.WaitForTimeout(t, "the degrade to be raised", wantWithin, func() bool {
		return !st.Available()
	})
	got := st.snapshot()
	if got.Reason == nil || *got.Reason != string(HistoryDegradeWriteFailed) {
		t.Fatalf("reason = %v, want writeFailed", got.Reason)
	}
	if got.Detail == nil || *got.Detail != "disk is full" {
		t.Errorf("detail = %v, want the store's own words", got.Detail)
	}
}

// The paired success, and the closing event of that interval: a store that
// keeps what it is given closes an open write-failure episode. A failure test
// with no success beside it proves only that the code can fail.
func TestSessionRecording_AWorkingStoreClosesTheDegrade(t *testing.T) {
	term := newFeedablePTY()
	rec := newFakeRecorder()
	st := NewHistoryStatus()
	// An episode from an earlier attempt, still open.
	st.Raise(HistoryDegradeWriteFailed, "disk was full")
	ws, stop := newRecordingWSServer(t, term,
		WithSessionOutputRecorder(rec), WithHistoryStatus(st))
	defer stop()

	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	_ = conn.Close()
	awaitNoSubscriber(t, ws, session.ID(sid))
	awaitPush(t, "output into a working store", pushOutput(term, recordingStream(4096)))

	awaitRecorded(t, rec, 4096)
	waittest.WaitForTimeout(t, "the degrade to close", wantWithin, func() bool {
		return st.Available()
	})
}

// 5. With recording off, the degrade is stated IN THE PRODUCT — off the real
// socket, in the answer the Settings screen reads on every render — and not
// only in a log line.
func TestHistoryStatus_SaysWhetherADetachedSessionIsRecorded(t *testing.T) {
	type wire struct {
		DetachedOutput struct {
			Recorded bool    `json:"recorded"`
			Reason   *string `json:"reason"`
		} `json:"detachedOutput"`
	}
	for _, tc := range []struct {
		name     string
		stance   content.SessionOutputStance
		recorded bool
		reason   string
	}{
		{"recording", content.SessionOutputKept, true, ""},
		{"history off", content.SessionOutputHistoryOff, false, "historyOff"},
		{"output retention off", content.SessionOutputRetentionOff, false, "outputOff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newFakeRecorder()
			rec.setStance(tc.stance)
			term := newFeedablePTY()
			ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(rec))
			defer stop()

			conn := connectWS(t, ws)
			resp := vaultCall(t, conn, "history.status", map[string]any{}, 1)
			if resp.Error != nil {
				t.Fatalf("history.status: %+v", resp.Error)
			}
			var got wire
			if err := json.Unmarshal(resp.Result, &got); err != nil {
				t.Fatalf("decode: %v\nraw: %s", err, resp.Result)
			}
			if got.DetachedOutput.Recorded != tc.recorded {
				t.Errorf("recorded = %v, want %v", got.DetachedOutput.Recorded, tc.recorded)
			}
			if tc.reason == "" {
				if got.DetachedOutput.Reason != nil {
					t.Errorf("reason = %q, want null while it is being recorded", *got.DetachedOutput.Reason)
				}
				return
			}
			if got.DetachedOutput.Reason == nil || *got.DetachedOutput.Reason != tc.reason {
				t.Errorf("reason = %v, want %q", got.DetachedOutput.Reason, tc.reason)
			}
		})
	}
}

// A server with no recorder wired says the same thing rather than claiming a
// capability it does not have: this is what the terminal runs as when the
// content key could not be read.
func TestHistoryStatus_NoRecorderIsNotRecording(t *testing.T) {
	term := newFeedablePTY()
	ws, stop := newRecordingWSServer(t, term)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.status", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("history.status: %+v", resp.Error)
	}
	var got struct {
		DetachedOutput struct {
			Recorded bool    `json:"recorded"`
			Reason   *string `json:"reason"`
		} `json:"detachedOutput"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.DetachedOutput.Recorded {
		t.Fatal("a server with no recorder claimed a detached session is being recorded")
	}
	if got.DetachedOutput.Reason == nil {
		t.Fatal("recorded is false with no reason; the person is told nothing")
	}
}

// Retention off means the recorder consumes nothing, so the ring goes back to
// client acks alone and the detached session throttles once it fills. That is
// the accepted degrade and it must be exactly what happens — not a silent
// drop of the bytes, which would be AD-10 broken instead of applied.
func TestSessionRecording_RetentionOffKeepsNothingAndDropsNothing(t *testing.T) {
	term := newFeedablePTY()
	rec := newFakeRecorder()
	rec.setStance(content.SessionOutputRetentionOff)
	ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(rec))
	defer stop()

	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	_ = conn.Close()
	awaitNoSubscriber(t, ws, session.ID(sid))

	awaitPush(t, "output with retention off", pushOutput(term, recordingStream(64<<10)))
	// Nothing is kept, and nothing is claimed to be.
	time.Sleep(100 * time.Millisecond)
	if got := rec.recorded(); len(got) != 0 {
		t.Fatalf("recorder kept %d bytes with retention off", len(got))
	}
	// What was produced is still in the ring, unacked and undropped: the
	// session did not lose it, it is waiting for somebody to want it.
	rx := ws.getRx(session.ID(sid))
	if rx == nil {
		t.Fatal("the session lost its ring")
	}
	waittest.WaitForTimeout(t, "the ring to hold what was produced", wantWithin, func() bool {
		data, _, needsReset := rx.ring.snapshot(0)
		return !needsReset && len(data) == 64<<10
	})
}

// The recorder's ONE wait that is not on the ring — the poll after the store
// declined to keep anything — has to end when the session does. Without that,
// a person with output retention off accumulates one goroutine per session
// they ever opened, each polling a terminal that is gone.
//
// The interval, both ends: the loop lives from pumpToRing until the ring
// closes, on every path it can take, and this is the path that had no second
// end at all.
func TestSessionRecording_TheLoopEndsWhenTheSessionDoes(t *testing.T) {
	rec := newFakeRecorder()
	rec.setStance(content.SessionOutputRetentionOff)
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithSessionOutputRecorder(rec))
	ring := newOutputRing()

	done := make(chan struct{})
	go func() {
		ws.recordSessionOutput(context.Background(), session.ID("gone"), ring)
		close(done)
	}()

	// Something to keep, and a store that will not keep it: the loop is now
	// in its poll.
	if err := ring.write(recordingStream(1024)); err != nil {
		t.Fatalf("write: %v", err)
	}
	waittest.WaitForTimeout(t, "the recorder to reach its poll", wantWithin, func() bool {
		return rec.appendCount() > 0
	})

	ring.close()
	select {
	case <-done:
	case <-time.After(wantWithin):
		t.Fatal("the recorder outlived the session it was recording")
	}
}
