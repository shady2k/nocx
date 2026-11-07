package transport

// The Operations row for work the backend does inside a person's call
// (ADR-8eej3e66 §8).
//
// A person's `open` is one JSON-RPC call and, on a first connection to a host
// where the helper is not installed, most of that call is a 15.3 MB upload to
// somebody else's machine. The call row the renderer makes for itself says
// "Opening your terminal", which is true and says nothing about what is
// happening on the link. The catalogue already declares the install as work a
// person is watching (`row: always`, "Installing the helper on this host"), so
// this file is the wire half of that declaration: the install reports what it
// is doing, and this puts it on the connection that made the call.
//
// # Why the row is addressed by the REQUEST
//
// The row belongs to the invocation, and the invocation's identity on the wire
// is the JSON-RPC id it was sent with, so a Cancel on the row is `rpc.cancel`
// with that id and needs no second addressing scheme (ADR-8eej3e66 §8: every
// call a person makes is cancellable).
//
// That id is per ATTEMPT, while the renderer's own identity for a call
// outlives a sealed retry and a wait for credit, so the two are separate
// counters and not always the same number. The renderer resolves this id to
// its own where it holds both (Dispatcher.callIDForRequest) and keys the row
// by the call's identity, which is what makes one operation ONE row rather
// than two — and what makes its Cancel reach THIS call rather than whichever
// call happens to hold that number as its own.
//
// # It is an indicator, never a ledger
//
// Both notifications are droppable (TryNotify, the refreshable class): a
// dropped update costs a stale byte count for a moment, and a dropped
// `operation.rowDone` costs a row that the request's own settlement clears on
// the renderer. Nothing may be derived from having seen one, and nothing here
// is replayed on reconnect — a socket that comes back has no open call left to
// report, because the request died with the socket (owner: request).

import (
	"encoding/json"
	"sync"

	"github.com/shady2k/nocx/internal/operation/rows"
)

// The two notification methods. A row exists between them and never after.
const (
	operationRowMethod     = "operation.row"
	operationRowDoneMethod = "operation.rowDone"
)

// operationRowParams is one row's current state
// (contracts/operation.row.schema.json). It is the whole row on every frame,
// not a delta, for the reason files.uploadProgress is: a renderer that missed
// every earlier frame can draw the row from the one it received.
type operationRowParams struct {
	// RequestID is the JSON-RPC id of the call this work belongs to, carried
	// verbatim (number or string) so the row's Cancel names the same request
	// the renderer's call did.
	RequestID json.RawMessage `json:"requestId"`
	Kind      string          `json:"kind"`
	Title     string          `json:"title"`
	Machine   string          `json:"machine"`
	Done      int64           `json:"done"`
	Total     int64           `json:"total"`
}

// operationRowDoneParams says the row goes (contracts/operation.rowDone.schema.json).
type operationRowDoneParams struct {
	RequestID json.RawMessage `json:"requestId"`
}

// operationRowReporter is the rows.Reporter one invocation writes through.
//
// It never blocks and it never panics on a request that has no id: `open` is a
// request by construction, and the nil guard is there because a reporter is
// the kind of value a later method could attach without thinking.
type operationRowReporter struct {
	r         Responder
	requestID json.RawMessage

	mu    sync.Mutex
	shown bool
	ended bool
}

func newOperationRowReporter(r Responder, requestID json.RawMessage) *operationRowReporter {
	return &operationRowReporter{r: r, requestID: append(json.RawMessage(nil), requestID...)}
}

// Show starts or updates the invocation's one row.
func (o *operationRowReporter) Show(row rows.Row) {
	o.mu.Lock()
	o.shown = true
	o.mu.Unlock()
	_ = o.r.TryNotify(operationRowMethod, mustMarshal(operationRowParams{
		RequestID: o.requestID,
		Kind:      row.Kind,
		Title:     row.Title,
		Machine:   row.Machine,
		Done:      row.Done,
		Total:     row.Total,
	}))
}

// End removes the row, once. A row this invocation never showed is not
// announced: an `open` that installed nothing must cost the socket nothing,
// and a `rowDone` for a row nobody drew is noise a busy window would pay per
// call.
func (o *operationRowReporter) End() {
	o.mu.Lock()
	if o.ended || !o.shown {
		o.ended = true
		o.mu.Unlock()
		return
	}
	o.ended = true
	o.mu.Unlock()
	_ = o.r.TryNotify(operationRowDoneMethod, mustMarshal(operationRowDoneParams{
		RequestID: o.requestID,
	}))
}
