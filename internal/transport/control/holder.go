package control

// WHO HOLDS AN ADMISSION (nocx-3an1s.1.8).
//
// "The terminal is busy" named no holder, and on 2026-11-05 a server that had
// been up for days refused work while nothing a person started was running.
// The question that would have answered it is "which request holds this
// admission, and for how long" — and nothing could ask it, because a permit
// was a token in a buffered channel and carried no identity.
//
// A holder travels in the CONTEXT and the two semaphores record it when they
// take a token. That is the one place that already has both facts: the request
// (the dispatcher binds it with WithHolder) and the acquisition (TryAcquire).
// No signature changed: an Admission is still acquired with a context, and a
// refusal now carries the holders it found.

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Holder is one request holding an admission.
type Holder struct {
	// Method is the JSON-RPC method, or empty for work with no frame.
	Method string
	// Request is the dispatcher's wire tag ("11/open#5"). It is safe to show:
	// a connection ordinal, a method and a request id name nothing outside
	// this process.
	Request string
	// Trace is the W3C trace id of the exchange.
	Trace string
	// Since is when the permit was taken.
	Since time.Time
}

// Held is how long this holder has held its permit as of now.
func (h Holder) Held(now time.Time) time.Duration {
	if h.Since.IsZero() {
		return 0
	}
	return now.Sub(h.Since)
}

type holderKeyType struct{}

var holderKey = holderKeyType{}

// WithHolder binds the request that is about to acquire admissions.
func WithHolder(ctx context.Context, h Holder) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, holderKey, h)
}

// HolderFrom is the request this context is serving, if the dispatcher bound
// one. Work with no frame (a background sweep) answers false and is recorded
// under an empty method.
func HolderFrom(ctx context.Context) (Holder, bool) {
	if ctx == nil {
		return Holder{}, false
	}
	h, ok := ctx.Value(holderKey).(Holder)
	return h, ok
}

// holderBook is one admission's live permits. It is bounded by the admission's
// own capacity, so it cannot grow with uptime.
type holderBook struct {
	mu   sync.Mutex
	next uint64
	held map[uint64]Holder
}

func newHolderBook() *holderBook {
	return &holderBook{held: make(map[uint64]Holder)}
}

func (b *holderBook) add(ctx context.Context, now time.Time) uint64 {
	h, _ := HolderFrom(ctx)
	h.Since = now
	b.mu.Lock()
	b.next++
	id := b.next
	b.held[id] = h
	b.mu.Unlock()
	return id
}

func (b *holderBook) release(id uint64) {
	b.mu.Lock()
	delete(b.held, id)
	b.mu.Unlock()
}

func (b *holderBook) snapshot() []Holder {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Holder, 0, len(b.held))
	for _, h := range b.held {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// holderSource is an admission that can name the requests holding it. Both
// semaphores implement it; a composite answers by asking its parts.
type holderSource interface {
	Holders() []Holder
}

// HoldersOf answers every request holding this admission, oldest first, or
// nil for an admission that holds no identity (a submission wrapper). It
// flattens a composite through Parts, the same way the vault-unlock question
// already asks a composite about its leaves.
func HoldersOf(a Admission) []Holder {
	out := make([]Holder, 0, 1)
	for _, part := range Parts(a) {
		if src, ok := part.(holderSource); ok {
			out = append(out, src.Holders()...)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// refusalKeyType carries the sink a refusal is reported to. The dispatcher
// installs it on the request context; capability's operations call
// ReportRefusal when an admission refuses them, so an in-handler refusal
// reaches the same record as a submission refusal.
type refusalKeyType struct{}

var refusalKey = refusalKeyType{}

// WithRefusalReporter binds the sink one request's refusals are reported to.
func WithRefusalReporter(ctx context.Context, report func(context.Context, Rejection)) context.Context {
	if ctx == nil || report == nil {
		return ctx
	}
	return context.WithValue(ctx, refusalKey, report)
}

// ReportRefusal hands a refusal to the request's sink, if it has one. It is
// called from the operation layer for a gate or lane refusal and from the
// dispatcher for a submission refusal — the two places a Rejection is born.
func ReportRefusal(ctx context.Context, rej Rejection) {
	if ctx == nil {
		return
	}
	if report, ok := ctx.Value(refusalKey).(func(context.Context, Rejection)); ok && report != nil {
		report(ctx, rej)
	}
}
