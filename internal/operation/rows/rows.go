// Package rows is the live half of the Operations surface: the rows a person
// watches while work they can see is under way (ADR-8eej3e66 §8).
//
// # Why this exists next to the catalogue
//
// The catalogue declares, per operation, WHEN a row may exist (`row` is
// `never`, `past-wait` or `always`). A `past-wait` row is a person's own call,
// and the renderer already knows it exists because it made the call
// (frontend/src/operations/call-operations.ts). An `always` row is different:
// a first helper install is work the backend started on a person's behalf
// inside a call that is still waiting, and the renderer cannot see it at all
// until somebody says so. That is this package — the one thing a provider
// reports while it works, and the way it reaches whoever is watching.
//
// # One row per invocation
//
// A Reporter belongs to one invocation, and it carries one row: the row the
// person sees for the work that invocation is doing. `Show` starts it on the
// first call and updates it afterwards, and `End` removes it. Nothing here
// queues, retries or remembers — a row is an indicator and never a ledger,
// exactly like files.uploadProgress — so a dropped update costs a stale
// number for a moment and never a wrong outcome.
//
// # It is transport-independent, deliberately
//
// Like the catalogue, this package imports nothing from internal/transport. A
// reporter is attached to the invocation's context by whoever owns the wire
// (internal/transport), and read by the work that has something to report
// (internal/app). The signature is the smallest one that answers "what is
// this doing, and how far has it got".
package rows

import "context"

// Row is one operation as a person watching it needs it: what it is doing,
// on which machine, and how far it has got.
type Row struct {
	// Kind is the surface's word for the sort of work this is — the
	// renderer's OperationKind spelling ('install' today). It travels
	// because a row's glyph and its cancel are decided from it, and the
	// backend is the only party that knows which kind of work it started.
	Kind string
	// Title is what the work is doing right now, in the person's terms. It
	// may change between Show calls while the row itself stays one row: the
	// row's identity is the invocation, not the sentence.
	Title string
	// Machine is which machine the work is on, as a person names it
	// (user@host, or the bare host when no user is known). A row is read
	// out of the context of the tab that started it, so an empty answer
	// says nothing rather than guessing.
	Machine string
	// Done is how many bytes of Total have moved. Total is zero where the
	// work counts nothing — the verification read is one ReadFile — and a
	// row with no total draws no bar rather than one that is full or empty
	// by accident.
	Done  int64
	Total int64
}

// Reporter is one invocation's way onto the Operations surface. Implementations
// must be safe to call from any goroutine and must never block: `Show` is on
// the path of the work it reports.
type Reporter interface {
	// Show starts this invocation's row, or updates it. The first call is
	// what makes the row appear.
	Show(row Row)
	// End removes the row. It is idempotent and safe after a settle: the
	// work that owned the row and the wire that carried it may both call it,
	// and the row leaves once.
	End()
}

type reporterKey struct{}

// WithReporter attaches the reporter an invocation's work reports through.
func WithReporter(ctx context.Context, r Reporter) context.Context {
	return context.WithValue(ctx, reporterKey{}, r)
}

// ReporterFrom returns the reporter the invocation carries, if any. Absent is
// the ordinary case — a backend-initiated install has no surface watching —
// and it is not an error.
func ReporterFrom(ctx context.Context) (Reporter, bool) {
	if ctx == nil {
		return nil, false
	}
	r, ok := ctx.Value(reporterKey{}).(Reporter)
	return r, ok && r != nil
}
