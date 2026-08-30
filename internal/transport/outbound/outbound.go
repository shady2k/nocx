// Package outbound owns the outbound side of a WebSocket connection: the
// socket, the bounded queues in front of it, and the single goroutine (the
// pump) that writes to it.
//
// The enforcement is the package boundary. The *websocket.Conn lives here
// and nowhere else; everything outside this package can put a frame on a
// queue, and nothing outside can reach the socket. A handler that wants to
// respond has exactly one option — a non-blocking enqueue — so a stuck
// renderer can delay the read loop by no more than one channel send.
//
// Frames are not one class. The refreshable data plane (PTY output,
// notifications) rides the shared queue: a full queue cannot report itself,
// so the overflow policy (see overflow) marks the connection
// outbound-stalled, reserves one control-overload slot for the stall
// notice, and closes the WebSocket if even that slot cannot be reached —
// the renderer's existing disconnect/reconnect surface then makes the
// failure visible, and sessions survive the disconnect by design (AD-9,
// the replay ring). Dropping a refreshable frame is safe: the renderer
// re-syncs from the next one.
//
// A JSON-RPC response is not refreshable — it is the other half of a
// promise, correlated by request id, and dropping it strands the caller
// forever. Responses therefore get reserved capacity (respQueue) that the
// data plane cannot consume, the pump drains them ahead of the data queue,
// and a response that cannot be queued even there closes the connection
// rather than disappearing (see TryEnqueueResponse). The one outcome that
// must not survive is silence.
package outbound

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Message type constants, mirroring gorilla/websocket. The transport passes
// its own websocket.* constants through; they are re-declared here so the
// pump can build the stall notice without a second import of the library.
const (
	TextMessage   = 1
	BinaryMessage = 2
)

// DefaultQueueDepth is the per-connection outbound queue depth in frames for
// the refreshable data plane: PTY output and control notifications. Sized
// against the data-plane credit window: internal/transport/ring.go bounds
// unacked output at CreditLimit = 64 KiB in FairChunk = 8 KiB frames, i.e.
// at most 8 data frames in flight per attached session. 256 frames holds a
// handful of sessions' credit bursts plus a control burst of a few dozen
// notifications, while still tripping the stall policy within roughly a
// second of burst traffic on a genuinely stuck renderer. JSON-RPC responses
// do not ride this queue — they have reserved capacity of their own
// (DefaultResponseQueueDepth), so data traffic cannot drop a response.
const DefaultQueueDepth = 256

// DefaultResponseQueueDepth is the per-connection reserved capacity for
// JSON-RPC responses, in frames. A response is the other half of a promise:
// the caller correlates by request id and waits forever if it is dropped,
// so responses get a queue the refreshable data plane cannot consume, and
// the pump drains it ahead of the data queue. Sized against the control
// plane's own admission bounds (the ordinary lane holds at most 8
// concurrent tasks, each of which enqueues at most one response; the read
// loop enqueues one more for its error paths), 64 is roughly four times
// the worst legitimate burst while still being a hard bound — a response
// that cannot be queued even here closes the connection instead of
// disappearing.
const DefaultResponseQueueDepth = 64

// DefaultWriteDeadline bounds each pump write. This preserves the
// 10-second bound the transport's wsWriteDeadline gave every WebSocket
// write: a stuck renderer costs one write deadline per frame, never a hang.
const DefaultWriteDeadline = 10 * time.Second

// StallNoticeMethod is the control-plane notification the reserved slot
// carries when the refreshable queue overflows. The renderer treats it as
// a cue to reconnect: a connection whose outbound data plane is saturated
// cannot deliver everything it owes, and the reconnect surface (with the
// session replay ring) is how the product makes that visible. Responses
// never take this path — an undeliverable response closes the connection
// (see TryEnqueueResponse) rather than announcing a stall and going quiet.
const StallNoticeMethod = "outbound.stalled"

// stallNotice is the pre-marshaled stall notification. It is queued through
// the reserved overload slot, never through the main queue (which is full
// by definition at that point).
var stallNotice = Frame{
	MsgType: TextMessage,
	Data:    []byte(`{"jsonrpc":"2.0","method":"outbound.stalled"}`),
}

// Frame is one queued outbound message: a WebSocket message type and its
// bytes. Control-plane envelopes are TextMessage; PTY output rides
// BinaryMessage (AD-1, raw bytes are never wrapped).
type Frame struct {
	MsgType int
	Data    []byte
}

