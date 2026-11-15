package assistant

// The approval chain's ledger thread (nocx-z9hj4, nocx-5dldy): an escalation
// is RECORDED — the proposal is an action entry whose payload names the exact
// binding, with its own interrupted attempt, and the approved call runs as a
// SUBSEQUENT attempt of that same entry (ADR-0020 decision 4). Asserted
// against a REAL content store by reading the thread back, never by
// asserting the writes (acceptance criterion 5).

import (
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// term dereferences an execution's termination reason for assertion.
func TestApprovalStore_Seam(t *testing.T) {
	store := NewApprovalStore()
	ap := Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: "hash-a", EntryID: "entry-proposal"}

	if store.IsPending(ap) || store.IsApproved(ap) {
		t.Fatal("a proposal nobody was asked about is pending or approved")
	}
	// A yes to a question nobody was asked records nothing.
	if store.Approve(ap) {
		t.Fatal("Approve of an unknown proposal returned true — a stale or unknown approval id must not resume anything")
	}
	if store.IsApproved(ap) {
		t.Fatal("Approve of an unknown proposal created an approval")
	}

	store.Request(ap)
	if !store.IsPending(ap) {
		t.Fatal("Requested proposal is not pending")
	}
	if eid, ok := store.EntryIDFor(ap); !ok || eid != "entry-proposal" {
		t.Fatalf("EntryIDFor = %q/%v, want the recorded proposal entry", eid, ok)
	}

	// The wire's approve carries only the five binding fields; the entry the
	// proposal was recorded under is the pending record's own.
	if !store.Approve(Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1", ArgHash: "hash-a"}) {
		t.Fatal("Approve of the pending proposal returned false")
	}
	if store.IsPending(ap) {
		t.Fatal("an answered proposal is still pending")
	}
	if !store.IsApproved(ap) {
		t.Fatal("the approved proposal is not approved")
	}
	if eid, ok := store.EntryIDFor(ap); !ok || eid != "entry-proposal" {
		t.Fatalf("EntryIDFor after approve = %q/%v, want the proposal entry preserved", eid, ok)
	}
	// A second approve of the same proposal answers nothing new.
	if store.Approve(ap) {
		t.Fatal("a second yes was recorded as a fresh decision")
	}
}

func TestApprovalStore_InvocationForPreservesUnresolvedResources(t *testing.T) {
	store := NewApprovalStore()
	invocation := content.Invocation{
		Commands: [][]string{{"cat", "$LOGFILE"}},
		Parsed:   true,
		Resources: content.ResourceReport{Unresolved: []content.UnresolvedResource{{
			Path: "$LOGFILE", Verb: content.ResourceRead,
			Reason: "could not resolve $LOGFILE without executing shell expansion",
		}}},
	}
	ap := Approval{
		RunID: "run-dynamic", Attempt: 1, Tool: "session.run",
		CallID: "call-dynamic", ArgHash: "hash-dynamic",
		CommandInvocation: true, Invocation: invocation,
	}
	store.Request(ap)

	got, parsed, command := store.InvocationFor(ap)
	if !parsed || !command {
		t.Fatalf("InvocationFor flags = parsed %v, command %v, want true/true", parsed, command)
	}
	if len(got.Resources.Unresolved) != 1 ||
		got.Resources.Unresolved[0].Path != "$LOGFILE" {
		t.Fatalf("InvocationFor resources = %+v, want the unresolved shell variable", got.Resources)
	}
	saved := content.InvocationRule{
		Selector: content.InvocationSelector{Exact: invocation.Commands},
		Decision: content.DecisionPermit,
	}
	if saved.Matches(got) {
		t.Fatal("a cloned unresolved invocation matched a standing rule")
	}
}

func TestApprovalStore_DeclinePreservesEffectForStandingWrite(t *testing.T) {
	store := NewApprovalStore()
	ap := Approval{
		RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call_1",
		ArgHash: "hash-a",
	}
	store.Request(ap)
	store.NoteEffect(Approval{
		RunID: ap.RunID, Attempt: ap.Attempt, Tool: ap.Tool, CallID: ap.CallID,
		ArgHash: ap.ArgHash, Effect: content.EffectObserve,
	})

	if !store.Decline(ap, DeclineCallAlways) {
		t.Fatal("Decline of the pending proposal returned false")
	}
	if effect, ok := store.EffectFor(ap); !ok || effect != content.EffectObserve {
		t.Fatalf("EffectFor after decline = %q/%v, want observe/true for the standing-write path", effect, ok)
	}
	if store.Decline(ap, DeclineCallAlways) {
		t.Fatal("a second Decline of the same proposal won after the first settlement")
	}
}

// ── criterion 5 / nocx-5dldy: the thread, read back from a real store ─────

// TestAsk_EscalationRecordsTheProposalThread: an escalation is a ledger
// fact — one action entry whose payload names tool, effect, arguments and
// the exact binding, and its own attempt closed interrupted. The call that
// is asking has NOT run: no artifact, no completed attempt.
//
// The assertions run INSIDE the question, from the approver's own seam: that
// is the only moment at which the proposal exists and the call has not run,
// and a test that read the store after the answer would be reading a call
// that already happened.
