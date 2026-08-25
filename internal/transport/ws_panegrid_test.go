package transport

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
)

// feedablePTY is a PTY a test can push output through. The package's own Stub
// answers EOF on the first Read, which is right for tests about opening a
// session and useless for a test about what flows through one.
type feedablePTY struct {
	mu   sync.Mutex
	pr   *io.PipeReader
	pw   *io.PipeWriter
	done chan struct{}
	once sync.Once
}

func newFeedablePTY() *feedablePTY {
	pr, pw := io.Pipe()
	return &feedablePTY{pr: pr, pw: pw, done: make(chan struct{})}
}

func (f *feedablePTY) Read(p []byte) (int, error)  { return f.pr.Read(p) }
func (f *feedablePTY) Write(p []byte) (int, error) { return len(p), nil }
func (f *feedablePTY) Close() error {
	f.once.Do(func() {
		_ = f.pw.CloseWithError(io.EOF)
		close(f.done)
	})
	return nil
}
func (f *feedablePTY) Resize(context.Context, uint16, uint16, uint16, uint16) error { return nil }
func (f *feedablePTY) Done() <-chan struct{}                                        { return f.done }

// emit pushes bytes as if the program in the pane had printed them.
func (f *feedablePTY) emit(t *testing.T, s string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.pw.Write([]byte(s)); err != nil {
		t.Fatalf("emit %q: %v", s, err)
	}
}

type feedableFactory struct{ p *feedablePTY }

func (f *feedableFactory) NewPTY(context.Context, pty.Config) (pty.Pty, error) { return f.p, nil }

func newGridWS(t *testing.T) (*WSServer, *panegrid.Store, *feedablePTY) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	term := newFeedablePTY()
	reg := session.New(logger, &feedableFactory{p: term})
	store := panegrid.New(logger)
	ws := NewWSServer(logger, reg, WithPaneGrid(store))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx); _ = term.Close() })
	return ws, store, term
}

// THE ACCEPTANCE CRITERION, and the reason the tee sits where it does: the
// grid is fed from the backend's own read path, so a client going away
// changes nothing about it.
//
// A pane with no grid is the control in the same test. Without it a green run
// could mean "the feed survives a disconnect" or "the assertion reads
// something that was never connected to the feed at all".
func TestTheGridKeepsBeingFedAfterTheClientDisconnects(t *testing.T) {
	ws, store, term := newGridWS(t)
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	// Enrolment through the Go seam, which is the only one there is: the act
	// belongs to the authenticated shell channel, and what reaches the
	// renderer is the classification (ws_paneobserve.go), never the act.
	if err := store.Enrol(sid, 40, 6); err != nil {
		t.Fatalf("enrol: %v", err)
	}

	term.emit(t, "before")
	waitFor(t, "the first bytes to reach the grid", wantWithin, func() bool {
		f, err := store.Frame(sid)
		return err == nil && strings.Contains(f.Text(0), "before")
	})

	// The client goes away. Nothing else changes.
	_ = conn.Close()

	term.emit(t, "\r\nafter")
	waitFor(t, "bytes written while no client is attached", wantWithin, func() bool {
		f, err := store.Frame(sid)
		return err == nil && strings.Contains(f.Text(1), "after")
	})

	f, err := store.Frame(sid)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if !strings.Contains(f.Text(0), "before") {
		t.Errorf("row 0 = %q; the pre-disconnect output was lost", f.Text(0))
	}
}

// The control: a session nobody enrolled has no grid, so the assertion above
// is about the enrolment and not about the store answering for everything.
func TestAnUnobservedSessionHasNoGrid(t *testing.T) {
	ws, store, term := newGridWS(t)
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	term.emit(t, "printed anyway")
	if store.Enrolled(sid) {
		t.Fatal("a session nobody asked about was enrolled")
	}
	if _, err := store.Frame(sid); err == nil {
		t.Fatal("Frame answered for a session with no grid")
	}
}

