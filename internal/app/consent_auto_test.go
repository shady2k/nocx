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
// The mode has one owner now (ADR-727g63tr decision 1): the host record, whose
// absence IS auto. The open ack and the consent resolver must still agree that
// an absent record is `auto` — one value, two readers — and these tests pin
// the agreement so a future edit to either reading breaks here rather than in
// the product.

import (
	"testing"

	"github.com/shady2k/nocx/internal/hostmode"
)

// TestAbsentModeResolvesToOneValue: a host record with no answer is auto, both
// in the consent resolver and in what the open ack reports. Two literals that
// agree today are not the property; one value read by both is.
func TestAbsentModeResolvesToOneValue(t *testing.T) {
	r := newResolver(withHelperArtifactAvailable(true))
	absent := r.Resolve(Machine{Fingerprint: "SHA256:absent"})
	declared := r.Resolve(Machine{Fingerprint: "SHA256:declared", Mode: hostmode.Auto})
	if absent != declared {
		t.Errorf("an absent mode resolved to %q but auto resolved to %q — one value, two answers", absent, declared)
	}
}

// TestExplicitScriptIsNeverRaisedToTheAsk, which only became assertable
// once silence stopped resolving to script. A host recorded script has
// answered; the ask is for those who have not.
func TestExplicitScriptIsNeverRaisedToTheAsk(t *testing.T) {
	r := newResolver(withHelperArtifactAvailable(true))

	if got := r.Resolve(explicitScript); got == ConsentRequired {
		t.Error("a host recorded script was raised to the consent ask — " +
			"script is an answer, not a gap")
	}
	if got := r.Resolve(machineWithNoStoredAnswer); got != ConsentRequired {
		t.Errorf("an unanswered host resolved to %q, want %q — "+
			"if silence is not askable, the user must edit every connection "+
			"just to be offered the helper", got, ConsentRequired)
	}
}

// TestExplicitAutoIsAskableLikeSilence: auto is the absence of a record, so a
// Machine that states auto must behave exactly as one that states nothing.
func TestExplicitAutoIsAskableLikeSilence(t *testing.T) {
	r := newResolver(withHelperArtifactAvailable(true))

	explicitAuto := Machine{Fingerprint: "SHA256:auto", Mode: hostmode.Auto}
	if got := r.Resolve(explicitAuto); got != ConsentRequired {
		t.Errorf("an explicit auto resolved to %q, want %q", got, ConsentRequired)
	}
}

// TestHelperIsAdditiveNotAlternative states §5.2 as an assertion instead of
// prose: "declining a deployed binary must not also decline shell scripts —
// different risks", and the inverse holds for the same reason. A host at
// helper has allowed the binary; nothing about that answer says they gave up
// the blocks.
//
// It failed in the direction nobody looks: picking the MOST capable mode on
// the axis delivered the LEAST, because the open-time gate had refused helper
// since the days the Tier-B carrier did not exist, and the helper landing did
// not move it (nocx-7k8ma).
//
// Both halves are asserted here, in one test, because separately they are
// each true of a broken product: the resolver alone cannot see that the
// shell was left plain, and the gate alone cannot see that the helper was
// allowed.
func TestHelperIsAdditiveNotAlternative(t *testing.T) {
	// The helper half: a host recorded helper still reaches the binary — but
	// only from the MACHINE's grant, which is the question the two axes
	// separated (ADR-0105, nocx-1kejz.3). This test used to assert that the
	// mode alone resolved to the helper, "with no surface having to ask";
	// that is exactly what let a deployment happen on a machine nobody had
	// answered for, so the granted machine is seeded here and the record is
	// what carries the request.
	store := seedGrantedDocument(t, t.TempDir(), explicitHelper.Fingerprint)
	r := newResolver(withStore(store), withHelperArtifactAvailable(true))
	if got := r.Resolve(explicitHelper); got != DesiredHelper {
		t.Errorf("helper resolved to %q, want %q — a granted machine serves the helper this host asked for", got, DesiredHelper)
	}

	// The scripts half: the same host still publishes the bundle and
	// integrates, exactly as auto and script do.
	if !hostmode.Helper.DeliversScripts() {
		t.Error("a helper session does not integrate — allowing the binary " +
			"silently declined the shell scripts, which §5.2 forbids in both directions")
	}

	// And the opt-out still opts out: raw is the only answer that means
	// "nothing", so the fix above must not have widened into it.
	if hostmode.Raw.DeliversScripts() {
		t.Error("raw integrates — the user's opt-out stopped opting out")
	}
}
