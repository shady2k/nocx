package hostconn

// The connection's prober and the ONE closing policy (design §5.3, "Silence
// and closing").
//
// The rule the whole file exists for: SILENCE ENDS NOTHING BY ITSELF. A probe
// that does not come back is evidence, and the evidence is shown to a person.
// What ends a connection is either the transport saying so (a reset, an end of
// file — a fact, not an inference) or this policy closing a connection on which
// nothing would be lost by closing it. A connection carrying a plain shell is
// never closed for silence: the person chooses to wait or to reconnect, and
// waiting costs a click while a wrong close costs the work.
//
// Every moment here is the injected clock's (clock.go). The policy is therefore
// assertable in seconds of simulated time, and the fault harness states Q3, Q5
// and the ten-minute case without waiting for any of them.

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
)

// guard is one connection's prober and closing policy. It runs for as long as
// the connection is the supervisor's live one.
//
// TIME. Every moment here is the injected clock's, and a move acts at the
// moment it was DUE rather than at the moment the loop noticed it: a test that
// advances thirty seconds at once must see the probe sent at fifteen and its
// deadline missed at thirty, exactly as a machine that had been left alone
// would have. That is what makes Q3 ("stalled within 30 s") assertable without
// waiting for it.
func (s *Supervisor) guard(l *live, gen uint64) {
	prober, ok := l.link.(Prober)
	if !ok {
		// A carrier that cannot ask the far side a question never reads
		// stalled: it stays honest about what it cannot measure
		// (design §5.1.2), and no policy may invent evidence it does not have.
		return
	}
	looker, _ := l.link.(Looker)

	var (
		// probeOut is non-nil while a probe is outstanding. There is at most
		// one: a parked SendRequest holds the mux's send lock, so a second
		// probe would queue behind it and measure nothing.
		probeOut chan error
		probedAt monoclock.Nanos
		// silentSince is the moment the outstanding probe was sent — the last
		// moment the connection could have been answering. Zero while the
		// connection is not stalled.
		silentSince monoclock.Nanos
		nextProbe   = l.nextProbe
		cadence     = l.probeInterval
		// lookOut is non-nil while a look-only dial is in flight.
		lookOut  chan error
		nextLook monoclock.Nanos
		// lookAnswered is set by the first look the host answers, and it stops
		// the cadence: a look can be logged as a preauth close, and a person
		// whose link is down must not be locked out by their own client.
		lookAnswered bool
		lookAt       monoclock.Nanos
		lookSent     monoclock.Nanos
		nudged       bool
	)

	for {
		now := s.clock.Now()
		wait := nextProbe
		switch {
		case probeOut != nil && silentSince == 0:
			wait = probedAt + span(ProbeDeadline)
		case silentSince != 0:
			wait = s.nextPolicyMove(now, silentSince, nextLook, lookAnswered, lookAt, lookOut != nil)
		}
		if now >= wait {
			// The move is due, and it acts at the moment it was due.
			if s.policyDue(l, gen, wait, now,
				cadence, &probeOut, &probedAt, &silentSince, &nextProbe,
				&lookOut, &nextLook, &lookAnswered, &lookAt, &lookSent, &nudged,
				prober, looker) {
				return
			}
			continue
		}
		select {
		case <-l.stop:
			return
		case err := <-probeOut:
			probeOut = nil
			if err != nil {
				// The transport has ended under the probe. The watcher on
				// Done reports it, and it is a fact rather than an inference
				// (design §5.3: "A transport error closes at once").
				return
			}
			// It answered. How long it took is the measurement the product
			// shows for a slow host; it is never a liveness verdict on its
			// own.
			s.answered(l, gen)
			s.observe(l, Observation{Answered: true, RoundTrip: time.Duration(s.clock.Now() - probedAt)})
			silentSince, lookAnswered, nudged, lookOut, lookAt = 0, false, false, nil, 0
			// The cadence is measured from the probe's SEND, not from the
			// moment this loop got to the answer: a host that answers late
			// must not also be asked late.
			nextProbe = probedAt + span(cadence)
		case err := <-lookOut:
			lookOut = nil
			if err != nil {
				// The host did not answer a new connection: the evidence is
				// that it is away, and the connection stays as it is.
				nextLook = lookSent + span(LookInterval)
				continue
			}
			if !lookAnswered {
				lookAnswered, lookAt = true, s.clock.Now()
				if !nudged {
					nudged = true
					// ONE nudge, only after a look: a request with no reply
					// wanted passes a parked probe, and a host that rebooted
					// answers it with a reset — a transport error, and an
					// unaided reconnect (Q5). A host that still holds the flow
					// acknowledges it and nothing changes, so it can end no
					// live shell.
					if err := prober.Nudge(); err != nil {
						return
					}
				}
			}
		case <-s.clock.After(time.Duration(wait - now)):
		}
	}
}

