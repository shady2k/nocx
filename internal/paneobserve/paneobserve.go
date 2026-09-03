// Package paneobserve turns an enrolled pane's grid into a state somebody can
// act on, and pushes it only when it changes.
//
// # Why this is not in panegrid and not in the driver
//
// panegrid answers what is on the screen, and its own comment says a verdict
// computed there would be a third power the AD-6 amendment does not grant.
// agentdriver answers what one frame means, holds no state between frames, and
// emits nothing. This package is the third thing neither may be: it remembers
// what a pane was last seen as, so that what crosses the wire is a CHANGE.
//
// # Only changes travel
//
// A renderer that receives an observation per sweep cannot tell a repaint from
// a state change, and an agent pane repaints continuously — the token counter
// alone moves on every response chunk. So the sweep classifies and compares,
// and says nothing when the answer is the one already sent.
//
// # The child rows are inside that rule, not an exception to it
//
// A pane's agent can spawn children, and its own chrome names them (nocx-o1v0h).
// Those rows carry an elapsed time and a token count that move on EVERY frame,
// so they were the first thing this rule could not have absorbed: keying the
// comparison on them emits eight times a second per pane, and keying on
// everything else while carrying them ships a clock that freezes at whatever
// it read when the set last moved — a stopped clock that looks live, which is
// worse than none.
//
// The resolution is upstream of the comparison rather than inside it. What
// crosses is only what is STABLE for the life of a row — which children exist,
// their names, and what each was given to do (agentdriver.Subagent) — so
// "emit when the answer changed" needs no exception, and the interval it
// produces is exact: a child row is on the wire from the first sweep in which
// the pane's chrome names it until the first sweep in which it does not. The
// measurement is still read, and still available to a caller looking at one
// frame; it simply does not cross a seam that only carries changes.
//
// Snapshot is the other half of that, and it exists for the same reason
// replayIntegration does in the transport: a state is not an event. A client
// that attaches after the last change must be able to ask what the pane is,
// because for a settled idle pane no further change is coming.
//
// # No timer is visible from a test
//
// Touch marks a pane dirty and is on the hot path of every session in the
// product, so it does nothing else. Sweep does the work. Production drives
// Sweep from a coalescing ticker at the composition root; a test drives it
// directly, and therefore asserts on a state change rather than on a duration.
package paneobserve

import (
	"sort"
	"sync"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
)

// Observation is what one pane was seen to be. It carries the agent because
// the receiver has to know whether an unknown means "this agent's driver could
// not tell" or "nocx has no driver for this agent at all".
type Observation struct {
	PaneID string
	Agent  string
	State  agentdriver.State
	// Children are the child agents this pane's agent has spawned, as its
	// own screen names them, in the order the screen drew them. Empty for
	// almost every pane, and empty is the ordinary answer rather than a
	// degraded one.
	//
	// They ride BESIDE the state and never through it. Their content cannot
	// reach the state at all — the driver decides the verdict from branches
	// no extractor is visible to — and the interval below is what keeps them
	// from deciding it here either.
	Children []agentdriver.Subagent
}

// sameChildren reports whether two child lists are the same reading. Ordered,
// because the panel's order is the lineage's order and a reordering is a
// different screen.
func sameChildren(a, b []agentdriver.Subagent) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Emit hands an observation on. It is called from the sweep, never from Touch.
type Emit func(Observation)

// Watcher observes the panes it has been told to watch. Nothing watches itself:
// the enrolment act names the pane and the agent, exactly as it does for the
// grid.
type Watcher struct {
	log     log.Logger
	grid    panegrid.Observer
	drivers *agentdriver.Registry
	emit    Emit

	mu    sync.Mutex
	panes map[string]*watched
}

type watched struct {
	agent string
	dirty bool
	// gone marks a pane whose agent has exited. The pane's SCREEN does not
	// stop moving when that happens — the shell is still there and still
	// repainting — so without this the next sweep would classify the
	// shell's own prompt and report an agent waiting for input.
	gone bool
	// seen is the last state EMITTED, and empty before the first sweep so
	// that a pane's first reading is always news.
	seen agentdriver.State
	// seenChildren is the last child list emitted, compared alongside seen.
	// It is a second field rather than part of a digest so that what is
	// compared is exactly what was sent — a digest is a third representation
	// of the answer, and a third representation is where the two come to
	// disagree.
	seenChildren []agentdriver.Subagent
}

// New returns a Watcher. It watches nothing until told, and reports nowhere
// until SetEmitter — the transport is built after the things that enrol into
// this, so the destination is bound afterwards, as it is for the lifecycle
// publisher's emitter.
func New(lg log.Logger, grid panegrid.Observer, drivers *agentdriver.Registry) *Watcher {
	return &Watcher{log: lg, grid: grid, drivers: drivers, panes: make(map[string]*watched)}
}

// SetEmitter binds where observations go. Until it is called a sweep does
// nothing AT ALL — it does not classify and does not record — so a pane's
// first state is still news once the destination exists, rather than having
// been swallowed by a sweep that had nowhere to put it.
func (w *Watcher) SetEmitter(emit Emit) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.emit = emit
}

// Watch opens the observation for a pane, and starts it dirty: the pane
// already has a screen by the time anybody says to watch it, and waiting for
// the next byte to report a state that is already true is how a settled pane
// stays invisible.
func (w *Watcher) Watch(paneID, agent string) {
	if paneID == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.panes[paneID] = &watched{agent: agent, dirty: true}
	w.log.Debug("pane observation opened", "pane_id", paneID, "agent", agent)
}

