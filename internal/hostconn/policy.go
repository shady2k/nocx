package hostconn

// The connection's prober and the ONE closing policy (design §5.3, "Silence
// and closing" and "On a wake signal").
//
// The rule the whole file exists for: SILENCE ENDS NOTHING BY ITSELF. A probe
// that does not come back is evidence, and the evidence is shown to a person.
// What ends a connection is either the transport saying so (a reset, an end of
// file — a fact, not an inference) or this policy closing a connection on which
// nothing would be lost by closing it. A connection carrying a plain shell is
// never closed for silence: the person chooses to wait or to reconnect, and
// waiting costs a click while a wrong close costs the work.
//
// A WAKE is the same policy with a shorter clock: the machine was asleep, every
// connection was frozen, and the one thing worth knowing is whether anything
// survived. So each live connection is asked to answer at once (unless a probe
// is already parked — a second would queue behind the mux's send lock), and an
// answer that does not come reads stalled five seconds after the signal (Q4).
// Thirty seconds without one ends a helper-owned connection: a socket that
// crossed a sleep is usually dead. A plain-shell connection follows the silence
// rule and asks.
//
// Every moment here is the injected clock's (clock.go). The policy is therefore
// assertable in seconds of simulated time, and the fault harness states Q3, Q4,
// Q5 and the ten-minute case without waiting for any of them.

import (
	"context"
	"time"

	"github.com/shady2k/nocx/internal/monoclock"
)

const (
	// ProbeInterval is how often a live connection is asked to answer. Half
	// the old keepalive's 30 s: the old default could not meet Q3.
	ProbeInterval = 15 * time.Second
	// ProbeDeadline is how long one probe may take before the connection reads
	// stalled — while the call itself stays parked. Silence is thus reported
	// within 30 s of a cut (Q3), and the parked probe is what notices the far
	// side the moment it answers.
	ProbeDeadline = 15 * time.Second
	// LookInterval is how often, while stalled, a look-only dial is made: TCP
	// and the SSH version exchange on a new connection, no key exchange and no
	// sign-in. The answer is evidence about the HOST, never a verdict on the
	// flow.
	LookInterval = 15 * time.Second
	// LookGrace is how long a helper-owned connection may stay silent after the
	// host has been shown to answer new connections before the policy closes
	// it. Reattaching such a session loses nothing (design §5.3).
	LookGrace = 30 * time.Second
	// SilenceCloseBudget is the outer bound for a helper-owned connection that
	// stays silent: five minutes, from the owner's "three minutes and then it
	// connected". A plain-shell connection has no such budget.
	SilenceCloseBudget = 5 * time.Minute
	// WakeProbeDeadline is how long a connection may take to answer the probe a
	// WAKE sent before it reads stalled: Q4's "within 5 s of the wake signal".
	WakeProbeDeadline = 5 * time.Second
	// WakeCloseAfter is how long a helper-owned connection may stay unanswered
	// after a wake before the policy ends it: a socket that crossed a sleep is
	// usually dead, and reattaching loses nothing.
	WakeCloseAfter = 30 * time.Second
)

// guardState is one connection's prober state: what is in flight, what is due,
// and whether the connection is proving itself after a wake.
type guardState struct {
	// probeSent says a probe is outstanding, and probeSeq is its number in the
	// connection's answer record (live.beginProbe). There is at most one: a
	// parked SendRequest holds the mux's send lock, so a second probe would
	// queue behind it and measure nothing.
	probeSent bool
	probeSeq  uint64
	// probedAt is when the outstanding probe was SENT, and silentSince is the
	// moment silence began (the send of the probe that did not come back). Zero
	// silentSince means the connection is not stalled.
	probedAt    monoclock.Nanos
	silentSince monoclock.Nanos
	// silentSeen is the clock reading at which this guard FIRST judged the
	// connection silent, and the close budget is spent from it — never from
	// probedAt. A clock that jumps puts the guard at the deadline and at the
	// end of a five-minute budget in the same pass, and no close may rest on
	// one reading taken after a jump.
	silentSeen monoclock.Nanos
	nextProbe  monoclock.Nanos
	// lookOut is non-nil while a look-only dial is in flight.
	lookOut      chan error
	nextLook     monoclock.Nanos
	lookSent     monoclock.Nanos
	lookAnswered bool
	lookAt       monoclock.Nanos
	nudged       bool
	// wokeAt is the moment of the last wake signal while this connection has
	// not answered since; zero once it has. It shortens the probe's deadline
	// and bounds the close.
	wokeAt monoclock.Nanos
	// suspendedAt is the moment the OS said the machine is going to sleep,
	// while no wake has been seen since. The guard asks nothing of a connection
	// on a sleeping machine: the process is frozen, and a probe sent into the
	// sleep would park and answer first thing on resume.
	suspendedAt monoclock.Nanos
	// lastEdge is the clock reading of the newest OS control edge this guard
	// has applied. Edges arrive from one stream but two goroutines' sends can
	// still cross in a select; an edge older than the one in hand is stale and
	// is ignored, so a wake can never be undone by the sleep that preceded it.
	lastEdge monoclock.Nanos
}

