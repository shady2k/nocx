package app

// DescendantPaneAccess and the revocation that cannot be outrun it carries
// (design §7, spec revision 6).
//
// This file has two halves with different production status. Resolve and
// the type that carries it (DescendantPaneAccess, Bind) are pure — they
// consult only workers.Registrar, which Task 7 wires in production
// (worker_auth.go's toolAuthorizer.BindRevoker uses the Registrar directly
// for the same reason). The helper-epoch half (paneAccessHub.revoke,
// admitting, noteCommitBy) needs a helper client and a monotonic clock that
// Task 5 has not built yet; paneHelpers and monotonicClock below are the
// seams that let this half compile and be fully tested against fakes before
// then. Nothing in app.go constructs a *paneAccessHub with real
// implementations of either — that wiring is Task 8's, once Task 5 exists.
// A toolAuthorizer never given one via BindPaneAccess simply leaves
// RunContext.PaneAccess nil, which is the correct, safe answer for "no
// adapter has bound descendant authority yet".

import (
	"context"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/workers"
)

// Nanos is a monotonic-clock reading in nanoseconds — CLOCK_MONOTONIC on
// Linux, a mach_continuous_time-based clock on Darwin (design §7.2). It is
// its own type, not time.Duration or time.Time, because a commitBy deadline
// is compared only against readings of the SAME clock the coordinator and a
// local helper share; a wall-clock time.Time would invite comparing it
// against the wrong clock the moment either side's wall clock stepped.
type Nanos int64

// systemMonoClock is the production monotonicClock: internal/monoclock's
// real CLOCK_MONOTONIC (or Darwin's mach_continuous_time-based equivalent)
// reading, narrowed to the Nanos type this package compares commitBy
// deadlines against. It is the Task 5 implementation pane_access_test.go's
// fakePaneClock stands in for.
type systemMonoClock struct{}

func (systemMonoClock) Now() Nanos { return Nanos(monoclock.Now()) }

// paneHelpers is the per-session helper wire operations revocation and
// session.read both need. AccessBump is revocation's own (§7.2); Snapshot
// and Target are session.read's read path (§6.1) — added here rather than
// as a second lookup interface because both questions resolve through the
// SAME per-session helper handle (paneHelperLookup.HelperFor already
// answers "which helper holds this pane's terminal", and a PaneReader
// built over a second, independent answer to that question would be a
// second owner of it). Task 5/8 supply the real implementation over
// helperclient.Client; tests supply a fake that can block, refuse, or
// answer immediately, so both revoke's waiting behaviour and a read's
// snapshot/mint sequence are fully exercisable without a real helper
// process.
type paneHelpers interface {
	// AccessBump asks the session's owner to move its access epoch past
	// above, and reports the epoch that resulted. It is expected to block
	// until every intent the owner holds older than the bump is terminal
	// (design §7.2) — that blocking IS the barrier revoke waits on.
	AccessBump(ctx context.Context, sessionID string, above uint64) (epoch uint64, err error)
	// Snapshot asks the session's runtime for a consistent read of its
	// screen (design §6.1): the frame a PaneReader classifies, and the
	// facts (access epoch, read barrier) a target minted from it inherits.
	Snapshot(ctx context.Context, sessionID string) (proto.SnapshotResult, error)
	// Target mints a one-shot, signed target from a retained snapshot —
	// always against the SAME snapshotId a prior Snapshot call answered,
	// never re-derived, so classification, rows and digest describe one
	// frame (design §6.1).
	Target(ctx context.Context, sessionID string, p proto.TargetParams) (proto.TargetResult, error)
	// Intent spends a target's token — session.keys' own write (Task 9,
	// design §6.5). Added alongside Snapshot/Target rather than through a
	// second per-session lookup, for the reason paneReader's own doc gives:
	// PaneKeys and a read resolve the identical "which helper holds this
	// pane" question, and a second answer to it would be a second owner of
	// one input (AGENTS.md).
	Intent(ctx context.Context, sessionID string, p proto.IntentParams) (proto.IntentResult, error)
	// IntentStatus polls a token-bound intent without spending or
	// re-presenting it (design §6.2) — PaneKeys' recovery path when a
	// transport error leaves an intent's own outcome unknown (design §7.2).
	IntentStatus(ctx context.Context, sessionID string, tokenID string) (proto.IntentStatusResult, error)
}

