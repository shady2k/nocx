package client

// The coordinator's half of the completion downlink (owner decision
// 2026-09-19): the lifecycle channel is authenticated HERE, in the
// coordinator's kernel, and the already-authenticated completion is carried
// DOWN to the helper session that owns the pane. Authentication does not
// move: this file is a carrier, not a second gate. What the kernel accepted
// is delivered in the order it was accepted, addressed to the session the
// open adopted; what the kernel refused is never observed at all; and a
// delivery that fails is retried until it lands, the helper refuses it or
// the session ends — reported either way, while the kernel's own execution
// state stands untouched.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/log"
)

// pendingCompletion is one accepted completion — or, entered set, one
// accepted environment entry (nocx-2v80t.3.21) — waiting for its turn on the
// wire. The two share one queue rather than two, so an entry that lands
// between two completions is delivered in the same order the kernel accepted
// all three: the runtime's own seal (ADR-0074) cares which interval an event
// closes, and a downlink that reordered them would hand it the wrong one.
type pendingCompletion struct {
	entered bool
	// entry is an environment entry's identity — the child domain that took
	// the lane — and travels with every attempt at delivering it, so the
	// helper's runtime can tell a retry of one entry from a second entry
	// (nocx-2v80t.3.28). Empty on a completion, whose fence is its identity.
	entry    string
	fence    [32]byte
	exitCode *int
}

// CompletionDownlink delivers the completions one hosted pane's kernel
// accepts to that pane's helper session, over the `lifecycle-complete` op on
// the `session` service.
//
// ORDER IS THE CONTRACT, AND THIS TYPE IS ITS ONE OWNER (nocx-2v80t.3.25).
// The helper's runtime reads a completion whose nonce differs from the one
// it has parked as the END of the parked interval (sessionruntime's
// Completed, settlePendingLocked), so two completions delivered in the
// opposite order to the one the kernel accepted them in seal the wrong
// interval as no-fence and hand its rows to the next. The pane's own
// transport and an ssh child's listener are two observing kernels over ONE
// downlink (nocx-2v80t.3.24), on two goroutines, so the order has to be
// fixed at the moment of acceptance and kept to the wire:
//
//   - Accept runs the kernel's Ingest under the downlink's acceptance lock
//     and queues what it accepted before releasing it, so the queue's order
//     IS the order the kernel accepted in, whichever source carried the
//     frame. An environment entry is queued from inside that same Ingest
//     (the emitter the publisher calls synchronously), so it takes its
//     place in the same order.
//   - One worker, started by Bind, is the only thing that ever sends, and it
//     sends the queue's head before anything behind it.
//
// THE ORDERING RACE THE BUFFER EXISTS FOR: the adapter (and with it the
// kernel) is built BEFORE the spawn RPC returns the helper session's
// identity, so a shell that completes a command inside that window is
// accepted by the kernel while the downlink still has no addressee.
// Completions observed before Bind wait in the queue — bounded, because a
// shell that ran dozens of commands before its own spawn was answered is not
// a window problem but a wedge worth reporting — and the worker Bind starts
// delivers them first.
//
// A FAILED DELIVERY IS RETRIED, NOT DROPPED. A boundary the runtime is never
// told about is an interval that never ends, so a send that failed for any
// reason that might pass — the helper not answering inside one delivery's
// bound, most of all — is tried again, the head holding its place, until it
// lands or the pane's session ends. Only an answer that cannot change is
// final: the helper refusing the op (it has no such session, or no such
// incarnation), or the connection itself being lost, which this carrier
// never comes back from — a re-adopted pane gets a downlink of its own. Every
// failure is logged with its cause through log.From on the session's
// context.
type CompletionDownlink struct {
	send func(context.Context, proto.LifecycleCompleteParams) error
	// sendEntered is send's sibling for an environment entry
	// (nocx-2v80t.3.21): the same carrier, the same session, no fence to
	// carry.
	sendEntered func(context.Context, proto.LifecycleEnteredParams) error
	// retryWait is the pause before attempt+1, answering early with the
	// context's error when the session ends first. It is a field so a test
	// can retry without a clock; production uses waitToRetry.
	retryWait func(ctx context.Context, attempt int) error
	ctx       context.Context

	// accept orders acceptance: held across the kernel's Ingest and the
	// enqueue it leads to, and never across a send.
	accept sync.Mutex

	mu    sync.Mutex
	space *sync.Cond // signalled whenever the queue shrinks or the worker stops
	bound bool
	entry HostSessionID
	// pending is THE queue: everything accepted and not yet settled, head
	// first. The head stays in it while the worker is sending it, so the
	// bound below counts what is in flight too.
	pending []pendingCompletion
	wake    chan struct{}
}

