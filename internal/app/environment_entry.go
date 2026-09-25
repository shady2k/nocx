package app

// A confirmed environment change's own wire (nocx-2v80t.3.21): the block a
// local command opened must have its rows artifact sealed — a closing
// screen appended, with no fence — the moment a CHILD domain takes the lane,
// exactly as the frontend already freezes the same block on the same event
// (frontend/src/lifecycle/projections.ts: "A child domain took the lane:
// the remote session has begun... Only growth counts... Depth 1 is the
// FIRST domain of the lane integrating, which is not an entry into
// anything").
//
// WHY THIS IS NOT internal/helper/client's CompletionObservingKernel'S JOB,
// measured rather than assumed. That wrapper sees only the events that
// arrive on ONE PANE'S OWN transport — a local nested shell's (sudo, su)
// hello does, because it shares the parent's own inherited descriptor, but
// an SSH CHILD'S hello does not: internal/app/childdomain.go's
// buildSSHChildBootstrap authenticates it on a lifecyclechannel.Listener of
// its own, built fresh per grant over `pub` directly
// (lifecyclechannel.NewListener(lg, pub)), never over the pane's wrapped
// kernel. A first version of this fix read the wrapper's own Ingest for
// domain_suspended instead, and that is wrong for an entirely different,
// also-measured reason: domain_suspended fires the instant the LOCAL shell
// decides to hand off, before the child process has produced a single byte
// — a screen read then holds only the echoed command line, which the
// entry's own output-start bound (sessionruntime's outputMarkSkipLocked)
// correctly excludes, leaving an EMPTY closing screen every time
// (nocx-2v80t.3.21, measured against a real sshd).
//
// What IS common to every transport, local descriptor or ssh-forwarded
// listener alike, is lifecyclepub.Publisher.Ingest's own body: it calls
// publishLane, which derives the lane's fact and hands it to WHATEVER
// Emitter pub.SetEmitter was given, unconditionally, for every accepted
// envelope on every transport. That is the one seam this file hooks.

import (
	"context"
	"sync"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
)

// environmentEntryObserver is what a hosted pane's completion downlink
// offers this file: the one call that seals its runtime's currently open
// interval with no fence (helperclient.CompletionDownlink.
// ObserveEnvironmentEntry). Named narrowly, on purpose, so this package
// depends on the one method it uses rather than on the downlink's whole
// shape.
//
// It also carries the pane's accepted completions (nocx-2v80t.3.24): an ssh
// child authenticates on a listener of its own, so its completions never
// cross the pane's own observing kernel, and the lane this registry keys is
// how the child's listener finds the pane's downlink (childdomain.go's
// sshChildKernel). The registry is therefore the lane -> pane-runtime
// observer map, and entries are one of the two facts it carries.
type environmentEntryObserver interface {
	ObserveEnvironmentEntry(entry string)
	helperclient.CompletionObserver
}

// environmentEntryRegistry maps a lifecycle lane to the pane's own
// observer, registered once the lane is known — before the pane's shell can
// say anything on it (helper_hosted.go, helper_git.go,
// session_readopt_lifecycle.go) — and consulted by environmentEntryEmitter
// for every lane whose stack it watches. It is the ONE owner of per-lane
// state here: the observer and the depth the emitter last saw live in one
// entry, and the entry lives exactly as long as the pane (nocx-2v80t.3.32).
type environmentEntryRegistry struct {
	mu     sync.Mutex
	byLane map[lifecycle.LaneID]*laneEntry
}

// laneEntry is one registered lane: its pane's observer, and the stack depth
// environmentEntryEmitter last read for it (seen false until the first read).
type laneEntry struct {
	observer environmentEntryObserver
	depth    int
	seen     bool
}

func newEnvironmentEntryRegistry() *environmentEntryRegistry {
	return &environmentEntryRegistry{byLane: make(map[lifecycle.LaneID]*laneEntry)}
}

// register names the observer for a lane for as long as life lasts. life is
// the pane's own lifetime — the context its completion downlink delivers
// under, which every route ends when the pane ends and on every rollback —
// so the lane is forgotten with the pane rather than kept for the life of the
// process. A later registration of the same lane (a re-adoption) replaces
// this one, and this lifetime's end then leaves it alone. A blank lane or a
// nil observer is silently ignored: neither wiring ever wants to register a
// lane it could not otherwise reach at all — the same "nil wires nothing"
// shape blockRows and publishScreen already have.
func (r *environmentEntryRegistry) register(life context.Context, lane lifecycle.LaneID, o environmentEntryObserver) {
	if lane == "" || o == nil {
		return
	}
	e := &laneEntry{observer: o}
	r.mu.Lock()
	r.byLane[lane] = e
	r.mu.Unlock()
	context.AfterFunc(life, func() {
		r.mu.Lock()
		if r.byLane[lane] == e {
			delete(r.byLane, lane)
		}
		r.mu.Unlock()
	})
}

func (r *environmentEntryRegistry) lookup(lane lifecycle.LaneID) (environmentEntryObserver, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byLane[lane]
	if !ok {
		return nil, false
	}
	return e.observer, true
}

