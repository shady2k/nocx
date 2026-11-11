package transport

// The stored-row read's own admission (owner, 2026-11-10, nocx-t7xhe).
//
// A response is not delivered when a handler returns it: it waits in the
// connection's critical queue and then in one socket write. The queue is 64
// frames and the process has 32 MiB; five hundred whole block bodies put more
// than that in it, the overflow policy closes the socket, and the person sees
// the Connection status modal for a read they never asked to be urgent.
//
// So a paged stored-row read holds a slot from before its store read until its
// RESPONSE has left the socket. With no slot free the server answers at once
// with retryAfterMs — it does not queue without bound, does not stop reading
// the socket, and does not close the connection. The answer is internal: the
// renderer's own priority queue waits the stated time and sends the most
// urgent request it has at that moment, so a person never meets it
// (ADR-y45sp41c, refining ADR-8eej3e66 §7 for this one internal retry).

import "sync"

// RowReadSlots is how many stored-row responses one connection may have in
// flight at once: in the handler still reading, in the critical queue, or in
// the socket write. Four is deliberately below the 64-frame critical queue
// and below the content lane's eight permits, so a whole page worth of
// responses cannot fill the queue even when every response is at the largest
// size the artifact ceiling allows.
const RowReadSlots = 4

// RowReadRetryAfterMs is the one retry hint the refusal carries. The renderer
// waits exactly this long before offering its most urgent pending request;
// the value is a scheduling hint, never a promise that the slot will be free.
const RowReadRetryAfterMs = 20

// RowReadDeferredCode is the JSON-RPC error code of the internal refusal. It
// sits in the server-error range and is distinct from OperationExpiredCode:
// this call did not expire, and nothing about it failed — another read owns
// the slot right now.
const RowReadDeferredCode = -32014

// rowReadFlow is one connection's slot pool. It is a counting semaphore, not
// a queue: an arriving request either takes a free slot or is answered with
// the retry hint, so the server never grows a backlog of reads nobody asked
// for yet.
type rowReadFlow struct {
	slots chan struct{}
}

func newRowReadFlow(n int) *rowReadFlow {
	if n <= 0 {
		n = RowReadSlots
	}
	return &rowReadFlow{slots: make(chan struct{}, n)}
}

func (f *rowReadFlow) tryAcquire() bool {
	if f == nil {
		return true
	}
	select {
	case f.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (f *rowReadFlow) release() {
	if f == nil {
		return
	}
	select {
	case <-f.slots:
	default:
	}
}

// rowReadDeferredData is the payload of the internal refusal, contracted in
// contracts/row.readDeferred.schema.json. The retry hint is the only field:
// the renderer's queue needs nothing else to decide when and what to send.
type rowReadDeferredData struct {
	RetryAfterMs int `json:"retryAfterMs"`
}

const rowReadDeferredMessage = "The stored rows are busy; retry"

// answerRowReadDeferred answers a paged stored-row read that found no slot.
// It is not an error a person reads — the renderer retries — but it is a real
// answer, so it travels as JSON-RPC error data rather than as silence.
func answerRowReadDeferred(r Responder, req jsonrpcRequest) {
	_ = r.TryError(req.ID, RPCError{
		Code:    RowReadDeferredCode,
		Message: rowReadDeferredMessage,
		Data:    rowReadDeferredData{RetryAfterMs: RowReadRetryAfterMs},
	})
}

// releaseOnce makes the slot's release exactly once even when the response
// path and a deferred error path both reach it.
func releaseOnce(release func()) func() {
	var once sync.Once
	return func() { once.Do(release) }
}
