package transport

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

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

	// Enrolment through the Go seam, which is the only one there is until
	// nocx-szb40.3 brings a wire method and the caller that justifies it.
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