// observeDepth records the lane's stack depth and answers the observer to
// tell when the stack GREW past its first domain since the last read — the
// rule checkGrowth documents. A lane no pane holds records nothing: a fact
// that arrives after the pane is gone must not bring its lane back.
func (r *environmentEntryRegistry) observeDepth(lane lifecycle.LaneID, depth int) (environmentEntryObserver, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byLane[lane]
	if !ok {
		return nil, false
	}
	before, seen := e.depth, e.seen
	e.depth, e.seen = depth, true
	if !seen || depth < 2 || depth <= before {
		return nil, false
	}
	return e.observer, true
}

// laneStacker is the one lifecyclepub.Publisher method environmentEntryEmitter
// needs: the lane's current domain stack. A narrow interface rather than the
// whole Publisher, for the same reason environmentEntryObserver is narrow —
// and because the real Publisher's State is what the kernel's own
// LaneSnapshot.Stack already answers (protocol §9's own field), so this adds
// no derivation of its own.
type laneStacker interface {
	State(lane lifecycle.LaneID) (lifecycle.LaneSnapshot, error)
}

// environmentEntryEmitter decorates the app's real lifecyclepub.Emitter
// (*transport.WSServer) with one extra observation: a lane's domain stack
// GROWING. It forwards every other emitter interface unchanged — a decorator
// that dropped ProjectionEmitter or AttemptTransitionEmitter would silently
// break the Ctrl+C hold (ws_signal.go's PublishAttemptStarted/Closed) and the
// projection-only path the same Publisher already serves, both of which
// assert on whatever pub.SetEmitter was given.
type environmentEntryEmitter struct {
	inner    lifecyclepub.Emitter
	stacks   laneStacker
	registry *environmentEntryRegistry
}

// newEnvironmentEntryEmitter wraps inner — the real emitter every other
// lifecycle consumer already depends on — with the one new observation this
// file adds. stacks is the publisher itself; registry is filled in as panes
// spawn.
func newEnvironmentEntryEmitter(inner lifecyclepub.Emitter, stacks laneStacker, registry *environmentEntryRegistry) *environmentEntryEmitter {
	return &environmentEntryEmitter{inner: inner, stacks: stacks, registry: registry}
}

// PublishLifecycle is the Emitter method every transport's Ingest reaches
// unconditionally. checkGrowth runs first — before the real emitter, never
// after — so a panic or a slow subscriber downstream can never suppress the
// one fact this file exists to catch; forwarding is unconditional regardless
// of what checkGrowth found.
func (e *environmentEntryEmitter) PublishLifecycle(f lifecyclepub.Fact) {
	e.checkGrowth(lifecycle.LaneID(f.Lane))
	e.inner.PublishLifecycle(f)
}

// checkGrowth reads the lane's stack fresh — the fact just published was
// derived from exactly this state, so the two can never disagree — and
// compares it with what the SAME lane's stack was the last time this ran.
// Growth from a depth already seen at least once, that reaches 2 or more, is
// the one signal this file exists to raise: a lane's FIRST domain
// establishing is depth 0 -> 1 (integration itself, not an entry into
// anything), and a parent reclaiming the lane after a child closes is a
// SHRINK, never counted here at all.
func (e *environmentEntryEmitter) checkGrowth(lane lifecycle.LaneID) {
	snap, err := e.stacks.State(lane)
	if err != nil {
		return
	}
	depth := len(snap.Stack)
	// The depth is kept with the lane's registration, so it is forgotten with
	// the pane (environmentEntryRegistry.observeDepth).
	if o, grew := e.registry.observeDepth(lane, depth); grew {
		// The entry is named by the child domain on top of the stack — the
		// one whose establishment grew it. A domain id is minted once and
		// never recurs, so it tells a retried delivery of this entry from
		// the next entry (nocx-2v80t.3.28).
		o.ObserveEnvironmentEntry(string(snap.Stack[depth-1]))
	}
}

// PublishLifecycleProjection forwards to the real emitter's own
// ProjectionEmitter, if it has one — this file adds no projection of its
// own, so there is nothing to check here.
func (e *environmentEntryEmitter) PublishLifecycleProjection(f lifecyclepub.Fact) {
	if pe, ok := e.inner.(lifecyclepub.ProjectionEmitter); ok {
		pe.PublishLifecycleProjection(f)
	}
}

// PublishAttemptStarted forwards to the real emitter's own
// AttemptTransitionEmitter, unchanged.
func (e *environmentEntryEmitter) PublishAttemptStarted(id lifecycle.AttemptID) {
	if te, ok := e.inner.(lifecyclepub.AttemptTransitionEmitter); ok {
		te.PublishAttemptStarted(id)
	}
}

// PublishAttemptClosed forwards to the real emitter's own
// AttemptTransitionEmitter, unchanged.
func (e *environmentEntryEmitter) PublishAttemptClosed(id lifecycle.AttemptID) {
	if te, ok := e.inner.(lifecyclepub.AttemptTransitionEmitter); ok {
		te.PublishAttemptClosed(id)
	}
}
