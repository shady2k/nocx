package transport

// nocx-isjh4, closer 2: a shell that exits while its pane is still open ends
// the block, and the coordinator's own exit classification (session.Session.
// ExitOutcome, read off the SAME channel every reconnect and every
// reconciliation reads) is what "the exit status has been read" names — see
// internal/session/session.go's realSession.ExitOutcome and this package's
// own monitorExit, which reads it before ever touching the registry. Once
// that is true, the session's helper-hosted channel is asked to END, not
// merely detach: releasing its window budget even though the pane stays on
// screen.
//
// This drives the REAL monitorExit/pumpToRing pair — ReadoptHostedSession
// wires both exactly as an ordinary hosted open does (ws_readopt.go's own
// doc) — over a channel that answers the SAME optional EndSession verb
// internal/helper/client.AttachedSession does, so what is asserted is the
// dispatch monitorExit actually makes, not a stand-in for it.

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// exitEndableChannel is a helper-hosted channel double: Done/WaitErr behave
// like the real watchers (record the outcome, THEN close done — the ordering
// ExitOutcome's read relies on), and Close/EndSession are two different,
// separately observable verbs — exactly the split
// internal/session/session.go's realSession.Close/EndSession dispatch on.
type exitEndableChannel struct {
	done chan struct{}

	mu      sync.Mutex
	waitErr error
	waitSet bool
	closed  bool
	ended   bool
}

func newExitEndableChannel() *exitEndableChannel {
	return &exitEndableChannel{done: make(chan struct{})}
}

func (c *exitEndableChannel) Read(p []byte) (int, error) {
	<-c.done
	return 0, io.EOF
}

func (c *exitEndableChannel) Write(p []byte) (int, error) { return len(p), nil }

func (c *exitEndableChannel) Resize(context.Context, uint16, uint16, uint16, uint16) error {
	return nil
}

func (c *exitEndableChannel) Done() <-chan struct{} { return c.done }

func (c *exitEndableChannel) WaitErr() (error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waitErr, c.waitSet
}

func (c *exitEndableChannel) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

// EndSession is the optional verb internal/helper/client.AttachedSession
// answers (nocx-isjh4): a caller that knows nobody will ever need this
// session again calls it instead of Close.
func (c *exitEndableChannel) EndSession(context.Context) error {
	c.mu.Lock()
	c.ended = true
	c.mu.Unlock()
	return nil
}

func (c *exitEndableChannel) state() (closed, ended bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed, c.ended
}

// exitAt records the wait outcome and THEN closes done, mirroring exactly
// what internal/pty and the ssh watchers do — the ordering ExitOutcome's own
// doc says it relies on.
func (c *exitEndableChannel) exitAt(err error) {
	c.mu.Lock()
	c.waitErr, c.waitSet = err, true
	c.mu.Unlock()
	close(c.done)
}

// fakeHelperExitStatus mirrors internal/helper/client.ExitStatus's shape —
// an error carrying ExitCode() — which is the branch of
// realSession.ExitOutcome's mapping a helper-hosted session's exit takes.
type fakeHelperExitStatus struct{ code int }

func (e *fakeHelperExitStatus) Error() string { return "helper session exited" }
func (e *fakeHelperExitStatus) ExitCode() int { return e.code }

func TestAShellExitingWithThePaneOpenReadsItsExitStatusAndEndsTheHelperSession(t *testing.T) {
	logger := log.NewSlogAdapter(slog.New(slog.NewTextHandler(io.Discard, nil)))
	reg := session.New(logger, nil)
	ws := NewWSServer(logger, reg)

	const sid = session.ID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	ch := newExitEndableChannel()

	err := ws.ReadoptHostedSession(context.Background(), sid,
		func(ctx context.Context, from uint64) (HostedSessionOpen, error) {
			sess, adoptErr := reg.Adopt(ctx,
				session.Config{Kind: session.KindRemote, Host: "far-host", PaneID: "pane-under-test"},
				sid, ch)
			if adoptErr != nil {
				return HostedSessionOpen{}, adoptErr
			}
			return HostedSessionOpen{Session: sess}, nil
		})
	if err != nil {
		t.Fatalf("adopting the session: %v", err)
	}

	sess, err := reg.Get(sid)
	if err != nil {
		t.Fatalf("the adopted session is not in the registry: %v", err)
	}

	// THE SHELL EXITS. Nobody closed the pane; the process ended on its own,
	// exactly as closer 2 is about.
	ch.exitAt(&fakeHelperExitStatus{code: 7})

	// "The exit status has been read" — session.Session.ExitOutcome is what
	// monitorExit itself reads before touching the registry, and what a
	// reconnecting client's own reconciliation reads. It needs no wait: it is
	// derived from ch.WaitErr(), which was already set (under the same lock)
	// before ch.done closed above.
	cause, status := sess.ExitOutcome()
	if cause != session.ExitExited {
		t.Fatalf("exit cause = %q, want %q — a shell that reported its own status must never read as a loss",
			cause, session.ExitExited)
	}
	if status != 7 {
		t.Fatalf("exit status = %d, want 7", status)
	}

	// AND THEN the helper session ends — not merely detaches. monitorExit
	// runs asynchronously off ch.Done(), so this is the one wait in the test,
	// and it is on the channel's own observable state rather than a sleep.
	waittest.WaitForTimeoutDetail(t, "the helper session to be told it is over", wantWithin,
		func() string {
			closed, ended := ch.state()
			return "closed=" + boolStr(closed) + " ended=" + boolStr(ended)
		},
		func() bool {
			_, ended := ch.state()
			return ended
		})

	closed, ended := ch.state()
	if closed {
		t.Error("the channel's plain Close ran; only EndSession may run for a shell that exited on its own " +
			"(Close alone would leave the helper believing this session is still claimable)")
	}
	if !ended {
		t.Fatal("EndSession never ran: the helper was never told this session's window budget can be released")
	}

	if _, err := reg.Get(sid); err == nil {
		t.Fatal("the session is still in the registry after its shell exited")
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
