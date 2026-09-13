// Package paneobserve turns a watched pane's screen into a state somebody can
// act on, and pushes it only when it changes.
//
// # Why this is not in the frame reader and not in the driver
//
// internal/paneview answers what is on the screen, and its own comment says a
// verdict computed there would be a third power the AD-6 amendment does not
// grant.
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
// # The third facet is a comparison, and a comparison needs a past
//
// Progress — has a WORKING pane moved lately — is the third thing an
// observation carries, and it is here rather than in the driver for the reason
// above plus one of its own. It is not a property of a frame: a frame is one
// instant, and "the transcript has stood still for a minute" is a statement
// about two instants. The rule CAN name the region (agentdriver's transcript
// extractor reads the rows the agent printed, stepping over the status stack
// whose spinner would otherwise move every second) and a frame can yield its
// text — but the comparison against the previous yield, and against a clock,
// belongs to the one component that already keeps what a pane was.
//
// So progress rides BESIDE the state and can never become one: nothing here
// may fold "stalled" into the closed set, and a pane that is working and
// stalled is still, in every listing and every comparison, a pane whose state
// is working. The threshold is a dependency of this watcher rather than a
// constant at the comparison, because the bead that asks for this facet says
// N is per-agent and belongs in settings; until that bead lands, New's
// WithStallAfter is the single seam it arrives through.
//
// # No timer is visible from a test
//
// Touch marks a pane dirty and is on the hot path of every session in the
// product, so it does nothing else. Sweep does the work. Production drives
// Sweep from a coalescing ticker at the composition root; a test drives it
// directly, and therefore asserts on a state change rather than on a duration.
// The clock is a dependency for the same reason: an interval with two ends has
// a low end as well as a high one, and a test that slept to reach either would
// be measuring the machine rather than the rule.
package paneobserve

import (
	"sort"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneview"
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
	// Progress is the THIRD facet: whether a pane that is working has moved
	// lately. It rides beside the state for the same reason the children do
	// and it decides nothing — a pane that is working and stalled is still,
	// in every listing and every comparison here, a pane whose state is
	// working.
	//
	// It is not a sixth member of agentdriver.State and must never become
	// one. "Stalled" is not what a screen is inviting; it is a comparison
	// between two readings of one taken over time, and the driver — which
	// holds no state between frames, deliberately — is structurally unable
	// to make it. See Watcher for where the comparison lives.
	//
	// ProgressMoving is the answer whenever the question cannot be asked:
	// a pane that is not working, and a pane whose rule reads no transcript.
	// A claim of stagnation is the expensive direction, and it is only made
	// on evidence.
	Progress Progress
}

// Progress is whether a working pane's transcript has moved recently.
//
// # Two values, and there is no third
//
// The coordinator's real question about a wave is not "who is working" but
// "has anything stalled", and a hung agent reports working forever because its
// spinner keeps spinning. So this is the comparison between what a pane WAS
// and what it is: two readings, taken over time, of the one region a rule
// named as the agent's own output.
//
// It answers for the WORKING states only, and that is a decision rather than
// an omission. A pane waiting on a human is not stalled — it is waiting, and
// the state already says so; a pane in the TUI's own error state is failing
// visibly, with retry chrome of its own, and folding it in here would give one
// condition two owners.
type Progress string

// ProgressMoving and ProgressStalled are the closed set. There is no Valid
// method beside them, unlike State's: a state crosses the wire as a string and
// is checked at the boundary it enters, while this facet is derived HERE and is
// what the schema's enum is checked against.
const (
	// ProgressMoving means the pane's transcript has changed within the
	// threshold, or that nothing can be said about it — a pane that is not
	// working, or one whose rule reads no transcript at all.
	ProgressMoving Progress = "moving"
	// ProgressStalled means the pane is working and its transcript has not
	// changed for longer than the threshold. The chrome around that
	// transcript may be animating the whole time; that is the case this
	// exists to see through.
	ProgressStalled Progress = "stalled"
)

