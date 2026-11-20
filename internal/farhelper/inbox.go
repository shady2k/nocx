package farhelper

import "github.com/shady2k/nocx/internal/helper/client"

// entryInbox coalesces observations and signals without allocating a queue.
// External callers only take this short lock and cancel work; no RPC, store
// lookup or subscriber call is permitted on their goroutine.
type entryInbox struct {
	observation *Connection
	interrupted bool
	inputs      uint64
	refresh     uint64
	freeze      uint64
	freezeAck   chan struct{}
	thaw        uint64
	lost        *client.Client
	close       bool
	serial      uint64
}

func (e *entry) enqueue(change func(*entryInbox), interrupt bool) {
	e.inboxMu.Lock()
	change(&e.inbox)
	if interrupt && e.interruptCancel != nil {
		e.interruptCancel()
	}
	e.inboxMu.Unlock()
	e.notify()
}

func (e *entry) apply(c Connection) {
	e.enqueue(func(in *entryInbox) {
		previous := e.ingress
		if c.Seq != 0 && previous.Seq != 0 && (c.Seq < previous.Seq || (c.Seq == previous.Seq && c.HostKeyFingerprint == previous.HostKeyFingerprint)) {
			return
		}
		if c.Generation != previous.Generation || c.HostKeyFingerprint != previous.HostKeyFingerprint || c.State != previous.State {
			in.interrupted = true
			if e.interruptCancel != nil {
				e.interruptCancel()
			}
		}
		e.ingress = c
		in.observation = &c
	}, false)
}
func (e *entry) inputsChanged() { e.enqueue(func(in *entryInbox) { in.inputs++ }, false) }
func (e *entry) refresh()       { e.enqueue(func(in *entryInbox) { in.refresh++ }, false) }
func (e *entry) freeze() <-chan struct{} {
	e.inboxMu.Lock()
	select {
	case <-e.stopped:
		e.inboxMu.Unlock()
		return e.stopped
	default:
	}
	if e.inbox.freezeAck == nil {
		e.inbox.freezeAck = make(chan struct{})
	}
	done := e.inbox.freezeAck
	e.inbox.serial++
	e.inbox.freeze = e.inbox.serial
	if e.interruptCancel != nil {
		e.interruptCancel()
	}
	e.inboxMu.Unlock()
	e.notify()
	return done
}

func (e *entry) thaw() {
	e.enqueue(func(in *entryInbox) { in.serial++; in.thaw = in.serial; in.inputs++ }, true)
}

func (e *entry) clientLost(peer *client.Client) {
	e.enqueue(func(in *entryInbox) { in.lost = peer }, false)
}
func (e *entry) close() { e.enqueue(func(in *entryInbox) { in.close = true }, true) }

func (e *entry) drainInbox() bool {
	e.inboxMu.Lock()
	in := e.inbox
	e.inbox = entryInbox{}
	e.inboxMu.Unlock()
	if in.freezeAck != nil {
		defer close(in.freezeAck)
	}
	if in.close {
		e.accept(event{kind: eventClose})
		return true
	}
	if in.observation != nil {
		e.accept(event{kind: eventObserve, conn: *in.observation, interrupted: in.interrupted})
	}
	if in.inputs != 0 {
		e.accept(e.checkInputs())
	}
	if in.freeze != 0 {
		e.accept(event{kind: eventFreeze})
	}
	if in.thaw > in.freeze {
		e.accept(event{kind: eventThaw})
		e.accept(e.checkInputs())
	}
	if in.lost != nil {
		// Any loss signal rechecks the CURRENT peer. A delayed watch for an
		// old peer cannot overwrite and hide the current peer's queued loss.
		e.mu.Lock()
		peer := e.client
		e.mu.Unlock()
		if peer != nil {
			select {
			case <-peer.Done():
				e.accept(event{kind: eventLost, peer: peer})
			default:
			}
		}
	}
	if in.refresh != 0 {
		e.accept(event{kind: eventRefresh})
	}
	return false
}