// maxPendingCompletions bounds the queue. Before Bind, running past it means
// the open path itself is wedged, and what arrives past it is dropped and
// reported — a silent unbounded buffer would hide exactly that. After Bind a
// full queue is a helper that is not answering, and what arrives waits for
// room instead: the ingest that accepted it is held, the way a synchronous
// delivery used to hold it, and nothing the kernel accepted is dropped.
const maxPendingCompletions = 16

// completionDeliveryTimeout bounds ONE attempt at a delivery. A completion is
// a small op on a connection the pane is already using; five seconds is this
// package's existing scale for exactly that shape (signalTimeout), and past
// it the attempt is abandoned and tried again rather than waited on — a
// helper that holds its connection open and never answers must not park the
// queue behind one call for the session's whole life.
const completionDeliveryTimeout = 5 * time.Second

// firstRetryPause is the pause before the first retry; each later one doubles,
// up to completionDeliveryTimeout. It exists so a helper that refuses to
// answer quickly is not asked again in a tight loop.
const firstRetryPause = 100 * time.Millisecond

// errNeverBound is the cause a completion is dropped with when the queue is
// full before Bind has named the session.
var errNeverBound = errors.New("the helper session's identity was never bound and the queue is full")

// CompletionSender is what the downlink needs from the pane's client: the
// carrier ops the already-authenticated completion, and an authenticated
// environment entry beside it (nocx-2v80t.3.21), travel down on. *Client is
// it; so is any carrier that forwards the session service — the re-adoption
// path's hostedCarrier, which is a connection and not always this client.
type CompletionSender interface {
	LifecycleComplete(ctx context.Context, params proto.LifecycleCompleteParams) error
	LifecycleEntered(ctx context.Context, params proto.LifecycleEnteredParams) error
}

// NewCompletionDownlink builds the downlink over this pane's client. The
// context is THE HOSTED SESSION'S LIFETIME — the caller's statement of when
// the pane this downlink serves is over — and never the request that
// happened to be running when the pane was opened: the open request's
// context is cancelled when the renderer's connection drops, while the PTY
// deliberately lives on (AD-9), and a completion the kernel accepts after
// that must still reach the helper session. It is also where the downlink's
// logger comes from (log.From). Cancelling it stops the worker Bind starts.
func NewCompletionDownlink(c CompletionSender, ctx context.Context) *CompletionDownlink {
	return newCompletionDownlink(ctx,
		func(ctx context.Context, params proto.LifecycleCompleteParams) error {
			return c.LifecycleComplete(ctx, params)
		},
		func(ctx context.Context, params proto.LifecycleEnteredParams) error {
			return c.LifecycleEntered(ctx, params)
		})
}

func newCompletionDownlink(
	ctx context.Context,
	send func(context.Context, proto.LifecycleCompleteParams) error,
	sendEntered func(context.Context, proto.LifecycleEnteredParams) error,
) *CompletionDownlink {
	d := &CompletionDownlink{
		send: send, sendEntered: sendEntered, retryWait: waitToRetry, ctx: ctx,
		wake: make(chan struct{}, 1),
	}
	d.space = sync.NewCond(&d.mu)
	return d
}

// Bind names the helper session the open adopted — the client boundary's
// own entry identity, exactly what the open path holds when the spawn RPC
// answers — and starts the worker that delivers the queue, head first, for
// as long as the session's context lives. A Bind that arrives twice is the
// open path's own idempotence and starts nothing twice.
func (d *CompletionDownlink) Bind(entry HostSessionID) {
	d.mu.Lock()
	if d.bound {
		d.mu.Unlock()
		return
	}
	d.bound = true
	d.entry = entry
	d.mu.Unlock()
	go d.run()
}