// prober drives one live connection's probe cadence and closing policy.
type prober struct {
	sup    *Supervisor
	live   *live
	gen    uint64
	clock  Clock
	prober Prober
	looker Looker
	st     guardState
}

// guard starts the connection's prober. It runs for as long as the connection
// is the supervisor's live one.
func (s *Supervisor) guard(l *live, gen uint64) {
	probe, ok := l.link.(Prober)
	if !ok {
		// A carrier that cannot ask the far side a question never reads
		// stalled: it stays honest about what it cannot measure
		// (design §5.1.2), and no policy may invent evidence it does not have.
		return
	}
	looker, _ := l.link.(Looker)
	start := guardState{nextProbe: l.nextProbe, wokeAt: l.initialWoke}
	if l.initialSleep != 0 {
		// A sleep with no later wake is the published connection's first fact.
		start.suspendedAt = l.initialSleep
		start.lastEdge = l.initialSleep
		l.suspended.Store(true)
	}
	g := &prober{
		sup: s, live: l, gen: gen, clock: s.clock, prober: probe, looker: looker,
		st: start,
	}
	g.run()
}

// run is the loop. A due move acts at the moment it was DUE rather than at the
// moment the loop noticed it: a test that advances thirty seconds at once must
// see the probe sent at fifteen and its deadline missed at thirty, exactly as a
// machine that had been left alone would have.
func (g *prober) run() {
	for {
		// A CONTROL EDGE IN HAND WINS THE NEXT MOVE, for the same reason an
		// answer does: "the machine is going to sleep" is a fact the guard has
		// already been told, and a due probe or a due deadline must not act as
		// if it were not. The senders are non-blocking, so at most one of each
		// is pending.
		g.drainControl()
		now := g.clock.Now()
		wait := g.next(now)
		if now >= wait {
			// AN ANSWER ALREADY IN HAND WINS THE BOUNDARY. A probe whose
			// result is waiting when its deadline is due answered within the
			// deadline; without this the select below could take the timer
			// first and read silence into an answer the loop had already been
			// given (measured: a connection that answers in microseconds
			// reading stalled because the clock crossed the deadline in the
			// same instant).
			if g.drainReady() {
				return
			}
			if g.due(wait) {
				return
			}
			continue
		}
		select {
		case <-g.live.stop:
			return
		case <-g.live.ask:
			// A caller needs the far side's own ordering (the pool's release
			// barrier): ask now, or let the probe already parked be the
			// evidence. Timed as the wake mk its due moment so the loop acts
			// at once.
			g.st.nextProbe = g.clock.Now()
		case <-g.live.controlWake:
			// The edges themselves are read by drainControl at the top of the
			// loop, coalesced per kind.
		case <-g.live.ready:
			// The far side's call returned and the answer was recorded under
			// the connection's answer lock; the record is the delivery.
			if g.consumeAnswer() {
				return
			}
		case err := <-g.st.lookOut:
			g.lookResult(err)
		case <-g.clock.After(time.Duration(wait - now)):
		}
	}
}

// drainControl applies every sleep or wake edge already in hand, in the order
// they were queued and with the timestamp check that makes an out-of-order
// arrival harmless.
func (g *prober) drainControl() {
	for _, edge := range g.live.takeEdges() {
		g.applyControl(edge)
	}
}

// applyControl applies one OS edge, refusing one older than the edge already
// applied: a wake followed by a stale sleep must not suspend a resumed
// connection.
func (g *prober) applyControl(edge controlEdge) {
	if edge.at != 0 && g.st.lastEdge != 0 && edge.at < g.st.lastEdge {
		return
	}
	if edge.at != 0 {
		g.st.lastEdge = edge.at
	}
	if edge.sleep {
		g.suspend(edge.at)
		return
	}
	g.woke(edge.at)
}