// Unwatch closes it, and forgets the last state with it. A pane watched again
// is a new incarnation, and comparing its first screen against what a previous
// one was would suppress the observation that says so.
func (w *Watcher) Unwatch(paneID string) {
	w.mu.Lock()
	_, ok := w.panes[paneID]
	delete(w.panes, paneID)
	w.mu.Unlock()
	if ok {
		w.log.Debug("pane observation closed", "pane_id", paneID)
	}
}

// Touch marks a pane as having moved. It is called for every chunk of every
// enrolled pane's output, so it takes one lock and does nothing else.
func (w *Watcher) Touch(paneID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p, ok := w.panes[paneID]; ok && !p.gone {
		p.dirty = true
	}
}

// Exited reports that a watched pane's AGENT is gone.
//
// It is the one state no driver may return, because it is a fact about the
// process rather than about the screen — reading it off the screen would mean
// believing an agent that printed the word. The enrolment act is what supplies
// it: an agent withdrawing is an agent finishing.
//
// The observation is terminal and RETAINED. Retained because a client that
// attaches afterwards must still be told what became of the pane; terminal
// because the shell underneath goes on drawing, and the next classification
// would be about the shell.
func (w *Watcher) Exited(paneID string) {
	w.mu.Lock()
	p, ok := w.panes[paneID]
	emit := w.emit
	if !ok || p.gone {
		// A pane nobody watches did not exit — it was never observed, and
		// saying otherwise would be a claim with no evidence behind it.
		w.mu.Unlock()
		return
	}
	p.gone = true
	p.dirty = false
	p.seen = agentdriver.StateExited
	// An agent that exited has no screen, so it names no children. Clearing
	// them is what keeps the retained observation from leaving the last rows
	// standing under a pane whose process is gone.
	p.seenChildren = nil
	agent := p.agent
	w.mu.Unlock()

	w.log.Debug("pane agent exited", "pane_id", paneID, "agent", agent)
	if emit == nil {
		return
	}
	emit(Observation{PaneID: paneID, Agent: agent, State: agentdriver.StateExited})
}

// Sweep classifies every pane that has moved since the last one, and emits the
// ones whose answer changed.
func (w *Watcher) Sweep() {
	w.mu.Lock()
	emit := w.emit
	w.mu.Unlock()
	if emit == nil {
		return
	}
	type job struct {
		paneID string
		agent  string
	}
	var jobs []job
	w.mu.Lock()
	for id, p := range w.panes {
		if p.dirty && !p.gone {
			jobs = append(jobs, job{paneID: id, agent: p.agent})
		}
	}
	w.mu.Unlock()

	for _, j := range jobs {
		f, err := w.grid.Frame(j.paneID)
		if err != nil {
			// The ordinary race: the session ended and the transport
			// withdrew the grid before whoever enrolled got to unwatch. The
			// pane is not observable, and inventing a state for it would be
			// the guess this whole path exists to refuse.
			w.clean(j.paneID)
			continue
		}
		o := w.drivers.Observe(j.agent, f)
		children := o.Subagents()
		if !w.commit(j.paneID, o.State, children) {
			continue
		}
		emit(Observation{PaneID: j.paneID, Agent: j.agent, State: o.State, Children: children})
	}
}

// commit clears the dirty flag and reports whether the observation is news.
// Both under one lock, so a Touch that lands mid-sweep is not lost.
//
// The state and the children are compared TOGETHER and stored together,
// because they are one answer about one screen: a pane whose verdict held
// while its children changed is news, and a pane whose children held while its
// verdict changed carries the same rows forward rather than dropping them.
func (w *Watcher) commit(paneID string, state agentdriver.State, children []agentdriver.Subagent) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.panes[paneID]
	if !ok || p.gone {
		// Unwatched, or the agent exited, while the frame was being read.
		return false
	}
	p.dirty = false
	if p.seen == state && sameChildren(p.seenChildren, children) {
		return false
	}
	p.seen = state
	p.seenChildren = children
	return true
}

func (w *Watcher) clean(paneID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p, ok := w.panes[paneID]; ok {
		p.dirty = false
	}
}

// Snapshot answers what a pane was last seen as, for a client that attached
// after the change that produced it. False for a pane nobody watches, and for
// one whose first sweep has not happened.
func (w *Watcher) Snapshot(paneID string) (Observation, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.panes[paneID]
	if !ok || p.seen == "" {
		return Observation{}, false
	}
	return Observation{PaneID: paneID, Agent: p.agent, State: p.seen, Children: p.seenChildren}, true
}

// Enrolled is one pane under observation: which pane, and which agent's rule
// governs it. Both halves come from the enrolment act and neither is inferred.
type Enrolled struct {
	PaneID string
	Agent  string
}

// Watching lists the panes under observation, in pane order.
//
// It answers from the enrolment act itself rather than from what a sweep has
// classified, so it is true from the instant Watch returns — which is what the
// emitting view (nocx-02uci) needs, because a settled screen may go a long
// time without a sweep having anything to say and a view that waited for one
// would show its operator nothing at all. Snapshot is the other question and
// stays the other question: that one answers what a pane WAS, and is silent
// until a first classification exists.
//
// A pane whose agent has EXITED is still listed. The observation is retained
// for exactly that reason, and dropping the pane here would take the last
// screen away from the person trying to work out what happened on it.
//
// Sorted, because this feeds a list a person reads and a map's order would
// reshuffle it under them on every poll.
func (w *Watcher) Watching() []Enrolled {
	w.mu.Lock()
	out := make([]Enrolled, 0, len(w.panes))
	for id, p := range w.panes {
		out = append(out, Enrolled{PaneID: id, Agent: p.agent})
	}
	w.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].PaneID < out[j].PaneID })
	return out
}
