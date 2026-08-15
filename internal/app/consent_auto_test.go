package app

// One absent mode, one answer — ADR-0033, nocx-7iisi.
//
// Before this, an unset desiredMode resolved two ways in one product: the
// open ack reported `script` (desiredModeForAck's hardcoded fallback) while
// the consent resolver treated the same absence as `auto` and could raise
// the ask. So a connection nobody had configured was DESCRIBED as script and
// then OFFERED an upgrade — the exact thing D8 forbids for a machine at
// script, reached through the gap between two defaults rather than through
// anyone's decision.
//
// The fix is not "make both say auto" as a coincidence of two literals; it
// is that both derive from the cascade's one default. These tests pin the
// agreement, so a future edit to either default breaks here rather than in
// the product.

import (
	"testing"

	"github.com/shady2k/nocx/internal/profile"
)

// TestAbsentModeResolvesToOneValue: the cascade's hardcoded default is what
// an absent mode means, and the consent resolver reads that same value
// rather than a literal of its own.
func TestAbsentModeResolvesToOneValue(t *testing.T) {
	eff, err := profile.ResolveEffectiveProfile(
		profile.SSHProfile{
			Base:    profile.Base{ID: "p1", Type: "ssh", Name: "web"},
			Options: profile.StoredSSHProfileOptions{Host: "h"},
		},
		nil,
		profile.SparseSSHOptions{},
	)
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}
	cascadeDefault := eff.ResolvedOptions.DesiredMode

	if cascadeDefault != profile.DesiredAuto {
		t.Fatalf("the cascade's default is %q; these tests and the resolver "+
			"both assume it is %q (ADR-0033)", cascadeDefault, profile.DesiredAuto)
	}

	// The resolver's own reading of "nothing was set" must be the same value.
	// A machine with an empty Mode is a connection the cascade never spoke
	// for — a direct host or an ad-hoc destination.
	r := newResolver(withHelperArtifactAvailable(true), withHelperRequested(true))
	absent := r.Resolve(Machine{Fingerprint: "SHA256:absent"})
	declared := r.Resolve(Machine{Fingerprint: "SHA256:declared", Mode: cascadeDefault})

	if absent != declared {
		t.Errorf("an absent mode resolved to %q but the cascade default %q resolved to %q — "+
			"one value, two answers", absent, cascadeDefault, declared)
	}
}

// TestExplicitScriptIsNeverRaisedToTheAsk is D8's rule, which only became
// assertable once silence stopped resolving to script. A user who chose
// script has answered; the ask is for those who have not.
func TestExplicitScriptIsNeverRaisedToTheAsk(t *testing.T) {
	r := newResolver(withHelperArtifactAvailable(true), withHelperRequested(true))

	if got := r.Resolve(explicitScript); got == ConsentRequired {
		t.Error("an explicit script was raised to the consent ask — " +
			"D8: script is an answer, not a gap")
	}
	if got := r.Resolve(machineWithNoStoredAnswer); got != ConsentRequired {
		t.Errorf("an unanswered machine resolved to %q, want %q — "+
			"if silence is not askable, the user must edit every connection "+
			"just to be offered the helper", got, ConsentRequired)
	}
}

// TestExplicitAutoIsAskableLikeSilence: auto is a storable answer meaning
// "not answered", so choosing it deliberately must behave exactly as
// choosing nothing. Otherwise a user who says auto over a group that says
// raw gets a mode the resolver does not recognise, and fails closed into
// Refused.
func TestExplicitAutoIsAskableLikeSilence(t *testing.T) {
	r := newResolver(withHelperArtifactAvailable(true), withHelperRequested(true))

	explicitAuto := Machine{Fingerprint: "SHA256:auto", Mode: profile.DesiredAuto}
	if got := r.Resolve(explicitAuto); got != ConsentRequired {
		t.Errorf("an explicit auto resolved to %q, want %q", got, ConsentRequired)
	}
}
