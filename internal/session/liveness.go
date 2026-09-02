package session

// Liveness (nocx-iarf9): what the backend currently believes about a session,
// as a projection with an epoch rather than a flag.
//
// The set is alive | dead | unknown | interrupted, and `unknown` is the reason
// the set exists. A session on a host that has stopped answering is neither
// alive nor dead: rendering it alive invites the user to type into nothing,
// rendering it dead throws away work that is very likely still running. Both
// lie, so the state that says "we do not currently know" is first class
// (design D7).
//
// The vocabulary is not invented here. `interrupted` is the content ledger's
// word (internal/content/ledger.go:336), where it already means a state chosen
// after a restart rather than an assertion of liveness, and ExitCause reuses it
// too (nocx-ictcq) — one word, one meaning, three places, which is AD-8 applied
// to a vocabulary rather than to a package.
//
// THE INVARIANT THAT SHAPES EVERYTHING BELOW: the two halves of the set are
// reached differently.
//
//   - alive and unknown are ASSERTED, by an observer with evidence about
//     reachability. They are revisable in both directions.
//   - dead and interrupted are DERIVED, from the session's own end and from
//     nothing else. No observation may assert either, whatever it carries,
//     and once one is reached the record is final.
//
// That is what "a node is dead only on an authoritative terminal event" means
// mechanically: `dead` has exactly one writer, livenessForExit, whose only
// input is ExitOutcome — the single owner of how a session ended.

import (
	"time"

	"github.com/shady2k/nocx/internal/ssh"
)

// Liveness is the projection's value.
type Liveness string

const (
	// LivenessAlive: the session exists and nothing says otherwise.
	LivenessAlive Liveness = "alive"
	// LivenessDead: the shell itself exited. An authoritative terminal event,
	// and the ONLY route to this value.
	LivenessDead Liveness = "dead"
	// LivenessUnknown: we cannot currently reach the session's host, and
	// nothing has ended. Neither alive nor dead, and the whole point of the
	// set: this is the state a child on an unreachable host is in, and the
	// only one that does not lie about it.
	LivenessUnknown Liveness = "unknown"
	// LivenessInterrupted: the session ended without an authoritative status —
	// the channel is gone, the host became unreachable for good, a teardown
	// beat the watcher. The backend cannot say the session died, so it does
	// not: it says it lost it.
	LivenessInterrupted Liveness = "interrupted"
)

// Terminal reports whether the value describes a session that has ended. A
// terminal record is final — no observation revives it — and it is the half of
// the set no observer may assert.
func (l Liveness) Terminal() bool {
	return l == LivenessDead || l == LivenessInterrupted
}

// LivenessState is the record: the value, the epoch that orders it against
// other observations of the same session, and when it was observed.
type LivenessState struct {
	Liveness   Liveness
	Epoch      uint64
	ObservedAt time.Time
	// RoundTrip is how long the last probe to this session's host took, or
	// zero when nothing measured one (a local session, or a probe that never
	// answered — an unanswered probe has no duration).
	//
	// A MEASUREMENT beside the value, never a value of its own: "can we reach
	// this host" and "how fast does it answer" are two questions, and folding
	// the second into the first would make a slow host render as a half-dead
	// one. It is what lets the product say a server is struggling, which is
	// the state a person actually meets — the vocabulary above can otherwise
	// only say gone.
	RoundTrip time.Duration
	// Slow is the GRADE of that measurement, and it is stored rather than
	// left to be re-derived because the grade has hysteresis: it enters and
	// leaves at different numbers, so it is a function of the measurement AND
	// the previous grade, which a reader holding only RoundTrip cannot
	// reproduce. A renderer that thresholded the milliseconds for itself
	// would be a second derivation of one concept — the shape AGENTS.md calls
	// this repository's most recurrent defect — and the two would agree
	// everywhere anyone looked and disagree exactly at the boundary.
	Slow bool
}

// Observation is one assertion about a session's reachability, addressed to a
// session INCARNATION (a Ref) rather than to an id.
//
// The epoch is stamped when the observation is MADE, not when it is applied,
// and that gap is the reason it exists: an observer that saw a host answer,
// and whose report is delayed behind a slower path, must not be able to revive
// a record that has since gone unknown. An observation whose epoch is not
// greater than the record's is dropped, so arrival order stops mattering —
// which is what lets observations come from several places without a shared
// clock (design D11).
type Observation struct {
	Liveness   Liveness
	Epoch      uint64
	ObservedAt time.Time
	RoundTrip  time.Duration
}

// The two round-trip thresholds, and why there are two.
//
// A link sitting exactly at one threshold would flap: probe, slow, probe,
// fine, probe, slow — an indicator blinking at the probe interval, which is
// worse than no indicator because it draws the eye and says nothing. So the
// grade ENTERS slow at one number and LEAVES it at a lower one, and the gap
// between them is what a steady link cannot cross by jitter alone.
//
// The numbers are chosen against what an SSH keepalive round trip normally
// is: single-digit milliseconds on a LAN, tens across a continent, and low
// hundreds on a satellite or a badly congested link. Half a second is past
// all of those — a host that takes that long to answer a request it does not
// even parse is busy, not far away.
const (
	slowRoundTripEnter = 500 * time.Millisecond
	slowRoundTripLeave = 300 * time.Millisecond
)

