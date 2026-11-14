package toolendpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
)

// cancelCallMethod is the one notification this endpoint accepts: a caller
// naming a request of its own, on the connection that carries it, and asking
// that it stop (nocx-98qho).
//
// IT IS A NOTIFICATION AND NOT A REQUEST, for two reasons. MCP's
// notifications/cancelled has this shape, and the adapter forwards it as this
// message; and a cancel owes no answer — the caller has already stopped
// waiting, so an answer would be a second thing to keep in step. It must also
// never be ANSWERED WITH AN ERROR: a notification carries no id, and an id-less
// error on this socket means the CONNECTION was refused to the adapter, so an
// error written for a malformed cancel would end the session instead of one
// call.
const cancelCallMethod = "calls.cancel"

// requestCancels is the set of calls in flight on ONE connection, keyed by the
// id the caller gave each. It is what makes a cancel name a CALL rather than a
// connection: the connection's context is the parent of every request, and
// ending it would take the siblings a stdio session shares the connection with
// (nocx-tlaft, nocx-98qho).
type requestCancels struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newRequestCancels() *requestCancels {
	return &requestCancels{cancels: make(map[string]context.CancelFunc)}
}

func (c *requestCancels) add(id json.RawMessage, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancels[requestKey(id)] = cancel
}

func (c *requestCancels) remove(id json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cancels, requestKey(id))
}

// cancel ends the named request and reports whether this connection held it. A
// false is not an error and is not reported: a cancel and an answer race by
// nature — the request may have finished between the caller deciding to stop it
// and this arriving — and either way nobody is waiting.
func (c *requestCancels) cancel(key string) bool {
	c.mu.Lock()
	cancel := c.cancels[key]
	c.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// requestKey is a request id as the caller spelled it. JSON-RPC allows a string
// or a number, and the endpoint carries the raw form through every path already,
// so the cancel compares those bytes rather than deciding afresh what an id is.
func requestKey(id json.RawMessage) string {
	return string(bytes.TrimSpace(id))
}

// cancelRequest handles a cancel notification and reports whether the message
// was one, so the read loop knows whether it was consumed. Everything else is
// left to the read loop, which refuses notifications it does not know.
//
// A malformed cancel is SWALLOWED rather than refused, for the reason
// cancelCallMethod states: there is no id to attach an error to, and the
// adapter reads an id-less error as a refusal of the whole connection.
func (e *Endpoint) cancelRequest(request rpcRequest, cancels *requestCancels) bool {
	if request.Method != cancelCallMethod {
		return false
	}
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		return true
	}
	key := requestKey(params.RequestID)
	if key == "" || key == "null" {
		return true
	}
	if cancels.cancel(key) {
		e.log().Debug("tool endpoint: call cancelled", "request_id", requestIDTag(params.RequestID))
	}
	return true
}
