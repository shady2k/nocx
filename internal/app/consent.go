package app

// The consent resolver: what the helper selection may do for one machine at
// one consultation (ADR-0034: the answer is keyed by the destination's
// host-key fingerprint, in internal/helper/consent; ADR-0068: the helper is
// decided by the connection, never by a feature).
//
// Two callers consult it, and only one may act on ConsentRequired.
// openHoldingLease, at connect, is where ADR-0068 puts the ask — beside the
// host-key verification, once per machine — so it is the only caller
// allowed to turn ConsentRequired into an actual prompt. helperGitFactory,
// at git.open, treats ConsentRequired exactly like Refused: a feature
// surface consumes whatever the connection already decided and may never
// raise the tier itself.
//
// This superseded D8, which put the ask at the git panel instead and needed
// a second condition — "a surface on this connection has asked for the
// helper" — to keep a shipped binary from opting every machine in silently
// (the 2026-08-10 footprint-consent design's auto ladder resolved helper
// from "a suitable binary exists for that platform" alone, which becomes
// true everywhere the day one ships). ADR-0068 answers that worry a
// different way: the connect-time ask runs once per machine, automatically,
// so the second condition — and the option that carried it — is gone with
// it.

import (
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/profile"
)

// Outcome is the resolver's answer for one machine at one consultation.
type Outcome string

const (
	// DesiredHelper — the machine resolves to the helper tier: install the
	// helper (if not complete) and serve it.
	DesiredHelper Outcome = "helper"
	// ConsentRequired — the machine has no helper-tier answer. Only the
	// connect-time caller (openHoldingLease, ADR-0068) may raise the ask
	// on this outcome; every other caller — git.open included — treats it
	// exactly like Refused, because no feature surface may raise a
	// machine's tier.
	ConsentRequired Outcome = "consentRequired"
	// Refused — nothing is written and nothing is asked: raw, script, a
	// denied answer, no artifact to offer, or — outside the connect-time
	// caller — a machine with no answer yet. The selection answers the §6
	// refusal states (unsupportedPlatform, execForbidden) or, for a
	// machine with no earned state, the not-available error naming the
	// connection setting that would change it.
	Refused Outcome = "refused"
)

// Machine is one consent decision: the remote host's public-key fingerprint
// and the destination's effective desired mode. The fingerprint is the
// whole identity (consent design §3.2) — the same machine reached any way
// is one answer. The mode is the resolved cascade answer; "" means the
// hardcoded auto default.
type Machine struct {
	Fingerprint string
	Mode        profile.DesiredMode
}

type option func(*resolver)

func newResolver(opts ...option) *resolver {
	r := &resolver{}
	for _, o := range opts {
		o(r)
	}
	return r
}

func withStore(s *consent.Store) option {
	return func(r *resolver) { r.store = s }
}

// withHelperArtifactAvailable sets whether a suitable helper binary exists
// for this machine's platform (D20). Fail-closed default: false.
func withHelperArtifactAvailable(b bool) option {
	return func(r *resolver) { r.artifactAvailable = b }
}

type resolver struct {
	store             *consent.Store
	artifactAvailable bool
}

// Resolve decides what the selection may do for m's machine: install and
// serve (DesiredHelper), raise the ask (ConsentRequired — the connect-time
// caller's alone to act on), or nothing (Refused). The fail-closed default
// is Refused — a resolver that has not been told the helper exists installs
// nothing and asks nothing (consent design §4.2: a failure to decide never
// swallows a command, and degrade is toward the plain terminal, never
// toward the larger privilege).
func (r *resolver) Resolve(m Machine) Outcome {
	mode := m.Mode
	if mode == "" {
		mode = profile.DesiredAuto
	}
	switch mode {
	case profile.DesiredRaw:
		// raw: nothing is written and nothing is asked (§4.2).
		return Refused
	case profile.DesiredHelper:
		// An explicit helper choice is the consent for the binary (§4.3).
		return DesiredHelper
	case profile.DesiredScript:
		// An explicit script is an ANSWER: "the shell tiers, and do not
		// offer me the binary" — script is an answer, not a gap.
		//
		// This arm is what ADR-0033 bought. Until auto existed as its own
		// value, script also carried every user who had never opened the
		// connection's settings, so refusing here would have refused
		// everyone; the code therefore fell through and asked a script user
		// on the reading that an ask is not a silent upgrade. That reading
		// left auto and script behaving identically at every branch, which
		// makes auto ceremony. With silence resolving to auto, the two can
		// finally differ where the design always said they did.
		//
		// The user is not stranded: refusedHelperReason names the modes
		// that do offer the helper, and both are one Select away.
		return Refused
	}
	// auto falls through: the machine's own answer, or the ask.
	if r.store != nil {
		if ans, ok := r.store.Lookup(m.Fingerprint); ok {
			switch ans {
			case consent.Granted:
				// A stored answer is honoured silently (§4.4).
				return DesiredHelper
			case consent.Denied:
				// Answered and declined: never asked again, never upgraded.
				return Refused
			}
		}
	}
	if !r.artifactAvailable {
		// No suitable binary for this platform (§3.1 arm 3): nothing to
		// offer, so no ask.
		return Refused
	}
	// No helper-tier answer, on a machine that has not answered: the
	// connect-time caller raises the ask (ADR-0068), before anything is
	// written (§4 step 4); every other caller treats this as Refused.
	return ConsentRequired
}