// drainReady applies any probe or look result that has already arrived, and
// reports whether the guard should end. It is checked before a due move, so a
// result that is in hand is never turned into silence by a deadline the loop
// reached in the same instant.
func (g *prober) drainReady() bool {
	if g.st.probeSent {
		if g.consumeAnswer() {
			return true
		}
	}
	if g.st.lookOut != nil {
		select {
		case err := <-g.st.lookOut:
			g.lookResult(err)
		default:
		}
	}
	return false
}

// next answers when the policy next has something to do.
func (g *prober) next(now monoclock.Nanos) monoclock.Nanos {
	st := &g.st
	if st.suspendedAt != 0 {
		// The machine is going to sleep. No probe is started and no silence is
		// judged until a wake arrives; a result that is already in flight still
		// arrives through the select.
		return now + span(SilenceCloseBudget)
	}
	switch {
	case st.probeSent && st.silentSince == 0:
		if st.wokeAt != 0 {
			return st.wokeAt + span(WakeProbeDeadline)
		}
		return st.probedAt + span(g.live.deadline())
	case st.silentSince != 0:
		return g.nextStalled(now)
	}
	return st.nextProbe
}

// nextStalled answers when the policy next has something to do on a connection
// that has stopped answering.
func (g *prober) nextStalled(now monoclock.Nanos) monoclock.Nanos {
	st := &g.st
	if g.sup.HoldsPlainShell() {
		// No close is due on a connection carrying a plain shell: that is the
		// person's choice. The moves that remain are a look that has not been
		// made and the probe's own answer (its channel is a select case) —
		// waiting on a budget that may never be spent would only keep the loop
		// awake.
		if !st.lookAnswered && st.lookOut == nil {
			return st.nextLook
		}
		return now + span(SilenceCloseBudget)
	}
	next := st.silentSince + span(SilenceCloseBudget)
	if st.wokeAt != 0 {
		if due := st.wokeAt + span(WakeCloseAfter); due < next {
			next = due
		}
	}
	if !st.lookAnswered && st.lookOut == nil && st.nextLook < next {
		next = st.nextLook
	}
	if grace := st.lookAt + span(LookGrace); st.lookAnswered && grace < next {
		next = grace
	}
	return next
}

// due performs the one move that was due at `at`, and reports whether the guard
// should end.
func (g *prober) due(at monoclock.Nanos) bool {
	st := &g.st
	if st.suspendedAt != 0 {
		return false
	}
	now := g.clock.Now()
	switch {
	case !st.probeSent && st.silentSince == 0:
		g.startProbe(at)
		return false
	case st.silentSince == 0:
		// ONLY A PROBE THE FAR SIDE HAS NOT ANSWERED IS SILENCE (design §5.3).
		// The answer is recorded by the goroutine that received it, under the
		// lock this read takes, so "outstanding" is an ORDERING fact and never
		// a race against a clock: if the call has returned, the answer is here
		// and the deadline is not a verdict.
		if outcome, answered := g.live.takeAnswer(st.probeSeq); answered {
			return g.probeResult(outcome)
		}
		// The call has not returned. Silence is now the state — and it is a
		// REPORT: it closes nothing by itself, and the recorded answer clears
		// it the moment it exists.
		st.silentSince = st.probedAt
		st.silentSeen = now
		st.nextLook = st.probedAt + span(LookInterval)
		// The moment the deadline expired, not the moment the guard got here:
		// a state that claimed more silence than the deadline would be a
		// measurement of the scheduler rather than of the host.
		g.sup.silent(g.live, g.gen, time.Duration(at-st.probedAt))
		g.sup.observe(g.live, Observation{Answered: false})
		return false
	}

	// Stalled. A look, if one is due and none is in flight.
	if st.lookOut == nil && !st.lookAnswered && now >= st.nextLook {
		g.startLook(at)
		return false
	}

	// A helper-owned connection closes on the evidence: thirty seconds after a
	// wake without an answer, thirty after the host answered a look while the
	// flow stayed silent, or at the outer budget.
	if g.sup.HoldsPlainShell() {
		return false
	}
	if st.wokeAt != 0 && now >= st.wokeAt+span(WakeCloseAfter) {
		return g.sup.endForSilence(g.live, g.gen, "did not answer after the wake")
	}
	if st.lookAnswered && now >= st.lookAt+span(LookGrace) {
		return g.sup.endForSilence(g.live, g.gen, "the host answers new connections and this one has been silent past the grace")
	}
	// THE BUDGET IS SPENT FROM THE JUDGEMENT, NOT FROM THE SEND (nocx-oalo1.4.3).
	// A clock that jumps puts the guard at the deadline and at the end of this
	// budget in the same pass, and a close taken there would rest on one
	// reading: the answer may be recorded a microsecond later. Spending the
	// budget from the moment this guard FIRST judged the connection silent
	// makes the close need a later pass — one in which a recorded answer is
	// read and clears the stall, which is what a healthy link always has.
	if st.silentSeen != 0 && now >= st.silentSeen+span(SilenceCloseBudget) {
		return g.sup.endForSilence(g.live, g.gen, "silent past the close budget")
	}
	return false
}

