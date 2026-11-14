package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestProgramEndedSentence_ReturnsNamedProcessCancellation(t *testing.T) {
	cause := &cancelCause{
		why:      "the assistant process ended the run",
		sentence: "the run ended while it was waiting, so it was stopped",
	}
	if !errors.Is(cause, context.Canceled) {
		t.Fatalf("cause = %v; named causes must remain cancellations", cause)
	}
	got, ok := ProgramEndedSentence(cause)
	if !ok || got != cause.sentence {
		t.Fatalf("ProgramEndedSentence(%v) = %q, %v; want %q, true", cause, got, ok, cause.sentence)
	}
}

func TestProgramEndedSentence_SaysNothingAboutAnOrdinaryCancellation(t *testing.T) {
	if _, ok := ProgramEndedSentence(context.Canceled); ok {
		t.Fatal("a bare cancellation was reported as a process-ended run")
	}
	if _, ok := ProgramEndedSentence(errors.New("something else")); ok {
		t.Fatal("an unrelated error was reported as a process-ended run")
	}
}

func TestTurnEndedReason_NamesTheEndingSomebodyChose(t *testing.T) {
	for _, reason := range []content.TerminationReason{content.TermUserKilled, content.TermAnswerRevoked} {
		cause := TurnEndedCause(reason)
		if !errors.Is(cause, context.Canceled) {
			t.Fatalf("cause %v must remain a cancellation", cause)
		}
		got, ok := TurnEndedReason(cause)
		if !ok || got != reason {
			t.Fatalf("TurnEndedReason(%v) = %q, %v; want %q, true", cause, got, ok, reason)
		}
	}
}

func TestTurnEndedReason_SaysNothingAboutAnEndingNobodyChose(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.New("something else"), nil} {
		if got, ok := TurnEndedReason(err); ok {
			t.Fatalf("TurnEndedReason(%v) = %q, true; want no reason", err, got)
		}
	}
}
