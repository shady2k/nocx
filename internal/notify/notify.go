// Package notify is the notification pipeline core: the Event record, the
// trust classes, the default-deny router, and the Sink contract. It is the
// pipeline of ADR-0047 and the notification design (§2): sources stamp an
// Event, the router resolves where it goes, sinks deliver.
//
// The two rules that are the point of the package:
//
//   - Routing resolves ONCE, in the router, before any sink is invoked. A
//     sink receives an immutable resolved destination (a Delivery) and may
//     never select a target, credential, method, retry or redirect
//     (ADR-0047 §2.3).
//   - Every sink invocation is synchronous, takes a finite-deadline context,
//     must stop retaining request data and return when cancelled, and may
//     not publish a callback after returning. Expiry cancels the
//     invocation; the closing event is the invocation's RETURN, so the
//     in-flight slot is released on return, never at deadline expiry.
//     Finalization is one-shot: a late result is ignored (ADR-0047 §2.2).
//
// It is wired at the composition root (internal/app). Ingress is the one
// entry point: it stamps what nocx owns, records the occurrence in the feed,
// and only then submits for delivery, so membership and delivery are two
// decisions and a suppressed event is still remembered. The routing table is
// built from the user's settings and swapped into the router atomically —
// a raise resolves against exactly one table, the one live when it began.
package notify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Kind is the event kind, stamped by the source adapter from the method
// invoked — never carried on the wire (ADR-0047 §2.2). The values below are
// the sources of the design (§3); the enum is open, and default-deny means
// a kind added later reaches no sink until a table row exists.
type Kind string

const (
	KindBlockFinished    Kind = "block.finished"    // block ledger (attested)
	KindSessionEnded     Kind = "session.ended"     // session registry (attested)
	KindTransferFinished Kind = "transfer.finished" // transfer registry (attested)
	KindProgramNotify    Kind = "program.notify"    // OSC 9 / OSC 777 (programRequest)
	KindBell             Kind = "bell"              // BEL (programRequest)
	KindPaneWorkFinished Kind = "pane.workFinished" // title-transition inference (heuristic)
	KindWaveUndispatched Kind = "wave.undispatched" // the wave record's backstop (attested)
)

// Trust is the trust class of an event, stamped by its source adapter —
// never carried on the wire. The class decides what the event may reach
// (ADR-0047 §3); the routing table is default-deny, and a class bound is a
// hard capability, not a suggestion.
type Trust string

const (
	// TrustAttested originates at a backend boundary that authenticated the
	// fact. It may reach every sink, and completion subscriptions.
	TrustAttested Trust = "attested"
	// TrustProgramRequest originates from a parsed, registered sequence the
	// program printed. It may reach every sink, never a completion
	// subscription.
	TrustProgramRequest Trust = "programRequest"
	// TrustHeuristic is an inference from stream content. It may reach
	// local attention only, never a network destination.
	TrustHeuristic Trust = "heuristic"
)

// Level is the severity of an event, stamped by nocx: a program cannot
// forge danger.
type Level string

const (
	LevelInfo    Level = "info"
	LevelSuccess Level = "success"
	LevelWarning Level = "warning"
	LevelDanger  Level = "danger"
)

// Attribution is the backend-stamped origin of an event, naming the tab,
// host and session it came from. Stamped by nocx from its session registry
// — never carried on the wire (ADR-0047 §2.2, §4.6).
type Attribution struct {
	// Backend names which backend raised this — "local" for this machine,
	// the same vocabulary internal/commandnames.LocalRoute already uses for
	// the same idea. nocx-if6 phase A makes session identity
	// (backendId, sessionId); carrying it from the first commit is what stops
	// every feed row needing a retrofit when the helper lands.
	Backend string

	// Tab keeps the old word on purpose (nocx-ehkvy). Everything else that
	// held the shell-bearing object is now a pane, but this field is not
	// filled with one: ws_notify.go stamps it from the WebSocket connection
	// id. Renaming it to Pane would not make it honest, it would make it
	// claim something it does not hold. nocx-wyp3p is that defect, and the
	// word moves when the value does.
	Tab     string
	Host    string
	Session string
}