// paneHelperLookup finds the helper that owns a session's pane — the same
// question paneScreen.owner (panescreen.go) answers for a screen read,
// narrowed to the AccessBump seam so this package need not import
// helperclient before Task 5 gives its client an AccessBump method. ok is
// false when nothing holds the session's pane any more, which revoke treats
// as nothing left to bump: a session that is already gone cannot commit a
// stale intent.
type paneHelperLookup interface {
	HelperFor(ctx context.Context, sessionID string) (paneHelpers, bool)
}

// monotonicClock is Now() on CLOCK_MONOTONIC (or the Darwin equivalent),
// narrowed to what revoke needs: a reading to compare a recorded commitBy
// against. Task 5 supplies the real clock; a fake lets a test drive "the
// deadline has passed" as an explicit, controlled fact rather than an
// elapsed wall-clock interval — the "wait on an observable state change,
// never on a duration" rule (AGENTS.md) applied to the one place this
// design genuinely has to wait something out.
type monotonicClock interface{ Now() Nanos }

// Authority names which of the two adapters bound a DescendantPaneAccess
// (spec §7.1: the tool endpoint's admission, or the kernel's own run) and
// the interval within it, so a caller inspecting an authority it was
// handed can tell which policy gate stands behind it.
//
// It is a SEALED SUM — EndpointAuthority and KernelAuthority, and nothing
// else may implement authoritySealed from outside this package — rather
// than one struct carrying a string "Kind" label. A label is a claim
// nothing checks: nocx-6q1uh.13a's adversarial pass bound the same
// controller under Kind "endpoint" and Kind "kernel" and found every
// downstream Resolve treated the two identically, because nothing ever
// read Kind below Bind. Two distinct types close that gap at compile time:
// a consumer that only makes sense for one adapter's own interval names
// the type it needs (AsEndpoint/AsKernel below) and is refused the other
// by a failed assertion, not by remembering to compare a string.
type Authority interface {
	// authoritySealed is unexported so only this package may add a third
	// variant — the same closure agentdriver's State enum and workers'
	// Effect set already rely on for their own "nothing else may answer
	// this question" guarantee.
	authoritySealed()
}

// EndpointAuthority is a DescendantPaneAccess bound by the tool endpoint's
// admission (worker_auth.go's Admit): AdmissionEpoch names the admission
// interval whose retirement ends this access too (retire, §7.2).
type EndpointAuthority struct {
	AdmissionEpoch toolendpoint.AdmissionEpoch
}

func (EndpointAuthority) authoritySealed() {}

// KernelAuthority is a DescendantPaneAccess bound by the kernel for one of
// its own runs (design §7.3: the assistant acting over workers IT
// spawned): RunID names the run whose own policy gate covers every call
// made under it.
type KernelAuthority struct {
	RunID string
}

func (KernelAuthority) authoritySealed() {}

// DescendantPaneAccess is the capability bound before dispatch by the
// adapter that authenticated the caller — never inferred from a call's own
// parameters (design §7.1). It reaches the panes of the bound controller's
// descendants: participants whose delegation chain reaches controller,
// resolved fresh, server-side, on every call.
type DescendantPaneAccess struct {
	hub        *paneAccessHub
	controller string
	identity   session.Identity
	authority  Authority
}

// Resolve asks whether sessionID belongs to the bound controller's subtree
// and currently permits e, returning the chain that proved it (or
// workers.ErrNotReachable). A nil access or a hub with no registrar behind
// it — an authority nothing bound to a live record — answers the same way
// a caller that cannot reach anything does: not reachable, never a panic
// and never a different error a caller would have to special-case.
func (a *DescendantPaneAccess) Resolve(ctx context.Context, sessionID string, e workers.Effect) (workers.Reach, error) {
	if a == nil || a.hub == nil || a.hub.registrar == nil {
		return workers.Reach{}, workers.ErrNotReachable
	}
	return a.hub.registrar.Resolve(ctx, a.controller, sessionID, e)
}

// Controller is the bound session every Resolve on this capability is about.
func (a *DescendantPaneAccess) Controller() string {
	if a == nil {
		return ""
	}
	return a.controller
}

// Identity is the incarnation Controller was bound under.
func (a *DescendantPaneAccess) Identity() session.Identity {
	if a == nil {
		return session.Identity{}
	}
	return a.identity
}

