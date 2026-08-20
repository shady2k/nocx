package ssh

// The master's ownership interval and its three loss events (design §6.2,
// assertions 24 and 25).
//
// No test here waits on a duration. The cleanup bound is driven by an
// injected clock that advances only when the code under test asks to wait,
// so "the five seconds passed" is a statement the test makes rather than one
// it sits through.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// testClock advances only when something waits on it. Injected time, and the
// only kind a deadline in this package is allowed to be driven by.
type testClock struct {
	now   time.Time
	waits int
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) After(d time.Duration) <-chan time.Time {
	c.waits++
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// ---------------------------------------------------------------------------
// Assertion 24: each loss interval produces its outcome, and the three events
// are distinguished from one another.

func TestOwnership_TheThreeLossEventsAreDistinguished(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "m")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatalf("stand in for the socket: %v", err)
	}
	alive := true
	transportDead := false
	probes := MasterProbes{
		SocketPresent: func(string) bool { return fileExists(socket) },
		ProcessAlive:  func(int) bool { return alive },
	}
	o := NewOwnership(log.NewSlogAdapter(nil), socket, 4242, probes, newTestClock())
	o.MarkProven()

	// Nothing lost: no event.
	if ev, lost := o.Detect(); lost {
		t.Fatalf("a healthy master reported %q", ev.Event)
	}

	// 1. The socket file goes while the process lives.
	if err := os.Remove(socket); err != nil {
		t.Fatalf("remove the socket: %v", err)
	}
	ev, lost := o.Detect()
	if !lost || ev.Event != LossSocketFile {
		t.Fatalf("event %q lost=%v, want %q — a missing socket is not a dead process", ev.Event, lost, LossSocketFile)
	}
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatalf("restore the socket: %v", err)
	}

	// 2. The process goes.
	alive = false
	ev, lost = o.Detect()
	if !lost || ev.Event != LossMasterProcess {
		t.Fatalf("event %q lost=%v, want %q", ev.Event, lost, LossMasterProcess)
	}
	alive = true

	// 3. The transport under it goes: reported by whoever was using a
	//    channel on it, because that is the only place it is observable.
	transportDead = true
	if transportDead {
		o.ReportTransportLoss(errors.New("channel ended"))
	}
	ev, lost = o.Detect()
	if !lost || ev.Event != LossTransport {
		t.Fatalf("event %q lost=%v, want %q", ev.Event, lost, LossTransport)
	}
	if !ev.EndsSession {
		t.Fatal("losing the transport did not end the session; there is no prompt left to keep and claiming one would be an outcome we cannot deliver")
	}
}

