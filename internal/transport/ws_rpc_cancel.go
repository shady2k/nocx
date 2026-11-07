package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/shady2k/nocx/internal/operation/catalog"

	"github.com/shady2k/nocx/internal/transport/control"
)

// Every call a person makes is cancellable (ADR-8eej3e66 §8). The method
// name is not a filter any more: a withdrawal reaches the request's context,
// and whether the work can stop is the handler's answer to that, not a list
// kept here.
func requestMethodCancellable(string) bool { return true }

// requestCancels owns the cancellation interval for request-scoped control
// work. A request ID is registered before submission, remains registered until
// its handler returns, and is keyed per WebSocket connection by its owner.
type requestCancels struct {
	mu      sync.Mutex
	running map[string]*requestCancel
}

type requestCancel struct {
	cancel context.CancelFunc

	// mu makes start-against-cancel one step. A queued worker asks begin
	// immediately before Run; a withdrawal sets withdrawn under the same
	// lock. Exactly one of them decides whether Run is called.
	mu        sync.Mutex
	withdrawn bool
	started   bool
	retained  []context.CancelFunc
}

// begin is the worker's start gate: false means the request was withdrawn
// while it waited, so its handler must not run.
func (e *requestCancel) begin() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.withdrawn {
		return false
	}
	e.started = true
	return true
}

// withdraw marks the request cancelled and ends its context. A running
// handler sees the context end; a queued one will be refused by begin.
func (e *requestCancel) withdraw() {
	e.mu.Lock()
	e.withdrawn = true
	retained := append([]context.CancelFunc(nil), e.retained...)
	e.mu.Unlock()
	e.cancel()
	for _, cancel := range retained {
		cancel()
	}
}

// retainWork registers work that outlives the handler but belongs to this
// request: agent.ask hands its stream to a background submission after the
// response, so the handler's return must not cancel it, while a withdrawal
// and a disconnect must. The request context itself remains cancellable for
// everything that does not opt in.
func (e *requestCancel) retainWork(cancel context.CancelFunc) {
	e.mu.Lock()
	if e.withdrawn {
		e.mu.Unlock()
		cancel()
		return
	}
	e.retained = append(e.retained, cancel)
	e.mu.Unlock()
}

type requestCancelContextKey struct{}

func withRequestCancel(ctx context.Context, e *requestCancel) context.Context {
	return context.WithValue(ctx, requestCancelContextKey{}, e)
}

// retainRequestWork marks cancel as belonging to the request in ctx. It
// reports false for a context with no request cancellation registered.
func retainRequestWork(ctx context.Context, cancel context.CancelFunc) bool {
	e, ok := ctx.Value(requestCancelContextKey{}).(*requestCancel)
	if !ok {
		return false
	}
	e.retainWork(cancel)
	return true
}

func newRequestCancels() *requestCancels {
	return &requestCancels{running: make(map[string]*requestCancel)}
}

func requestIDKey(id json.RawMessage) string {
	return string(id)
}

func (c *requestCancels) register(id json.RawMessage, cancel context.CancelFunc) (*requestCancel, error) {
	key := requestIDKey(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.running[key]; exists {
		return nil, errors.New("request id is already in flight")
	}
	entry := &requestCancel{cancel: cancel}
	c.running[key] = entry
	return entry, nil
}

func (c *requestCancels) cancel(id json.RawMessage) bool {
	c.mu.Lock()
	entry, ok := c.running[requestIDKey(id)]
	c.mu.Unlock()
	if !ok {
		return false
	}
	entry.withdraw()
	return true
}

func (c *requestCancels) drop(id json.RawMessage, entry *requestCancel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[requestIDKey(id)] != entry {
		return
	}
	// Work the handler RETAINED outlives it, so its cancellation must remain
	// reachable: a disconnect has to end the run, and rpc.cancel by the id
	// the renderer holds must still find it. The entry stays until the
	// connection closes.
	entry.mu.Lock()
	retained := len(entry.retained)
	entry.mu.Unlock()
	if retained != 0 {
		return
	}
	delete(c.running, requestIDKey(id))
}

func (c *requestCancels) cancelAll() {
	c.mu.Lock()
	entries := make([]*requestCancel, 0, len(c.running))
	for _, entry := range c.running {
		entries = append(entries, entry)
	}
	c.mu.Unlock()
	for _, entry := range entries {
		// The connection context is the parent of every run derived from it,
		// so a disconnect reaches retained work without an explicit
		// withdrawal. Retained cancels stay for rpc.cancel, which is a
		// withdrawal of the request while the connection lives on.
		entry.cancel()
	}
}

type rpcCancelParams struct {
	ID json.RawMessage `json:"id"`
}

func validateRPCCancelRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "params are required"
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return "params must be a JSON object"
	}
	if len(fields) != 1 {
		return "params must contain only id"
	}
	id, ok := fields["id"]
	if !ok || len(id) == 0 || bytes.Equal(bytes.TrimSpace(id), []byte("null")) {
		return "id is required"
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(id))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "id must be a string or number"
	}
	switch value.(type) {
	case string, json.Number:
		return ""
	default:
		return "id must be a string or number"
	}
}

func rpcCancelSpec(immediate control.Submission) methodSpec {
	return reg(immediate, catalog.OpRpcCancel, params(validateRPCCancelRaw), func(w *wsConn, _ *connState, r Responder) handlerFunc {
		return func(_ context.Context, req jsonrpcRequest) {
			var p rpcCancelParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: params must be an object"})
				return
			}
			// The renderer sends this as a notification. A request-shaped
			// cancellation gets a normal empty response for harnesses and
			// diagnostics; an unknown target is deliberately idempotent.
			w.requestCancels.cancel(p.ID)
			// THE CREDIT IS RETURNED HERE, on the read loop (ADR-8eej3e66
			// §7). The client frees its slot the moment it withdraws, so the
			// server must free the same slot before it reads the client's
			// next frame — otherwise a well-behaved client that cancels and
			// immediately acts again would briefly be over the window and be
			// disconnected for it. The handler runs inline, so this release
			// is ordered before every later request on this connection.
			w.credit.releaseByID(p.ID)
			if req.ID != nil {
				_ = r.TryResult(req.ID, json.RawMessage(`{}`))
			}
		}
	})
}

type connectionContextKey struct{}

// withConnectionContext makes the connection's own context reachable from a
// request context. Work a handler starts that belongs to the connection or
// session — a run stream, an output pump — must derive from it, not from the
// request context, which ends when the handler returns; deriving from the
// connection keeps disconnect semantics (a lost socket ends the work) while
// the request's own withdrawal stays a separate signal.
func withConnectionContext(ctx, connection context.Context) context.Context {
	return context.WithValue(ctx, connectionContextKey{}, connection)
}

// connectionContext returns the context the connection's read loop owns.
func connectionContext(ctx context.Context) (context.Context, bool) {
	c, ok := ctx.Value(connectionContextKey{}).(context.Context)
	return c, ok && c != nil
}
