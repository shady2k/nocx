package transport

// The two upload notifications, and the asymmetry between them (spec §5.3).
//
// files.uploadProgress is live and lossy: addressed to the binding's
// session's CURRENT subscriber, resolved at emit time, dropped when there
// is none. files.uploadDone is retained when there is none and flushed on
// attach. The first test in this file is the reason the asymmetry exists —
// without it a person whose laptop slept through a 400 MB upload comes back
// to a UI that says "uploading" about a transfer that finished ten minutes
// ago.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transfer"
)

// ── helpers ───────────────────────────────────────────────────────────────

// retainedCount reads the depth of a session's retained-outcome queue. A
// test-only accessor, in the test file: production has no reader for it,
// and a production method only tests call is dead code with a docstring.
func (u *transferRegistry) retainedCount(sid session.ID) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.retained[sid])
}

// containsString is the "was this path announced" predicate.
func containsString(haystack []string, want string) bool {
	for _, s := range haystack {
		if s == want {
			return true
		}
	}
	return false
}

// drainNotifications collects every notification of one method arriving in
// a window. It is used ONLY for absence assertions where the window's end
// is not the assertion — the presence half is always awaited on an
// observable state change.
func drainNotifications(t *testing.T, conn *websocket.Conn, method string, d time.Duration) []json.RawMessage {
	t.Helper()
	var got []json.RawMessage
	deadline := time.Now().Add(d)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return got
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return got
		}
		var n struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params json.RawMessage  `json:"params"`
		}
		if err := json.Unmarshal(msg, &n); err != nil {
			continue
		}
		if n.ID == nil && n.Method == method {
			got = append(got, n.Params)
		}
	}
}

