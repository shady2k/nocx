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

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
)

// pendingCompletion is one accepted completion waiting for the open to learn
// which helper session it belongs to.
type pendingCompletion struct {
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

// CompletionSender is the one op the downlink needs from the pane's client:
// the carrier op the already-authenticated completion travels down on.
// *Client is it; so is any carrier that forwards the session service — the
// re-adoption path's hostedCarrier, which is a connection and not always
// this client.
type CompletionSender interface {
	LifecycleComplete(ctx context.Context, params proto.LifecycleCompleteParams) error
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
	if err := d.ctx.Err(); err != nil {
		d.reportf("the completion the kernel accepted did not reach the helper session: %v", err)
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
	if err := d.send(d.ctx, params); err != nil {
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
type CompletionObservingKernel struct {
	lifecyclechannel.Kernel
	downlink *CompletionDownlink
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
func NewCompletionObservingKernel(k lifecyclechannel.Kernel, d *CompletionDownlink) *CompletionObservingKernel {
	return &CompletionObservingKernel{Kernel: k, downlink: d}
}

// Ingest forwards to the kernel and observes acceptance.
func (k *CompletionObservingKernel) Ingest(t lifecycle.TransportID, env lifecycle.Envelope) error {
	err := k.Kernel.Ingest(t, env)
	if err == nil && env.Event.Kind == lifecycle.KindComplete && env.Event.Complete != nil {
		k.downlink.Observe(env.Event.Complete.Fence, env.Event.Complete.ExitCode)
	}
	return err
}
