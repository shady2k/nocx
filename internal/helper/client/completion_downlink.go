package client

// The coordinator's half of the completion downlink (owner decision
// 2026-09-19): the lifecycle channel is authenticated HERE, in the
// coordinator's kernel, and the already-authenticated completion is carried
// DOWN to the helper session that owns the pane. Authentication does not
// move: this file is a carrier, not a second gate. What the kernel accepted
// is delivered once, addressed to the session the open adopted; what the
// kernel refused is never observed at all; and a delivery that fails is
// reported while the kernel's own execution state stands untouched.

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
)

// pendingCompletion is one accepted completion — or, entered set, one
// accepted environment entry (nocx-2v80t.3.21) — waiting for the open to
// learn which helper session it belongs to. The two share one buffer and one
// FIFO rather than two, so an entry that lands between two completions is
// delivered in the same order the kernel accepted all three: the runtime's
// own seal (ADR-0074) cares which interval an event closes, and a downlink
// that reordered them behind the bind would hand it the wrong one.
type pendingCompletion struct {
	entered  bool
	fence    [32]byte
	exitCode *int
}

// CompletionDownlink delivers the completions one hosted pane's kernel
// accepts to that pane's helper session, over the `lifecycle-complete` op on the `session` service.
//
// THE ORDERING RACE IT EXISTS FOR: the adapter (and with it the kernel) is
// built BEFORE the spawn RPC returns the helper session's identity, so a
// shell that completes a command inside that window is accepted by the
// kernel while the downlink still has no addressee. Completions observed
// before Bind are BUFFERED — bounded, because a shell that ran dozens of
// commands before its own spawn was answered is not a window problem but a
// wedge worth reporting — and delivered in the order the kernel accepted
// them the moment Bind names the session.
type CompletionDownlink struct {
	send func(context.Context, proto.LifecycleCompleteParams) error
	// sendEntered is send's sibling for an environment entry
	// (nocx-2v80t.3.21): the same carrier, the same session, no fence to
	// carry.
	sendEntered func(context.Context, proto.LifecycleEnteredParams) error
	// report is told about every failed delivery. Nil reports nowhere, and
	// the failure is still the kernel's state's business: nothing here ever
	// reaches back into the kernel, whose execution state is exactly what it
	// set when it accepted the completion.
	report func(error)
	ctx    context.Context

	mu      sync.Mutex
	bound   bool
	entry   HostSessionID
	pending []pendingCompletion
}

// maxPendingBounds the completions buffered while the spawn RPC is in
// flight. One per command the shell could have completed in that window is
// already generous; running past it means the open path itself is wedged,
// and a silent unbounded buffer would hide exactly that.
const maxPendingCompletions = 16

// completionDeliveryTimeout bounds ONE delivery, and the reason is the
// caller: deliver runs on the adapter's ingest goroutine, synchronously
// under the kernel's Ingest, so a helper that holds its connection open and
// never answers would park that goroutine — and with it every later
// lifecycle event for this pane — until the session itself ended. A
// completion is a small op on a connection the pane is already using; five
// seconds is this package's existing scale for exactly that shape
// (signalTimeout), and anything past it is a wedge to report rather than a
// slow answer to wait for.
const completionDeliveryTimeout = 5 * time.Second

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
// that must still reach the helper session. report may be nil.
func NewCompletionDownlink(c CompletionSender, ctx context.Context, report func(error)) *CompletionDownlink {
	return &CompletionDownlink{
		send: func(ctx context.Context, params proto.LifecycleCompleteParams) error {
			return c.LifecycleComplete(ctx, params)
		},
		sendEntered: func(ctx context.Context, params proto.LifecycleEnteredParams) error {
			return c.LifecycleEntered(ctx, params)
		},
		report: report,
		ctx:    ctx,
	}
}

// Bind names the helper session the open adopted — the client boundary's
// own entry identity, exactly what the open path holds when the spawn RPC
// answers. Completions the kernel accepted before this call are delivered
// now, in acceptance order; a Bind that arrives twice is the open path's
// own idempotence and delivers nothing twice.
func (d *CompletionDownlink) Bind(entry HostSessionID) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.bound {
		return
	}
	d.bound = true
	d.entry = entry
	pending := d.pending
	d.pending = nil
	// Delivered UNDER the lock, deliberately: the guarantee is acceptance
	// order, and an Observe racing this drain must not deliver a newer
	// completion ahead of the older ones the bind is still sending. The
	// only contenders for this mutex are the pump goroutine and this one
	// call, so a delivery parked behind the drain is bounded by the same
	// bound the buffer has.
	for _, c := range pending {
		d.deliver(c)
	}
}