// consumeAnswer applies the recorded answer to the probe the guard is waiting
// for, if it has arrived, and reports whether the guard should end.
//
// It is the ONE way an answer becomes an outcome. The goroutine that receives
// the far side's call records it on the connection (live.recordAnswer) and
// pokes live.ready; nothing is delivered through a channel of its own, so there
// is no second copy of an answer that a deadline could overtake.
func (g *prober) consumeAnswer() bool {
	if !g.st.probeSent {
		return false
	}
	outcome, answered := g.live.takeAnswer(g.st.probeSeq)
	if !answered {
		return false
	}
	return g.probeResult(outcome)
}

// probeOutcome is one probe's answer: how long the far side took, measured by
// the carrier over the wire, and its error if it did not come back.
type probeOutcome struct {
	roundTrip time.Duration
	err       error
}

// startProbe asks the connection to answer, as of `at`.
func (g *prober) startProbe(at monoclock.Nanos) {
	seq := g.live.beginProbe()
	g.st.probeSeq = seq
	g.st.probeSent = true
	g.st.probedAt = at
	g.st.nextProbe = at + span(g.live.interval())
	go func() {
		// Probe takes no context: a call that does not come back stays parked
		// inside the library, holding the mux's send lock. That is the design's
		// own state (design §5.3: "the parked probe"), and it is why the
		// deadline is the supervisor's and not the call's.
		roundTrip, err := g.prober.Probe(context.Background())
		// THE ANSWER IS RECORDED BEFORE ANYTHING ELSE, under the lock the
		// deadline is read under: from here on, "the far side has answered" is
		// an ordering fact and no clock can call it silence.
		g.live.recordAnswer(seq, roundTrip, err)
	}()
}

// startLook makes one look-only dial, as of `at`.
func (g *prober) startLook(at monoclock.Nanos) {
	out := make(chan error, 1)
	g.st.lookOut = out
	g.st.lookSent = at
	g.st.nextLook = at + span(LookInterval)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), ProbeDeadline)
		defer cancel()
		out <- g.looker.Look(ctx)
	}()
}

// suspend marks the connection as going to sleep at `at`. Whatever probe was
// already outstanding stays — it is the evidence that will answer on resume —
// and nothing new is asked.
func (g *prober) suspend(at monoclock.Nanos) {
	g.st.suspendedAt = at
	g.live.suspended.Store(true)
}

// woke starts the connection's proof of life after a wake signal seen at `at`.
//
// The moment is the signal's, not this loop's: a fan-out that ran a moment late
// must still measure the five seconds from the signal, or a slow scheduler
// would buy the connection time it has not earned.
func (g *prober) woke(at monoclock.Nanos) {
	g.st.suspendedAt = 0
	g.live.suspended.Store(false)
	g.st.wokeAt = at
	if !g.st.probeSent {
		// The probe a wake sends is due at the signal: the loop's next pass
		// sees nextProbe == at and starts it there.
		g.st.nextProbe = at
	}
	// With a probe already outstanding nothing is sent: it is the evidence, and
	// a second would queue behind the mux's send lock. Its deadline becomes the
	// wake's, because an answer is what the wake asked for.
}

