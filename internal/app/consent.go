package app

// The consent decision for the remote helper (remote-helper design D8):
// what git.open may do for one machine. The 2026-08-10 footprint-consent
// design's auto ladder resolved relay from "a suitable binary exists for
// that platform" alone — forward structure written before any helper
// existed. The day one ships, that arm becomes true everywhere, and every
// user would be asked, on every new machine, about a feature they never
// reached for. D8 adds the second condition: auto resolves to relay only
// when a surface on that connection has asked for the helper. The ask moves
// to the moment the user opens the git panel.

import (
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/profile"
)

// DesiredAuto is the hardcoded default of the delivery-mode axis (the
// 2026-08-10 design §3.1). The value is not yet a stored DesiredMode — that
// cascade change belongs to the footprint-consent epic — but resolution
// treats an absent mode as auto, which is the same thing.
const DesiredAuto profile.DesiredMode = "auto"

// Outcome is the resolver's answer for one machine at one consultation.
type Outcome string

const (
	// DesiredRelay — the machine resolves to the relay tier: install the
	// helper (if not complete) and serve it.
	DesiredRelay Outcome = "relay"
	// ConsentRequired — the machine has no relay-tier answer; git.open
	// answers consentRequired and the surface offers the consent flow.
	ConsentRequired Outcome = "consentRequired"
	// Refused — nothing is written and nothing is asked: raw, a denied
	// answer, no surface asked, or no artifact to offer. The selection
	// answers the §6 refusal states (unsupportedPlatform, execForbidden)
	// or, for a machine with no earned state, the not-available error.
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

// withHelperRequested sets whether a surface on this connection has asked
// for the helper — D8's second condition. Fail-closed default: false.
func withHelperRequested(b bool) option {
	return func(r *resolver) { r.requested = b }
}

type resolver struct {
	store             *consent.Store
	artifactAvailable bool
	requested         bool
}

// Resolve decides what the selection may do for m's machine: install and
// serve (DesiredRelay), raise the ask (ConsentRequired), or nothing
// (Refused). The fail-closed default is Refused — a resolver that has not
// been told the helper exists or that a surface asked for it installs
// nothing and asks nothing (consent design §4.2: a failure to decide never
// swallows a command, and degrade is toward the plain terminal, never
// toward the larger privilege).
func (r *resolver) Resolve(m Machine) Outcome {
	mode := m.Mode
	if mode == "" {
		mode = DesiredAuto
	}
	switch mode {
	case profile.DesiredRaw:
		// raw: nothing is written and nothing is asked (§4.2).
		return Refused
	case profile.DesiredRelay:
		// An explicit relay choice is the consent for the binary (§4.3).
		return DesiredRelay
	}
	// script (explicit) and auto fall through: the relay arm of the ladder
	// needs more than a binary (D8).
	if !r.requested {
		// No surface on this connection asked for the helper: nothing
		// happens — not even the ask. This is the second condition, and it
		// is what keeps shipping a binary from opting every machine in.
		return Refused
	}
	if r.store != nil {
		if ans, ok := r.store.Lookup(m.Fingerprint); ok {
			switch ans {
			case consent.Granted:
				// A stored answer is honoured silently (§4.4).
				return DesiredRelay
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
	// No relay-tier answer: the ask is raised at the feature (D8). An
	// explicit script is never silently upgraded — the ask is the
	// non-silent path.
	return ConsentRequired
}