// Socket is the write seam the pump writes through. *websocket.Conn
// satisfies it via the WebSocket adapter. Tests inject a fake whose
// WriteMessage blocks on a channel, which is how the acceptance tests hold
// the pump mid-write deterministically.
type Socket interface {
	ReadMessage() (int, []byte, error)
	SetWriteDeadline(t time.Time) error
	WriteMessage(msgType int, data []byte) error
	Close() error
}

// WebSocket adapts a *websocket.Conn to Socket. This is the ONLY place a
// *websocket.Conn exists in the process's outbound path.
type WebSocket struct{ Conn *websocket.Conn }

// NewWebSocket adapts a *websocket.Conn to Socket and applies the ingress
// read limit in the same breath: after this constructor the raw connection
// is owned by this package and no other reference may outlive the call.
// readLimit bounds a single inbound frame at the protocol layer (gorilla
// refuses an oversized frame with close code 1009 during ReadMessage).
func NewWebSocket(conn *websocket.Conn, readLimit int64) WebSocket {
	conn.SetReadLimit(readLimit)
	return WebSocket{Conn: conn}
}

func (w WebSocket) ReadMessage() (int, []byte, error) { return w.Conn.ReadMessage() }

func (w WebSocket) SetReadDeadline(t time.Time) error { return w.Conn.SetReadDeadline(t) }

func (w WebSocket) SetWriteDeadline(t time.Time) error { return w.Conn.SetWriteDeadline(t) }

func (w WebSocket) WriteMessage(msgType int, data []byte) error {
	return w.Conn.WriteMessage(msgType, data)
}

func (w WebSocket) Close() error { return w.Conn.Close() }

// renderer from delaying another, and the budget keeps the whole process
// from buffering unboundedly when many connections stall at once.
//
// A frame's bytes are reserved on enqueue and released when the pump
// dequeues it (or when a closed connection drains its queue). Bytes held by
// an in-flight pump write are not counted — the bound is on queued frames,
// which is the memory this budget exists to cap.
type Budget struct {
	queued atomic.Int64
	max    int64
}

// NewBudget returns a Budget that allows at most maxBytes of queued outbound
// frames across all its connections.
func NewBudget(maxBytes int64) *Budget { return &Budget{max: maxBytes} }

func (b *Budget) tryReserve(n int64) bool {
	for {
		cur := b.queued.Load()
		if cur+n > b.max {
			return false
		}
		if b.queued.CompareAndSwap(cur, cur+n) {
			return true
		}
	}
}

func (b *Budget) release(n int64) { b.queued.Add(-n) }

// ErrStalled is returned by TryEnqueue when the frame had to be dropped:
// the connection's outbound queue is full (or the process-wide budget is
// exhausted), the connection is marked outbound-stalled, and the stall
// notice has been reserved (or the connection closed — see Conn.Stalled and
// ErrConnClosed).
var ErrStalled = errors.New("outbound queue full: frame dropped")

// ErrConnClosed is returned by TryEnqueue and WaitForRoom once the
// connection has been closed. Callers treat it as terminal.
var ErrConnClosed = errors.New("outbound connection closed")

// Config tunes a Conn. Zero values select the defaults.
type Config struct {
	// QueueDepth is the per-connection queue depth in frames; 0 selects
	// DefaultQueueDepth.
	QueueDepth int
	// WriteDeadline bounds each pump write; 0 selects
	// DefaultWriteDeadline.
	WriteDeadline time.Duration
	// Budget is the process-wide byte cap shared across connections; nil
	// disables the additional bound.
	Budget *Budget
	// OnStall observes health↔stalled transitions: called with true when
	// the connection becomes outbound-stalled and with false when the
	// queue accepts a frame again. May be nil.
	OnStall func(stalled bool)
}

// Conn is one connection's outbound side: a bounded refreshable-frame queue,
// a bounded reserved response queue, and the pump goroutine that drains
// them. All methods are safe for concurrent use. Exactly one goroutine
// (pump) ever writes to the socket.
type Conn struct {
	sock Socket

	queue     chan Frame // refreshable: PTY output, notifications
	respQueue chan Frame // reserved: JSON-RPC responses (never dropped)
	overload  chan Frame // capacity 1: the reserved control-overload slot
	room      chan struct{}

	stalled    atomic.Bool
	stallCount atomic.Uint64
	budget     *Budget
	onStall    func(bool)

	closeOnce sync.Once
	closed    chan struct{} // closed by Close()
	done      chan struct{} // closed when the pump exits
}

