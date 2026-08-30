package ssh

// The case the prober exists for and cannot currently see.
//
// A machine that suspends does not close its TCP connections: no FIN, no RST,
// nothing. The socket stays open and every byte written to it disappears. That
// is the ONLY shape in which a transport "dies without saying so" — a peer that
// closes cleanly makes the channel's Done fire and needs no prober at all.
//
// SendRequest against such a socket does not fail. x/crypto/ssh writes the
// global request and then blocks on a bare channel receive with no deadline
// and no context (mux.go's `msg, ok := <-m.globalResponses`), holding
// globalSentMu for the whole wait. So the prober's loop is stuck INSIDE one
// probe: the tally never advances, countMax is never reached, and neither the
// observer nor the Close that ends the session ever happens. What eventually
// breaks the deadlock is the kernel's own TCP keepalive, at a latency nobody
// in this package chose — which is to say the configured 30s x 3 governs
// nothing in the one case it was written for.

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// blackHoleTarget is a transport that has stopped answering without saying so:
// SendRequest blocks until the connection is closed, exactly as a probe on a
// suspended machine's socket does.
//
// Close RELEASES the blocked probe, which is what a real close does — closing
// the underlying conn makes x/crypto's reader fail and every waiter on
// globalResponses observe the channel close. A fake whose Close left the probe
// blocked would let a timeout fix look correct while leaking the prober's
// goroutine on every probe, which is the defect one layer down.
type blackHoleTarget struct {
	release   chan struct{}
	closeOnce func()
	closed    atomic.Bool
	probes    atomic.Int32
}

func newBlackHoleTarget() *blackHoleTarget {
	t := &blackHoleTarget{release: make(chan struct{})}
	var once bool
	t.closeOnce = func() {
		if !once {
			once = true
			close(t.release)
		}
	}
	return t
}

func (b *blackHoleTarget) SendRequest(string, bool, []byte) (bool, []byte, error) {
	b.probes.Add(1)
	<-b.release
	return false, nil, errors.New("ssh: connection closed")
}

func (b *blackHoleTarget) Close() error {
	if b.closed.CompareAndSwap(false, true) {
		b.closeOnce()
	}
	return nil
}

// A transport that stops answering must be given up on within a bounded time —
// whether the probe FAILS or HANGS — and the prober's goroutine must end with
// it. Today neither happens: one hung probe stalls the loop, and even stop()
// cannot be observed while it is stalled.
func TestKeepaliveGivesUpOnATransportThatHangs(t *testing.T) {
	target := newBlackHoleTarget()
	defer target.closeOnce()

	var unresponsive atomic.Int32
	stop, done := startKeepalive(target, 20*time.Millisecond, 3, func(r Reachability) {
		if !r.Responsive {
			unresponsive.Add(1)
		}
	})
	defer stop()

	// Three intervals is 60ms; a second is thirty of them, generous enough
	// that a slow machine is not what this measures.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !target.closed.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	if !target.closed.Load() {
		t.Fatalf("the prober never gave up on a transport that stopped answering: "+
			"probes=%d unresponsive-reports=%d", target.probes.Load(), unresponsive.Load())
	}

	// And it ended. A give-up that leaves the probe's goroutine parked on a
	// socket nobody will ever answer is the same leak wearing a fix.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the prober gave up but its goroutine did not exit")
	}
}
