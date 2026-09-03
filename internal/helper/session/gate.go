package session

import "sync"

// gate is a generation channel: wait() hands out a channel that closes on the
// next signal(), and a fresh one takes its place. It is how everything in this
// package waits on an observable state change rather than on a duration — a
// waiter selects it against a context, which is what sync.Cond cannot do
// without a helper goroutine and a poll loop.
//
// The same device is in internal/transport's ring, where it was introduced for
// the same reason; it is spelled once HERE because this package needs it in
// two places — the window's writes and a subscriber's acks — and two
// hand-rolled copies of a synchronisation primitive is how one of them
// eventually loses a wakeup.
type gate struct {
	mu sync.Mutex
	ch chan struct{}
}

func newGate() *gate { return &gate{ch: make(chan struct{})} }

// wait returns the channel that closes on the next signal. A caller must take
// it BEFORE it re-checks the state it is waiting on, or it can miss the
// signal that fired in between.
func (g *gate) wait() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ch
}

// signal wakes every current waiter.
func (g *gate) signal() {
	g.mu.Lock()
	defer g.mu.Unlock()
	close(g.ch)
	g.ch = make(chan struct{})
}