// Observe delivers one accepted completion, or buffers it when Bind has not
// named the session yet. It runs outside the kernel's lock, on the adapter
// pump's goroutine: the send is one control-plane round trip, and the
// lifecycle pipe buffers whatever arrives behind it — milliseconds of delay,
// never a lost frame.
//
// The fence is the kernel's own render nonce, passed through verbatim; exit
// is what the shell named, and nil when it named nothing. Neither is judged
// here: this method exists because the kernel already accepted the fact.
func (d *CompletionDownlink) Observe(fence [32]byte, exit *int) {
	c := pendingCompletion{fence: fence, exitCode: exit}
	d.mu.Lock()
	if !d.bound {
		if len(d.pending) >= maxPendingCompletions {
			d.mu.Unlock()
			d.reportf("the helper session's identity was never bound; dropping the completion the kernel accepted")
			return
		}
		d.pending = append(d.pending, c)
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	d.deliver(c)
}

// ObserveEnvironmentEntry delivers one accepted environment entry
// (nocx-2v80t.3.21) — a child domain taking the lane over a running local
// command, Ingest's own condition on the stack growing — or buffers it when
// Bind has not named the session yet. Observe's own shape, with no fence to
// carry: there is none for this boundary.
func (d *CompletionDownlink) ObserveEnvironmentEntry() {
	c := pendingCompletion{entered: true}
	d.mu.Lock()
	if !d.bound {
		if len(d.pending) >= maxPendingCompletions {
			d.mu.Unlock()
			d.reportf("the helper session's identity was never bound; dropping the environment entry the kernel accepted")
			return
		}
		d.pending = append(d.pending, c)
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	d.deliver(c)
}

// deliver sends one completion DOWN. The incarnation is derived HERE, once,
// from the entry the spawn returned: the helper mints one session runtime
// per PTY, at generation 1, over the session id it minted — that fact is
// newSessionRuntime's (internal/helper/session), and
// TestTheRuntimeIncarnationIsTheSessionAtGenerationOne is the pin that fails
// the day the helper ever mints differently. The wire's Incarnation is
// deliberately NOT HostSessionID: that Generation names the helper INSTALL,
// a string, and parsing it as the runtime's numeric generation is the defect
// the separate spelling exists to make impossible.
func (d *CompletionDownlink) deliver(c pendingCompletion) {
	// THE STOP, CHECKED AT DISPATCH. The context is the hosted session's
	// lifetime; when it is done the downlink is over, and a pane whose
	// session has ended has no runtime left that matches the nonce. Sending
	// anyway would put the op on a wire the session's end has already
	// condemned — and ask the helper to cancel an exchange nobody wants —
	// so the stop is decided HERE rather than left to the transport's
	// post-write handling of an already-cancelled context: the completion is
	// reported, and the kernel's execution state stands as it set it.
	//
	// WHERE THE BOUNDARY IS, exactly: a delivery may BEGIN only while the
	// session's context is live, and one already on the wire when the
	// session ends finishes there. It is not tightened past that on
	// purpose — serialising the write against the cancellation would hold a
	// lock across a network call to buy a helper answering "no such
	// session" to an op nobody is waiting for.
	if err := d.ctx.Err(); err != nil {
		if c.entered {
			d.reportf("the environment entry the kernel accepted did not reach the helper session: %v", err)
			return
		}
		d.reportf("the completion the kernel accepted did not reach the helper session: %v", err)
		return
	}
	if c.entered {
		params := proto.LifecycleEnteredParams{
			Session: proto.HostSessionID{
				Generation: proto.GenerationID(d.entry.Generation),
				Session:    d.entry.Session,
			},
			Incarnation: proto.Incarnation{Session: d.entry.Session, Generation: 1},
		}
		ctx, cancel := context.WithTimeout(d.ctx, completionDeliveryTimeout)
		defer cancel()
		if err := d.sendEntered(ctx, params); err != nil {
			d.reportf("the environment entry the kernel accepted did not reach the helper session: %v", err)
		}
		return
	}
	var exit *int
	if c.exitCode != nil {
		e := *c.exitCode
		exit = &e
	}
	params := proto.LifecycleCompleteParams{
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(d.entry.Generation),
			Session:    d.entry.Session,
		},
		Incarnation: proto.Incarnation{Session: d.entry.Session, Generation: 1},
		Nonce:       hex.EncodeToString(c.fence[:]),
		ExitCode:    exit,
	}
	// ONE DELIVERY, BOUNDED. The deadline hangs off the session's own
	// context, so the pane's end still ends the wait first.
	ctx, cancel := context.WithTimeout(d.ctx, completionDeliveryTimeout)
	defer cancel()
	if err := d.send(ctx, params); err != nil {
		// The kernel's execution state is what it set when it accepted this
		// completion, and nothing here can or should change it: the report
		// is the whole of this failure's handling (criterion 3).
		d.reportf("the completion the kernel accepted did not reach the helper session: %v", err)
	}
}

func (d *CompletionDownlink) reportf(format string, args ...any) {
	if d.report != nil {
		d.report(fmt.Errorf(format, args...))
	}
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

// CompletionObserver is what an observing kernel reports an accepted
// completion to. *CompletionDownlink is the one production implementation.
type CompletionObserver interface {
	Observe(fence [32]byte, exit *int)
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
// wrapper is completions alone (Observe); an authenticated environment entry
// (nocx-2v80t.3.21, ObserveEnvironmentEntry) is a LANE-level fact rather than
// a per-transport one — the child domain that triggers it authenticates on
// its OWN transport, over a listener this wrapper never sees (a local
// nested shell shares the parent's descriptor, but an ssh child's hello
// arrives on a forwarded connection of its own) — so it is observed at
// internal/app's environmentEntryEmitter, which decorates the one
// lifecyclepub.Emitter every transport's Ingest already reports to,
// regardless of which one carried the frame.
func (k *CompletionObservingKernel) Ingest(t lifecycle.TransportID, env lifecycle.Envelope) error {
	err := k.Kernel.Ingest(t, env)
	if err != nil {
		return err
	}
	if env.Event.Kind == lifecycle.KindComplete && env.Event.Complete != nil {
		k.downlink.Observe(env.Event.Complete.Fence, env.Event.Complete.ExitCode)
	}
	return err
}
