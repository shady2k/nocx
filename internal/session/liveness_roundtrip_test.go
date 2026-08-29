package session

// The round-trip grade: what makes "the server is struggling" sayable, and
// what keeps it from being said every probe.
//
// The prober measures every probe (internal/ssh). If every measurement were
// published, a healthy connection would put a notification on the wire every
// keepalive interval, for a number nobody reads. If none were, the product
// could say only "gone". The grade is the line between those: a measurement
// is recorded always and published only when it crosses a boundary.

import (
	"testing"
	"time"
)

func TestRoundTripGrade_EntersAndLeavesAtDifferentThresholds(t *testing.T) {
	// A link that is merely far away is not slow.
	if roundTripGrade(false, 120*time.Millisecond) {
		t.Error("120ms graded slow — a cross-continent link is not a struggling host")
	}
	// One that takes half a second to answer a request it does not parse is.
	if !roundTripGrade(false, slowRoundTripEnter) {
		t.Error("the enter threshold did not grade slow")
	}
	// And once slow, it stays slow until it is comfortably better — the gap
	// is what a steady link cannot cross by jitter alone.
	if !roundTripGrade(true, 400*time.Millisecond) {
		t.Error("a slow link that improved slightly was graded fine: the indicator will flap")
	}
	if roundTripGrade(true, 200*time.Millisecond) {
		t.Error("a link that recovered past the leave threshold stayed slow")
	}
}

// A probe that never answered measures nothing, and nothing must not read as
// "fast". Zeroing the record here is how the indicator would drop to fine at
// the exact moment the host stopped answering.
func TestRoundTripGrade_AnUnmeasuredProbeKeepsThePreviousGrade(t *testing.T) {
	if !roundTripGrade(true, 0) {
		t.Error("an unmeasured probe cleared the slow grade")
	}
	if roundTripGrade(false, 0) {
		t.Error("an unmeasured probe invented a slow grade")
	}
}

// The publication rule, on the record itself: the value changing publishes,
// the grade changing publishes, and a probe that confirms both does not.
func TestApplyObservation_PublishesOnGradeChangeAndNotOnEveryProbe(t *testing.T) {
	// A live session: the record's derivation reads the channel, so a bare
	// struct would answer a nil one.
	s := sessionWithChannel(&waitErrChannel{done: make(chan struct{})})
	s.liveness = LivenessState{Liveness: LivenessAlive, Epoch: 1, RoundTrip: 20 * time.Millisecond}

	// A second healthy probe, a little different: nothing to say.
	if _, changed := s.applyObservation(Observation{
		Liveness: LivenessAlive, Epoch: 2, ObservedAt: time.Now(), RoundTrip: 25 * time.Millisecond,
	}); changed {
		t.Error("a healthy probe published: a healthy connection must stay silent on the wire")
	}

	// The host becomes slow: that is news.
	if _, changed := s.applyObservation(Observation{
		Liveness: LivenessAlive, Epoch: 3, ObservedAt: time.Now(), RoundTrip: 900 * time.Millisecond,
	}); !changed {
		t.Error("a host that became slow published nothing")
	}

	// Still slow, slower: not news again.
	if _, changed := s.applyObservation(Observation{
		Liveness: LivenessAlive, Epoch: 4, ObservedAt: time.Now(), RoundTrip: 1200 * time.Millisecond,
	}); changed {
		t.Error("a host that was already slow published again")
	}

	// Recovered: news.
	if _, changed := s.applyObservation(Observation{
		Liveness: LivenessAlive, Epoch: 5, ObservedAt: time.Now(), RoundTrip: 30 * time.Millisecond,
	}); !changed {
		t.Error("a host that recovered published nothing")
	}
}

// The guard that replaced the struct conversion: every field an observer may
// assert must reach the record. It used to be enforced by
// `LivenessState(obs)` failing to compile; the record now holds a derived
// field no observer may set, so the conversion is gone and this is what
// stands in its place.
func TestApplyObservation_CarriesEveryObservedField(t *testing.T) {
	s := sessionWithChannel(&waitErrChannel{done: make(chan struct{})})
	s.liveness = LivenessState{Liveness: LivenessAlive, Epoch: 1}

	at := time.Now().Add(-time.Minute).UTC()
	obs := Observation{
		Liveness:   LivenessUnknown,
		Epoch:      7,
		ObservedAt: at,
		RoundTrip:  640 * time.Millisecond,
	}
	if applied, _ := s.applyObservation(obs); !applied {
		t.Fatal("the observation was refused")
	}
	got := s.liveness
	if got.Liveness != obs.Liveness {
		t.Errorf("Liveness = %q, want %q", got.Liveness, obs.Liveness)
	}
	if got.Epoch != obs.Epoch {
		t.Errorf("Epoch = %d, want %d", got.Epoch, obs.Epoch)
	}
	if !got.ObservedAt.Equal(obs.ObservedAt) {
		t.Errorf("ObservedAt = %v, want %v", got.ObservedAt, obs.ObservedAt)
	}
	if got.RoundTrip != obs.RoundTrip {
		t.Errorf("RoundTrip = %v, want %v", got.RoundTrip, obs.RoundTrip)
	}
	// And the derived one, which no observer sets and the record must.
	if !got.Slow {
		t.Error("Slow = false for a 640ms round trip: the grade was not derived")
	}
}