// New creates a Conn and starts its pump.
func New(sock Socket, cfg Config) *Conn {
	depth := cfg.QueueDepth
	if depth <= 0 {
		depth = DefaultQueueDepth
	}
	c := &Conn{
		sock:      sock,
		queue:     make(chan Frame, depth),
		respQueue: make(chan Frame, DefaultResponseQueueDepth),
		overload:  make(chan Frame, 1),
		room:      make(chan struct{}, 1),
		budget:    cfg.Budget,
		onStall:   cfg.OnStall,
		closed:    make(chan struct{}),
		done:      make(chan struct{}),
	}
	go c.pump(cfg.WriteDeadline)
	return c
}

// TryEnqueue queues one refreshable frame — PTY output, a control
// notification — without blocking. On success the frame is owned by the
// pump and will be written exactly once. On a full queue (or exhausted
// budget) it applies the overflow policy and returns ErrStalled; the frame
// is dropped, which is safe because a notification is refreshable state the
// renderer re-syncs from the next one. After Close it returns ErrConnClosed.
// JSON-RPC responses must NOT use this path: use TryEnqueueResponse, which
// has reserved capacity and is never silently dropped.
func (c *Conn) TryEnqueue(msgType int, data []byte) error {
	select {
	case <-c.closed:
		return ErrConnClosed
	default:
	}

	if c.budget != nil && !c.budget.tryReserve(int64(len(data))) {
		return c.overflow(msgType, data)
	}
	select {
	case c.queue <- Frame{MsgType: msgType, Data: data}:
		// The queue accepted a frame again: whatever stall was in
		// effect is over.
		if c.stalled.Swap(false) {
			c.setStall(false)
		}
		return nil
	default:
		if c.budget != nil {
			c.budget.release(int64(len(data)))
		}
		return c.overflow(msgType, data)
	}
}

// TryEnqueueResponse queues a JSON-RPC response without blocking. It is the
// only way a response may reach the socket: a response is the other half of
// a promise — the caller correlates by request id and waits forever if it
// is dropped — so it gets reserved capacity (respQueue) that the refreshable
// data plane cannot consume, and the pump drains responses ahead of data.
// Accepting a response does not clear the stall flag: that flag reports the
// refreshable queue, which may still be full.
//
// A response that cannot be queued even in the reserved capacity (respQueue
// full, or the process-wide budget exhausted) is never silently dropped:
// the connection closes and returns ErrStalled. The renderer's
// disconnect/reconnect surface then rejects the caller's pending promise —
// a result, an error, or a closed connection, never silence. After Close it
// returns ErrConnClosed.
func (c *Conn) TryEnqueueResponse(data []byte) error {
	select {
	case <-c.closed:
		return ErrConnClosed
	default:
	}

	if c.budget != nil && !c.budget.tryReserve(int64(len(data))) {
		c.overflowResponse()
		return ErrStalled
	}
	select {
	case c.respQueue <- Frame{MsgType: TextMessage, Data: data}:
		return nil
	default:
		if c.budget != nil {
			c.budget.release(int64(len(data)))
		}
		c.overflowResponse()
		return ErrStalled
	}
}

// overflow is the terminal policy for a full refreshable queue. A full
// queue cannot report itself — a queued "busy" notification cannot be
// delivered either — so:
//
//  1. mark the connection outbound-stalled (observed via Stalled() and the
//     OnStall transition callback);
//  2. attempt one reserved control-overload slot — the single-slot overload
//     channel that carries the stall notice and jumps the queue;
//  3. if even that slot is occupied, a previous notice is still undelivered
//     and the pump has drained nothing: the connection is beyond help.
//     Close the WebSocket and let the renderer's disconnect/reconnect
//     surface take over (sessions survive by design, AD-9).
//
// The busy frame itself is dropped: there is nowhere to put it, and the
// stall notice is the renderer's cue to reconnect and resync. Responses
// never come through here — see overflowResponse.
func (c *Conn) overflow(msgType int, data []byte) error {
	if !c.stalled.Swap(true) {
		c.setStall(true)
	}
	c.stallCount.Add(1)
	select {
	case c.overload <- stallNotice:
		return ErrStalled
	default:
		c.Close()
		return ErrStalled
	}
}

// overflowResponse is the terminal policy for a response that cannot be
// queued: the reserved response queue is full or the process-wide budget is
// exhausted. The stall notice is not an option — it announces a saturated
// connection but delivers no answer, which is the same silence — so the
// connection closes: the renderer's disconnect/reconnect surface makes the
// failure visible and every in-flight promise rejects on disconnect. The
// stall transition is still reported so the episode is observable.
func (c *Conn) overflowResponse() {
	if !c.stalled.Swap(true) {
		c.setStall(true)
	}
	c.stallCount.Add(1)
	c.Close()
}

