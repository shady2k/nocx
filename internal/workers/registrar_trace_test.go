package workers

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// THE SILENT THIRTY SECONDS (nocx-4l2a5.4).
//
// On 2026-09-09 a coordinator's workers.spawn failed and the backend log held
// two lines about it: a pane had been opened, and half a minute later the
// registration had failed. Which of the six steps was waiting, and for how
// long, could not be read — so the diagnosis was a code read, and it had to be
// done twice.
//
// The assertion is on what a person can READ, which is why it greps the output
// rather than counting calls on a spy.
func TestARegistrationThatNeverEnrolsSaysWhichStepWasWaiting(t *testing.T) {
	var out bytes.Buffer
	h := newHarness(t)
	h.enrol.never = true
	h.reg.log = log.NewSlogAdapter(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))

	if _, err := h.register(context.Background()); err == nil {
		t.Fatal("a registration whose enrolment never arrives must fail")
	}

	written := out.String()
	for _, want := range []string{
		// The step, named while it is still waiting — so a log read DURING
		// the wait says what is happening, not only afterwards.
		"worker: waiting for the participant's enrolment",
		"deadline_ms=50",
		// The outcome, with how long it actually waited.
		"worker: the enrolment never arrived",
		"waited_ms=",
		// And what was undone, because a compensation nobody can see is how a
		// participant outlives its own registration unnoticed.
		"worker: compensating a failed registration",
		"kill_launcher=true",
	} {
		if !strings.Contains(written, want) {
			t.Fatalf("the log does not carry %q:\n%s", want, written)
		}
	}
}

// EVERY LINE OF ONE REGISTRATION BELONGS TO ONE SPAN. That is what makes a
// grep for a trace_id return the whole of it rather than the line somebody
// happened to guess at.
func TestEveryLineOfARegistrationSharesItsTrace(t *testing.T) {
	var out bytes.Buffer
	h := newHarness(t)
	h.reg.log = log.NewSlogAdapter(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))

	caller, callerSpan := log.StartSpan(context.Background())
	if _, err := h.register(caller); err != nil {
		t.Fatalf("register: %v", err)
	}

	written := out.String()
	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(written), "\n") {
		if line == "" {
			continue
		}
		lines++
		if !strings.Contains(line, "trace_id="+callerSpan.TraceID) {
			t.Fatalf("a line of the registration left the caller's trace:\n%s", line)
		}
	}
	if lines == 0 {
		t.Fatalf("the registration wrote nothing:\n%s", written)
	}
}