// postUploadAsync sends a body from another goroutine, for the tests whose
// assertion is what arrives on the WEBSOCKET while the POST is still open —
// the handler holds the request until the transfer settles, so a
// synchronous post would deadlock against reading the notifications it
// produces. It reports nothing: the transfer's own outcome is the
// assertion, and a t.Fatalf off the test goroutine is not allowed anyway.
func postUploadAsync(ws *WSServer, ticket string, body []byte) {
	resp, err := uploadHTTPClient.Post(uploadURLFor(ws, ticket), "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// pausingSinkReported is the byte count the pausing sink announces before
// it stops. Any non-zero number does; naming it keeps the assertion and
// the sink from drifting apart.
const pausingSinkReported int64 = 2048

// pausingSink reports progress once and then holds inside Put until the
// test releases it. It is what makes a progress notification DETERMINISTIC
// rather than merely likely: the emitter selects between the mailbox and
// the transfer's done channel, so a transfer that can finish while the
// notification is being built could legitimately emit nothing at all, and
// a test written against the real sink would be a race with a long fuse.
type pausingSink struct {
	reported  chan struct{}
	released  chan struct{}
	closeOnce sync.Once
}

func newPausingSink() *pausingSink {
	return &pausingSink{reported: make(chan struct{}), released: make(chan struct{})}
}

func (s *pausingSink) Put(_ context.Context, u transfer.Upload, r io.Reader, progress func(int64)) (transfer.Outcome, error) {
	if r != nil {
		_, _ = io.Copy(io.Discard, r)
	}
	progress(pausingSinkReported)
	close(s.reported)
	<-s.released
	return transfer.Outcome{State: transfer.StateWritten, FinalName: u.Name}, nil
}

func (s *pausingSink) release() { s.closeOnce.Do(func() { close(s.released) }) }

// failingSink is a write half that fails and says what it left behind. An
// error and a non-empty Outcome.Stranded are not alternatives (the sink's
// own contract), which is the shape files.uploadDone has to carry.
type failingSink struct {
	err      error
	stranded []string
}

func (f *failingSink) Put(_ context.Context, _ transfer.Upload, r io.Reader, progress func(int64)) (transfer.Outcome, error) {
	if r != nil {
		n, _ := io.Copy(io.Discard, r)
		progress(n)
	}
	return transfer.Outcome{Stranded: f.stranded}, f.err
}

// dropSubscriber closes the connection and waits for the SERVER to observe
// it — an observable state change, never a duration. From here the session
// has no subscriber, which is the state both notifications are defined
// against and the state they answer differently.
func dropSubscriber(t *testing.T, e *filesTestEnv, sid string) {
	t.Helper()
	rx := e.ws.getRx(session.ID(sid))
	if rx == nil {
		t.Fatalf("no rx for session %s", sid)
	}
	_ = e.conn.Close()
	waitFor(t, "the server to observe the drop", wantWithin, func() bool {
		c, _ := rx.getSubscriber()
		return c == nil
	})
}

// reattach opens a NEW connection and attaches it to the session, the way
// an AD-9 reconnect does.
func reattach(t *testing.T, e *filesTestEnv, sid string, id int) *websocket.Conn {
	t.Helper()
	conn := connectWS(t, e.ws)
	t.Cleanup(func() { _ = conn.Close() })
	raw := jsonrpcCallWithID(t, conn, "attach", map[string]any{
		"sessionId": sid,
		"offset":    0,
	}, id)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("attach: unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("attach: %+v", env.Error)
	}
	return conn
}

// uploadDone is files.uploadDone decoded the way a renderer decodes it.
type uploadDone struct {
	TransferID string   `json:"transferId"`
	Outcome    string   `json:"outcome"`
	FinalName  string   `json:"finalName"`
	Error      string   `json:"error"`
	Stranded   []string `json:"stranded"`
}

func readUploadDone(t *testing.T, conn *websocket.Conn) uploadDone {
	t.Helper()
	raw := readNotification(t, conn, "files.uploadDone", wantWithin)
	var got uploadDone
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("files.uploadDone: decode: %v", err)
	}
	return got
}

// ── the reconnect ─────────────────────────────────────────────────────────

// TestUploadDone_SurvivesAReconnectWithNoSubscriber is the reason Task 7
// exists. A person starts an upload, the laptop sleeps, the WebSocket
// drops, the transfer finishes anyway on its own session-bounded context,
// and the person comes back. Addressed the way progress is — current
// subscriber, dropped when there is none — the terminal outcome would be
// emitted into nothing and the UI would say "uploading" for the rest of
// the session about a transfer that is over.
func TestUploadDone_SurvivesAReconnectWithNoSubscriber(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	body := []byte("the laptop slept while this was in flight")
	started := callUpload(t, e.conn, uploadParams(bid, dir, "late.txt", int64(len(body))), 3).mustResult(t)
	if started.Ticket == "" {
		t.Fatalf("want the stream branch, got %+v", started)
	}

	dropSubscriber(t, e, sid)

	// The transfer finishes with nobody attached: the POST is its own
	// connection and the transfer is bounded by the SESSION, not by the
	// WebSocket that started it.
	if code, resp := postUpload(t, e.ws, started.Ticket, body); code != http.StatusOK {
		t.Fatalf("POST /upload = %d %q, want 200", code, resp)
	}
	if state := awaitTransferState(t, e.ws, started.TransferID); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
	// #nosec G304 — a path the test itself built under t.TempDir().
	if got, err := os.ReadFile(filepath.Join(dir, "late.txt")); err != nil || string(got) != string(body) { //nolint:gosec // see above
		t.Fatalf("destination = %q, %v; want the uploaded bytes", got, err)
	}

	// Re-attach: the outcome the person could not be told is told now.
	connB := reattach(t, e, sid, 4)
	done := readUploadDone(t, connB)
	if done.TransferID != started.TransferID {
		t.Errorf("transferId = %q, want %q", done.TransferID, started.TransferID)
	}
	if done.Outcome != uploadStateWritten {
		t.Errorf("outcome = %q, want %q", done.Outcome, uploadStateWritten)
	}
	if done.FinalName != "late.txt" {
		t.Errorf("finalName = %q, want %q", done.FinalName, "late.txt")
	}
}

// TestUploadDone_RetentionIsClearedOnDelivery is the other half of the
// retention claim. A flush that delivered without clearing would replay
// every finished transfer on every reconnect for the rest of the session.
func TestUploadDone_RetentionIsClearedOnDelivery(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	body := []byte("once, not on every reconnect")
	started := callUpload(t, e.conn, uploadParams(bid, dir, "once.txt", int64(len(body))), 3).mustResult(t)
	dropSubscriber(t, e, sid)
	if code, resp := postUpload(t, e.ws, started.Ticket, body); code != http.StatusOK {
		t.Fatalf("POST /upload = %d %q, want 200", code, resp)
	}
	if state := awaitTransferState(t, e.ws, started.TransferID); state != uploadStateWritten {
		t.Fatalf("state = %q", state)
	}

	connB := reattach(t, e, sid, 4)
	if done := readUploadDone(t, connB); done.TransferID != started.TransferID {
		t.Fatalf("transferId = %q, want %q", done.TransferID, started.TransferID)
	}
	if n := e.ws.transfers.retainedCount(session.ID(sid)); n != 0 {
		t.Fatalf("%d outcomes still retained after delivery, want 0", n)
	}

	// A second reattach must be told nothing: the outcome was consumed by
	// the first one.
	_ = connB.Close()
	waitFor(t, "the server to observe the second drop", wantWithin, func() bool {
		c, _ := e.ws.getRx(session.ID(sid)).getSubscriber()
		return c == nil
	})
	connC := reattach(t, e, sid, 5)
	if got := drainNotifications(t, connC, "files.uploadDone", 300*time.Millisecond); len(got) != 0 {
		t.Fatalf("a second reattach replayed %d outcomes, want 0", len(got))
	}
}

// TestUploadDone_RetentionIsBounded pins the bound. Retention that grew
// without a ceiling would be an unbounded queue keyed by a session nobody
// ever comes back to.
func TestUploadDone_RetentionIsBounded(t *testing.T) {
	var reg transferRegistry
	sid := session.ID("s1")
	for i := 0; i < maxRetainedDone+10; i++ {
		reg.retainDone(sid, retainedDone{
			method: "files.uploadDone",
			params: mustMarshal(filesUploadDoneParams{
				TransferID: fmt.Sprintf("t%03d", i), Outcome: uploadStateWritten, Stranded: []string{},
			}),
		})
	}
	if n := reg.retainedCount(sid); n != maxRetainedDone {
		t.Fatalf("retained %d outcomes, want the bound %d", n, maxRetainedDone)
	}
	// The OLDEST went, not the newest: what a returning person is looking
	// at is the recent end.
	first, ok := reg.popDone(sid)
	if !ok {
		t.Fatal("nothing retained")
	}
	var got filesUploadDoneParams
	if err := json.Unmarshal(first.params, &got); err != nil {
		t.Fatalf("decode retained outcome: %v", err)
	}
	if want := "t010"; got.TransferID != want {
		t.Fatalf("oldest surviving = %q, want %q — the bound must drop the oldest", got.TransferID, want)
	}
}

// TestUploadDone_RetentionEndsWithTheSession: a session nobody can attach
// to again can have no reader, so its outcomes are dropped rather than kept
// forever.
func TestUploadDone_RetentionEndsWithTheSession(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	body := []byte("nobody is coming back for this")
	started := callUpload(t, e.conn, uploadParams(bid, dir, "gone.txt", int64(len(body))), 3).mustResult(t)
	dropSubscriber(t, e, sid)
	if code, resp := postUpload(t, e.ws, started.Ticket, body); code != http.StatusOK {
		t.Fatalf("POST /upload = %d %q, want 200", code, resp)
	}
	awaitTransferState(t, e.ws, started.TransferID)
	if n := e.ws.transfers.retainedCount(session.ID(sid)); n != 1 {
		t.Fatalf("retained %d, want 1 before the session ends", n)
	}

	// The real teardown entry point, not the leaf it calls: closing a
	// terminal closes its bindings and, with them, its transfers.
	e.ws.filesSessionClosed(session.ID(sid))
	if n := e.ws.transfers.retainedCount(session.ID(sid)); n != 0 {
		t.Fatalf("retained %d after the session ended, want 0", n)
	}
}

// ── progress is lossy, and that is not a failure ─────────────────────────

// TestUploadProgress_EveryNotificationDroppedStillCompletes is the claim
// stated as an assertion: progress is an INDICATOR, not a ledger. A
// transfer with no subscriber for its entire life — the laptop asleep, the
// socket gone — writes exactly the same bytes to exactly the same place and
// reports exactly the same outcome as one somebody watched.
func TestUploadProgress_EveryNotificationDroppedStillCompletes(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	// Several times the sink's 1 KiB test chunk, so the copy loop reports
	// progress many times over and every one of those reports is dropped.
	body := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64 KiB, 64 chunks
	started := callUpload(t, e.conn, uploadParams(bid, dir, "unwatched.bin", int64(len(body))), 3).mustResult(t)

	dropSubscriber(t, e, sid)

	if code, resp := postUpload(t, e.ws, started.Ticket, body); code != http.StatusOK {
		t.Fatalf("POST /upload = %d %q, want 200", code, resp)
	}
	if state := awaitTransferState(t, e.ws, started.TransferID); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
	// #nosec G304 — a path the test itself built under t.TempDir().
	got, err := os.ReadFile(filepath.Join(dir, "unwatched.bin")) //nolint:gosec // see above
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("destination is %d bytes, want %d — an unwatched transfer must write the same file a watched one does", len(got), len(body))
	}
	// The sink DID report progress: the notifications were dropped, not
	// the reporting. Without this the test would also pass on a transfer
	// that never called progress at all.
	rt := e.ws.transfers.get(started.TransferID)
	if rt == nil {
		t.Fatal("the transfer left the registry before it could be inspected")
	}
	if _, _, sent, _ := rt.snapshot(); sent != int64(len(body)) {
		t.Fatalf("progress reported %d bytes, want %d", sent, len(body))
	}
}

