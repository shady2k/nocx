package transport

// Per-session resize and close semantics (worker G brief):
//
// resize is the only control method the renderer sends fire-and-forget, and
// on an SSH session it is a window-change round trip that can block on a dead
// transport. The two halves of the defect: a resize that blocks the read loop
// freezes every tab, and a resize serialized behind the session's close would
// recreate head-of-line blocking — the close, the one operation that could
// tear the dead channel down, must never queue behind it.
//
// The acceptance tests drive the real socket (stallServer + connectWS) with
// channel fakes that stand in for the PTY/SSH channel. gatedResizeChannel
// models a window-change that cannot complete on its own: it blocks until
// either the test releases it or the caller's context is cancelled, and only
// a released call counts as applied — the cancelled path applies nothing,
// exactly like a dead SSH transport whose window-change never returns.

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/ssh"
)

// resizeCall records one resize that was actually applied to the channel —
// the PTY state, the only thing a user can observe.
type resizeCall struct {
	cols, rows uint16
}

// gatedResizeChannel is the dead-transport stand-in for the resize path.
type gatedResizeChannel struct {
	done      chan struct{}
	closeOnce sync.Once
	// release, once closed, lets every blocked Resize complete and apply.
	release chan struct{}
	// started fires (buffered 1) when a resize enters Resize — it is in
	// flight and cannot be coalesced away.
	started chan struct{}
	// returned carries one token per Resize return, so a test can observe
	// that a blocked resize was cancelled.
	returned chan struct{}

	mu      sync.Mutex
	applied []resizeCall
}

func newGatedResizeChannel() *gatedResizeChannel {
	return &gatedResizeChannel{
		done:     make(chan struct{}),
		release:  make(chan struct{}),
		started:  make(chan struct{}, 1),
		returned: make(chan struct{}, 1),
	}
}

func (c *gatedResizeChannel) Read(p []byte) (int, error) { return 0, io.EOF }
func (c *gatedResizeChannel) Write(p []byte) (int, error) {
	<-c.done
	return 0, io.ErrClosedPipe
}

func (c *gatedResizeChannel) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	return nil
}
func (c *gatedResizeChannel) Done() <-chan struct{}                     { return c.done }
func (c *gatedResizeChannel) ShellIntegrationReason() ssh.RefusalReason { return ssh.ReasonNone }

func (c *gatedResizeChannel) Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error {
	select {
	case c.started <- struct{}{}:
	default:
	}
	select {
	case <-c.release:
		c.mu.Lock()
		c.applied = append(c.applied, resizeCall{cols: cols, rows: rows})
		c.mu.Unlock()
		select {
		case c.returned <- struct{}{}:
		default:
		}
		return nil
	case <-ctx.Done():
		select {
		case c.returned <- struct{}{}:
		default:
		}
		return ctx.Err()
	}
}

func (c *gatedResizeChannel) appliedResizes() []resizeCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]resizeCall(nil), c.applied...)
}

// sendResizeRaw writes a resize RPC without awaiting its response — exactly
// how the renderer sends it (fire-and-forget, void … .catch(() => {})).
func sendResizeRaw(t *testing.T, conn *websocket.Conn, sid string, cols, rows uint16, id int) {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "resize",
		"params": map[string]any{
			"sessionId": sid,
			"cols":      cols,
			"rows":      rows,
			"xpixel":    0,
			"ypixel":    0,
		},
	})
	if err != nil {
		t.Fatalf("marshal resize: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write resize: %v", err)
	}
}

// collectResponses reads frames until every requested id has a response,
// skipping notifications and other ids, and returns the raw responses.
// An admitted request may never be left without a completion, so a missing
// response fails the test.
func collectResponses(t *testing.T, conn *websocket.Conn, ids []int, d time.Duration) map[int]json.RawMessage {
	t.Helper()
	want := make(map[int]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	got := make(map[int]json.RawMessage, len(ids))
	deadline := time.Now().Add(d)
	_ = conn.SetReadDeadline(deadline)
	for len(got) < len(ids) {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response: %v (have %d of %d responses)", err, len(got), len(ids))
		}
		var env struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(data, &env) != nil || len(env.ID) == 0 || string(env.ID) == "null" {
			continue // notification
		}
		var id int
		if json.Unmarshal(env.ID, &id) != nil || !want[id] {
			continue
		}
		got[id] = data
	}
	return got
}

func assertResponseOK(t *testing.T, data json.RawMessage, id int) {
	t.Helper()
	var resp struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("decode response %d: %v", id, err)
	}
	if resp.Error != nil {
		t.Fatalf("response %d is an error: code %d (body %s)", id, resp.Error.Code, data)
	}
}

