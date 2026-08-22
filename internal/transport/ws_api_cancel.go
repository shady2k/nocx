package transport

// Which running exchange a token names — the whole of api.request.cancel's
// state, and it is small on purpose.
//
// THE TOKEN IS THE RENDERER'S OWN, NOT THE JSON-RPC REQUEST ID. The id
// belongs to the dispatcher: it is minted per call, consumed when the result
// arrives, and never handed to the caller that asked. Opening it up so one
// button could name a request would be a second addressing scheme over the
// same thing — and the first surface that wanted to cancel something else
// would either copy it or invent a third. A client-minted token costs one
// field, is generated where the Stop button lives, and means the dispatcher
// keeps its id to itself.
//
// THE INTERVAL, BOTH ENDS. A token resolves from BEFORE the exchange starts
// — registered on the line above the Send call, so no window exists in which
// a person can press Stop and be told there is nothing to stop — until the
// exchange has SETTLED, whichever way it settled, because the drop is
// deferred in the same function. Never after: a second Stop on a run that
// has already come back finds nothing and is refused, which is the honest
// answer rather than a silent success on an exchange nobody can affect.
//
// SCOPED TO THE CONNECTION. A token is a name a window chose, and two
// windows may choose the same one; keyed by token alone, one window's Stop
// would end another window's run. The connection id is the window, so the
// key is the pair.

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// errTokenInFlight refuses a SEND whose token already names a running
// exchange on this connection. Accepting it would leave two exchanges under
// one name and a Stop that could only guess which it meant — so the send is
// refused rather than the cancel being made ambiguous.
var errTokenInFlight = errors.New("a request is already running under this token")

// errUnknownToken refuses a CANCEL that names no running exchange. Refused
// BY NAME rather than answered emptily: "there was nothing to stop" and "it
// is stopped" are different facts, and a caller that cannot tell them apart
// cannot report either.
var errUnknownToken = errors.New("no request is running under this token")

// cancelKey is one window's name for one exchange.
type cancelKey struct {
	conn  uint64
	token string
}

// sendCancels is the registry. One per server, shared by every connection —
// the key carries the connection, so sharing the map costs nothing and
// saves a lifecycle nobody would remember to end.
type sendCancels struct {
	mu      sync.Mutex
	running map[cancelKey]context.CancelFunc
}

func newSendCancels() *sendCancels {
	return &sendCancels{running: make(map[cancelKey]context.CancelFunc)}
}

// register names a running exchange. It fails rather than overwriting: an
// overwrite would strand the first exchange's cancel and leave its entry to
// be dropped by whichever of the two finished last.
func (c *sendCancels) register(conn uint64, token string, cancel context.CancelFunc) error {
	key := cancelKey{conn: conn, token: token}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, running := c.running[key]; running {
		return fmt.Errorf("%w (token %q)", errTokenInFlight, token)
	}
	c.running[key] = cancel
	return nil
}

// stop fires the cancel for a token and reports whether there was one. It
// does NOT remove the entry — the send's own deferred drop does that, and
// having one owner of the removal is what makes a second Stop find nothing
// rather than race the first one's cleanup.
func (c *sendCancels) stop(conn uint64, token string) bool {
	c.mu.Lock()
	cancel, running := c.running[cancelKey{conn: conn, token: token}]
	c.mu.Unlock()
	if !running {
		return false
	}
	// Outside the lock: cancel wakes whatever is blocked on the context,
	// and holding a registry-wide mutex across that would serialise every
	// other window's Stop behind it.
	cancel()
	return true
}

// drop ends the interval. Deferred by the send, so it runs on every exit
// path including a panic.
func (c *sendCancels) drop(conn uint64, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.running, cancelKey{conn: conn, token: token})
}
