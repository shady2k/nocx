package app

// THE COORDINATOR'S OWN LIFECYCLE CURSOR (ADR-0077).
//
// A coordinator that goes away and a coordinator that comes back share no
// memory, only the store. The PTY leg already resumes from what the store
// holds — the recording's length — and the owner's decision of 2026-09-30
// puts the lifecycle leg on the same footing: the re-adopt asks the helper
// for its lifecycle stream from the offset one past the last frame whose
// effect THIS machine stored. Nothing before that offset is offered again
// (ADR-0024's reason for resuming at the head holds: those frames carry a
// capability the adopted domain still honours), and nothing after it is
// skipped (ADR-0076 decision 4: an end the helper saw while nobody was
// attached settles its block on return).
//
// Two facts meet here and nowhere else. The adapter knows where in ITS
// carrier each frame ends, and hands this type each frame to apply
// (lifecyclechannel.WithFrameScope). The bridge knows which stretch of the
// helper's stream it handed that carrier — every byte it relays arrives at
// the adapter's end of an in-memory pipe verbatim and in order, and the
// attachment's own ingest cursor says where each stretch ends in the
// helper's stream. So the bridge records each relayed stretch BEFORE it
// writes it, and this type translates the frame's end into the helper's
// stream and applies the frame inside the store's own frame
// (content.ApplyLifecycleFrame): every write the frame's projection makes
// and the cursor past it commit as ONE transaction, or none of them does.
// There is no window in which the rows are stored and the cursor is not.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/log"
)

// lifecycleCursorStore is where the cursor is kept: the session's binding
// row (content.LedgerRepository satisfies it). Nil keeps nothing — a
// coordinator with no store has no record for the next one to resume from.
type lifecycleCursorStore interface {
	ApplyLifecycleFrame(ctx context.Context, sessionID string, offset uint64, apply func(ctx context.Context) error) error
}

// lifecycleOffsets is the attachment's ingest cursor: the helper stream
// offset one past the last lifecycle byte its reader handed over.
type lifecycleOffsets interface {
	LifecycleIngested() proto.StreamOffset
}

// relayedSpan is one stretch the bridge handed the adapter's carrier: it ends
// at pipeEnd in the carrier and at streamEnd in the helper's stream.
type relayedSpan struct {
	pipeEnd   uint64
	streamEnd uint64
}

// cursorWait is one waiter for the applied cursor to reach target.
type cursorWait struct {
	target uint64
	ch     chan struct{}
}

// lifecycleCursor is one lifecycle leg's applied cursor. Built before the
// adapter (its report is an adapter option), bound to its session once the
// helper has named it, and fed by the bridge.
type lifecycleCursor struct {
	ctx      context.Context
	store    lifecycleCursorStore
	stopping *atomic.Bool

	mu      sync.Mutex
	sid     string
	spans   []relayedSpan
	piped   uint64
	applied uint64
	waits   []cursorWait
	ended   bool
}

// newLifecycleCursor builds a cursor whose writes run under ctx's values and
// never under its cancellation: the leg outlives the request that opened it.
func newLifecycleCursor(ctx context.Context, store lifecycleCursorStore, stopping *atomic.Bool) *lifecycleCursor {
	return &lifecycleCursor{ctx: context.WithoutCancel(ctx), store: store, stopping: stopping}
}

// bind names the session the cursor is stored under and the helper stream
// offset the attachment starts at — where "nothing applied yet" stands.
func (c *lifecycleCursor) bind(sid string, from proto.StreamOffset) {
	c.mu.Lock()
	c.sid = sid
	c.applied = uint64(from)
	c.releaseLocked()
	c.mu.Unlock()
}

// relayed records one stretch the bridge is about to hand the adapter: n
// bytes ending at streamEnd in the helper's stream. It must run BEFORE the
// write: the carrier is an in-memory pipe, so the adapter can read, apply and
// report a frame from these bytes before the write even returns.
func (c *lifecycleCursor) relayed(n int, streamEnd proto.StreamOffset) {
	if n <= 0 {
		return
	}
	c.mu.Lock()
	c.piped += uint64(n)
	c.spans = append(c.spans, relayedSpan{pipeEnd: c.piped, streamEnd: uint64(streamEnd)})
	c.mu.Unlock()
}

