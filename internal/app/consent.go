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
//
// AND A CONNECTION THAT NAMES THE HELPER IS ASKED TOO (ADR-0105, nocx-1kejz.3).
// Until then this ladder answered an explicit `helper` with DesiredHelper and
// never consulted the store — "an explicit helper choice is the consent for
// the binary", §4.3 — which read the host's METHOD as the machine's
// PERMISSION. A host recorded `helper` therefore deployed a binary it had
// never been asked about, and the editor's own consent field (a leftover
// ADR-0034 deleted the owner of) was the only place that question appeared to
// be answered. The method and the permission are two
// questions with two owners — the connection for the first, the machine for
// the second — so `helper` now waits for the same answer `auto` waits for.

import (
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/hostmode"
)

// Outcome is the resolver's answer for one machine at one consultation.
type Outcome string

const (
	// DesiredHelper — the machine resolves to the helper tier: install the
	// helper (if not complete) and serve it.
	DesiredHelper Outcome = "helper"
	// ConsentRequired — the machine has no helper-tier answer, and the
	// connection asks for the helper: silence (auto) or the explicit
	// `helper` method (ADR-0105). Only the connect-time caller
	// (openHoldingLease, ADR-0068) may raise the ask on this outcome; every
	// other caller — git.open included — treats it exactly like Refused,
	// because no feature surface may raise a machine's tier.
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
// and the method the HOST keeps (ADR-727g63tr decision 1). The fingerprint is
// the whole identity (consent design §3.2) — the same machine reached any way
// is one answer. The mode is the record's answer; "" means auto, which is the
// same "not yet answered" (ADR-0033).
type Machine struct {
	Fingerprint string
	Mode        hostmode.Mode
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
		mode = hostmode.Auto
	}
	switch mode {
	case hostmode.Raw:
		// raw: nothing is written and nothing is asked (§4.2).
		return Refused
	case hostmode.Helper:
		// An explicit helper names the METHOD this host uses; it is not the
		// machine's permission to have a binary deployed on it
		// (ADR-0105). Falling through to the machine-keyed half is what
		// makes the difference real: a granted machine serves the helper
		// exactly as before, a denied one is never upgraded, and an
		// unanswered one is ASKED at connect instead of being handed a
		// deployment nobody consented to.
		//
		// This arm used to return DesiredHelper unconditionally —
		// "an explicit helper choice is the consent for the binary" (§4.3)
		// — which merged the two questions and left the store unread on
		// the one path a person is most likely to take deliberately.
		return r.forMachine(m)
	case hostmode.Script:
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
	return r.forMachine(m)
}

// forMachine is the machine-keyed half of the ladder, and the ONE answer to
// "may this machine have the helper": the store's grant, the store's denial,
// or — for a machine nobody has asked — the connect-time ask. Both `auto`
// (nothing answered) and `helper` (the method answered, the permission not)
// reach it, which is what keeps the permission in one place while the method
// stays where a connection keeps it (AD-8, ADR-0105).
//
// Silence is not consent and neither is a method choice. The fail-closed
// default is unchanged: a machine with nothing to offer — no artifact for its
// platform (§3.1 arm 3) — is never asked, because there is nothing to ask
// about.
func (r *resolver) forMachine(m Machine) Outcome {
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
		return Refused
	}
	// No helper-tier answer, on a machine that has not answered: the
	// connect-time caller raises the ask (ADR-0068), before anything is
	// written (§4 step 4); every other caller treats this as Refused.
	return ConsentRequired
}
