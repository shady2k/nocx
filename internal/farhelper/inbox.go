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
	thaw        uint64
	lost        *client.Client
	publish     bool
	retiring    []*client.Client
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
func (e *entry) inputsChanged() { e.enqueue(func(in *entryInbox) { in.inputs++ }, true) }
func (e *entry) refresh()       { e.enqueue(func(in *entryInbox) { in.refresh++ }, false) }
func (e *entry) freeze() {
	e.enqueue(func(in *entryInbox) { in.serial++; in.freeze = in.serial }, true)
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
	for _, peer := range in.retiring {
		_ = peer.Close()
	}
	if in.close {
		e.finishClose()
		return true
	}
	if in.interrupted {
		e.mu.Lock()
		peer := e.detachClientLocked()
		e.attempt = 0
		e.nextAttemptAt = 0
		_, changed := e.publishLocked()
		e.mu.Unlock()
		if peer != nil {
			e.closePeer(peer)
		}
		if changed {
			e.requestPublication()
		}
	}
	if in.observation != nil {
		e.handleApply(*in.observation)
	}
	if in.inputs != 0 {
		e.handleInputsChanged()
	}
	if in.freeze != 0 || in.thaw != 0 {
		// Even a freeze immediately followed by thaw must retire the old client.
		if in.freeze != 0 {
			e.handleFreeze()
		}
		if in.thaw > in.freeze {
			e.handleThaw()
		}
	}
	if in.lost != nil {
		e.handleClientLost(in.lost)
	}
	if in.refresh != 0 {
		e.handleRefresh()
	}
	if in.publish {
		e.mu.Lock()
		status := e.lastStatus
		e.mu.Unlock()
		e.r.publishEntry(e, status)
	}
	return false
}

func (e *entry) requestPublication() { e.enqueue(func(in *entryInbox) { in.publish = true }, false) }
func (e *entry) closePeer(peer *client.Client) {
	e.enqueue(func(in *entryInbox) { in.retiring = append(in.retiring, peer) }, false)
}