// Each loss interval produces its own outcome, and the interval is what
// decides — the same event means different things before ownership is proven,
// after it, and after integration is live.
func TestOwnership_EachLossIntervalHasItsOwnOutcome(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mark        func(*Ownership)
		event       LossEvent
		wantReason  RefusalReason
		wantEnds    bool
		wantSilence bool
	}{
		{
			name:        "before ownership proof the line is plain ssh",
			mark:        func(*Ownership) {},
			event:       LossSocketFile,
			wantSilence: true,
		},
		{
			name:       "after proof, before integration is live",
			mark:       func(o *Ownership) { o.MarkProven() },
			event:      LossSocketFile,
			wantReason: ReasonMasterLost,
		},
		{
			name:       "after proof, the master process instead",
			mark:       func(o *Ownership) { o.MarkProven() },
			event:      LossMasterProcess,
			wantReason: ReasonMasterLost,
		},
		{
			name:       "after integration is live",
			mark:       func(o *Ownership) { o.MarkProven(); o.MarkIntegrated() },
			event:      LossSocketFile,
			wantReason: ReasonChannelLost,
		},
		{
			name:       "the transport, at any point, ends the session",
			mark:       func(o *Ownership) { o.MarkProven(); o.MarkIntegrated() },
			event:      LossTransport,
			wantReason: ReasonTransportLost,
			wantEnds:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := NewOwnership(log.NewSlogAdapter(nil), "/nx/m", 1, MasterProbes{}, newTestClock())
			tc.mark(o)
			got := o.Outcome(tc.event)
			if tc.wantSilence {
				if got.Reason != ReasonNone {
					t.Fatalf("before ownership was proven the product named %q; nothing had happened yet", got.Reason)
				}
				if got.EndsSession {
					t.Fatal("a loss before ownership ended the user's session")
				}
				return
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("reason %q, want %q", got.Reason, tc.wantReason)
			}
			if got.EndsSession != tc.wantEnds {
				t.Fatalf("EndsSession=%v, want %v", got.EndsSession, tc.wantEnds)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Assertion 25: the ownership interval CLOSES. After the last owned session
// ends the master exits and the socket is removed, within 5 s of injected
// time. Without that event the socket and the master process are a footprint
// with no end.

func TestOwnership_ClosesWhenTheLastOwnedSessionEnds(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "m")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatalf("stand in for the socket: %v", err)
	}
	clock := newTestClock()
	exited := false
	closedSessions := 0
	probes := MasterProbes{
		SocketPresent: func(string) bool { return fileExists(socket) },
		ProcessAlive:  func(int) bool { return !exited },
		Terminate: func() error {
			exited = true
			return nil
		},
	}
	o := NewOwnership(log.NewSlogAdapter(nil), socket, 4242, probes, clock)
	o.MarkProven()
	o.Own(closerFunc(func() error { closedSessions++; return nil }))
	o.Own(closerFunc(func() error { closedSessions++; return nil }))

	start := clock.Now()
	res := o.Close(context.Background())
	if closedSessions != 2 {
		t.Fatalf("%d owned sessions were closed, want 2 — the interval closes when the LAST of them has finished", closedSessions)
	}
	if !res.MasterExited {
		t.Fatal("the master did not exit; a master with no closing event is a footprint with no end")
	}
	if !res.SocketRemoved {
		t.Fatal("the socket was not removed after the master's exit was confirmed")
	}
	if fileExists(socket) {
		t.Fatal("the socket file is still there")
	}
	if elapsed := clock.Now().Sub(start); elapsed > MasterCleanupBound {
		t.Fatalf("cleanup took %s of injected time, past the %s bound", elapsed, MasterCleanupBound)
	}
}

// The bound is a bound, not a hope: a master that will not go is given five
// seconds of injected time and no more, and the socket is removed anyway
// rather than left behind with the process.
func TestOwnership_TheCleanupIsBoundedEvenWhenTheMasterWillNotGo(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "m")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatalf("stand in for the socket: %v", err)
	}
	clock := newTestClock()
	probes := MasterProbes{
		SocketPresent: func(string) bool { return fileExists(socket) },
		ProcessAlive:  func(int) bool { return true }, // never exits
		Terminate:     func() error { return errors.New("the master refused to exit") },
	}
	o := NewOwnership(log.NewSlogAdapter(nil), socket, 4242, probes, clock)
	o.MarkProven()

	start := clock.Now()
	res := o.Close(context.Background())
	if res.MasterExited {
		t.Fatal("the cleanup reported an exit it never confirmed")
	}
	elapsed := clock.Now().Sub(start)
	if elapsed > MasterCleanupBound {
		t.Fatalf("the cleanup ran for %s of injected time, past the %s bound", elapsed, MasterCleanupBound)
	}
	if elapsed < MasterCleanupBound {
		t.Fatalf("the cleanup gave up after %s; the bound is %s and a master is entitled to all of it", elapsed, MasterCleanupBound)
	}
	if !res.SocketRemoved {
		t.Fatal("the socket outlived the bounded cleanup")
	}
	if res.Err == nil {
		t.Fatal("a master that would not exit was reported as a clean close")
	}
}

// Close is idempotent: the ownership interval closes once, whatever runs it.
func TestOwnership_ClosesOnce(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "m")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatalf("stand in for the socket: %v", err)
	}
	exited := false
	terminates := 0
	o := NewOwnership(log.NewSlogAdapter(nil), socket, 1, MasterProbes{
		SocketPresent: func(string) bool { return fileExists(socket) },
		ProcessAlive:  func(int) bool { return !exited },
		Terminate:     func() error { terminates++; exited = true; return nil },
	}, newTestClock())
	o.MarkProven()
	_ = o.Close(context.Background())
	_ = o.Close(context.Background())
	if terminates != 1 {
		t.Fatalf("the master was asked to exit %d times, want 1", terminates)
	}
	if o.Phase() != PhaseClosed {
		t.Fatalf("phase %v after close, want PhaseClosed", o.Phase())
	}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