func (c *Conn) setStall(stalled bool) {
	if c.onStall != nil {
		c.onStall(stalled)
	}
}

// WaitForRoom blocks until the queue has room or ctx is done. It is used by
// the per-session ring streams (the transport's ringToConn), which are
// credit-bounded loops rather than handlers: they are allowed to wait for
// the renderer to drain. Handlers must never call this — their contract is
// the non-blocking TryEnqueue.
func (c *Conn) WaitForRoom(ctx context.Context) error {
	for {
		select {
		case <-c.closed:
			return ErrConnClosed
		default:
		}
		if len(c.queue) < cap(c.queue) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.closed:
			return ErrConnClosed
		case <-c.room:
		}
	}
}

// ReadMessage forwards to the socket. Only the transport's read loop calls
// it. gorilla permits concurrent ReadMessage/WriteMessage, so the pump
// writing while this reads is the supported configuration.
func (c *Conn) ReadMessage() (int, []byte, error) { return c.sock.ReadMessage() }

// SetReadDeadline forwards the transport's liveness deadline when the socket
// supports it. The optional assertion keeps test sockets focused on the
// behavior they model while the WebSocket adapter always provides the seam.
func (c *Conn) SetReadDeadline(t time.Time) error {
	setter, ok := c.sock.(interface{ SetReadDeadline(time.Time) error })
	if !ok {
		return nil
	}
	return setter.SetReadDeadline(t)
}

// Stalled reports whether the connection is currently outbound-stalled
// (the queue overflowed and has not accepted a frame since).
func (c *Conn) Stalled() bool { return c.stalled.Load() }

// StallCount counts overflow episodes since construction.
func (c *Conn) StallCount() uint64 { return c.stallCount.Load() }

// Close closes the socket and stops the pump. Idempotent and safe from any
// goroutine: closing the socket unblocks an in-flight pump write (gorilla
// permits Close concurrently with WriteMessage), so the pump exits promptly
// even mid-write. Frames still queued are released from the budget: they
// will never be written.
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		if c.budget != nil {
			for {
				select {
				case f := <-c.queue:
					c.budget.release(int64(len(f.Data)))
				case f := <-c.respQueue:
					c.budget.release(int64(len(f.Data)))
				default:
					goto drained
				}
			}
		drained:
		}
		_ = c.sock.Close()
		c.pingRoom()
	})
}

// Done returns a channel closed when the pump has exited.
func (c *Conn) Done() <-chan struct{} { return c.done }

// pump is the single writer to the socket. It drains the reserved overload
// slot first, then responses, then the refreshable queue: the stall notice
// is never stuck behind the frames it is reporting, and a response — the
// other half of a promise — is never stuck behind a burst of data the
// caller is not waiting on.
func (c *Conn) pump(writeDeadline time.Duration) {
	defer close(c.done)
	deadline := writeDeadline
	if deadline <= 0 {
		deadline = DefaultWriteDeadline
	}
	for {
		// Priority drain of the overload slot.
		select {
		case f := <-c.overload:
			if c.write(deadline, f) != nil {
				return
			}
			continue
		default:
		}
		// Responses next: reserved capacity, drained ahead of data.
		select {
		case f := <-c.respQueue:
			if c.budget != nil {
				c.budget.release(int64(len(f.Data)))
			}
			if c.write(deadline, f) != nil {
				return
			}
			continue
		default:
		}
		select {
		case <-c.closed:
			return
		case f := <-c.overload:
			if c.write(deadline, f) != nil {
				return
			}
		case f := <-c.respQueue:
			if c.budget != nil {
				c.budget.release(int64(len(f.Data)))
			}
			if c.write(deadline, f) != nil {
				return
			}
		case f := <-c.queue:
			// The frame left the queue: its budget reservation is
			// spent (the overload notice never had one).
			if c.budget != nil {
				c.budget.release(int64(len(f.Data)))
			}
			if c.write(deadline, f) != nil {
				return
			}
			c.pingRoom()
		}
	}
}

// write performs one bounded socket write. On failure it closes the socket
// (the connection is dead) and reports the error so the pump exits.
func (c *Conn) write(deadline time.Duration, f Frame) error {
	_ = c.sock.SetWriteDeadline(time.Now().Add(deadline))
	if err := c.sock.WriteMessage(f.MsgType, f.Data); err != nil {
		_ = c.sock.Close()
		return err
	}
	return nil
}

func (c *Conn) pingRoom() {
	select {
	case c.room <- struct{}{}:
	default:
	}
}
