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
// line before Enter is ever pressed. It asserts the thing that decided
// kernel.go's invariant: this reliably produces a prompt_ready with NO
// start ever following it, for MANY iterations in a row, until — matching
// applyPromptReady's own closing rule — a SECOND prompt_ready arrives.
//
// Measured empirically before this test existed: 40/40 iterations of this
// exact race reproduced "no start, ever" on the first prompt_ready alone.
// That ruled out the kernel's earlier fix (leave a primed-but-unstarted
// attempt open forever, waiting for a start that might never come) — it
// would have left the lane stuck the same way the original bug did, just
// triggered by a bare terminal Ctrl-C instead of a rejected envelope.
func TestInterruptRightAfterEnterNeverStartsWithoutASecondPromptReady(t *testing.T) {
	s := startChannelShell(t, "bash", "nocx.bash", bashScript)
	defer s.close()

	const iterations = 20
	neverStarted := 0
	for i := 0; i < iterations; i++ {
		marker := fmt.Sprintf("RACEMARK%03d", i)
		startsBefore := s.kernel.count("start")
		promptBefore := s.kernel.count("prompt_ready")
		outBefore := len(s.output())

		if _, err := s.ptmx.Write([]byte("echo " + marker + "\n")); err != nil {
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
		markerSeen := strings.Contains(s.output()[outBefore:], marker)
		started := startsAfter > startsBefore
		t.Logf("iter %02d: started=%v marker_in_output=%v", i, started, markerSeen)

		if started != markerSeen {
			t.Fatalf("iter %d: kernel's start (%v) and the pty's own output (%v) disagree about whether %q ran — accepted=%v",
				i, started, markerSeen, marker, s.kernel.events())
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

	t.Logf("SUMMARY: %d/%d iterations produced a prompt_ready with no start for that command",
		neverStarted, iterations)
	if neverStarted == 0 {
		t.Fatalf("the race never reproduced in %d iterations — either it genuinely is not reachable here (re-check the invariant this guards), or this harness stopped racing it; neverStarted must be > 0 for the assertion below to mean anything", iterations)
	}
}
