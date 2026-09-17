package shellintegration

// Reproduction for nocx-xn63t.6.1's second half: is "prompt_ready with no
// start ever" — the case internal/lifecycle's applyPromptReady PRIMED-
// attempt invariant exists to survive — actually reachable, or only a
// theoretical worry? Answered here against a REAL bash driving the REAL
// hooks: racing \x03 immediately after a fully-typed command line.

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestInterruptRightAfterEnterNeverStartsWithoutASecondPromptReady races
// \x03 against a just-submitted, fully-typed command line — unlike
// TestBashInterruptAnnouncesNoPhantomCommand, which interrupts a PARTIAL
// line before Enter is ever pressed. It asserts, for every iteration
// (never one chosen outcome — the race can land either way, and a test
// that demanded a specific side is itself timing-dependent): the kernel's
// own `start` count agrees with whether the command actually ran, and the
// shell always reaches a fresh prompt afterward (the attempt always ends,
// one way or the other — never a permanent wedge).
//
// THE ORACLE FOR "did it actually run" IS NOT "is the marker text in the
// pty output" (nocx-xn63t.6.1, CI run 35171283722, ci-backend
// with-secret-service): a terminal ECHOES the bytes it is handed as they
// are typed, whether or not the line is ever executed, so `echo RACEMARKi`
// leaves "RACEMARKi" in the output even on an iteration the shell
// discarded outright — that run's own accepted list was hello,
// prompt_ready, prompt_ready, with NO start ever, which is the reachable
// case working exactly as designed; the failure was the test's, not the
// product's. The oracle instead has to be a value the LINE AS TYPED does
// not contain and only bash's own evaluation produces: arithmetic
// expansion. The typed line carries the literal text `$((1+1))`, which a
// terminal echoes back unevaluated; only an actual execution turns it into
// `2`, so the output contains the marker's EVALUATED form if and only if
// the command really ran.
func TestInterruptRightAfterEnterNeverStartsWithoutASecondPromptReady(t *testing.T) {
	s := startChannelShell(t, "bash", "nocx.bash", bashScript)
	defer s.close()

	const iterations = 20
	neverStarted := 0
	for i := 0; i < iterations; i++ {
		typedMarker := fmt.Sprintf("RACE$((1+1))MARK%03d", i) // echoed verbatim, never run
		ranMarker := fmt.Sprintf("RACE2MARK%03d", i)          // present only if bash evaluated it
		startsBefore := s.kernel.count("start")
		promptBefore := s.kernel.count("prompt_ready")
		outBefore := len(s.output())

		if _, err := s.ptmx.Write([]byte("echo " + typedMarker + "\n")); err != nil {
			t.Fatalf("iter %d: write command: %v", i, err)
		}
		// No synchronization here on purpose — this IS the race: the
		// production sequence is submitAttempt (opens the pending
		// attempt) immediately followed by the command's bytes, with
		// Stop's signal landing some small, uncontrolled interval later.
		// Back-to-back writes from one goroutine are the tightest analogue
		// this harness can produce.
		if _, err := s.ptmx.Write([]byte("\x03")); err != nil {
			t.Fatalf("iter %d: write interrupt: %v", i, err)
		}

		// interrupt_pty_test.go's interruptUntilPrompt documents readline
		// occasionally absorbing \x03 with no abort ever following —
		// roughly 1 in 100, worse under a starved container CPU, and
		// reproduced there against plain upstream bash carrying no nocx
		// hooks at all. That is a readline liveness issue, not this race,
		// so the same mitigation applies: resend the interrupt (never the
		// command) on the same schedule before concluding a settle never
		// came.
		settled := false
		deadline := time.Now().Add(15 * time.Second)
		nextRetry := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if s.kernel.count("prompt_ready") > promptBefore {
				settled = true
				break
			}
			if time.Now().After(nextRetry) {
				if _, err := s.ptmx.Write([]byte("\x03")); err != nil {
					t.Fatalf("iter %d: resend interrupt: %v", i, err)
				}
				nextRetry = time.Now().Add(3 * time.Second)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !settled {
			t.Fatalf("iter %d: never reached a fresh prompt_ready within 15s — accepted=%v output_tail=%q",
				i, s.kernel.events(), s.output()[outBefore:])
		}

		startsAfter := s.kernel.count("start")
		ranForReal := strings.Contains(s.output()[outBefore:], ranMarker)
		started := startsAfter > startsBefore
		t.Logf("iter %02d: started=%v ran_for_real=%v", i, started, ranForReal)

		// THE INVARIANT, true for either side of the race: the kernel's own
		// record of whether a command started must agree with whether bash
		// actually evaluated it — never "it always starts" or "it never
		// starts", either of which would make this test as timing-dependent
		// as the race it is watching.
		if started != ranForReal {
			t.Fatalf("iter %d: kernel's start (%v) and bash's own evaluation (%v) disagree about whether %q ran — accepted=%v output_tail=%q",
				i, started, ranForReal, typedMarker, s.kernel.events(), s.output()[outBefore:])
		}
		if !started {
			neverStarted++
		}

		// Whether or not this line ran, get back to a known prompt before
		// the next iteration: a genuinely swallowed line can leave
		// characters sitting in readline's buffer (nocx-yjen), so send a
		// harmless newline to flush any partial state and resynchronize.
		if _, err := s.ptmx.Write([]byte("\n")); err != nil {
			t.Fatalf("iter %d: resync newline: %v", i, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Informational only, deliberately not asserted: WHICH side of the race
	// each iteration lands on is exactly the timing this test must not
	// depend on — a fixed expectation here ("must reproduce N times") would
	// make the test as environment-sensitive as the race it watches. The
	// invariant above (checked every iteration, unconditionally) is the
	// whole test; this line is for a human reading -v output. Measured
	// directly on this machine: 40/40, then 20/20, iterations produced no
	// start — evidence the race is reachable here, not a floor this test
	// enforces elsewhere.
	t.Logf("SUMMARY: %d/%d iterations produced a prompt_ready with no start for that command",
		neverStarted, iterations)
}
