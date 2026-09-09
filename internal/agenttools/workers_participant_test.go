package agenttools

import (
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// A9: the holder's own resources live inside the object. A participant names
// its own mailbox and has no way to express another one, which is what makes
// "cannot read a neighbour's mail" a property of the type rather than of a
// check somebody has to remember to write.
func TestWorkerParticipantNamesOnlyItsOwnMailbox(t *testing.T) {
	p := NewWorkerParticipant("worker-1")
	if got := p.Participant(); got != "worker-1" {
		t.Fatalf("participant = %q, want worker-1", got)
	}
	if got := p.Mailbox(); got != "worker-1" {
		t.Fatalf("mailbox = %q, want the participant's own id", got)
	}
}

// A nil capability answers empty rather than panicking, for the reason
// WorkerCoordinator does: the dispatcher's type switch is what proves the
// distinction exhaustive, and a nil arriving here means that switch was
// bypassed — which must fail as a refusal, not as a crash.
func TestWorkerParticipantNilNamesNothing(t *testing.T) {
	var p *WorkerParticipant
	if got := p.Participant(); got != "" {
		t.Fatalf("nil participant = %q, want empty", got)
	}
	if got := p.Mailbox(); got != "" {
		t.Fatalf("nil mailbox = %q, want empty", got)
	}
}

// The two capabilities are two TYPES and not one with a role flag, so a
// coordinator's capability can never be read as a participant's by a
// consumer that forgot to check a boolean.
func TestGroupCapabilitiesAreTwoTypes(t *testing.T) {
	var coordinator Capability = NewWorkerCoordinator("session-1", nil)
	var participant Capability = NewWorkerParticipant("worker-1")
	if _, ok := coordinator.(*WorkerParticipant); ok {
		t.Fatal("a coordinator capability satisfies *WorkerParticipant")
	}
	if _, ok := participant.(*WorkerCoordinator); ok {
		t.Fatal("a participant capability satisfies *WorkerCoordinator")
	}
}

// narrowWorkerParticipant takes the id from the run context and never from the
// call's arguments. The participant id is backend-owned (A9) and a call that
// could name one would be the ambient dispatcher API ADR-0028 rejects.
func TestNarrowWorkerParticipantTakesTheIDFromTheRunContext(t *testing.T) {
	capability, err := narrowWorkerParticipant(
		content.Grant{}, nil, RunContext{Session: "worker-session", Participant: "worker-1"},
	)
	if err != nil {
		t.Fatalf("narrow: %v", err)
	}
	p, ok := capability.(*WorkerParticipant)
	if !ok {
		t.Fatalf("narrow returned %T, want *WorkerParticipant", capability)
	}
	if p.Participant() != "worker-1" {
		t.Fatalf("narrowed participant = %q, want worker-1", p.Participant())
	}
}

// A run context with no participant is a caller the authorizer did not
// establish as a worker. It is refused at the narrow rather than narrowed to
// an empty capability: an empty participant names mailbox "", and a mailbox
// belonging to nobody must never be reachable.
func TestNarrowWorkerParticipantRefusesARunThatIsNoParticipant(t *testing.T) {
	if _, err := narrowWorkerParticipant(
		content.Grant{}, nil, RunContext{Session: "some-session"},
	); err == nil {
		t.Fatal("narrowing a run with no participant succeeded")
	}
}