// TestUploadProgress_CoalescesToOneInFlight is the flood bound, asserted on
// the mailbox rather than on a stopwatch. A thousand chunks in a row leave
// exactly ONE pending notification, and it carries the newest byte count —
// which is what keeps a fast local link from filling the connection's
// refreshable queue and tripping the stall notice that makes the renderer
// reconnect.
func TestUploadProgress_CoalescesToOneInFlight(t *testing.T) {
	rt := &runningTransfer{
		id:           "t",
		done:         make(chan struct{}),
		progressWake: make(chan struct{}, 1),
	}
	for i := int64(1); i <= 1000; i++ {
		rt.progress(i)
	}
	if n := len(rt.progressWake); n != 1 {
		t.Fatalf("%d notifications pending after 1000 chunks, want at most 1 in flight", n)
	}
	if _, _, sent, _ := rt.snapshot(); sent != 1000 {
		t.Fatalf("the pending notification would carry %d bytes, want the newest count 1000", sent)
	}
	// Draining it leaves nothing behind: 1000 chunks cost one frame, not a
	// queue of 1000 that merely started late.
	<-rt.progressWake
	if n := len(rt.progressWake); n != 0 {
		t.Fatalf("%d notifications still queued after one was taken, want 0", n)
	}
}

// TestUploadProgress_ReachesTheCurrentSubscriber is the paired success. A
// lossy indicator that was never emitted at all would satisfy every test
// above.
func TestUploadProgress_ReachesTheCurrentSubscriber(t *testing.T) {
	sink := newPausingSink()
	e := newUploadTestEnvWithSink(t, sink)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	// onExists:"skip" is the branch that needs no body, so the transfer
	// runs the moment files.upload answers and this test needs no HTTP at
	// all. The sink decides the outcome, not the decision.
	params := uploadParams(bid, dir, "watched.bin", 4096)
	params["onExists"] = "skip"
	started := callUpload(t, e.conn, params, 3).mustResult(t)

	// The sink is holding inside Put, having reported progress. The
	// transfer cannot settle until this test lets it, so the emitter
	// cannot be racing rt.done: the notification below is guaranteed, not
	// merely likely.
	<-sink.reported
	raw := readNotification(t, e.conn, "files.uploadProgress", wantWithin)
	var got filesTransferProgressParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("files.uploadProgress: decode: %v", err)
	}
	if got.TransferID != started.TransferID {
		t.Errorf("transferId = %q, want %q", got.TransferID, started.TransferID)
	}
	if got.Bytes != pausingSinkReported {
		t.Errorf("bytes = %d, want %d", got.Bytes, pausingSinkReported)
	}
	if got.Total != 4096 {
		t.Errorf("total = %d, want the declared size 4096", got.Total)
	}
	sink.release()
	if state := awaitTransferState(t, e.ws, started.TransferID); state != uploadStateWritten {
		t.Fatalf("state = %q", state)
	}
}

