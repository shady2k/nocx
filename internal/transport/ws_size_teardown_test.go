package transport

// TWO BEHAVIOURS THAT WERE TRUE BY READING AND BY NOTHING ELSE.
//
// nocx-eidfb.2 shipped the foreground rule — the client that attached last
// owns the channel's size, and the size returns to the named default when the
// last client detaches — and it recorded, honestly, that two of the paths
// holding that rule up had no test naming them. Both are teardown paths, and
// both are invisible from the outside except as an absence: a terminal that
// did NOT jump back to 80x24, and a person who was NOT shown a failure.
//
//   1. announceDisplacement removes the session from the LOSER's connection
//      state. Without it the loser's own teardown would walk that state, find
//      a session it no longer holds, and return it to the default — so
//      closing the window that lost a pane would resize the terminal the
//      other window is looking at.
//
//   2. monitorExit empties the subscriber's state and tombstones the lane
//      before the connection can tear down. Without it, the ordinary way a
//      session ends — the shell exits, then the window closes — would ask a
//      dead channel to resize and report the failure at the person.
//
// Both are asserted here against the same seam a person reaches: two windows
// and one pane, and a shell that exits under a window that is still open.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// presenceCounter is the observable end of a connection's teardown.
//
// The count is published from unregisterConn, a DEFERRED call in
// handleSession, so it is reported after the body has finished deciding what
// to do with every session the connection held — including admitting any
// return-to-default. Waiting on it is waiting on a state change, which is why
// this is here rather than a sleep.
type presenceCounter struct {
	mu sync.Mutex
	n  int
}

func (p *presenceCounter) ClientsAttached(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n = n
}

func (p *presenceCounter) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

func awaitClients(t *testing.T, p *presenceCounter, want int) {
	t.Helper()
	waittest.WaitForTimeoutDetail(t, "the attached-client count to settle", wantWithin,
		func() string { return fmt.Sprintf("%d clients are attached", p.count()) },
		func() bool { return p.count() == want })
}

// newTeardownWS builds a resize-recording server whose log is captured and
// whose connection count is observable — the two things these tests read.
func newTeardownWS(t *testing.T) (*WSServer, *activeFactory, *presenceCounter, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	logger := log.NewSlogAdapter(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	f := &activeFactory{}
	seen := &presenceCounter{}
	ws := NewWSServer(logger, session.New(logger, f), WithClientPresence(seen))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = ws.Stop(ctx)
		if p := f.pty(); p != nil {
			_ = p.Close()
		}
	})
	return ws, f, seen, buf
}

// resizeThroughTheLane sends one resize and waits for its ANSWER, which is the
// only barrier there is on the session's lane: the lane serves one op at a
// time in the order it admitted them, and an op is answered when it has been
// applied. So anything admitted before this call has happened by the time it
// returns — which is what turns "the teardown must not resize" from a race
// into an assertion.
func resizeThroughTheLane(t *testing.T, conn *websocket.Conn, tap *socketTap, id int, sid string, size session.Size) {
	t.Helper()
	raw := tapCall(t, conn, tap, id, "resize", map[string]any{
		"sessionId": sid,
		"cols":      size.Cols, "rows": size.Rows,
		"xpixel": size.XPixel, "ypixel": size.YPixel,
	})
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("resize: %v\nraw: %s", err, raw)
	}
	if env.Error != nil {
		t.Fatalf("resize was refused: %+v", env.Error)
	}
}