// The interval outlives a resize, so the grid has to follow the pane through
// one. Both powers the AD-6 amendment grants a grid are POSITIONAL — a chrome
// anchor is a thing at a column — so a grid left at the size it was enrolled
// at is not merely stale: the program repaints at the new width while the
// emulator keeps wrapping at the old one, and every column a reader trusts is
// off by the difference.
func TestTheGridFollowsThePaneThroughAResize(t *testing.T) {
	ws, store, term := newGridWS(t)
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	if err := store.Enrol(sid, 20, 6); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	jsonrpcCallWithID(t, conn, "resize", map[string]any{"sessionId": sid, "cols": 60, "rows": 8}, 2)

	// Waited for by hand rather than with waitFor, because the interesting
	// failure is not "it did not arrive" — it is WHY. The grid resize happens
	// on the lane's apply, ahead of the response this call already read, so a
	// pane that still holds a grid and still answers at the old size is a
	// defect in the tee. A pane that holds NO grid is a different event
	// entirely: its session ended and the interval closed underneath the test.
	// A bare timeout reports the first while meaning either, which is how a
	// starved container gets read as a broken feature.
	deadline := time.Now().Add(wantWithin)
	for {
		f, err := store.Frame(sid)
		if err == nil && f.Cols == 60 && f.Rows == 8 {
			break
		}
		if !time.Now().Before(deadline) {
			if !store.Enrolled(sid) {
				t.Fatalf("the pane lost its grid before the resize could reach it: "+
					"the session's output ended and closed the interval (frame error %v)", err)
			}
			t.Fatalf("the pane still holds a grid and it did not follow the resize: frame=%dx%d err=%v",
				f.Cols, f.Rows, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// And the new geometry is the one the emulator actually lays out at: at
	// twenty columns this line would have wrapped.
	term.emit(t, "a line longer than twenty columns")
	waitFor(t, "the line to arrive unwrapped", wantWithin, func() bool {
		f, err := store.Frame(sid)
		return err == nil && strings.Contains(f.Text(0), "a line longer than twenty columns")
	})
}

// A pane nobody enrolled is resized like any other, and the grid path must be
// silent about it: most panes never hold a grid and every one of them is
// resized, so ErrNotEnrolled here is the ordinary answer rather than a fault.
func TestResizingAPaneWithNoGridChangesNothing(t *testing.T) {
	ws, store, _ := newGridWS(t)
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	jsonrpcCallWithID(t, conn, "resize", map[string]any{"sessionId": sid, "cols": 60, "rows": 8}, 2)

	if store.Enrolled(sid) {
		t.Fatal("a resize enrolled a pane; only the enrolment act may do that")
	}
}

// THE OTHER END OF THE INTERVAL, and the reason it is asserted separately from
// the withdrawal the caller sends: this one covers the caller that never sends
// it. A wrapper killed rather than returned, a shell that died, a session torn
// down under both — in every one of those the enrolment's own close never
// happens, and a grid whose only close is the caller's is a grid that leaks.
//
// AGENTS.md names exactly this shape: an invariant written with a start and no
// named closing event buys a test that guards only the start.
func TestTheGridIsGoneWhenTheSessionsOutputEnds(t *testing.T) {
	ws, store, term := newGridWS(t)
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	if err := store.Enrol(sid, 40, 6); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	term.emit(t, "watched")
	waitFor(t, "the grid to be fed", wantWithin, func() bool {
		f, err := store.Frame(sid)
		return err == nil && strings.Contains(f.Text(0), "watched")
	})

	// The session's output ends. Nobody withdrew anything.
	if err := term.Close(); err != nil {
		t.Fatalf("close the pane's pty: %v", err)
	}

	waitFor(t, "the interval to close when the output ended", wantWithin, func() bool {
		return !store.Enrolled(sid)
	})
	if _, err := store.Frame(sid); err == nil {
		t.Error("the grid still answers for a pane whose output is over")
	}
}
