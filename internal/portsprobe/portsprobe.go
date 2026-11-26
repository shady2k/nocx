// Package portsprobe owns what a port sample IS and how a host's listener
// table is read — the result vocabulary and the five table parsers — with no
// dependency on any transport.
//
// The split exists for one reason, and it is a link, not a style (design
// §5.10, C30): the far helper runs on a host we do not own, is cross-compiled
// from this tree, and must answer "what listens here" itself. Whatever that
// artifact links, it carries — so the parsers and the types they return may
// reach the standard library and nothing else from this repository. A
// package that also named internal/ssh, internal/helper/... or
// internal/farhelper would drag the coordinator's whole connection stack into
// the binary the helper deploys to somebody else's machine. The same fact is
// what makes internal/nativeports (this machine's kernel read) importable by
// both sides: it returns these types too, so the local panel and the far
// host's sample render identically.
//
// The ladder that decides WHICH parser to run stays in the caller: today
// internal/discovery's Detector over the exec seam. This package answers two
// questions only — "is this text the dialect of ss/netstat/busybox
// netstat/lsof/sockstat, and if so what does it say" and "what does this set
// of listeners mean as an outcome".
package portsprobe

import "time"

// State is the overall outcome of a discovery sample (spec §5). The five
// result states are exact: a successful empty result means "no listeners
// observed", and every other state means could-not-determine — never "no
// ports". StatePending is not a result: it is the state before the first
// sample has completed.
type State string

const (
	StateAvailable                 State = "available"
	StateAvailableLimited          State = "available-limited"
	StateUnavailable               State = "unavailable"
	StateFailedTransiently         State = "failed-transiently"
	StatePermissionOrPolicyRefused State = "permission-or-policy-refused"
	StatePending                   State = "pending"
)

// AddressFamily is the IP family of a listener's bind address.
type AddressFamily string

const (
	FamilyIPv4 AddressFamily = "ipv4"
	FamilyIPv6 AddressFamily = "ipv6"
)

// ProcessEvidence is three-valued — known | permission-denied | unsupported
// — never an empty string (spec §5): "nobody owns it" and "I was not allowed
// to see" are different facts and must render differently.
type ProcessEvidence string

const (
	EvidenceKnown            ProcessEvidence = "known"
	EvidencePermissionDenied ProcessEvidence = "permission-denied"
	EvidenceUnsupported      ProcessEvidence = "unsupported"
)

// Process is the process evidence for one listener. Name and PID are valid
// when Evidence is EvidenceKnown.
type Process struct {
	Evidence ProcessEvidence
	Name     string
	PID      int
}

// Listener is one listening TCP port, as a probe table reported it.
type Listener struct {
	Family  AddressFamily
	Address string // bind address as the probe reported it (may be a wildcard)
	Port    int
	Process Process
}

// Sample is one reading of a target's listener table: the outcome, the rows,
// which dialect produced them, and the diagnostics needed to explain a
// read that produced none.
type Sample struct {
	State          State
	Listeners      []Listener
	Probe          string        // dialect that produced the sample; "" when none did
	ProbesTried    []string      // probes attempted for this pass, in order
	Duration       time.Duration // wall time of the sampling pass
	Classification string        // human-readable why: refusal detail, truncation, ...
	Stderr         string        // bounded excerpt of the probe's stderr, for diagnostics
	Canceled       bool          // ctx canceled, or the lease closed while sampling; State/Listeners are the previous result
}

// SampleState projects the listeners of a valid sample into a state: a
// successful empty result means "no listeners observed" (available); when
// every row's process evidence is degraded — the probe cannot provide it
// (busybox netstat) or none was visible (ss as non-root on a fully owned
// table, or a local read whose owners were not visible) — the sample is
// available-limited. Both the ladder and the native local reader project
// through this one function, so both transports agree on what the evidence
// means.
func SampleState(listeners []Listener) State {
	if len(listeners) == 0 {
		return StateAvailable
	}
	for _, l := range listeners {
		if l.Process.Evidence == EvidenceKnown {
			return StateAvailable
		}
	}
	return StateAvailableLimited
}
