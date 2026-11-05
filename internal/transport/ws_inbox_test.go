package transport

// A connection's inbound frames have ONE owner.
//
// Every wait in this package used to be its own reader: jsonrpcCallWithID
// looped until the response id matched, readNotification looped until the
// method matched, vaultCall and awaitResponse looped until their own id
// matched — and each one silently DROPPED every frame it read on the way.
// Two of those waits on one connection is therefore a lottery. The server
// starts a transfer's emitter goroutine before the handler answers
// (startUpload, startDownload), teardown writes exit and git.changed from
// goroutines of their own, and the outbound pump is a single writer with no
// promise that a response overtakes a notification enqueued before it. So a
// notification can legitimately precede the response to the call that
// caused it, the response-shaped reader eats it, and the notification-shaped
// reader that follows waits for a frame that was already delivered.
//
// It never recovers, and that is why the symptom is a hang rather than a
// miss: gorilla/websocket stores the FIRST read error in c.readErr and
// returns it from every later read, so once one wait deadlines the
// connection is finished for the rest of the test.
//
// The failure is environmental only in WHICH wait loses. nocx-2h08 has been
// collecting the names since 2026-08-11 — TestLifecycleChanged_*,
// TestTabbyExecute_VaultRetry, TestFilesChanged_*, TestGitChanged_*,
// TestWSServer_CreditCloseUnblocksWriter — and nocx-hbdw4.2 added three
// more (TestUploadProgress_ReachesTheCurrentSubscriber,
// TestUploadSkipped_DoesNotInvalidate,
// TestFilesDownloadDone_ACancelledDownloadIsNotAFailure). Same package,
// same commit, a different name each run. Twice the answer was to widen the
// bound — 2 s, then 5 s, then wantWithin's 30 s — and twice it failed at
// exactly the new number, because the frame was not late. It was gone.
//
// So no reader in this package may consume a frame it does not want. A
// frame that arrives for somebody else is RETAINED, in arrival order, and
// handed to whoever asks for it next. The deadline then answers the only
// question it was ever supposed to answer — did this arrive at all — and a
// slower machine simply waits longer for the same frames.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// errWaitExpired is what a wait returns when its deadline passed with the
// frame it wanted neither retained nor arriving. It is deliberately
// distinct from a socket error: "the server never sent this" and "the
// connection broke" are different findings and a test should say which.
var errWaitExpired = errors.New("deadline passed before the frame arrived")

// wsInbox holds the frames read off one connection that the reader which
// read them did not want. Arrival order is preserved: the wire's order is
// part of what several tests in this package assert (a files.changed
// invalidation precedes its files.uploadDone; the open ack precedes every
// session-scoped notification, AD-7), so a buffer that reordered them would
// be a second defect wearing the first one's fix.
type wsInbox struct {
	mu     sync.Mutex
	frames [][]byte
}

// wsInboxes maps a connection to its retained frames.
//
// Keyed by the *websocket.Conn rather than held on a test env because the
// helpers take a bare connection — openSessionOnConn, jsonrpcCallWithID and
// readNotification are shared by the files, git, vault, layout and
// lifecycle envs, and threading an inbox through all of them would be the
// same change with more edits. The map holds the key alive, so an address
// cannot be recycled underneath a stale entry; connectWS drops its
// connection's entry on cleanup, which bounds the map to the connections a
// single test has open.
var wsInboxes sync.Map // *websocket.Conn → *wsInbox

func inboxOf(conn *websocket.Conn) *wsInbox {
	if b, ok := wsInboxes.Load(conn); ok {
		if box, ok := b.(*wsInbox); ok {
			return box
		}
	}
	b, _ := wsInboxes.LoadOrStore(conn, &wsInbox{})
	box, _ := b.(*wsInbox)
	return box
}

// forgetInbox drops a connection's retained frames. Called from the
// cleanup of whoever dialled it.
func forgetInbox(conn *websocket.Conn) { wsInboxes.Delete(conn) }

// take removes and returns the first retained frame want accepts.
func (b *wsInbox) take(want func([]byte) bool) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, f := range b.frames {
		if want(f) {
			b.frames = append(b.frames[:i], b.frames[i+1:]...)
			return f, true
		}
	}
	return nil, false
}

// keep appends a frame somebody else will want.
func (b *wsInbox) keep(msg []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.frames = append(b.frames, msg)
}