// Authority is which adapter bound this capability, and under what
// interval.
func (a *DescendantPaneAccess) Authority() Authority {
	if a == nil {
		return nil
	}
	return a.authority
}

// AsEndpoint returns the endpoint admission this access was bound under,
// and false when it was bound by the kernel instead (or a is nil/unbound).
// A consumer that only makes sense for the endpoint's own interval — for
// instance, one that must close when THAT admission retires — asks this
// rather than comparing a label nothing enforced: the wrong variant is
// refused here, at the type, by the failed assertion (nocx-6q1uh.13a).
func (a *DescendantPaneAccess) AsEndpoint() (EndpointAuthority, bool) {
	if a == nil {
		return EndpointAuthority{}, false
	}
	ep, ok := a.authority.(EndpointAuthority)
	return ep, ok
}

// AsKernel is AsEndpoint's counterpart for the kernel's own run interval.
func (a *DescendantPaneAccess) AsKernel() (KernelAuthority, bool) {
	if a == nil {
		return KernelAuthority{}, false
	}
	k, ok := a.authority.(KernelAuthority)
	return k, ok
}

// paneAccessHub is the coordinator-side half of revocation that cannot be
// outrun (design §7.2). Registrar.Revoke / RevokeController name which
// delegations end and which pane sessions that affects, purely at the
// record level; this hub turns that session list into
// session.access.bump calls and tracks, per session, whether a bump is
// outstanding (admitting) and the latest commitBy this coordinator has
// promised an intent under (noteCommitBy) — the deadline a bump that never
// acknowledges is waited out against.
//
// No coordinator lock is ever held across a call into paneHelpers: mu
// guards only this hub's own bookkeeping (pending, epochs, commitBy), and
// every helper call happens with mu released.
type paneAccessHub struct {
	registrar *workers.Registrar
	lookup    paneHelperLookup
	clock     monotonicClock

	mu       sync.Mutex
	pending  map[string]bool
	epochs   map[string]uint64
	commitBy map[string]Nanos
}

// newPaneAccessHub builds a hub over registrar, using lookup to find each
// session's helper and clock to bound how long an unacknowledged bump is
// waited out. lookup or clock may be nil in a test that never exercises
// revoke; Bind and Resolve need neither.
func newPaneAccessHub(registrar *workers.Registrar, lookup paneHelperLookup, clock monotonicClock) *paneAccessHub {
	return &paneAccessHub{
		registrar: registrar,
		lookup:    lookup,
		clock:     clock,
		pending:   make(map[string]bool),
		epochs:    make(map[string]uint64),
		commitBy:  make(map[string]Nanos),
	}
}

// Bind mints a DescendantPaneAccess for controller's current incarnation
// and authority interval. Called by the adapter that authenticated the
// caller, before dispatch (spec §7.1) — never by a tool, and never from a
// call's own parameters.
func (h *paneAccessHub) Bind(controller string, identity session.Identity, a Authority) *DescendantPaneAccess {
	return &DescendantPaneAccess{hub: h, controller: controller, identity: identity, authority: a}
}

// noteCommitBy records the latest commitBy this coordinator has sent an
// intent to sessionID under. A later revoke's wait for that session, if its
// bump never acknowledges, is bounded by the LATEST commitBy recorded here:
// once the injected clock reports a reading past it, no intent this
// coordinator sent to that session can still commit (design §7.2), whatever
// the helper's state or the connection's.
func (h *paneAccessHub) noteCommitBy(sessionID string, commitBy Nanos) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, ok := h.commitBy[sessionID]; !ok || commitBy > cur {
		h.commitBy[sessionID] = commitBy
	}
}

// admitting reports whether sessionID currently accepts a new intent: false
// exactly while a bump revoke sent for it is outstanding, from the moment
// revoke starts it until it resolves — by ack or by its commitBy deadline.
func (h *paneAccessHub) admitting(sessionID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.pending[sessionID]
}

func (h *paneAccessHub) markPending(sessionID string, pending bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if pending {
		h.pending[sessionID] = true
		return
	}
	delete(h.pending, sessionID)
}

func (h *paneAccessHub) aboveFor(sessionID string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.epochs[sessionID]
}

