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
	// feed is the stand's stand-in for the runtime beside the PTY: every byte
	// the program printed goes to it as well as to the session's reader.
	//
	// In production the runtime IS the reader (the helper's pump ingests before
	// it delivers); here the reader is the product's own pump, so a stand that
	// wants a screen to exist has to keep the emulator fed itself. Without this
	// the watcher classifies an EMPTY pane — which is what it did, and what
	// turned a working turn into "unknown".
	feed func([]byte)
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
	feed := f.feed
	f.mu.Unlock()
	if feed != nil {
		// Fed FIRST, so the screen the observer reads is one that already
		// holds what was printed — the product's own order (the helper
		// ingests before it delivers).
		feed([]byte(s))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.pw.Write([]byte(s)); err != nil {
		t.Fatalf("emit %q: %v", s, err)
	}
}

// feedsTo installs the pane source this stand's PTY reports into.
func (f *feedablePTY) feedsTo(views *paneviewtest.Views, paneID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.feed = func(b []byte) { views.Feed(paneID, b) }
}

type feedableFactory struct{ p *feedablePTY }

func (f *feedableFactory) NewPTY(context.Context, pty.Config) (pty.Pty, error) { return f.p, nil }

// recordingPaneAdmissions is the admission interval's end, recorded. What a test
// needs to see from the transport is WHICH session's interval it ended; what
// ending one does to a live tool connection is asserted in internal/app, over a
// real endpoint and a real socket, because that is where the connection lives.
type recordingPaneAdmissions struct {
	mu    sync.Mutex
	ended []string
}

func (r *recordingPaneAdmissions) SessionEnded(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ended = append(r.ended, sessionID)
}

func (r *recordingPaneAdmissions) sessions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ended...)
}

// newPanesWS wires the transport with the store it reads through, over a source
// that answers with bytes a test supplies. Extra options are the seams a test
// adds to that shape, so there is one stand rather than one per seam.
func newPanesWS(t *testing.T, extra ...WSServerOption) (*WSServer, *paneviewtest.Views, *feedablePTY) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	term := newFeedablePTY()
	reg := session.New(logger, &feedableFactory{p: term})
	store := paneviewtest.NewViews(logger)
	opts := append([]WSServerOption{WithPaneScreens(store.Store)}, extra...)
	ws := NewWSServer(logger, reg, opts...)
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

// AND THE SAME END REACHES THE AUTHORITY (nocx-9mn6z).
//
// The observation and the screen are what the transport itself reads; the tool
// endpoint's admission is the third thing a session's enrolment opened, and it
// is the one that outlives a session nobody ended deliberately. It was told
// nothing, so an exited-but-retained session kept its admitted caller and the
// grant inside it.
//
// The three ways a session's end arrives all pass through unwatchPane — a
// program that exited, an explicit close, and this call itself — so the two
// assertions below are the funnel and one of its callers. The effect on a LIVE
// tool connection is asserted in internal/app, where the socket is.
func TestTheAdmissionIntervalClosesWhenTheSessionsOutputEnds(t *testing.T) {
	admissions := &recordingPaneAdmissions{}
	ws, store, term := newPanesWS(t, WithPaneAdmissions(admissions))
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	if err := store.Watch(sid, 40, 6); err != nil {
		t.Fatalf("watch: %v", err)
	}

	if err := term.Close(); err != nil {
		t.Fatalf("close the pane's pty: %v", err)
	}

	waittest.WaitForTimeout(t, "the session's admission interval to close", 5*time.Second, func() bool {
		ended := admissions.sessions()
		return len(ended) == 1 && ended[0] == sid
	})
	if got := admissions.sessions(); len(got) != 1 {
		t.Fatalf("ended intervals = %v, want exactly the session that ended", got)
	}
}

// The funnel itself, called for a session whose end came from neither of the
// paths above: whatever ends a session, the interval goes with it.
func TestUnwatchingAPaneEndsItsAdmissionInterval(t *testing.T) {
	admissions := &recordingPaneAdmissions{}
	ws, _, _ := newPanesWS(t, WithPaneAdmissions(admissions))

	ws.unwatchPane(session.ID("sess-unwatched"))

	if got := admissions.sessions(); len(got) != 1 || got[0] != "sess-unwatched" {
		t.Fatalf("ended intervals = %v, want the unwatched session", got)
	}
}