// sameTranscript reports whether two transcript readings are the same yield,
// in the same order. Ordered because the region reads the screen in one
// direction and a reordering is a different screen.
func sameTranscript(a, b []string) bool {
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

// DefaultStallAfter is how long a working pane's transcript may stand still
// before its progress reads stalled.
//
// It is a named default rather than a number at the comparison, because the
// bead that asks for this facet also says N is per-agent and belongs in
// settings (nocx-y6w66) — and that bead has not started, so this is the one
// place a value lives until it has an owner. The seam the setting writes into
// is Config.StallAfter, and nothing else here reads the threshold.
//
// A minute is chosen for what a coordinator does with the answer: a live
// transcript in the committed corpus moves within seconds, and a pane that has
// said nothing at all for a minute is worth a look while there is still time
// to do something about it. A false stalled is the expensive direction, so the
// number is generous rather than tight, and every state that cannot support
// the claim answers moving instead.
const DefaultStallAfter = 60 * time.Second

// Config is what a watcher cannot decide for itself: the clock it reads and the
// threshold it compares a transcript against.
//
// The ZERO VALUE IS PRODUCTION — time.Now, and DefaultStallAfter — so the
// composition root may say nothing at all, and a test says exactly the two
// things it means to vary. It is a struct rather than a list of options
// because both halves are dependencies and neither is a decoration: the clock
// is varied by a test that must state an interval instead of waiting for one,
// and the threshold is where the per-agent setting the bead calls for
// (nocx-y6w66) will arrive.
type Config struct {
	// Now is the clock this watcher reads. Nil means time.Now.
	Now func() time.Time
	// StallAfter is how long a working pane's transcript may stand still
	// before its progress reads stalled. Zero or less means
	// DefaultStallAfter — a threshold of zero would report every working
	// pane stalled on its first reading.
	StallAfter time.Duration
}

// Emit hands an observation on. It is called from the sweep, never from Touch.
type Emit func(Observation)

// Screens is the seam onto a pane's screen (AD-8). One method, because the
// observer may READ a frame and may not enrol, withdraw, classify or type: the
// interval is the store's and the verdict is a caller's.
type Screens interface {
	Frame(paneID string) (paneview.Frame, error)
}

// Watcher observes the panes it has been told to watch. Nothing watches itself:
// the enrolment act names the pane and the agent, and the store that answers
// the act is what holds the interval a read needs.
type Watcher struct {
	log     log.Logger
	screens Screens
	drivers *agentdriver.Registry
	emit    Emit
	// now is the clock this watcher reads, and stallAfter is the threshold it
	// compares against. Both are fields rather than package-level state so
	// that a test can state an interval instead of waiting for one.
	now        func() time.Time
	stallAfter time.Duration

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
	// seenProgress is the last PROGRESS emitted, compared alongside the other
	// two. A pane whose verdict held while its progress changed is news, and
	// it is the only way the transition this facet exists for — working and
	// moving becoming working and stalled — ever reaches a renderer.
	seenProgress Progress
	// transcript is the last transcript yield OBSERVED, which is not the
	// same thing as the last one emitted: the yield moves on almost every
	// frame of a live turn while the progress it implies stays moving, and
	// the bookkeeping below has to follow the yield rather than the
	// emission. Nil means the rule read no transcript, which is not a
	// measurement at all.
	transcript []string
	// transcriptAt is when that yield last CHANGED. A zero time means the
	// pane has never been measured, and a pane that has never been measured
	// can never be called stalled.
	transcriptAt time.Time
}

// New returns a Watcher. It watches nothing until told, and reports nowhere
// until SetEmitter — the transport is built after the things that enrol into
// this, so the destination is bound afterwards, as it is for the lifecycle
// publisher's emitter.
//
// A zero Config is production: the wall clock, and DefaultStallAfter. A test
// passes the clock it advances and the interval it is asserting about, because
// the threshold has two ends and a test that waited for either would be
// measuring the machine rather than the rule.
func New(lg log.Logger, screens Screens, drivers *agentdriver.Registry, cfg Config) *Watcher {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.StallAfter <= 0 {
		cfg.StallAfter = DefaultStallAfter
	}
	return &Watcher{
		log:        lg,
		screens:    screens,
		drivers:    drivers,
		now:        cfg.Now,
		stallAfter: cfg.StallAfter,
		panes:      make(map[string]*watched),
	}
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
	// For the same reason it has no transcript either, and nothing about a
	// process that has ended is "working but not moving": the facet falls
	// back to the answer it gives whenever the question cannot be asked.
	p.seenProgress = ProgressMoving
	p.transcript = nil
	p.transcriptAt = time.Time{}
	agent := p.agent
	w.mu.Unlock()

	w.log.Debug("pane agent exited", "pane_id", paneID, "agent", agent)
	if emit == nil {
		return
	}
	emit(Observation{PaneID: paneID, Agent: agent, State: agentdriver.StateExited, Progress: ProgressMoving})
}

// Sweep classifies every pane that has moved since the last one — and every
// working pane whose stall deadline has passed — and emits the ones whose
// answer changed.
//
// # Why the deadline is in the work set
//
// The dirty flag is set by the session's READ path, so it says a pane emitted
// bytes. Progress is not a question about bytes: a suspended or wedged agent
// paints nothing at all, and a sweep that only looked at panes that had moved
// would never look at it again — the one pane whose stall is the whole point
// of the facet would be the one pane nobody could report. So a pane already
// working, already measured, and already past the threshold is swept whether it
// moved or not. It stops qualifying the moment its progress is emitted as
// stalled, which is why this costs one classification per stalled pane rather
// than a poll per pane per tick.
func (w *Watcher) Sweep() {
	w.mu.Lock()
	emit := w.emit
	if emit == nil {
		w.mu.Unlock()
		return
	}
	now := w.now()
	type job struct {
		paneID string
		agent  string
	}
	var jobs []job
	for id, p := range w.panes {
		if p.gone {
			continue
		}
		if p.dirty || overdue(p, w.stallAfter, now) {
			jobs = append(jobs, job{paneID: id, agent: p.agent})
		}
	}
	w.mu.Unlock()

	for _, j := range jobs {
		f, err := w.screens.Frame(j.paneID)
		if err != nil {
			// The ordinary race: the session ended and the transport
			// unwatched the pane before whoever enrolled got to withdraw it,
			// or the helper that holds it stopped answering. The pane is not
			// observable, and inventing a state for it would be the guess
			// this whole path exists to refuse.
			w.clean(j.paneID)
			continue
		}
		o := w.drivers.Observe(j.agent, f)
		progress, news := w.commit(j.paneID, o.State, o.Subagents(), o.Transcript(), now)
		if !news {
			continue
		}
		emit(Observation{
			PaneID:   j.paneID,
			Agent:    j.agent,
			State:    o.State,
			Children: o.Subagents(),
			Progress: progress,
		})
	}
}

// overdue reports whether a working pane's transcript has stood still past the
// threshold, so that a pane sending nothing at all is still looked at. A pane
// with no transcript measurement never qualifies: a stall is a claim, and this
// is the condition under which one could be made — see Sweep.
func overdue(p *watched, after time.Duration, now time.Time) bool {
	return p.seen.Working() &&
		p.seenProgress != ProgressStalled &&
		!p.transcriptAt.IsZero() &&
		now.Sub(p.transcriptAt) > after
}

// commit records the transcript yield this sweep read, clears the dirty flag,
// and reports the pane's progress together with whether the observation is
// news.
//
// All of it under one lock, so a Touch that lands mid-sweep is not lost.
//
// The state, the children and the progress are compared TOGETHER and stored
// together, because they are one answer about one screen: a pane whose verdict
// held while its children changed is news, a pane whose children held while its
// verdict changed carries the same rows forward rather than dropping them, and
// a pane whose VERDICT held while its PROGRESS changed is news too — that one
// transition is the whole reason the facet exists.
//
// The transcript yield is bookkeeping rather than an emitted value, and the
// two are deliberately different fields: a live turn's transcript changes on
// almost every frame while its progress stays moving, so what is compared is
// what was sent and what is TIMED is what was read.
func (w *Watcher) commit(paneID string, state agentdriver.State, children []agentdriver.Subagent, transcript []string, now time.Time) (Progress, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.panes[paneID]
	if !ok || p.gone {
		// Unwatched, or the agent exited, while the frame was being read.
		return ProgressMoving, false
	}
	p.dirty = false
	moved := !sameTranscript(p.transcript, transcript)
	if moved {
		p.transcript = transcript
		p.transcriptAt = now
	}
	progress := progressOf(p, state, moved, w.stallAfter, now)
	if p.seen == state && sameChildren(p.seenChildren, children) && p.seenProgress == progress {
		return progress, false
	}
	p.seen = state
	p.seenChildren = children
	p.seenProgress = progress
	return progress, true
}

// progressOf answers the progress a pane is in, given what this reading found.
//
// It is a function of the reading and the pane's own record rather than a
// method that decides anything, so that the LIVE read (Classify) and the
// cached one (Sweep, and the Snapshot that reports it) cannot answer the same
// question differently.
func progressOf(p *watched, state agentdriver.State, moved bool, after time.Duration, now time.Time) Progress {
	switch {
	case !state.Working():
		// Only a working pane can be stalled: a pane waiting on a human is
		// waiting, and the state already says so.
		return ProgressMoving
	case moved:
		// The transcript moved under this very reading, so the interval
		// starts here whatever it was before.
		return ProgressMoving
	case p.transcriptAt.IsZero():
		// Nothing has ever been measured on this pane — no rule with a
		// transcript extractor, or a screen the region could not reach. An
		// unmeasurable pane is not a stalled one.
		return ProgressMoving
	case now.Sub(p.transcriptAt) > after:
		return ProgressStalled
	}
	return ProgressMoving
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
	return Observation{
		PaneID:   paneID,
		Agent:    p.agent,
		State:    p.seen,
		Children: p.seenChildren,
		Progress: p.seenProgress,
	}, true
}

// Classify reads paneID's CURRENT frame and answers what it is classified as
// RIGHT NOW — the live reading Sweep would produce if it ran this instant,
// rather than the cache Snapshot answers from. It touches none of dirty,
// seen, seenChildren, seenProgress or the transcript record, and it emits
// nothing: it is a READ, not a second sweep, and must never make a later real
// Sweep believe this pane was already reported.
//
// THE PROGRESS IT ANSWERS IS DERIVED AND NOT RECORDED, exactly as the observer
// rules require of a read. It is computed from the record the last sweep left
// — the yield it last saw and when that yield last changed — compared against
// the yield of the frame read here. A yield that differs moved under this very
// reading, and the answer is moving because a pane whose transcript just
// changed has not stalled; the timestamp the next real sweep writes is what
// gives that comparison an interval.
//
// It exists for exactly one caller (nocx-f545a.7, the race a review of
// 1ffd3a56 found): a wait that has just written a confirm key into a pane
// and needs to know what the screen shows now, not what a coalescer last
// swept it as. Touch is called only on the session's OWN READ side (this
// package's own doc) — nothing about writing into a pane touches it — so
// immediately after such a write, Snapshot's cache is, with certainty,
// still the reading from before the write. A caller that cannot afford that
// staleness asks here instead; internal/app's classifyingReadiness is the
// adapter that lets it reuse the same wait Snapshot's callers use.
//
// EVERY ORDINARY READER KEEPS USING SNAPSHOT. A settled pane's cache is
// exactly as current as this call would be, at a fraction of the cost, and
// re-running the driver on every read is precisely what this package's own
// "only changes travel" design exists to avoid — see the package doc.
//
// Same two absence answers as Snapshot, so a caller cannot tell which of the
// two methods produced a false or an exited reading from its shape alone:
// false for a pane nobody watches, and StateExited — with no read of the
// screen at all — for one whose agent has already gone. A frame the store
// cannot currently produce (the ordinary race: the session ended and the
// pane was unwatched a moment ago) is answered as an absent reading too,
// which is stricter than Sweep's own handling of the same race — Sweep
// clears the pane's dirty flag when this happens because it owns that
// bookkeeping; Classify owns none of it and leaves the pane exactly as it
// found it. A pane unwatched while its frame was being read is the same
// absence, answered the same way: there is no record left to read a progress
// against, and inventing one would be the guess this path refuses.
func (w *Watcher) Classify(paneID string) (Observation, bool) {
	w.mu.Lock()
	p, ok := w.panes[paneID]
	if !ok {
		w.mu.Unlock()
		return Observation{}, false
	}
	if p.gone {
		agent := p.agent
		w.mu.Unlock()
		return Observation{PaneID: paneID, Agent: agent, State: agentdriver.StateExited, Progress: ProgressMoving}, true
	}
	agent := p.agent
	lastTranscript := p.transcript
	w.mu.Unlock()

	f, err := w.screens.Frame(paneID)
	if err != nil {
		return Observation{}, false
	}
	o := w.drivers.Observe(agent, f)
	transcript := o.Transcript()

	w.mu.Lock()
	cur, still := w.panes[paneID]
	if !still || cur.gone {
		w.mu.Unlock()
		return Observation{}, false
	}
	progress := progressOf(cur, o.State, !sameTranscript(lastTranscript, transcript), w.stallAfter, w.now())
	w.mu.Unlock()
	return Observation{PaneID: paneID, Agent: agent, State: o.State, Children: o.Subagents(), Progress: progress}, true
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