// logRecords is the captured log, one decoded record per line.
func logRecords(t *testing.T, buf *syncBuffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// ── 1. the window that LOST a pane does not resize it on its way out ───────

// Two windows, one pane. The second takes it, so the first is displaced and no
// longer holds it — and then the person closes the first window, which is the
// ordinary thing to do with a window whose pane has moved.
//
// What must NOT happen is the departing window's teardown treating that
// session as one of its own and handing it back to the named default: the
// other window is looking at it, and its terminal would jump to 80x24 for
// something that happened in a window nobody was using. The only thing
// standing between the two is announceDisplacement dropping the session from
// the loser's connection state before the teardown ever walks it.
//
// Asserted on the PTY's own resize log rather than on the size afterwards: a
// spurious return-to-default followed by anything else would be invisible in
// the final value, and it is the resize itself — a real ioctl on the channel
// the other window is using — that must never happen.
func TestActiveClient_TheLoserLeaving_DoesNotResizeThePaneTheWinnerHolds(t *testing.T) {
	ws, f, seen, _ := newTeardownWS(t)

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	opened := openAtSize(t, ws, connA, tapA, 1, session.Size{Cols: 100, Rows: 30})
	sid := opened.SessionID

	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)
	held := session.Size{Cols: 132, Rows: 43}
	attachAt(t, connB, tapB, 2, opened, 0, held)
	tapNotify(t, tapA, "session.displaced", wantWithin)
	awaitEffectiveSize(t, ws, sid, held)

	// The loser's window closes. The count settling at one is the teardown
	// having finished: it is published after the body decided what to do with
	// every session the connection was holding.
	_ = connA.Close()
	awaitClients(t, seen, 1)

	// The barrier. Anything the teardown admitted on this session's lane is
	// ahead of this op and has been applied by the time it answers.
	resizeThroughTheLane(t, connB, tapB, 3, sid, held)

	for _, got := range f.pty().appliedResizes() {
		if got == session.DefaultSize() {
			t.Fatalf("the pane the winner holds was resized to the default %+v when the loser's window closed; every resize applied: %+v",
				session.DefaultSize(), f.pty().appliedResizes())
		}
	}
	if got := effectiveSizeOf(t, ws, sid); got != held {
		t.Fatalf("EffectiveSize = %+v after the loser left, want the winner's %+v", got, held)
	}
}

// ── 2. a shell that exits, and then its window closes ─────────────────────

// The ordinary way a session ends: the shell exits on its own, and some time
// later the person closes the window it was in. The window's teardown walks
// the sessions the connection held and hands each one back to the named
// default — and this one has no channel left to hand anything to.
//
// monitorExit is what makes that a non-event, and MEASURED WHILE WRITING
// THIS, by three things rather than the one nocx-eidfb.2 recorded: it drops
// the session's rx binding (removeRx), empties the subscriber's state
// (state.remove) and tombstones the lane (closeLane), all before the teardown
// can reach any of them. Disabling any one of them leaves the other two
// holding; only with all three gone does the warning appear. Worth knowing
// before anyone "simplifies" one of them on the grounds that the others cover
// it — that is true of each of them separately and of none of them together.
//
// What must not appear is the failure takeSize reports when a resize does not
// land — "the terminal is running at a different grid than the window" —
// because there is no terminal, no window, and nothing for a person to do
// about it.
//
// The control for the absence is its sibling
// TestActiveClient_TheForegroundSizeHoldsUntilTheLastClientDetaches: the same
// teardown, on a session that is still alive, DOES return it to the default.
// So this asserts that one path is quiet, not that the path does nothing.
func TestActiveClient_AnExitedSessionsWindowClosingReportsNoFailure(t *testing.T) {
	ws, f, seen, buf := newTeardownWS(t)

	conn := connectWS(t, ws)
	tap := newSocketTap(conn)
	opened := openAtSize(t, ws, conn, tap, 1, session.Size{Cols: 100, Rows: 30})
	sid := session.ID(opened.SessionID)

	// The shell exits. The session leaving the registry is monitorExit having
	// run — an observable state, and the one that says the teardown below will
	// meet an already-dismantled session.
	_ = f.pty().Close()
	waittest.WaitForTimeout(t, "the exited session to leave the registry", wantWithin, func() bool {
		_, err := ws.registry.Get(sid)
		return err != nil
	})

	// And now the window closes.
	_ = conn.Close()
	awaitClients(t, seen, 0)

	const failure = "the session could not be resized to the client that owns it"
	records := logRecords(t, buf)
	// An absence is only evidence if the thing it is an absence FROM could
	// have been recorded. A capture that silently caught nothing would pass
	// this test for ever.
	if len(records) == 0 {
		t.Fatal("nothing was captured from the backend's log; the absence below would say nothing")
	}
	for _, rec := range records {
		msg, _ := rec["msg"].(string)
		if strings.Contains(msg, failure) {
			t.Fatalf("closing the window of a session whose shell had already exited reported a resize failure: %+v", rec)
		}
	}
}