func (h *paneAccessHub) setEpoch(sessionID string, epoch uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.epochs[sessionID] = epoch
}

func (h *paneAccessHub) commitByFor(sessionID string) (Nanos, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.commitBy[sessionID]
	return d, ok
}

// pollInterval is how often revokeOne re-checks the injected clock against
// a session's commitBy while a helper's AccessBump has not yet answered. It
// is a scheduling yield, not a timeout: the loop's exit condition is always
// h.clock.Now() crossing the recorded deadline — an explicit, test-driven
// fact — never elapsed wall-clock time. Sub-millisecond would busy-spin for
// no benefit; a full second would make a test that advances the clock and
// expects a prompt "deadline" outcome wait a second for no reason.
const pollInterval = time.Millisecond

// revoke sends session.access.bump to every session in sessions and waits
// each out independently: an ack once the helper's AccessBump returns
// successfully, or — for a helper that never answers — the latest commitBy
// noteCommitBy recorded for that session having passed on the injected
// clock. It never holds mu, or any other coordinator lock, while calling
// into a helper: the call happens on its own goroutine, and only the result
// is read back under mu.
func (h *paneAccessHub) revoke(ctx context.Context, sessions []string) map[string]string {
	result := make(map[string]string, len(sessions))
	if len(sessions) == 0 {
		return result
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, sid := range sessions {
		h.markPending(sid, true)
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			confirmed := h.revokeOne(ctx, sid)
			h.markPending(sid, false)
			mu.Lock()
			result[sid] = confirmed
			mu.Unlock()
		}(sid)
	}
	wg.Wait()
	return result
}

type bumpOutcome struct {
	epoch uint64
	err   error
}

// revokeOne is revoke's per-session body. It is separate so the "no lock
// held across a helper call" property is visible in one place: everything
// above this line runs with mu held for a lookup at most; the helper call
// itself is made on its own goroutine, entirely outside any lock this hub
// or the Registrar holds.
func (h *paneAccessHub) revokeOne(ctx context.Context, sessionID string) string {
	if h.lookup == nil {
		return "ack"
	}
	helper, ok := h.lookup.HelperFor(ctx, sessionID)
	if !ok {
		// Nothing holds this pane's terminal any more. There is no old
		// intent left that could still commit against it.
		return "ack"
	}
	above := h.aboveFor(sessionID)
	deadline, hasDeadline := h.commitByFor(sessionID)

	resultCh := make(chan bumpOutcome, 1)
	go func() {
		epoch, err := helper.AccessBump(ctx, sessionID, above)
		resultCh <- bumpOutcome{epoch, err}
	}()

	if !hasDeadline || h.clock == nil {
		res := <-resultCh
		if res.err != nil {
			return "deadline"
		}
		h.setEpoch(sessionID, res.epoch)
		return "ack"
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case res := <-resultCh:
			if res.err != nil {
				return "deadline"
			}
			h.setEpoch(sessionID, res.epoch)
			return "ack"
		case <-ctx.Done():
			return "deadline"
		case <-ticker.C:
			if h.clock.Now() >= deadline {
				return "deadline"
			}
		}
	}
}

// RevokeParticipant is Registrar.Revoke followed by the helper-epoch bump
// over the sessions it names — the two halves §7.2 describes as one
// operation, composed here so a caller triggering it (Close, admit's
// terminalization path, once Task 8 wires this hub into them) gets both
// without re-deriving the sequence. cause travels through to the record's
// own log line only.
func (h *paneAccessHub) RevokeParticipant(ctx context.Context, root workers.ParticipantID, cause string) (map[string]string, error) {
	if h.registrar == nil {
		return nil, nil
	}
	sessions, err := h.registrar.Revoke(ctx, root, cause)
	if err != nil {
		return nil, err
	}
	return h.revoke(ctx, sessions), nil
}

// RevokeController is RevokeParticipant's counterpart for a controller
// SESSION rather than a participant — worker_auth.retire's shape, once this
// hub (rather than the Registrar directly) is wired there.
func (h *paneAccessHub) RevokeController(ctx context.Context, controller string, cause string) (map[string]string, error) {
	if h.registrar == nil {
		return nil, nil
	}
	sessions, err := h.registrar.RevokeController(ctx, controller, cause)
	if err != nil {
		return nil, err
	}
	return h.revoke(ctx, sessions), nil
}