// Event is the closed record of the pipeline. Provenance is structural:
// the protected fields (Kind, Trust, Level, Attribution, At) are not on the
// wire and are stamped by whoever the field comment names; only SessionID,
// Title and Body are carried on the wire, and SessionID is addressing, not
// attribution (ADR-0047 §2.2).
type Event struct {
	// SessionID is addressing: which terminal parsed the sequence. Set by
	// the source adapter; the backend rejects an id not live on the
	// connection and derives every attributed field from its registry.
	SessionID string

	// Title and Body are the presentation fields. Set by the source; may
	// be stream-derived. They are untrusted presentation data — never
	// control data, never an opaque blob to concatenate (ADR-0047 §2.3).
	Title string
	Body  string

	// Kind and Trust are stamped by the source adapter.
	Kind  Kind
	Trust Trust

	// Level is stamped by nocx.
	Level Level

	// Attribution is stamped by nocx from the authenticated session context.
	Attribution Attribution

	// At is stamped by nocx at ingress, which is the first nocx-owned stage
	// (ingress.go). It was the router's job until the feed arrived; the stamp
	// moved so that a helper replaying a buffered batch keeps its own instants
	// instead of having them rewritten to the moment it reconnected.
	At time.Time
}

// Destination is the immutable, fully-resolved "where" of one delivery. The
// router resolves it during Resolve, before any sink is invoked; a sink
// receives it read-only and may never select an alternate target,
// credential, method, retry or redirect (ADR-0047 §2.3). Program-supplied
// fields never participate in its construction in any position.
type Destination struct {
	// Target is the resolved target identifier. For a local sink it is the
	// sink's own name; for a network route it is the configured target the
	// router resolved. Built from user configuration, trusted metadata and
	// secrets — never from the payload.
	Target string
}

// Route is one resolved delivery: the sink to invoke and the immutable
// destination it receives.
type Route struct {
	Sink        Sink
	Destination Destination
}

// Delivery is what one sink invocation receives: the event and the
// immutable resolved destination.
type Delivery struct {
	Event       Event
	Destination Destination
}

// Sink validates, redacts, encodes and delivers — nothing else, and never
// "where". It is synchronous: it must honor cancellation, stop retaining
// request data and return when ctx is done, and it may not publish a
// callback after returning.
type Sink interface {
	// Deliver delivers one event to the immutable resolved destination.
	// ctx carries a finite deadline imposed by the router.
	Deliver(ctx context.Context, d Delivery) error

	// LeavesMachine reports whether delivering through this sink leaves the
	// machine (a network destination). The router enforces the heuristic
	// trust bound with it: an inference never leaves the machine
	// (ADR-0047 §3). Every sink must declare this — an undeclared network
	// sink is a fail-open.
	LeavesMachine() bool
}

// Key identifies one routing-table cell: a (kind, trust) pair.
type Key struct {
	Kind  Kind
	Trust Trust
}

// Table is the default-deny routing table. A (kind, trust) pair reaches a
// sink only where a row says so; one table governs both the ordinary route
// and the ad-hoc completion-subscription route (ADR-0047 §3).
//
// One table value is immutable: nothing mutates a Table after it has been
// handed to a router. A CHANGE replaces the whole value (SetTable), which is
// what makes the swap atomic with respect to a raise — see the interval named
// on SetTable.
type Table map[Key][]Route

// RouteKind selects which route an event resolves through.
type RouteKind int

const (
	// RouteRaise is the ordinary delivery route.
	RouteRaise RouteKind = iota
	// RouteSubscription is the ad-hoc "notify me when done" route. Only an
	// attested event may match it (ADR-0047 §3).
	RouteSubscription
)

// Limits are the router's global admission bounds. Every sink invocation
// runs under a finite deadline; admission beyond a bound is a visible
// failed delivery, never an unbounded queue (ADR-0047 §2.2).
type Limits struct {
	// MaxInFlight bounds events whose sinks are currently being invoked.
	// When all slots are busy, an event waits in the queue instead.
	MaxInFlight int

	// MaxQueued bounds events admitted but waiting for an in-flight slot.
	MaxQueued int

	// MaxRetained bounds the total payload bytes the router holds in the
	// queue (UTF-8 title+body). An admission that would exceed it fails.
	MaxRetained int64

	// DeliveryTimeout is the finite deadline of one sink invocation.
	DeliveryTimeout time.Duration
}

// RefusedError is the failed delivery an event gets when admission refused
// it: one of the router's global limits was exceeded and the event was
// never invoked.
type RefusedError struct {
	// Limit is the limit that refused the delivery.
	Limit string
}

func (e *RefusedError) Error() string {
	return "notify: delivery refused: " + e.Limit
}

// The limit names a RefusedError reports.
const (
	LimitQueued   = "queued"
	LimitRetained = "retained bytes"
)

// ErrTrustCapability is returned by NewRouter when a table row grants a
// sink to a trust class that may never reach it — a heuristic row granting
// a network sink can never fire, and building it silently would leave a
// configured route that always resolves to nothing (ADR-0047 §3).
var ErrTrustCapability = errors.New("notify: table row exceeds its trust class capability")