// roundTripGrade folds a measured round trip into the two states the product
// draws, carrying the previous grade so the thresholds can differ by
// direction. A zero measurement is not a grade: nothing was measured, so the
// previous one stands.
func roundTripGrade(prevSlow bool, rtt time.Duration) bool {
	if rtt <= 0 {
		return prevSlow
	}
	if prevSlow {
		return rtt > slowRoundTripLeave
	}
	return rtt >= slowRoundTripEnter
}

// livenessForExit maps how a session ended onto the terminal half of the set.
// The ONE writer of dead and interrupted, taking its input from ExitOutcome,
// which is itself the single owner of the classification (nocx-ictcq). Nothing
// else in the product may produce either value.
func livenessForExit(cause ExitCause) Liveness {
	if cause == ExitExited {
		return LivenessDead
	}
	return LivenessInterrupted
}

// SetLivenessObserver registers the watcher told when a session's liveness
// VALUE changes. It is given the session's Ref, not the new state: the record
// is the authority, and a watcher that reads it back cannot publish a value
// that was already stale when the closure fired. A nil observer, or none at
// all, is ordinary — the registry runs the whole projection without one.
func (r *Reg) SetLivenessObserver(fn func(Ref)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.livenessObserver = fn
}

// Observe applies an observation to the session ref names, and reports whether
// it was applied. It refuses, in this order:
//
//   - a ref naming no live session, or a different incarnation of one
//     (SameIncarnation is the single owner of that question);
//   - a terminal claim — no observer may assert an end;
//   - an observation of a session that has already ended;
//   - an observation not newer than the record (epoch <= the record's), which
//     is the late-observation rule.
//
// This is the only door into the record's non-terminal half.
func (r *Reg) Observe(ref Ref, obs Observation) bool {
	if obs.Liveness.Terminal() {
		// Refused before anything is looked up: an end is derived, never
		// asserted, so there is no session for which this could be valid.
		return false
	}
	r.mu.Lock()
	s, ok := r.sessions[ref.ID]
	if !ok || !ref.Identity.SameIncarnation(ref.ID, s) {
		r.mu.Unlock()
		return false
	}
	observer := r.livenessObserver
	r.mu.Unlock()

	applied, changed := s.applyObservation(obs)
	if changed && observer != nil {
		observer(ref)
	}
	return applied
}

// observeHost applies what the keepalive prober learned about one machine to
// every session running on it. It is the production producer of `unknown`.
//
// The granularity is the host, deliberately. A keepalive failure is evidence
// about a MACHINE — the transport to it stopped answering — and the pooled
// connection it rides is shared by every tab to the same principal (AD-4), so
// there is no single session it belongs to. Marking every session on that host
// unknown claims exactly what the evidence supports and no more, which is what
// `unknown` is for; a session elsewhere is untouched.
//
// The epoch is minted per session, from that session's own counter, at the
// moment the observation is made.
func (r *Reg) observeHost(host string, reach ssh.Reachability) {
	if host == "" {
		return
	}
	value := LivenessUnknown
	if reach.Responsive {
		value = LivenessAlive
	}
	at := time.Now()

	r.mu.Lock()
	refs := make([]Ref, 0, len(r.sessions))
	for _, s := range r.sessions {
		if s.host == host {
			refs = append(refs, Ref{ID: s.id, Identity: s.identity})
		}
	}
	r.mu.Unlock()

	for _, ref := range refs {
		// Minting inside the loop, through Observe's own path, keeps one
		// route into the record: this function decides WHAT it saw, never how
		// the record is written.
		epoch, ok := r.mintLivenessEpoch(ref)
		if !ok {
			continue
		}
		r.Observe(ref, Observation{Liveness: value, Epoch: epoch, ObservedAt: at, RoundTrip: reach.RoundTrip})
	}
}

// mintLivenessEpoch takes the next epoch from the named session's counter, or
// reports that the session is gone. Monotonic per session, never reused: two
// observations of one session are always ordered, and that is all the ordering
// the record needs, because an observation of another incarnation is refused
// by identity rather than by number.
func (r *Reg) mintLivenessEpoch(ref Ref) (uint64, bool) {
	r.mu.Lock()
	s, ok := r.sessions[ref.ID]
	r.mu.Unlock()
	if !ok || !ref.Identity.SameIncarnation(ref.ID, s) {
		return 0, false
	}
	// Read the record before minting. The first read is also the first stamp
	// (livenessLocked resolves an unobserved record to alive, from this same
	// counter), so minting first would produce an observation the initial
	// stamp then outranks — and the first thing anyone ever observed about a
	// session would be silently dropped.
	_ = s.Liveness()
	return s.livenessEpochs.Add(1), true
}

