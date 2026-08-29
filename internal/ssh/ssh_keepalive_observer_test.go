package ssh

// The keepalive prober is the one thing in this repository that already knows
// a host has stopped answering while the session is still open — its own
// comment in connection/resolver.go calls it "what notices a transport that
// died without saying so". Until now it kept that knowledge to itself and
// spent it only on the decision to close the connection.
//
// These tests pin what it now reports (nocx-iarf9): a probe that fails while
// the connection is still being given a chance is the evidence behind
// `unknown`, and only the last failure — the one that closes the connection —
// is a loss.

import (
	"errors"
	"testing"
	"time"
)

// The fold, driven directly: no ticker, no server, no duration. A probe that
// fails while retries remain says "not answering"; the one that exhausts them
// says "give up", which closes the connection and becomes the session's loss.
func TestKeepaliveTally_ReportsUnresponsiveBeforeGivingUp(t *testing.T) {
	tally := keepaliveTally{countMax: 3}

	if got := tally.probe(false); got != keepaliveUnresponsive {
		t.Errorf("first failure = %v, want keepaliveUnresponsive", got)
	}
	if got := tally.probe(false); got != keepaliveUnresponsive {
		t.Errorf("second failure = %v, want keepaliveUnresponsive", got)
	}
	if got := tally.probe(false); got != keepaliveGiveUp {
		t.Errorf("third failure = %v, want keepaliveGiveUp", got)
	}
}

// A successful probe resets the count and reports the host answering again —
// the transition out of unknown, which is what stops the state from being a
// one-way door.
func TestKeepaliveTally_SuccessAfterFailureIsAReturn(t *testing.T) {
	tally := keepaliveTally{countMax: 3}

	// A probe that confirms what is already believed is silent: otherwise
	// every healthy connection reports once per tick forever.
	if got := tally.probe(true); got != keepaliveSteady {
		t.Errorf("success with nothing to correct = %v, want keepaliveSteady", got)
	}

	tally.probe(false)

	if got := tally.probe(true); got != keepaliveResponsive {
		t.Errorf("success after a failure = %v, want keepaliveResponsive", got)
	}
	// The count reset: two more failures must not be enough to give up.
	tally.probe(false)
	if got := tally.probe(false); got != keepaliveUnresponsive {
		t.Errorf("second failure after a reset = %v, want keepaliveUnresponsive", got)
	}
}

// countMax <= 0 means a single failure closes the connection — the behaviour
// this file inherited, kept exactly. There is no "not answering" window at
// all, so the first failure is a give-up and nothing reports unknown.
func TestKeepaliveTally_NoRetriesGivesUpImmediately(t *testing.T) {
	for _, countMax := range []int{0, -1} {
		tally := keepaliveTally{countMax: countMax}
		if got := tally.probe(false); got != keepaliveGiveUp {
			t.Errorf("countMax %d: first failure = %v, want keepaliveGiveUp", countMax, got)
		}
	}
}

// keepaliveFake is a probe target the test drives: it answers what the test
// says and announces every probe, so the test waits on an observed state
// change rather than on a duration.
type keepaliveFake struct {
	probes chan struct{}
	closed chan struct{}
	fail   bool
}

func newKeepaliveFake(fail bool) *keepaliveFake {
	return &keepaliveFake{probes: make(chan struct{}, 8), closed: make(chan struct{}), fail: fail}
}

func (f *keepaliveFake) SendRequest(string, bool, []byte) (bool, []byte, error) {
	select {
	case f.probes <- struct{}{}:
	default:
	}
	if f.fail {
		return false, nil, errors.New("connection lost")
	}
	return true, nil, nil
}

func (f *keepaliveFake) Close() error {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	return nil
}

// The wiring, end to end through the real goroutine: a host that stops
// answering reports itself unresponsive to the observer BEFORE the connection
// is closed. That ordering is the whole feature — a report that arrived only
// with the close would say nothing the exit notification does not already say.
func TestKeepalive_ReportsAnUnresponsiveHostBeforeClosing(t *testing.T) {
	fake := newKeepaliveFake(true)
	reports := make(chan bool, 8)

	stop, done := startKeepalive(fake, time.Millisecond, 3, func(r Reachability) {
		reports <- r.Responsive
	})
	if stop == nil {
		t.Fatal("startKeepalive returned nil stop for a non-zero interval")
	}
	t.Cleanup(func() { <-done })

	select {
	case responsive := <-reports:
		if responsive {
			t.Fatal("reported responsive from a target that fails every probe")
		}
	case <-time.After(wantWithinKeepalive):
		t.Fatal("no unresponsive report from a host that stopped answering")
	}

	// The connection is still ours to close at that point, not already closed:
	// unknown describes a session we have NOT given up on.
	select {
	case <-fake.closed:
		t.Fatal("the connection was closed before any retry was spent")
	default:
	}

	// And the give-up still happens: the retries run out and the transport is
	// closed, which is what becomes the session's loss.
	select {
	case <-fake.closed:
	case <-time.After(wantWithinKeepalive):
		t.Fatal("the prober never gave up on a host that never answered")
	}
}

// A prober with no observer wired must behave exactly as before — the
// composition root is free not to wire one, and a nil callback is not a crash.
func TestKeepalive_WorksWithNoObserver(t *testing.T) {
	fake := newKeepaliveFake(true)
	stop, done := startKeepalive(fake, time.Millisecond, 1, nil)
	if stop == nil {
		t.Fatal("startKeepalive returned nil stop for a non-zero interval")
	}
	select {
	case <-fake.closed:
	case <-time.After(wantWithinKeepalive):
		t.Fatal("the prober never closed a connection that never answered")
	}
	<-done
}

// wantWithinKeepalive is a failsafe, not a schedule: every assertion above
// completes as soon as the state it waits for is observed.
const wantWithinKeepalive = 30 * time.Second
