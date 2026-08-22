package transport

// The two halves of every wait in this package, pinned against a wire whose
// ORDER the test chooses rather than races.
//
// A real WSServer cannot be asked to put a notification in front of a
// response — that ordering is the thing that varies with the machine, which
// is why the defect took eleven days and eight test names to name. So these
// drive the helpers against a scripted server that writes exactly the
// frames named here in exactly this order. What is under test is the
// READER, and the reader is the half that was wrong.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// scriptedWire dials a server that answers one request with the frames
// given, in order, and then sends nothing further.
func scriptedWire(t *testing.T, frames ...string) *websocket.Conn {
	t.Helper()
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
		for _, f := range frames {
			if err := c.WriteMessage(websocket.TextMessage, []byte(f)); err != nil {
				return
			}
		}
		// Hold the connection open so a wait for something never sent
		// reaches its deadline instead of a close.
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { forgetInbox(conn); _ = conn.Close() })
	return conn
}

// TestNotificationBeforeItsResponseSurvivesTheResponseWait is the defect
// itself, in the order the machine only sometimes produces: the
// notification a call causes arrives BEFORE the call's response. Waiting
// for the response must not cost the notification.
//
// Before the inbox this failed by hanging for the full bound and then
// reporting an i/o timeout on a frame the server had already sent — which
// is every failure nocx-2h08 and nocx-hbdw4.2 recorded.
func TestNotificationBeforeItsResponseSurvivesTheResponseWait(t *testing.T) {
	conn := scriptedWire(t,
		`{"jsonrpc":"2.0","method":"files.uploadDone","params":{"outcome":"written"}}`,
		`{"jsonrpc":"2.0","id":7,"result":{"transferId":"t1"}}`,
	)

	resp := jsonrpcCallWithID(t, conn, "files.upload", map[string]any{}, 7)
	var env struct {
		Result struct {
			TransferID string `json:"transferId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("response: %v", err)
	}
	if env.Result.TransferID != "t1" {
		t.Fatalf("transferId = %q, want t1", env.Result.TransferID)
	}

	raw := readNotification(t, conn, "files.uploadDone", wantWithin)
	var got struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("files.uploadDone: %v", err)
	}
	if got.Outcome != "written" {
		t.Fatalf("outcome = %q, want written", got.Outcome)
	}
}

// TestFramesAreRetainedInArrivalOrder is the half a buffer gets wrong. The
// wire's order is load-bearing here — settleUpload invalidates the
// destination BEFORE it announces the outcome, deliberately, so a renderer
// is never told a transfer is done over a directory it has not been told to
// re-list — and a reader that handed them back in the order it was ASKED
// for them would let that assertion pass on a wire that had it backwards.
func TestFramesAreRetainedInArrivalOrder(t *testing.T) {
	conn := scriptedWire(t,
		`{"jsonrpc":"2.0","method":"files.changed","params":{"path":"/a"}}`,
		`{"jsonrpc":"2.0","method":"files.changed","params":{"path":"/b"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{}}`,
	)
	jsonrpcCallWithID(t, conn, "files.upload", map[string]any{}, 1)

	var seen []string
	for range 2 {
		raw := readNotification(t, conn, "files.changed", wantWithin)
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("files.changed: %v", err)
		}
		seen = append(seen, p.Path)
	}
	if len(seen) != 2 || seen[0] != "/a" || seen[1] != "/b" {
		t.Fatalf("paths = %v, want [/a /b] — the order they were sent in", seen)
	}
}

// TestAwaitNotificationFailsWhenTheNotificationIsAbsent is the negative
// control, and it is the reason the two above are worth anything. A wait
// that cannot expire is not a wait, it is a hang with better manners: it
// would report a missing notification as a package-wide timeout under
// whatever test was unlucky, which is the failure this whole change exists
// to stop.
//
// The duration here IS the meaning of the test, which is the one use
// wantWithin's doc reserves for a literal.
func TestAwaitNotificationFailsWhenTheNotificationIsAbsent(t *testing.T) {
	conn := scriptedWire(t, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	jsonrpcCallWithID(t, conn, "files.upload", map[string]any{}, 1)

	start := time.Now()
	raw, err := awaitNotification(conn, "files.uploadDone", 200*time.Millisecond)
	if err == nil {
		t.Fatalf("a notification the server never sent was reported as delivered: %s", raw)
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("gave up after %v, before its own %v window", elapsed, 200*time.Millisecond)
	}
}

// And a retained frame is not mistaken for the one being waited for: the
// inbox answers by predicate, never by "something is buffered".
func TestARetainedFrameDoesNotSatisfyADifferentWait(t *testing.T) {
	conn := scriptedWire(t,
		`{"jsonrpc":"2.0","method":"files.changed","params":{"path":"/a"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{}}`,
	)
	jsonrpcCallWithID(t, conn, "files.upload", map[string]any{}, 1)

	if raw, err := awaitNotification(conn, "files.uploadDone", 200*time.Millisecond); err == nil {
		t.Fatalf("a retained files.changed answered a wait for files.uploadDone: %s", raw)
	}
	// …and it is still there for the wait it belongs to.
	if raw := readNotification(t, conn, "files.changed", wantWithin); raw == nil {
		t.Fatal("the retained files.changed was consumed by the wait that refused it")
	}
}