// probeResult applies one probe's answer or failure; it reports whether the
// guard should end.
func (g *prober) probeResult(outcome probeOutcome) bool {
	g.st.probeSent = false
	// THE BARRIER'S WAITERS ARE PART OF THIS ANSWER (review finding 1): the
	// pool's release barrier asked this connection to produce one, and the
	// same outcome is what it needs — no second keepalive, no second closer.
	g.sup.mu.Lock()
	g.sup.notifyWaiters(g.live, outcome.err)
	g.sup.mu.Unlock()
	if outcome.err != nil {
		// The transport has ended under the probe. The watcher on Done reports
		// it, and it is a fact rather than an inference (design §5.3: "A
		// transport error closes at once").
		return true
	}
	g.sup.answered(g.live, g.gen)
	// The round trip is the CARRIER's measurement, over the wire: the clock the
	// supervisor is handed may be the test's, and it certainly cannot see the
	// packet.
	g.sup.observe(g.live, Observation{Answered: true, RoundTrip: outcome.roundTrip})
	// The cadence is measured from the probe's SEND, not from the moment this
	// loop got to the answer: a host that answers late must not also be asked
	// late.
	g.st.nextProbe = g.st.probedAt + span(g.live.interval())
	// AN ANSWER DOES NOT CLEAR A LATER SLEEP: a probe sent before the sleep
	// edge answered about a machine that was already going away. The guard
	// asks nothing more until the wake.
	if g.st.suspendedAt == 0 || g.st.probedAt >= g.st.suspendedAt {
		g.st.suspendedAt = 0
		g.live.suspended.Store(false)
	}
	g.st.silentSince, g.st.silentSeen, g.st.lookAnswered, g.st.nudged = 0, 0, false, false
	g.st.lookOut, g.st.lookAt, g.st.lookSent, g.st.wokeAt = nil, 0, 0, 0
	return false
}

// lookResult applies one look-only dial's answer.
func (g *prober) lookResult(err error) {
	g.st.lookOut = nil
	if err != nil {
		// The host did not answer a new connection: the evidence is that it is
		// away, and the connection stays as it is.
		g.st.nextLook = g.st.lookSent + span(LookInterval)
		return
	}
	if g.st.lookAnswered {
		return
	}
	g.st.lookAnswered, g.st.lookAt = true, g.clock.Now()
	if g.st.nudged {
		return
	}
	g.st.nudged = true
	// ONE nudge, only after a look: a request with no reply wanted passes a
	// parked probe, and a host that rebooted answers it with a reset — a
	// transport error, and an unaided reconnect (Q5). A host that still holds
	// the flow acknowledges it and nothing changes, so it can end no live
	// shell.
	// OFF THE LOOP: a nudge is a transport write, and a write can wait behind
	// a rekey or a blocked peer. The guard's budgets, wakes and stop must not
	// wait with it; the watcher on Done reports a transport that the write
	// discovers is over.
	go func() {
		if err := g.prober.Nudge(); err != nil {
			g.sup.log.Debug("hostconn: the post-look nudge failed", "connection", g.sup.key.String(), "error", err)
		}
	}()
}

// silent reports the connection stalled, with how long it has gone unanswered.
func (s *Supervisor) silent(l *live, gen uint64, silent time.Duration) {
	s.mu.Lock()
	if s.live == l && s.generation == gen {
		s.advance(Event{Kind: Silent, Silent: silent})
	}
	s.mu.Unlock()
}

// answered reports that a parked probe finally came back.
func (s *Supervisor) answered(l *live, gen uint64) {
	s.mu.Lock()
	if s.live == l && s.generation == gen {
		s.advance(Event{Kind: Answered})
	}
	s.mu.Unlock()
}

// endForSilence closes a connection the policy may close, and records why. The
// state is `reconnecting` — the transport is whole but the connection is over,
// and the next acquire dials afresh.
//
// THE PLAIN-SHELL RECHECK IS UNDER THIS LOCK, with the close decision: a lease
// that declares a shell between the policy's look and this call must not be
// closed for silence, and only the same critical section can promise that.
func (s *Supervisor) endForSilence(l *live, gen uint64, why string) bool {
	s.mu.Lock()
	// ONE HOLD: the holder recheck and the fence are the same critical
	// section, so a shell (or a rider) acquired between them cannot exist —
	// an acquire reads `closing` under this same mutex before it increments
	// anything.
	if s.live != l || s.generation != gen || s.holdsPlainShellLocked() || !s.fenceLocked(l) {
		s.mu.Unlock()
		return false
	}
	s.dropRidersLocked(l)
	s.notifyWaiters(l, ErrLinkLost)
	s.mu.Unlock()

	s.finishFenced(l, Event{Kind: Lost, Cause: CauseSilence})
	s.log.Warn("hostconn: closing a connection that stopped answering", "connection", s.key.String(), "why", why)
	return true
}

// span renders a duration as a clock reading's difference.
func span(d time.Duration) monoclock.Nanos { return monoclock.Nanos(d) }