// awaitFrame is the one read in this package. It answers with the first
// frame want accepts — retained first, then off the socket — and retains
// everything else it sees on the way.
//
// The deadline is absolute and set on every pass rather than recomputed as
// a remaining budget: gorilla makes a read error permanent, so a wait that
// re-armed a full budget after a failed read spun at full speed until its
// bound and then blamed the deadline for a socket that had already failed
// (nocx-2bvy). One absolute deadline cannot do that.
//
// THE DEADLINE BELONGS TO THE WAIT, NOT TO THE CONNECTION (nocx-atpw5.1.37).
// It is cleared before this returns, because the next read is not this wait's:
// a test that goes on to read the DATA plane with a reader of its own —
// newWSReader, drainWithAcks — was reading under a deadline set here, and it
// fired 30 s later as an i/o timeout. That is TestWSServer_Resize's failure
// exactly: it waited its full window and then reported "connection closed
// before stty output arrived", which is the deadline talking, not the socket —
// and gorilla stores the first read error for every later read, so the
// connection was finished for the rest of the test.
func awaitFrame(conn *websocket.Conn, deadline time.Time, want func([]byte) bool) ([]byte, error) {
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	b := inboxOf(conn)
	if msg, ok := b.take(want); ok {
		return msg, nil
	}
	for {
		if !time.Now().Before(deadline) {
			return nil, errWaitExpired
		}
		_ = conn.SetReadDeadline(deadline)
		mt, msg, err := conn.ReadMessage()
		if err != nil {
			// A DEADLINE THAT PASSED IS NOT A BROKEN SOCKET (see errWaitExpired):
			// gorilla reports it as a net timeout, and keeping "the server never
			// sent this" apart from "the connection broke" is the whole reason
			// the sentinel exists. Reported raw, every timed-out wait blamed the
			// socket, and a test could not say which had happened.
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				return nil, errWaitExpired
			}
			return nil, err
		}
		if want(msg) {
			return msg, nil
		}
		// Only the CONTROL plane is retained. A binary frame is raw PTY
		// output (AD-1) that no control-plane wait can ever be looking
		// for, and the data-plane tests read it with readers of their own
		// — drainWithAcks, the fairness and ingress suites — which go
		// straight to the socket. Retaining it here would hand those
		// readers the frame AFTER the ones sitting in this buffer, which
		// is the same defect pointed at the other plane.
		if mt == websocket.TextMessage {
			b.keep(msg)
		}
	}
}

// jsonrpcFrame is as much of an envelope as the predicates below need. A
// notification is exactly a frame with no id (JSON-RPC 2.0 §4.1).
type jsonrpcFrame struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
}

func decodeFrame(msg []byte) (jsonrpcFrame, bool) {
	var f jsonrpcFrame
	if err := json.Unmarshal(msg, &f); err != nil {
		return f, false
	}
	return f, true
}

// isResponseTo accepts the response frame carrying this request id.
func isResponseTo(id int) func([]byte) bool {
	return func(msg []byte) bool {
		f, ok := decodeFrame(msg)
		if !ok || f.ID == nil || string(*f.ID) == "null" {
			return false
		}
		var got int
		return json.Unmarshal(*f.ID, &got) == nil && got == id
	}
}

// isNotification accepts a notification carrying this method.
func isNotification(method string) func([]byte) bool {
	return func(msg []byte) bool {
		f, ok := decodeFrame(msg)
		return ok && f.ID == nil && f.Method == method
	}
}

// isAnyNotification accepts a notification carrying any of these methods.
func isAnyNotification(methods ...string) func([]byte) bool {
	return func(msg []byte) bool {
		f, ok := decodeFrame(msg)
		if !ok || f.ID != nil {
			return false
		}
		for _, m := range methods {
			if f.Method == m {
				return true
			}
		}
		return false
	}
}

// awaitNotification is readNotification without the t.Fatalf, so a test can
// assert the wait FAILS. A wait nobody has watched fail is a wait nobody
// knows works: three of these were green all session while one of them
// could not have failed for the right reason.
func awaitNotification(conn *websocket.Conn, method string, d time.Duration) (json.RawMessage, error) {
	msg, err := awaitFrame(conn, time.Now().Add(d), isNotification(method))
	if err != nil {
		return nil, err
	}
	f, _ := decodeFrame(msg)
	return f.Params, nil
}

// TestAwaitFrame_LeavesNoReadDeadlineBehind (nocx-atpw5.1.37): a wait's
// deadline is the WAIT's. The read that follows belongs to whoever makes it —
// in TestWSServer_Resize, newWSReader reading the data plane — and a deadline
// left behind by an earlier wait closed that reader's channel with an i/o
// timeout, which the test then reported as "connection closed before stty
// output arrived". Gorilla stores a read error for every later read, so that
// one stale deadline was also the end of the connection.
//
// The wait here SUCCEEDS, because that is the shape that leaked: a wait that
// found its frame returns with its deadline still armed, and the next reader
// inherits it. The pause is the stimulus — it puts that deadline in the past —
// and the assertion is that the reader still reads.
func TestAwaitFrame_LeavesNoReadDeadlineBehind(t *testing.T) {
	sess := newRegWithReal(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)
	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("decode open result: %v", err)
	}
	sid := r.Result.SessionID

	// ONE WAIT, SHORT, AND SATISFIED: exactly what jsonrpcCallWithID does,
	// with a bound small enough that the pause below outlives it.
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "resize",
		"params": map[string]any{"sessionId": sid, "cols": 100, "rows": 30, "xpixel": 0, "ypixel": 0},
	})
	if err != nil {
		t.Fatalf("marshal resize: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write resize: %v", err)
	}
	if _, err := awaitFrame(conn, time.Now().Add(2*time.Second), isResponseTo(2)); err != nil {
		t.Fatalf("the resize wait answered %v, want its response", err)
	}

	// Past the wait's own bound, and nothing has happened on the wire.
	time.Sleep(2200 * time.Millisecond)

	sidBytes, _ := session.IDToBytes(session.ID(sid))
	marker := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: sidBytes, Payload: []byte("echo READY-deadline\n")}
	if err := conn.WriteMessage(websocket.BinaryMessage, marker.Encode()); err != nil {
		t.Fatalf("write the marker: %v", err)
	}

	// NO DEADLINE OF OUR OWN: a stale one is then the only bound this read
	// could have, and the watchdog is what keeps the bug from hanging the suite.
	type readResult struct {
		msg []byte
		err error
	}
	done := make(chan readResult, 1)
	go func() {
		_, msg, err := conn.ReadMessage()
		done <- readResult{msg: msg, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("the read after a satisfied wait failed: %v — the wait's deadline outlived the wait", got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no frame after a satisfied wait, and no error either")
	}
}