// nextPolicyMove answers when the policy next has something to do while the
// connection is stalled.
func (s *Supervisor) nextPolicyMove(now, silentSince, nextLook monoclock.Nanos, lookAnswered bool, lookAt monoclock.Nanos, lookInFlight bool) monoclock.Nanos {
	if s.HoldsPlainShell() {
		// No close is due on a connection carrying a plain shell: that is the
		// person's choice. The moves that remain are a look that has not been
		// made and the probe's own answer (its channel is a select case) —
		// waiting on a budget that may never be spent would only keep the
		// loop awake.
		if !lookAnswered && !lookInFlight {
			return nextLook
		}
		return now + span(SilenceCloseBudget)
	}
	next := silentSince + span(SilenceCloseBudget)
	if !lookAnswered && !lookInFlight && nextLook < next {
		next = nextLook
	}
	if grace := lookAt + span(LookGrace); lookAnswered && grace < next {
		next = grace
	}
	return next
}

// policyDue performs the one move that was due at `at`, observed at `now`, and
// reports whether the guard should end.
func (s *Supervisor) policyDue(
	l *live, gen uint64, at, now monoclock.Nanos, cadence time.Duration,
	probeOut *chan error, probedAt *monoclock.Nanos, silentSince *monoclock.Nanos,
	nextProbe *monoclock.Nanos, lookOut *chan error, nextLook *monoclock.Nanos,
	lookAnswered *bool, lookAt *monoclock.Nanos, lookSent *monoclock.Nanos, nudged *bool,
	prober Prober, looker Looker,
) bool {
	switch {
	case *probeOut == nil && *silentSince == 0:
		// Ask the connection to answer.
		out := make(chan error, 1)
		*probeOut = out
		*probedAt = at
		*nextProbe = at + span(cadence)
		go func() {
			// Probe takes no context: a call that does not come back stays
			// parked inside the library, holding the mux's send lock. That is
			// the design's own state (design §5.3: "the parked probe"), and it
			// is why the deadline is the supervisor's and not the call's.
			_, err := prober.Probe(context.Background())
			out <- err
		}()
		return false
	case *silentSince == 0:
		// The probe passed its deadline. Silence is now the state, and the
		// probe stays parked: it is the evidence that will notice the answer.
		*silentSince = *probedAt
		*nextLook = *probedAt + span(LookInterval)
		s.silent(l, gen, time.Duration(now-*probedAt))
		s.observe(l, Observation{Answered: false})
		return false
	}

	// Stalled. A look, if one is due and none is in flight.
	if *lookOut == nil && !*lookAnswered && now >= *nextLook {
		if looker == nil {
			// No look is possible (a routed plan, or a carrier without one):
			// the budget is the only bound left.
			*nextLook = now + span(SilenceCloseBudget)
			return false
		}
		out := make(chan error, 1)
		*lookOut = out
		*lookSent = at
		*nextLook = at + span(LookInterval)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), ProbeDeadline)
			defer cancel()
			out <- looker.Look(ctx)
		}()
		return false
	}

	// A helper-owned connection closes on the evidence: 30 s after the host
	// answered a look while the flow stayed silent, or at the outer budget.
	if s.HoldsPlainShell() {
		return false
	}
	if *lookAnswered && now >= *lookAt+span(LookGrace) {
		return s.endForSilence(l, gen, "the host answers new connections and this one has been silent past the grace")
	}
	if now >= *silentSince+span(SilenceCloseBudget) {
		return s.endForSilence(l, gen, "silent past the close budget")
	}
	return false
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
func (s *Supervisor) endForSilence(l *live, gen uint64, why string) bool {
	s.mu.Lock()
	if s.live != l || s.generation != gen {
		s.mu.Unlock()
		return true
	}
	s.live = nil
	s.generation++
	s.advance(Event{Kind: Lost, Cause: CauseSilence})
	s.mu.Unlock()
	s.log.Warn("hostconn: closing a connection that stopped answering", "connection", s.key.String(), "why", why)
	_ = l.link.Close()
	return true
}

// span renders a duration as a clock reading's difference.
func span(d time.Duration) monoclock.Nanos { return monoclock.Nanos(d) }