// Outcome is the result of raising one event.
type Outcome struct {
	// Event is the event this outcome reports on. The router copies it onto
	// every outcome it can return through, because the handler that observes
	// a failure receives an Outcome and nothing else.
	//
	// It is here for the failure surface (D3, nocx-r6pxp): a delivery that
	// fails AFTER notify.raise answered has no caller left to tell, and the
	// feed row that records it must carry the ORIGINAL event's attribution
	// so it lands beside the notification it is about. Without this the
	// handler cannot name the event that failed, and the only alternative is
	// somewhere else remembering "the event I just submitted" — two owners
	// of one fact, which is what AD-8 is about.
	//
	// It never goes on the wire. Outcome is a Go value the transport reads
	// Err from and never marshals, and the fields this carries are exactly
	// the ones ADR-0047 §2.2 keeps off the wire.
	Event Event

	// Resolved is the route set resolution produced, in table order, before
	// any invocation. Empty means default-deny: no row for the (kind,
	// trust) pair, or the route kind refused the trust class.
	Resolved []Route

	// Results is one result per resolved route, in order. A route whose
	// sink returned an error has that error here: a sink-level rejection is
	// a failed delivery and never removes the route from the resolved set.
	Results []RouteResult

	// Err is non-nil when the event was never delivered: admission refused
	// (a limit was exceeded) or the caller cancelled while the event was
	// queued.
	Err error
}

// RouteResult is the outcome of one resolved route.
type RouteResult struct {
	Route Route
	Err   error
}

// Router is the only holder of "where": it maps (kind, trust) through the
// default-deny table, bounds admission globally, and invokes every resolved
// sink synchronously. It is safe for concurrent use.
type Router struct {
	// table is the live routing table, replaced whole by SetTable. It is an
	// atomic pointer rather than a mutex-guarded field because every reader
	// takes exactly one load and no reader may ever hold two: a raise reads
	// it once, at the top of Raise, and carries the routes it got for the
	// rest of its life.
	table  atomic.Pointer[Table]
	limits Limits

	mu       sync.Mutex
	inFlight int
	queue    []*pending
	retained int64
}

// pending is one admitted event waiting for or holding an in-flight slot.
type pending struct {
	ev      Event
	routes  []Route
	bytes   int64
	started chan struct{} // closed when an in-flight slot was handed over
}

// NewRouter validates limits and the table's trust-class capability bounds,
// then returns a router. A row a trust class may never reach is refused
// here — loudly, at construction — rather than resolved to nothing forever.
// The same check runs again on every table SetTable is handed.
func NewRouter(table Table, limits Limits) (*Router, error) {
	if limits.MaxInFlight < 1 {
		return nil, errors.New("notify: MaxInFlight must be at least 1")
	}
	if limits.DeliveryTimeout <= 0 {
		return nil, errors.New("notify: DeliveryTimeout must be positive")
	}
	if limits.MaxQueued < 0 || limits.MaxRetained < 0 {
		return nil, errors.New("notify: queue limits must not be negative")
	}
	if err := validateTable(table); err != nil {
		return nil, err
	}
	r := &Router{limits: limits}
	r.table.Store(&table)
	return r, nil
}

// validateTable enforces the trust-class capability bound over a whole table:
// a heuristic row may never reach a sink that leaves the machine (ADR-0047
// §3). It returns on the FIRST offending row and reports nothing partial,
// because its callers apply a table whole or not at all.
//
// It is a function rather than a method so it runs identically on the table a
// router is built with and on every table it is later handed. A check that
// ran only at construction would be a security control that stops holding the
// moment the table becomes user-authored (D3).
func validateTable(table Table) error {
	for key, routes := range table {
		for _, route := range routes {
			if route.Sink == nil {
				return fmt.Errorf("notify: table row %q/%q has no sink", key.Kind, key.Trust)
			}
			if key.Trust == TrustHeuristic && route.Sink.LeavesMachine() {
				return fmt.Errorf("%w: heuristic %q row grants a network sink", ErrTrustCapability, key.Kind)
			}
		}
	}
	return nil
}

// SetTable re-validates a table and, only if it passes, makes it the live one.
//
// The interval, both ends named: a raise resolves against exactly ONE table —
// the one live at the instant Raise called Resolve — and it keeps the routes
// it got there until its last sink invocation has returned. A swap takes
// effect for raises that call Resolve after the store below, and for no
// others. No raise ever sees half of two tables, because no raise reads the
// table twice.
//
// A table that fails validation is refused WHOLE: the previous table stays
// live and nothing of the new one is applied. Partially applying a routing
// table would silently grant a route nobody chose, which is worse than
// refusing the change and saying so (D3).
func (r *Router) SetTable(table Table) error {
	if err := validateTable(table); err != nil {
		return err
	}
	r.table.Store(&table)
	return nil
}