// hostLivenessObserver binds this registry to one host name, producing the
// callback the ssh package's keepalive prober calls. The ssh package never
// learns which host it is talking about — the connection is pooled and belongs
// to no single session — so the name is captured here, where the open knew it.
func (r *Reg) hostLivenessObserver(host string) ssh.LivenessObserver {
	return func(reach ssh.Reachability) { r.observeHost(host, reach) }
}

// Liveness returns the session's current projection, deriving the terminal
// half on demand.
//
// The derivation is a READ rather than an event, which is what makes "dead
// only on an authoritative terminal event" structural instead of a rule
// somebody has to keep: the channel's Done is the fact, ExitOutcome is the
// classification, and there is no writer that could produce a terminal value
// from anything else. The alternative — a goroutine per session watching Done
// so it could stamp the moment — buys a more precise ObservedAt at the cost of
// a second thing that can decide a session has ended.
func (s *realSession) Liveness() LivenessState {
	s.livenessMu.Lock()
	defer s.livenessMu.Unlock()
	return s.livenessLocked()
}

// livenessLocked is Liveness with s.livenessMu held: the shared derivation, so
// a read and an observation cannot disagree about what the record says.
func (s *realSession) livenessLocked() LivenessState {
	// Terminal is final. Checked first so a session that has ended is never
	// re-derived and never re-stamped.
	if s.liveness.Liveness.Terminal() {
		return s.liveness
	}
	if s.ch == nil {
		if s.liveness.Liveness == "" {
			s.liveness = LivenessState{
				Liveness:   LivenessAlive,
				Epoch:      s.livenessEpochs.Add(1),
				ObservedAt: time.Now(),
			}
		}
		return s.liveness
	}
	select {
	case <-s.ch.Done():
		cause, _ := s.ExitOutcome()
		s.liveness = LivenessState{
			Liveness:   livenessForExit(cause),
			Epoch:      s.livenessEpochs.Add(1),
			ObservedAt: time.Now(),
		}
	default:
		// A session with no observation yet is alive: it exists, its channel
		// is open, and nothing has said otherwise. Stamped on first read so
		// the record always carries an epoch to order against.
		if s.liveness.Liveness == "" {
			s.liveness = LivenessState{
				Liveness:   LivenessAlive,
				Epoch:      s.livenessEpochs.Add(1),
				ObservedAt: time.Now(),
			}
		}
	}
	return s.liveness
}

// applyObservation writes obs onto the record if it may. Returns whether it
// was applied at all, and whether the VALUE changed — the second is what the
// watcher is told about, so a probe confirming what is already believed does
// not publish a notification per tick.
func (s *realSession) applyObservation(obs Observation) (applied, changed bool) {
	s.livenessMu.Lock()
	defer s.livenessMu.Unlock()
	cur := s.livenessLocked()
	if cur.Liveness.Terminal() {
		return false, false
	}
	if obs.Epoch <= cur.Epoch {
		// Not newer. Equal counts as not newer: equal epochs carry no
		// ordering, and applying one would make the record depend on arrival
		// order — the thing the epoch replaces.
		return false, false
	}
	// The conversion is the point rather than a shortcut: an Observation and
	// a LivenessState are the same words, one asserted and one recorded, so a
	// field added to either without the other stops compiling here.
	// Field by field rather than a conversion. It USED to be
	// `LivenessState(obs)`, and the conversion was the guard: the two structs
	// were the same words, one asserted and one recorded, so a field added to
	// either without the other stopped compiling. That guard is spent now
	// that the record holds something no observer may assert — the grade,
	// which is derived here from the observation and the previous grade. What
	// replaces it is TestApplyObservation_CarriesEveryObservedField, which
	// fails if an observed field stops reaching the record.
	next := LivenessState{
		Liveness:   obs.Liveness,
		Epoch:      obs.Epoch,
		ObservedAt: obs.ObservedAt,
		RoundTrip:  obs.RoundTrip,
	}
	// A measurement the probe did not make must not erase the last one: an
	// unanswered probe says nothing about how fast the host answers, and
	// zeroing the record here would make the indicator drop to "fine" at the
	// exact moment the host stopped answering.
	if next.RoundTrip <= 0 {
		next.RoundTrip = cur.RoundTrip
	}
	wasSlow := cur.Slow
	nowSlow := roundTripGrade(wasSlow, next.RoundTrip)
	next.Slow = nowSlow
	s.liveness = next
	// Two reasons to tell the watcher, and only these two. The VALUE changed
	// (reachable ↔ not), or the round trip crossed a GRADE boundary. A probe
	// that merely confirms what is believed — including one reporting a round
	// trip a millisecond off the last — publishes nothing, which is what keeps
	// a healthy connection silent on the wire while still being measured
	// every probe.
	return true, obs.Liveness != cur.Liveness || nowSlow != wasSlow
}
