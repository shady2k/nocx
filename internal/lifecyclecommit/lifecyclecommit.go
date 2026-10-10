// Package lifecyclecommit is the one ordered queue of what a lifecycle frame
// sends out of the process (ADR-0077 decision 12). A frame's ingest decides
// things the store records — in one transaction, committed or not at all —
// and things it tells others: the shell its ACCEPT, grant or enrolment
// answer, the renderer its lifecycle fact, block notification and history
// receipt, the helper its completion. Telling any of them before the frame's
// transaction commits tells them of a frame that may yet fail every attempt,
// and then the next coordinator applies it again and tells them twice, or
// the store never holds what they were told. So every such effect is queued
// here, in the order the frame caused it, runs once the frame commits, and
// is dropped when it does not.
//
// The store owns the frame (content.ApplyLifecycleFrame): it Begins the
// queue in the frame's context and Ends it when the frame ends. Everyone else
// only calls After, which outside a frame runs the effect at once — the
// unframed paths (a leg with no store, a replay, the rows plane) are
// unchanged.
package lifecyclecommit

import (
	"context"
	"sync"
)

type queueKey struct{}

// Queue is one frame's pending effects.
type Queue struct {
	mu        sync.Mutex
	ended     bool
	committed bool
	fx        []effect
}

type effect struct {
	key any
	fn  func(committed bool)
}

// Begin opens a frame's queue and answers the context that carries it.
func Begin(ctx context.Context) (context.Context, *Queue) {
	q := &Queue{}
	return context.WithValue(ctx, queueKey{}, q), q
}

// After queues fn for the end of the frame ctx carries; committed says
// whether the frame was stored. Effects run in the order they were queued,
// on the goroutine that ends the frame, with the store's connection already
// free — fn may not use the frame's context for a store call. A second call
// with the same non-nil key in one frame adds nothing. Outside a frame, and
// after the frame ended, fn runs at once.
func After(ctx context.Context, key any, fn func(committed bool)) {
	q, _ := ctx.Value(queueKey{}).(*Queue)
	if q == nil {
		fn(true)
		return
	}
	q.mu.Lock()
	if q.ended {
		committed := q.committed
		q.mu.Unlock()
		fn(committed)
		return
	}
	if key != nil {
		for _, e := range q.fx {
			if e.key == key {
				q.mu.Unlock()
				return
			}
		}
	}
	q.fx = append(q.fx, effect{key: key, fn: fn})
	q.mu.Unlock()
}

// OnCommit queues fn to run only if the frame ctx carries commits: the
// common case, an effect that is simply dropped with a frame that failed.
func OnCommit(ctx context.Context, fn func()) {
	After(ctx, nil, func(committed bool) {
		if committed {
			fn()
		}
	})
}

// End runs the queue, in order, once. An effect queued while it runs — an
// effect's own consequence — runs after the ones before it.
func (q *Queue) End(committed bool) {
	q.mu.Lock()
	q.committed = committed
	q.mu.Unlock()
	for {
		q.mu.Lock()
		if len(q.fx) == 0 {
			q.ended = true
			q.mu.Unlock()
			return
		}
		e := q.fx[0]
		q.fx = q.fx[1:]
		q.mu.Unlock()
		e.fn(committed)
	}
}
