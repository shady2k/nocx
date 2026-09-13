package transport

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneview/paneviewtest"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// What the transport does with a pane's screen, now that it does not derive one
// (nocx-ygxjv.3, ADR-0066).
//
// The old subject of this file was a TEE: the backend read every session's
// bytes and fed a grid of its own, so the screen survived a client going away.
// That tee is gone — the emulator is beside the PTY, in the helper that owns it
// — and with it three of the behaviours this file used to assert: the feed
// surviving a disconnect, the grid following a resize, and bytes arriving in a
// grid at all. What is left, and what these tests are, is the transport's own
// half: it READS a frame for a surface, it does not invent one, and it closes
// the observation when the session ends.

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

// newPanesWS wires the transport with the store it reads through, over a source
// that answers with bytes a test supplies.
func newPanesWS(t *testing.T) (*WSServer, *paneviewtest.Views, *feedablePTY) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	term := newFeedablePTY()
	reg := session.New(logger, &feedableFactory{p: term})
	store := paneviewtest.NewViews(logger)
	ws := NewWSServer(logger, reg, WithPaneScreens(store.Store))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx); _ = term.Close() })
	return ws, store, term
}

// TestAPaneNobodyWatchesHasNoFrame is the interval's teeth at the transport
// boundary: the surface asks the store, and the store refuses a pane nobody is
// watching. Without this the emitting view would read any session the registry
// happens to hold.
func TestAPaneNobodyWatchesHasNoFrame(t *testing.T) {
	ws, store, term := newPanesWS(t)
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	term.emit(t, "printed anyway")
	store.Feed(sid, []byte("printed anyway"))

	if store.Watched(sid) {
		t.Fatal("a session nobody asked about was watched")
	}
	if _, err := store.Frame(sid); err == nil {
		t.Fatal("the store answered for a pane nobody watches")
	}

	// And the same pane answers once the interval is open, which is what makes
	// the refusal above about the interval rather than about the store being
	// unable to read anything at all.
	if err := store.Watch(sid, 40, 6); err != nil {
		t.Fatalf("watch: %v", err)
	}
	f, err := store.Frame(sid)
	if err != nil {
		t.Fatalf("frame for a watched pane: %v", err)
	}
	if f.Cols != 40 || f.Rows != 6 {
		t.Errorf("frame is %dx%d, want the size the pane was declared at", f.Cols, f.Rows)
	}
}

// TestAResizeDoesNotEnrol: most panes are never watched and every one of them
// is resized, so the resize path must not touch the interval. The transport has
// no resize hook onto the store at all any more — the runtime's geometry
// follows the PTY's ioctl, which the helper performs — and this is the
// assertion that keeps a future hook from re-opening one.
func TestAResizeDoesNotEnrol(t *testing.T) {
	ws, store, _ := newPanesWS(t)
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	jsonrpcCallWithID(t, conn, "resize", map[string]any{"sessionId": sid, "cols": 60, "rows": 8}, 2)

	if store.Watched(sid) {
		t.Fatal("a resize watched a pane; only the enrolment act may do that")
	}
}

// TestTheObservationClosesWhenTheSessionsOutputEnds is the other end of the
// interval, and the end that covers a caller that never sends its own
// withdrawal: a wrapper killed rather than returned, a shell that died, a
// session torn down under both. AGENTS.md names this shape — an invariant
// written with a start and no named closing event buys a test that guards only
// the start.
func TestTheObservationClosesWhenTheSessionsOutputEnds(t *testing.T) {
	ws, store, term := newPanesWS(t)
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	if err := store.Watch(sid, 40, 6); err != nil {
		t.Fatalf("watch: %v", err)
	}
	store.Feed(sid, []byte("watched"))
	if _, err := store.Frame(sid); err != nil {
		t.Fatalf("frame while the pane is watched: %v", err)
	}

	// The session's output ends. Nobody withdrew anything.
	if err := term.Close(); err != nil {
		t.Fatalf("close the pane's pty: %v", err)
	}

	waittest.WaitForTimeout(t, "the interval to close when the output ended", 5*time.Second, func() bool {
		return !store.Watched(sid)
	})
	if _, err := store.Frame(sid); err == nil {
		t.Error("the store still answers for a pane whose session is over")
	}
}