func assertResponseError(t *testing.T, data json.RawMessage, id, wantCode int) {
	t.Helper()
	var resp struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("decode response %d: %v", id, err)
	}
	if resp.Error == nil {
		t.Fatalf("response %d: want error code %d, got success", id, wantCode)
	}
	if resp.Error.Code != wantCode {
		t.Fatalf("response %d: want error code %d, got %d", id, wantCode, resp.Error.Code)
	}
}

// closeSessionAwait sends the close RPC and waits for its response within d.
// d is short on purpose: close must never queue behind a dead resize.
func closeSessionAwait(t *testing.T, conn *websocket.Conn, sid string, id int, d time.Duration) json.RawMessage {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "close",
		"params":  map[string]string{"sessionId": sid},
	})
	if err != nil {
		t.Fatalf("marshal close: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write close: %v", err)
	}
	return collectResponses(t, conn, []int{id}, d)[id]
}

// waitApplied polls the channel's applied list until want holds, or fails.
func waitApplied(t *testing.T, c *gatedResizeChannel, want []resizeCall, what string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if got := c.appliedResizes(); len(got) == len(want) {
			eq := true
			for i := range want {
				if got[i] != want[i] {
					eq = false
					break
				}
			}
			if eq {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("%s: applied resizes = %+v, want %+v", what, c.appliedResizes(), want)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// --- acceptance 1: a dead resize does not delay input to ANOTHER session ---

func TestDeadResize_DoesNotDelayAnotherSession(t *testing.T) {
	// Never released: the window-change never completes, like a jump host
	// behind a NAT that drops packets without an RST.
	blocked := newGatedResizeChannel()
	live := newLiveChannel()
	ws := stallServer(t, blocked, live)
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	deadSID := openSSHOverSocket(t, ws, conn, 1)
	liveSID := openSSHOverSocket(t, ws, conn, 2)

	// Fire a resize at the dead session, then wait until it is genuinely
	// in flight: the read loop must already have moved past the request.
	sendResizeRaw(t, conn, deadSID, 100, 30, 1)
	select {
	case <-blocked.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the dead resize never started")
	}

	// The healthy tab types. Its input must arrive promptly — a blocked
	// resize on one session must not freeze the read loop.
	sendData(t, conn, liveSID, "hostname\n")
	deadline := time.After(5 * time.Second)
	for {
		if live.received() == "hostname\n" {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the healthy tab's input never arrived (got %q): a dead resize is still freezing the read loop",
				live.received())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// --- acceptance 2: close preempts its own session's dead resize ------------
// The head-of-line test, the one that matters: the one operation that can
// tear down the dead channel must not queue behind the blocked resize.

func TestDeadResize_DoesNotDelayItsOwnClose(t *testing.T) {
	blocked := newGatedResizeChannel()
	ws := stallServer(t, blocked)
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	sid := openSSHOverSocket(t, ws, conn, 1)

	sendResizeRaw(t, conn, sid, 100, 30, 1)
	select {
	case <-blocked.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the dead resize never started")
	}

	// close must complete while the resize is still blocked in flight.
	closeResp := closeSessionAwait(t, conn, sid, 2, 5*time.Second)
	assertResponseOK(t, closeResp, 2)

	// And the close admission must have cancelled the blocked resize: it
	// returns (with a cancelled context) instead of staying blocked forever.
	select {
	case <-blocked.returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the blocked resize was never cancelled by the close")
	}
}

// --- acceptance 3: rapid resizes settle on the last dimensions, at the PTY --

func TestRapidResizesSettleOnLastDimensions(t *testing.T) {
	gated := newGatedResizeChannel()
	ws := stallServer(t, gated)
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	sid := openSSHOverSocket(t, ws, conn, 1)

	// A window drag fires a burst of resizes with nobody awaiting the
	// answers — exactly how the renderer sends them. Hold the first in
	// flight so the burst lands while the worker is busy; the intermediate
	// sizes have no value and must be coalesced away.
	sendResizeRaw(t, conn, sid, 100, 30, 11)
	select {
	case <-gated.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first resize never started")
	}
	sendResizeRaw(t, conn, sid, 120, 40, 12)
	sendResizeRaw(t, conn, sid, 140, 50, 13)

	// Response 12 arrives only when the read loop has coalesced request 12
	// away (superseded by 13): the burst is queued behind the in-flight
	// resize and the intermediate size is gone. Awaiting it before
	// releasing the gate is what makes the coalescing assertion below
	// deterministic rather than a race with the read loop.
	awaitResponse(t, conn, 12, 5*time.Second)
	close(gated.release)

	// Every admitted request is answered (the renderer ignores the answers,
	// but a request may not be left without a completion).
	resps := collectResponses(t, conn, []int{11, 13}, 5*time.Second)
	assertResponseOK(t, resps[11], 11)
	assertResponseOK(t, resps[13], 13)

	// The PTY settles on the last dimensions, and only them: the burst is
	// coalesced, never replayed one size at a time.
	waitApplied(t, gated, []resizeCall{{cols: 100, rows: 30}, {cols: 140, rows: 50}},
		"rapid resize burst")
}

// awaitResponse waits for the response with the given id. It is the sync
// point for "the read loop has processed a specific request".
//
// Notifications and other ids seen on the way are RETAINED, not discarded:
// the tests that use this collect them afterwards, and this used to be the
// reader that had already thrown them away (ws_inbox_test.go).
func awaitResponse(t *testing.T, conn *websocket.Conn, id int, d time.Duration) {
	t.Helper()
	if _, err := awaitFrame(conn, time.Now().Add(d), isResponseTo(id)); err != nil {
		t.Fatalf("await response %d: %v", id, err)
	}
}

// --- acceptance 4: after close admission begins, no resize reaches the
// session — neither the queued one, nor the in-flight one, nor a later one.

func TestResizeAfterCloseAdmissionNeverReachesSession(t *testing.T) {
	gated := newGatedResizeChannel()
	ws := stallServer(t, gated)
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	sid := openSSHOverSocket(t, ws, conn, 1)

	// One resize in flight, one queued behind it, when close is admitted.
	sendResizeRaw(t, conn, sid, 100, 30, 1)
	select {
	case <-gated.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first resize never started")
	}
	sendResizeRaw(t, conn, sid, 120, 40, 2)

	// Close admission begins while a resize is in flight and one is queued.
	// The close response and the two resize completions (the queued one
	// dropped, the in-flight one cancelled) can reach the wire in any
	// order, so they are collected in one pass.
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "close",
		"params":  map[string]string{"sessionId": sid},
	})
	if err != nil {
		t.Fatalf("marshal close: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write close: %v", err)
	}
	resps := collectResponses(t, conn, []int{1, 2, 3}, 5*time.Second)
	assertResponseOK(t, resps[3], 3)
	assertResponseOK(t, resps[1], 1)
	assertResponseOK(t, resps[2], 2)

	// Release the gate: even then, nothing may reach the session — the
	// in-flight resize was cancelled by the admission, the queued one dropped.
	close(gated.release)
	settled := false
	deadline := time.After(500 * time.Millisecond)
	for !settled {
		if got := gated.appliedResizes(); len(got) != 0 {
			t.Fatalf("a resize reached the session after close admission: %+v", got)
		}
		select {
		case <-deadline:
			settled = true
		case <-time.After(5 * time.Millisecond):
		}
	}
	// A resize arriving after the close completed is refused outright.
	sendResizeRaw(t, conn, sid, 200, 60, 4)
	resps = collectResponses(t, conn, []int{4}, 5*time.Second)
	assertResponseError(t, resps[4], 4, -32602)
	if got := gated.appliedResizes(); len(got) != 0 {
		t.Fatalf("a resize reached the closed session: %+v", got)
	}
}

// --- acceptance 5: a close from one connection must not let another
// connection's resize use the session the close already removed ------------

func TestResizeFromAttachedConnectionAfterCloseIsRefused(t *testing.T) {
	gated := newGatedResizeChannel()
	ws := stallServer(t, gated)
	connA := connectWS(t, ws)
	connB := connectWS(t, ws)
	t.Cleanup(func() { _ = connA.Close() })
	t.Cleanup(func() { _ = connB.Close() })

	sid := openSSHOverSocket(t, ws, connA, 1)

	// Connection B attaches, so the session is in B's connState too — the
	// state gate alone can no longer refuse B's resize.
	at := jsonrpcCallWithID(t, connB, "attach", map[string]any{
		"sessionId": sid,
		"offset":    0,
	}, 2)
	var ar struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(at, &ar)
	if ar.Error != nil {
		t.Fatalf("attach failed: code %d", ar.Error.Code)
	}

	// A closes the session. B's connState still names it.
	closeResp := closeSessionAwait(t, connA, sid, 3, 5*time.Second)
	assertResponseOK(t, closeResp, 3)

	// B resizes the closed session: refused (the registry gate), and
	// nothing reaches the PTY.
	sendResizeRaw(t, connB, sid, 200, 60, 4)
	resps := collectResponses(t, connB, []int{4}, 5*time.Second)
	assertResponseError(t, resps[4], 4, -32602)

	deadline := time.After(200 * time.Millisecond)
	for {
		if got := gated.appliedResizes(); len(got) != 0 {
			t.Fatalf("a resize reached the closed session: %+v", got)
		}
		select {
		case <-deadline:
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}