// ── §5.5 "Refresh": the row appears with nobody pressing anything ────────

// newRefreshEnv boots an upload environment whose poll loop cannot fire
// during the test. The digest poll would eventually notice a new file on
// its own, and a test that let it run could not tell "the upload
// invalidated the directory" from "the poll got there first" — which is
// exactly the difference an OVERWRITE depends on, where the name, the size
// and possibly the digest are all unchanged.
func newRefreshEnv(t *testing.T, opts ...WSServerOption) *filesTestEnv {
	t.Helper()
	e := newUploadTestEnv(t, opts...)
	e.ws.filesPollInterval = time.Hour // read by filesPollLoop at files.watch
	return e
}

// newRefreshEnvWithSink is newRefreshEnv with the binding's write half
// chosen by the test.
func newRefreshEnvWithSink(t *testing.T, sink transfer.Sink) *filesTestEnv {
	t.Helper()
	e := newUploadTestEnvWithSink(t, sink)
	e.ws.filesPollInterval = time.Hour
	return e
}

// awaitDoneCollectingChanges reads until files.uploadDone arrives and
// returns every files.changed seen on the way. settleUpload invalidates
// before it announces the outcome, so "before the done" is the whole window
// in which a refresh for this transfer could appear — which makes the
// absence assertion as deterministic as the presence one.
func awaitDoneCollectingChanges(t *testing.T, conn *websocket.Conn) (uploadDone, []string) {
	t.Helper()
	var changed []string
	deadline := time.Now().Add(wantWithin)
	for {
		msg, err := awaitFrame(conn, deadline, isAnyNotification("files.changed", "files.uploadDone"))
		if err != nil {
			t.Fatalf("waiting for files.uploadDone: %v", err)
		}
		n, _ := decodeFrame(msg)
		switch n.Method {
		case "files.changed":
			var p filesChangedParams
			if err := json.Unmarshal(n.Params, &p); err == nil {
				changed = append(changed, p.Path)
			}
		case "files.uploadDone":
			var done uploadDone
			if err := json.Unmarshal(n.Params, &done); err != nil {
				t.Fatalf("files.uploadDone: decode: %v", err)
			}
			return done, changed
		}
	}
}