// Accept runs ingest — the kernel's acceptance of one envelope — in this
// downlink's acceptance order, and queues completion when ingest accepted
// it. `err == nil` from ingest IS the kernel's acceptance (every refusal is
// a sentinel error), so a refused completion queues nothing; completion is
// nil for an envelope that is not one. The lock is held across ingest on
// purpose: the gap between the kernel accepting a completion and the queue
// learning of it is exactly where a second source's completion used to
// overtake it.
func (d *CompletionDownlink) Accept(ingest func() error, completion *lifecycle.Complete) error {
	d.accept.Lock()
	defer d.accept.Unlock()
	if err := ingest(); err != nil {
		return err
	}
	if completion != nil {
		c := pendingCompletion{fence: completion.Fence}
		if completion.ExitCode != nil {
			e := *completion.ExitCode
			c.exitCode = &e
		}
		d.enqueue(c)
	}
	return nil
}

// ObserveEnvironmentEntry queues one accepted environment entry
// (nocx-2v80t.3.21) — a child domain taking the lane over a running local
// command, Ingest's own condition on the stack growing. It is called from
// inside the Ingest that grew the stack, which is Accept's own, so the entry
// takes its place in acceptance order without a lock of its own.
//
// entry is the entry's identity: the child domain whose establishment took
// the lane. A failed send is retried, and an attempt that timed out may have
// landed, so every attempt carries the same identity and the helper seals
// one interval per entry rather than one per delivery (nocx-2v80t.3.28).
func (d *CompletionDownlink) ObserveEnvironmentEntry(entry string) {
	d.enqueue(pendingCompletion{entered: true, entry: entry})
}

