package client

import (
	"encoding/json"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// The fs service's `changed` notification, as this side of the wire receives
// it (nocx-oalo1.12.10).
//
// # Why the routing is here and not in a service package
//
// A notification has no request to answer and no reply to route: the frame
// arrives on the client's read pump and is either handed to whoever subscribed
// or dropped. This package already owns that pump and every other notify arm
// (`sessionNotify`), so a second owner of "which frame goes where" would be a
// second read of the wire.
//
// # Why the id and not the connection
//
// One connection can carry many watches — one per directory a panel follows —
// and the id the caller minted is the only thing that tells them apart. The map
// is keyed by exactly that, so a subscription is a single map entry whose
// removal ends delivery and nothing else: no goroutine, no channel, no
// reference to a connection beyond the client's own lifetime.

// fsHandler is one registration: the callback, and the token that says the
// stop function ending this registration is still the one that installed the
// entry under its id. A token rather than a comparison of the function values,
// because Go function values are not comparable and the safe alternatives
// (reflect pointers, unsafe) are two ways to be roughly right where an integer
// is exact.
type fsHandler struct {
	token uint64
	fn    func(proto.FSChanged)
}

// OnFSChanged registers fn to receive every `changed` notification carrying
// id on this connection, and answers the function that ends the registration.
//
// fn runs on the read pump. It must return promptly without waiting for I/O
// or channel capacity. The fs watch uses it to record the revision and queue
// a hint for the consumer.
//
// Calling the returned stop twice is harmless, and it never ends a LATER
// registration under the same id: a re-subscription installs its handler
// before the old stop runs, and the token is what keeps the two apart.
func (c *Client) OnFSChanged(id string, fn func(proto.FSChanged)) (stop func()) {
	if id == "" || fn == nil {
		return func() {}
	}
	c.mu.Lock()
	if c.fsWatches == nil {
		c.fsWatches = make(map[string]fsHandler)
	}
	c.nextID++
	token := c.nextID
	c.fsWatches[id] = fsHandler{token: token, fn: fn}
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		if current, ok := c.fsWatches[id]; ok && current.token == token {
			delete(c.fsWatches, id)
		}
		c.mu.Unlock()
	}
}

// fsChanged delivers one `changed` notification to the handler registered for
// its id, if any. An unregistered id is not an error and is not logged as one:
// a notification can overtake the unwatch that ended its subscription, and
// dropping it is the whole of what the removal meant.
func (c *Client) fsChanged(raw json.RawMessage) {
	var changed proto.FSChanged
	if err := json.Unmarshal(raw, &changed); err != nil {
		c.log.Warn("malformed fs changed notification", "err", err)
		return
	}
	c.mu.Lock()
	handler, ok := c.fsWatches[changed.ID]
	c.mu.Unlock()
	if !ok || handler.fn == nil {
		return
	}
	handler.fn(changed)
}
