package agenttools

import "time"

// OutputTrust describes whether a tool result may contain text influenced by
// the program or data it observed. It is deliberately not derived from the
// effect lattice: a mutating command can return just as much attacker-
// influenced text as an observing command.
type OutputTrust string

const (
	OutputTrustUnset     OutputTrust = ""
	OutputTrustTrusted   OutputTrust = "trusted"
	OutputTrustUntrusted OutputTrust = "untrusted"
)

func supportedOutputTrust(trust OutputTrust) bool {
	switch trust {
	case OutputTrustTrusted, OutputTrustUntrusted:
		return true
	default:
		return false
	}
}

// TruncationPolicy says how a result bound is enforced when the source has
// more bytes than the declared result window.
type TruncationPolicy string

const (
	TruncationUnset    TruncationPolicy = ""
	TruncationDropTail TruncationPolicy = "drop-tail"
)

func supportedTruncation(policy TruncationPolicy) bool {
	return policy == TruncationDropTail
}

// ResultBound is the declaration's result window. MaxBytes is the maximum
// source payload retained by an executor; the result must report the returned
// window and the omitted remainder rather than looking complete.
type ResultBound struct {
	MaxBytes   int64
	Truncation TruncationPolicy
}

func (b ResultBound) Valid() bool {
	return b.MaxBytes > 0 && supportedTruncation(b.Truncation)
}

// CancellationPolicy states the observable outcome when the run context is
// cancelled. Cancellation is propagated to the executor and reported as an
// error; it is never silently converted into a successful empty result.
type CancellationPolicy string

const (
	CancellationUnset       CancellationPolicy = ""
	CancellationReturnError CancellationPolicy = "return-error"
)

func supportedCancellation(policy CancellationPolicy) bool {
	return policy == CancellationReturnError
}

// validToolDeadline is intentionally a finite-duration check. Each row carries
// its own deadline, and the execution seam applies it to the tool context.
func validToolDeadline(deadline time.Duration) bool {
	return deadline > 0
}