// enqueue puts one accepted fact at the back of the queue and wakes the
// worker. See maxPendingCompletions for what a full queue does.
func (d *CompletionDownlink) enqueue(c pendingCompletion) {
	d.mu.Lock()
	for {
		// THE STOP. A session that has ended has no runtime left to tell,
		// and nothing is put on a wire the session's end has condemned.
		if err := d.ctx.Err(); err != nil {
			d.mu.Unlock()
			d.lost(c, err)
			return
		}
		if len(d.pending) < maxPendingCompletions {
			break
		}
		if !d.bound {
			d.mu.Unlock()
			d.lost(c, errNeverBound)
			return
		}
		d.space.Wait()
	}
	d.pending = append(d.pending, c)
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// run is the worker: the only sender. It delivers the queue's head, and only
// then removes it, until the session's context ends — and then everything
// still queued is reported undelivered, because it was.
func (d *CompletionDownlink) run() {
	for {
		d.mu.Lock()
		for len(d.pending) == 0 && d.ctx.Err() == nil {
			d.mu.Unlock()
			select {
			case <-d.wake:
			case <-d.ctx.Done():
			}
			d.mu.Lock()
		}
		if err := d.ctx.Err(); err != nil {
			abandoned := d.pending
			d.pending = nil
			d.space.Broadcast()
			d.mu.Unlock()
			for _, c := range abandoned {
				d.lost(c, err)
			}
			return
		}
		head := d.pending[0]
		d.mu.Unlock()

		d.deliver(head)

		d.mu.Lock()
		d.pending = d.pending[1:]
		d.space.Broadcast()
		d.mu.Unlock()
	}
}

// deliver settles one queued fact: it reaches the helper session, or it is
// reported lost with the cause that made it so. It retries every failure
// that might pass (retryable) and gives up only on one that cannot, or on
// the session's end.
//
// THE STOP IS CHECKED BEFORE EVERY ATTEMPT. The context is the hosted
// session's lifetime; when it is done the downlink is over, and a pane whose
// session has ended has no runtime left that matches the nonce. An attempt
// may BEGIN only while the session's context is live, and one already on
// the wire when the session ends finishes there — its own context ends with
// the session's, which is as far as a write already made can be recalled.
func (d *CompletionDownlink) deliver(c pendingCompletion) {
	for attempt := 1; ; attempt++ {
		if err := d.ctx.Err(); err != nil {
			d.lost(c, err)
			return
		}
		err := d.sendOnce(c)
		if err == nil {
			if attempt > 1 {
				log.From(d.ctx).Info("helper: the "+c.kind()+" the kernel accepted reached the helper session on a retry",
					"attempt", attempt)
			}
			return
		}
		if !retryable(err) || d.ctx.Err() != nil {
			d.lost(c, err)
			return
		}
		log.From(d.ctx).Warn("helper: delivering the "+c.kind()+" the kernel accepted failed; retrying",
			"attempt", attempt, "err", err)
		if waitErr := d.retryWait(d.ctx, attempt); waitErr != nil {
			d.lost(c, fmt.Errorf("%w; the session ended before the retry: %w", err, waitErr))
			return
		}
	}
}

// sendOnce is one attempt, bounded by completionDeliveryTimeout. The
// incarnation is derived HERE, from the entry the spawn returned: the helper
// mints one session runtime per PTY, at generation 1, over the session id
// it minted — that fact is newSessionRuntime's (internal/helper/session),
// and TestTheRuntimeIncarnationIsTheSessionAtGenerationOne is the pin that
// fails the day the helper ever mints differently. The wire's Incarnation is
// deliberately NOT HostSessionID: that Generation names the helper INSTALL,
// a string, and parsing it as the runtime's numeric generation is the defect
// the separate spelling exists to make impossible.
func (d *CompletionDownlink) sendOnce(c pendingCompletion) error {
	session := proto.HostSessionID{
		Generation: proto.GenerationID(d.entry.Generation),
		Session:    d.entry.Session,
	}
	inc := proto.Incarnation{Session: d.entry.Session, Generation: 1}
	// The deadline hangs off the session's own context, so the pane's end
	// still ends the wait first.
	ctx, cancel := context.WithTimeout(d.ctx, completionDeliveryTimeout)
	defer cancel()
	if c.entered {
		return d.sendEntered(ctx, proto.LifecycleEnteredParams{Session: session, Incarnation: inc, Entry: c.entry})
	}
	return d.send(ctx, proto.LifecycleCompleteParams{
		Session:     session,
		Incarnation: inc,
		Nonce:       hex.EncodeToString(c.fence[:]),
		ExitCode:    c.exitCode,
	})
}

// retryable answers whether a failed attempt might succeed if made again. A
// refusal is the helper's own answer and is final; a lost connection is
// final for this carrier (client.go: a lost transport fails every later
// request too); a request too large for a frame will be too large again.
// Everything else — above all one attempt's own deadline passing — may
// pass.
func retryable(err error) bool {
	var refusal *RefusalError
	switch {
	case errors.As(err, &refusal), errors.Is(err, ErrLost), errors.Is(err, ErrRequestTooLarge):
		return false
	}
	return true
}

// waitToRetry pauses before attempt+1: firstRetryPause, doubling, never
// longer than one attempt's own bound. It answers the context's error when
// the session ends first.
func waitToRetry(ctx context.Context, attempt int) error {
	pause := firstRetryPause
	for i := 1; i < attempt && pause < completionDeliveryTimeout; i++ {
		pause *= 2
	}
	if pause > completionDeliveryTimeout {
		pause = completionDeliveryTimeout
	}
	t := time.NewTimer(pause)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// lost reports one accepted fact that did not reach the helper session. The
// kernel's execution state is exactly what it set when it accepted the fact,
// and nothing here reaches back into it; what is lost is the runtime's
// boundary, and the log line — with the cause chain intact — is how anybody
// learns of it.
func (d *CompletionDownlink) lost(c pendingCompletion, cause error) {
	log.From(d.ctx).Warn("helper: the "+c.kind()+" the kernel accepted did not reach the helper session",
		"err", fmt.Errorf("%s not delivered: %w", c.kind(), cause))
}

func (c pendingCompletion) kind() string {
	if c.entered {
		return "environment entry"
	}
	return "completion"
}

// CompletionObservingKernel wraps the kernel seam a hosted pane's adapter
// drives, and observes the completions it ACCEPTS. `err == nil` from Ingest
// is exactly the kernel's acceptance — every refusal is a sentinel error —
// and only accepted completions are observed: a finish the kernel refuses
// (wrong epoch, capability or sequence) reaches nothing downstream, which
// is the whole of the not-a-second-gate rule's other half. The wrapper is
// per pane, so every completion it sees rode this pane's own channel —
// nested child domains included, which is correct: the helper session owns
// the whole lane, and its runtime matches completions by nonce.
//
// "Every completion it sees rode this pane's own channel" is the whole of
// what one wrapper can see, and it is not the whole lane: an ssh child
// authenticates on a listener of its own (internal/app/childdomain.go's
// buildSSHChildBootstrap), so the child's completions never cross the pane's
// wrapper. That listener is given a wrapper of its own over the SAME
// downlink (nocx-2v80t.3.24) — which is why the observer is an interface:
// the app side reaches the downlink through a lane registry rather than
// holding the concrete type.
type CompletionObservingKernel struct {
	lifecyclechannel.Kernel
	downlink CompletionObserver
}

// CompletionObserver is what an observing kernel hands every ingest to, so
// the observer can fix acceptance order at the moment of acceptance
// (CompletionDownlink.Accept). *CompletionDownlink is the one production
// implementation.
type CompletionObserver interface {
	Accept(ingest func() error, completion *lifecycle.Complete) error
}

// CompletionObservingAdoptingKernel is the adopting form of the observing
// wrapper: the seam a re-adopted pane's adapter drives (AdoptingKernel),
// which needs the adoption the wrapped kernel already had — forwarded
// un-gated, because an adoption is not a completion and the wrapper adds no
// gate on it either — with every frame the adapter ingests still observed by
// the ordinary wrapper it embeds. It exists because AdoptingKernel is a
// separate interface on purpose: a kernel that only ever mints must not gain
// an adopt by being wrapped, so the adopt is taken from the kernel, not
// minted by the wrapper.
type CompletionObservingAdoptingKernel struct {
	CompletionObservingKernel
	adopting lifecyclechannel.AdoptingKernel
}

// NewCompletionObservingAdoptingKernel wraps an adopting kernel for a
// re-adopted pane's adapter. The adapter cannot tell it apart from the
// kernel it wrapped.
func NewCompletionObservingAdoptingKernel(k lifecyclechannel.AdoptingKernel, d *CompletionDownlink) *CompletionObservingAdoptingKernel {
	return &CompletionObservingAdoptingKernel{
		CompletionObservingKernel: CompletionObservingKernel{Kernel: k, downlink: d},
		adopting:                  k,
	}
}

// AdoptDomain forwards the wrapped kernel's own adoption.
func (k *CompletionObservingAdoptingKernel) AdoptDomain(lane lifecycle.LaneID, domain lifecycle.DomainID, epoch uint64, capability lifecycle.Capability, recovery lifecycle.FenceNonce, t lifecycle.TransportID) (lifecycle.DomainHandle, error) {
	return k.adopting.AdoptDomain(lane, domain, epoch, capability, recovery, t)
}

// NewCompletionObservingKernel wraps k. The wrapper satisfies the same
// lifecyclechannel.Kernel seam, so the adapter cannot tell it apart.
func NewCompletionObservingKernel(k lifecyclechannel.Kernel, d CompletionObserver) *CompletionObservingKernel {
	return &CompletionObservingKernel{Kernel: k, downlink: d}
}

// Ingest forwards to the kernel and observes acceptance. What crosses this
// wrapper is completions alone (Accept); an authenticated environment entry
// (nocx-2v80t.3.21, ObserveEnvironmentEntry) is a LANE-level fact rather than
// a per-transport one — the child domain that triggers it authenticates on
// its OWN transport, over a listener this wrapper never sees (a local
// nested shell shares the parent's descriptor, but an ssh child's hello
// arrives on a forwarded connection of its own) — so it is observed at
// internal/app's environmentEntryEmitter, which decorates the one
// lifecyclepub.Emitter every transport's Ingest already reports to,
// regardless of which one carried the frame.
//
// EVERY envelope goes through the observer's Accept, not only completions:
// an environment entry is observed from inside the Ingest of whatever frame
// grew the stack, and it has to take its place in the same order.
func (k *CompletionObservingKernel) Ingest(t lifecycle.TransportID, env lifecycle.Envelope) error {
	var completion *lifecycle.Complete
	if env.Event.Kind == lifecycle.KindComplete {
		completion = env.Event.Complete
	}
	return k.downlink.Accept(func() error { return k.Kernel.Ingest(t, env) }, completion)
}
