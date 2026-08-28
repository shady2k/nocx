package assistant

import (
	"context"
	"errors"
	"testing"
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
