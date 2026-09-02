package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/shady2k/nocx/internal/transport/control"
)

// Only read-only completion calls are cancellable. Mutating control methods
// deliberately keep their existing context and cannot be withdrawn by a
// renderer-side supersession.
var cancellableRequestMethods = map[string]struct{}{
	"fs.complete":    {},
	"history.query":  {},
	"shell.complete": {},
}

func requestMethodCancellable(method string) bool {
	_, ok := cancellableRequestMethods[method]
	return ok
}

// requestCancels owns the cancellation interval for request-scoped control
// work. A request ID is registered before submission, remains registered until
// its handler returns, and is keyed per WebSocket connection by its owner.
type requestCancels struct {
	mu      sync.Mutex
	running map[string]*requestCancel
}

type requestCancel struct {
	cancel context.CancelFunc
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
	entry.cancel()
	return true
}

func (c *requestCancels) drop(id json.RawMessage, entry *requestCancel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[requestIDKey(id)] == entry {
		delete(c.running, requestIDKey(id))
	}
}

func (c *requestCancels) cancelAll() {
	c.mu.Lock()
	entries := make([]*requestCancel, 0, len(c.running))
	for _, entry := range c.running {
		entries = append(entries, entry)
	}
	c.mu.Unlock()
	for _, entry := range entries {
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
	return reg(immediate, "rpc.cancel", params(validateRPCCancelRaw), func(w *wsConn, _ *connState, r Responder) handlerFunc {
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
			if req.ID != nil {
				_ = r.TryResult(req.ID, json.RawMessage(`{}`))
			}
		}
	})
}