// applyFrame is the adapter's frame scope (lifecyclechannel.FrameScope): the
// frame ending at consumed bytes into the carrier is applied by apply, inside
// the store's frame, with the cursor at the helper stream offset it ends at.
//
// A FRAME THAT ARRIVES AFTER THE COORDINATOR BEGAN STOPPING IS NOT APPLIED
// (ADR-0077). Stopping closes the sessions this frame's projection records
// against, so what it would store is not what the frame says; it is not
// applied at all — not in the kernel, not in the store — the cursor stays
// before it, and the next coordinator applies it from there. That is a
// handover and not a failure: nothing is logged as an error, and the pane
// is told nothing. A frame already in hand when stopping began, and failing
// because of it, is left the same way.
//
// Any other frame the store could not record, after every attempt the store
// makes at it (content.ApplyLifecycleFrame), returns the error: nothing of
// it — rows or cursor — was stored, the adapter halts the leg and reports
// the loss, and the next coordinator resumes before it.
func (c *lifecycleCursor) applyFrame(consumed uint64, kind lifecycle.EventKind, apply func(ctx context.Context)) error {
	if c.isStopping() {
		return lifecyclechannel.ErrFrameLeftForNext
	}
	c.mu.Lock()
	offset, ok := c.streamOffsetLocked(consumed)
	sid, store := c.sid, c.store
	c.mu.Unlock()
	if !ok {
		log.From(c.ctx).Warn("lifecycle cursor: a frame ends outside every relayed stretch; it is applied and the cursor stays where it was",
			"session", sid, "consumed", consumed)
		apply(c.ctx)
		return nil
	}
	if store != nil && sid != "" {
		err := store.ApplyLifecycleFrame(c.ctx, sid, offset, func(ctx context.Context) error {
			apply(ctx)
			// Stopping began while the frame's projection ran: what it wrote
			// — or answered, finding the sessions closing — is not what the
			// frame says, however cleanly the store took it. Nothing of it is
			// committed, whatever its writes answered (nocx-zg3k3.5.11: a
			// start whose entry was never recorded committed its cursor, and
			// the next coordinator never saw the command begin).
			if c.isStopping() {
				return content.ErrFrameAbandoned
			}
			return nil
		})
		if err != nil && (errors.Is(err, content.ErrFrameAbandoned) || c.isStopping()) {
			return lifecyclechannel.ErrFrameLeftForNext
		}
		if err != nil {
			attempts, held := 1, time.Duration(0)
			var failed *content.FrameFailedError
			if errors.As(err, &failed) {
				attempts, held = failed.Attempts, failed.Held
			}
			log.From(c.ctx).Error("lifecycle cursor: the frame's effect could not be stored on any attempt; nothing of it was, the cursor has not moved, and the leg halts",
				"session", sid, "kind", kind, "attempts", attempts, "held_ms", held.Milliseconds(), "offset", offset, "error", err)
			return err
		}
	} else {
		apply(c.ctx)
	}
	c.mu.Lock()
	if offset > c.applied {
		c.applied = offset
		c.releaseLocked()
	}
	c.mu.Unlock()
	return nil
}

func (c *lifecycleCursor) isStopping() bool {
	return c.stopping != nil && c.stopping.Load()
}

// streamOffsetLocked translates a carrier position into the helper stream
// offset, and forgets every stretch wholly behind it.
func (c *lifecycleCursor) streamOffsetLocked(consumed uint64) (uint64, bool) {
	for i, span := range c.spans {
		if span.pipeEnd < consumed {
			continue
		}
		c.spans = c.spans[i:]
		behind := span.pipeEnd - consumed
		if behind > span.streamEnd {
			return 0, false
		}
		return span.streamEnd - behind, true
	}
	return 0, false
}

// reached answers a channel that closes once the applied cursor reaches
// target — every frame the helper's window held up to there is applied — or
// the leg has ended and will apply nothing more.
func (c *lifecycleCursor) reached(target proto.StreamOffset) <-chan struct{} {
	ch := make(chan struct{})
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended || c.applied >= uint64(target) {
		close(ch)
		return ch
	}
	c.waits = append(c.waits, cursorWait{target: uint64(target), ch: ch})
	return ch
}

// end says the adapter's pump has stopped: nothing more will be applied, so
// nobody waits for it.
func (c *lifecycleCursor) end() {
	c.mu.Lock()
	c.ended = true
	c.releaseLocked()
	c.mu.Unlock()
}

func (c *lifecycleCursor) releaseLocked() {
	kept := c.waits[:0]
	for _, w := range c.waits {
		if c.ended || c.applied >= w.target {
			close(w.ch)
			continue
		}
		kept = append(kept, w)
	}
	c.waits = kept
}
