package ssh

// What a probe of a live connection reports, and who is told about it.
//
// The prober itself is NOT here any more: it lives in the connection manager
// behind hostconn.Carrier (design §5.3, nocx-oalo1.4.3). A pooled connection no
// longer arms one, and nothing in this package may close a connection because a
// probe went unanswered — silence ends nothing by itself, and the one closing
// policy is the supervisor's.
//
// The two types stay because they are the vocabulary the product's host status
// is written in: internal/app's helper session registers an observer, and the
// session registry grades what it is told (internal/session/liveness.go). What
// changed is the producer, not the words.

import "time"

// LivenessObserver is told what a probe learns about the far end of ONE
// connection: false when a probe failed and the connection is still being given
// a chance, true when it answers again (nocx-iarf9).
//
// It is deliberately not told about the give-up. Under the one closing policy
// there is no give-up to report: a silent connection either answers, ends as a
// transport error, or is closed by the policy — and each of those is already
// reported by the state the supervisor keeps and by the end of a session.
//
// The observer is bound by whoever acquired the connection, so it carries its
// own idea of which host this is; this package does not name it.
type LivenessObserver func(Reachability)

// Reachability is one probe's finding about the far end: whether it answered,
// and how long it took to do so.
//
// The round trip is here because the prober is the ONLY thing in nocx that
// measures one. Without it the product can say a host is gone and cannot say a
// host is struggling — and "struggling" is the state a person actually meets,
// on a loaded server or a long link. It is a measurement and not a second
// liveness value: whether the host is REACHABLE and how FAST it answers are two
// questions, and folding the second into the first would make a slow host look
// like a half-dead one (AD-8: one owner per fact).
//
// RoundTrip is zero for a probe that never answered — an unanswered probe has
// no duration, and reporting the budget it spent would be reporting the timeout
// rather than the host.
type Reachability struct {
	Responsive bool
	RoundTrip  time.Duration
}