// Resolve maps (kind, trust) through the default-deny table for one route.
// It is the only place "where" is decided: the returned routes are a copy,
// and a sink receives its destination as an immutable value. The
// subscription route matches only an attested event; the trust class bound
// was already enforced when the table was built.
func (r *Router) Resolve(kind Kind, trust Trust, route RouteKind) []Route {
	if route == RouteSubscription && trust != TrustAttested {
		return nil
	}
	// One load, once. This is the instant the raise binds to a table.
	table := *r.table.Load()
	return append([]Route(nil), table[Key{Kind: kind, Trust: trust}]...)
}

// Raise resolves once, admits the event under the global limits, and
// invokes every resolved sink synchronously. It returns when every
// invocation has returned or admission refused the event; a queued wait is
// bounded by ctx, and a caller that cancels while queued is removed from
// the queue (the router stops retaining its data) and gets ctx.Err().
//
// At is NOT stamped here any more. Ingress is the first nocx-owned stage and
// stamps it once (ingress.go); a second stamp here would overwrite the instant
// a replayed batch carries. A zero At reaching this point means something
// bypassed ingress, which is a wiring defect rather than a value to repair.
func (r *Router) Raise(ctx context.Context, ev Event) Outcome {
	routes := r.Resolve(ev.Kind, ev.Trust, RouteRaise)
	if len(routes) == 0 {
		return Outcome{Event: ev}
	}

	p := &pending{
		ev:      ev,
		routes:  routes,
		bytes:   int64(len(ev.Title) + len(ev.Body)),
		started: make(chan struct{}),
	}

	r.mu.Lock()
	switch {
	case r.inFlight < r.limits.MaxInFlight:
		r.inFlight++
		r.mu.Unlock()
		return r.deliver(ctx, p)
	case len(r.queue) >= r.limits.MaxQueued:
		r.mu.Unlock()
		return Outcome{Event: ev, Resolved: routes, Err: &RefusedError{Limit: LimitQueued}}
	case r.retained+p.bytes > r.limits.MaxRetained:
		r.mu.Unlock()
		return Outcome{Event: ev, Resolved: routes, Err: &RefusedError{Limit: LimitRetained}}
	default:
		r.queue = append(r.queue, p)
		r.retained += p.bytes
		r.mu.Unlock()
	}

	select {
	case <-p.started:
		return r.deliver(ctx, p)
	case <-ctx.Done():
		r.mu.Lock()
		select {
		case <-p.started:
			// The slot was handed over before the cancellation landed; the
			// delivery still runs, with a done ctx that makes every sink
			// invocation return promptly.
			r.mu.Unlock()
			return r.deliver(ctx, p)
		default:
			r.removeQueued(p)
			r.mu.Unlock()
			return Outcome{Event: ev, Resolved: routes, Err: ctx.Err()}
		}
	}
}

// deliver invokes every resolved sink synchronously, each under its own
// finite DeliveryTimeout derived from ctx, then releases the in-flight
// slot. The release is the closing event of the invocation: it happens on
// return, never at deadline expiry, and exactly once per admitted event —
// a sink that calls back after returning is ignored because there is no
// handle left to reach.
func (r *Router) deliver(ctx context.Context, p *pending) (out Outcome) {
	defer r.release()
	out.Event = p.ev
	out.Resolved = p.routes
	for _, route := range p.routes {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					out.Results = append(out.Results, RouteResult{
						Route: route,
						Err:   fmt.Errorf("notify: sink %T panicked: %v", route.Sink, rec),
					})
				}
			}()
			sinkCtx, cancel := context.WithTimeout(ctx, r.limits.DeliveryTimeout)
			defer cancel()
			err := route.Sink.Deliver(sinkCtx, Delivery{Event: p.ev, Destination: route.Destination})
			out.Results = append(out.Results, RouteResult{Route: route, Err: err})
		}()
	}
	return out
}

// release returns the in-flight slot and hands it to the head of the queue,
// if any. It is deferred so a panicking sink cannot leak the slot.
func (r *Router) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inFlight--
	if len(r.queue) > 0 {
		next := r.queue[0]
		r.queue = r.queue[1:]
		r.retained -= next.bytes
		r.inFlight++
		close(next.started)
	}
}

// removeQueued drops p from the queue and refunds its bytes. Caller holds
// mu.
func (r *Router) removeQueued(p *pending) {
	for i, q := range r.queue {
		if q == p {
			r.queue = append(r.queue[:i], r.queue[i+1:]...)
			r.retained -= p.bytes
			return
		}
	}
}