// TestUploadWritten_InvalidatesTheDestinationDirectory is spec §5.5's
// "Refresh" clause — the one requirement of the design that had no task
// until the plan's self-review caught it. Without it a person uploads a
// file, is told it landed, and looks at a listing that does not contain it
// until they press something.
func TestUploadWritten_InvalidatesTheDestinationDirectory(t *testing.T) {
	e := newRefreshEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	e.watchDir(t, bid, []string{dir}, 3)

	body := []byte("a row that appears by itself")
	started := callUpload(t, e.conn, uploadParams(bid, dir, "fresh.txt", int64(len(body))), 4).mustResult(t)
	go postUploadAsync(e.ws, started.Ticket, body)

	done, changed := awaitDoneCollectingChanges(t, e.conn)
	if done.Outcome != uploadStateWritten {
		t.Fatalf("outcome = %q, want %q", done.Outcome, uploadStateWritten)
	}
	if !containsString(changed, dir) {
		t.Fatalf("files.changed paths = %v; a written upload must invalidate %s so the panel re-lists it", changed, dir)
	}
}

// TestUploadSkipped_DoesNotInvalidate is the other direction, and it is
// what stops the invalidation being "announce a change after every
// transfer". Nothing was written, so nothing changed, and a refresh here
// would be a listing re-fetched to show the same rows.
func TestUploadSkipped_DoesNotInvalidate(t *testing.T) {
	e := newRefreshEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	// Something is already there, and the person chose to keep it.
	if err := os.WriteFile(filepath.Join(dir, "taken.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e.watchDir(t, bid, []string{dir}, 3)

	params := uploadParams(bid, dir, "taken.txt", 4)
	params["onExists"] = "skip"
	started := callUpload(t, e.conn, params, 4).mustResult(t)
	if started.Ticket != "" {
		t.Fatalf("skip needs no body, got a ticket: %+v", started)
	}

	done, changed := awaitDoneCollectingChanges(t, e.conn)
	if done.Outcome != uploadStateSkipped {
		t.Fatalf("outcome = %q, want %q", done.Outcome, uploadStateSkipped)
	}
	if len(changed) != 0 {
		t.Fatalf("files.changed for %v after a skipped upload; nothing was written, so nothing changed", changed)
	}
	// #nosec G304 — a path the test itself built under t.TempDir().
	if got, err := os.ReadFile(filepath.Join(dir, "taken.txt")); err != nil || string(got) != "mine" { //nolint:gosec // see above
		t.Fatalf("destination = %q, %v; skip must not touch it", got, err)
	}
}

// TestUploadFailed_DoesNotInvalidate, and carries the account of what it
// left behind. A failure that invalidated would tell the panel to re-list a
// directory whose contents it did not change; a failure that flattened
// stranded[] to one field would tell a person about one of the two files
// now sitting on their disk.
func TestUploadFailed_DoesNotInvalidate(t *testing.T) {
	stranded := []string{"/dest/.nocx-upload-1", "/dest/.nocx-backup-1"}
	sink := &failingSink{err: errors.New("transfer: promote /dest/f: connection lost"), stranded: stranded}
	e := newRefreshEnvWithSink(t, sink)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	e.watchDir(t, bid, []string{dir}, 3)

	body := []byte("bytes that never land")
	started := callUpload(t, e.conn, uploadParams(bid, dir, "doomed.txt", int64(len(body))), 4).mustResult(t)
	go postUploadAsync(e.ws, started.Ticket, body)

	done, changed := awaitDoneCollectingChanges(t, e.conn)
	if done.Outcome != uploadStateFailed {
		t.Fatalf("outcome = %q, want %q", done.Outcome, uploadStateFailed)
	}
	if len(changed) != 0 {
		t.Fatalf("files.changed for %v after a failed upload; nothing was written", changed)
	}
	if done.Error == "" {
		t.Error("a failed outcome carries no error — the person is told it failed and not why")
	}
	if len(done.Stranded) != 2 || done.Stranded[0] != stranded[0] || done.Stranded[1] != stranded[1] {
		t.Fatalf("stranded = %v, want %v — both paths, because the fallback can leave a temp AND a backup", done.Stranded, stranded)
	}
}
