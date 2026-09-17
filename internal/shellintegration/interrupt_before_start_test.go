package shellintegration

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// interruptIterations is the production-shaped race: the interrupt is written
// back to back with the command, exactly as a fast Stop follows Enter.
const interruptIterations = 20

// Reproduction for nocx-xn63t.6.1's second half: is "prompt_ready with no
// start ever" — the case internal/lifecycle's applyPromptReady PRIMED-
// attempt invariant exists to survive — actually reachable, or only a
// theoretical worry? Answered here against a REAL bash driving the REAL
// hooks: racing \x03 immediately after a fully-typed command line — unlike
// TestBashInterruptAnnouncesNoPhantomCommand, which interrupts a PARTIAL line
// before Enter is ever pressed.
//
// THE ORACLE IS FOUR CASES, NOT TWO, and that is the fix this test needed
// (CI run 35190081902, ci-backend with-secret-service, iteration 17 of 20).
// It used to ask "did a start arrive?" and "did the submitted line's marker
// evaluate?" and fail when they disagreed, which folds two different facts
// into one verdict: a command DID start (the kernel was truthful — it named
// what bash ran) while the submitted line never ran, because bash executed a
// DIFFERENT command. classifyInterrupt below keeps them apart, and each
// disagreement gets its own failure:
//
//	verdictRanSubmitted  a start arrived, it names the submitted line, and
//	                     the submitted line's marker evaluated
//	verdictNeverStarted  no start, no marker — the interrupt won the race,
//	                     which is the case this test exists to reach
//	verdictLostByte      a start arrived and names something OTHER than the
//	                     submitted line: the shell ran a command the user did
//	                     not submit — its own failure, never folded into
//	                     "the kernel and bash disagree"
//	verdictKernelMissed  the submitted line ran and no start was recorded:
//	                     the kernel's half, and ours
//
// THE ORACLE FOR "did the SUBMITTED LINE run" is not "is the marker text in
// the pty output" (nocx-xn63t.6.1, CI run 35171283722, ci-backend
// with-secret-service): a terminal ECHOES the bytes it is handed as they are
// typed, whether or not the line is ever executed, so `echo RACEMARKi` leaves
// "RACEMARKi" in the output even on an iteration the shell discarded outright
// — that run's own accepted list was hello, prompt_ready, prompt_ready, with
// NO start ever, which is the reachable case working exactly as designed; the
// failure was the test's, not the product's. The oracle instead has to be a
// value the LINE AS TYPED does not contain and only bash's own evaluation
// produces: arithmetic expansion. The typed line carries the literal text
// `$((1+1))`, which a terminal echoes back unevaluated; only an actual
// execution turns it into `2`, so the output contains the marker's EVALUATED
// form if and only if the command really ran.
//
// WHAT verdictLostByte FINDS IS NOT OURS, measured rather than assumed:
// upstream bash resumes the READLINE BUFFER at the index an interrupted parse
// had reached and executes the rest of the line. yy_readline_get() feeds the
// parser one character at a time from current_readline_line[index++]
// (parse.y:1505-1562, the READLINE branch, bash 5.2); a SIGINT noticed at the
// parser's next QUIT check calls throw_to_top_level(), which calls
// reset_parser() (sig.c:400-443); reset_parser() frees the LEXER's copy —
// shell_input_line — but leaves current_readline_line and
// current_readline_line_index untouched (parse.y:3299-3321, where only
// shell_input_line is freed), so the reader loop resumes mid-line and bash
// runs a FRONT-TRUNCATED remainder of the accepted line. The CI run's own
// evidence is exactly that: the kernel's start named
// `cho RACE$((1+1))MARK017` and bash's own error was `Command 'cho' not
// found` — the `e` of `echo` was gone, and the start the kernel reported was
// truthful.
//
// Reproduced directly, with no nocx code in the stand and with it: a
// 3000-argument line (bash's parse of an accepted line is the window the
// interrupt has to land in) raced against \x03 produced
// `bash: x02050: command not found` / `bash: x01710: command not found` —
// front-truncated remainders, cut 16000-20000 bytes into a 21000-byte line —
// 8/8 iterations with no nocx code at all and 7/8 through the real hooks and
// the real kernel (the eighth discarded the line). Those runs had the machine
// otherwise busy, and the reproduction is scheduler-sensitive: on an idle
// machine every synchronization tried — aiming the interrupt at readline's
// deprep, holding bash in [accept, exec] with SIGSTOP, 8-iteration runs of
// each — produced 0/8. SIGSTOP did not reach a clean window either: it left
// readline wedged with the line unrun, so what is established is that an idle
// machine does not reach the window, not that descheduling is the only thing
// that opens it. So the reproduction is recorded with its numbers here and in
// the commit that landed this, and not as a test: a test that silently stops
// reproducing is worse than none. What is deterministic, and what earns its
// place in the suite, is TestInterruptOracleNamesTheLostByte, which drives
// this oracle with that CI run's verbatim evidence.
func TestInterruptRightAfterEnterNeverStartsWithoutASecondPromptReady(t *testing.T) {
	s := startChannelShell(t, "bash", "nocx.bash", bashScript)
	defer s.close()

	neverStarted := 0
	for i := 0; i < interruptIterations; i++ {
		typed := fmt.Sprintf("echo RACE$((1+1))MARK%03d", i) // echoed verbatim, never run
		startsBefore := s.kernel.count("start")
		promptBefore := s.kernel.count("prompt_ready")
		outBefore := len(s.output())

		if _, err := s.ptmx.Write([]byte(typed + "\n")); err != nil {
			t.Fatalf("iter %d: write command: %v", i, err)
		}
		// No synchronization here on purpose — this IS the race: the
		// production sequence is submitAttempt (opens the pending attempt)
		// immediately followed by the command's bytes, with Stop's signal
		// landing some small, uncontrolled interval later. Back-to-back
		// writes from one goroutine are the tightest analogue this harness
		// can produce.
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

		accepted := s.output()[outBefore:]
		startedCmd, started := iterationStart(s.kernel.events(), startsBefore)
		verdict := classifyInterrupt(typed, accepted, startedCmd, started)

		switch verdict {
		case verdictLostByte:
			t.Fatalf("iter %d: %s — %d bytes were submitted (%q), and the start the kernel accepted names %d bytes: %q — %s. "+
				"The kernel is not the liar here: bash's parser resumed a stale readline buffer after the interrupt and executed its remainder, a defect upstream of nocx (see this file's header). "+
				"accepted=%v output_tail=%q",
				i, verdict, len(typed), head(typed, 60), len(startedCmd), head(startedCmd, 120),
				truncationShape(typed, startedCmd), s.kernel.events(), tail(accepted, 400))
		case verdictRanSubmitted:
			// the kernel and the shell agree; nothing to report
		case verdictNeverStarted:
			neverStarted++
		case verdictKernelMissed:
			t.Fatalf("iter %d: %s — the submitted line ran and the kernel recorded no start for it; accepted=%v output_tail=%q",
				i, verdict, s.kernel.events(), tail(accepted, 400))
		}
		t.Logf("iter %02d: %s", i, verdict)

		// Whether or not this line ran, get back to a known prompt before
		// the next iteration: a genuinely swallowed line can leave
		// characters sitting in readline's buffer (nocx-yjen), so send a
		// harmless newline to flush any partial state and resynchronize, and
		// wait for the prompt it produces rather than for a duration. The
		// count is taken BEFORE the write so a prompt that arrives while it
		// is going out is still waited for by the comparison.
		resyncBefore := s.kernel.count("prompt_ready")
		if _, err := s.ptmx.Write([]byte("\n")); err != nil {
			t.Fatalf("iter %d: resync newline: %v", i, err)
		}
		resynced := false
		resyncDeadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(resyncDeadline) {
			if s.kernel.count("prompt_ready") > resyncBefore {
				resynced = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !resynced {
			// The invariant this test states in its own words: the attempt
			// always ends, one way or the other — never a permanent wedge.
			// A resync that never prompts is that wedge.
			t.Fatalf("iter %d: a newline did not bring the shell back to a prompt within 15s — accepted=%v output_tail=%q",
				i, s.kernel.events(), tail(s.output(), 400))
		}
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
		neverStarted, interruptIterations)
}

// interruptVerdict is what one iteration's evidence says happened. Naming all
// four is the oracle: an absent start and a start naming another command are
// opposite findings, and the old two-way comparison reported both as one
// disagreement between the kernel and bash.
type interruptVerdict string

const (
	// verdictRanSubmitted: the submitted line ran, and the start names it.
	verdictRanSubmitted interruptVerdict = "ran_submitted"
	// verdictNeverStarted: nothing ran — the interrupt won the race.
	verdictNeverStarted interruptVerdict = "never_started"
	// verdictLostByte: a start arrived naming something other than the
	// submitted line. The shell executed a command the user did not submit.
	verdictLostByte interruptVerdict = "lost_byte"
	// verdictKernelMissed: the submitted line ran with no start recorded.
	verdictKernelMissed interruptVerdict = "kernel_missed"
)

// classifyInterrupt is the whole oracle. submitted is the line as written to
// the pty (no newline), pty is the pty's bytes since the iteration began,
// startedCmd is the command text of the iteration's start ("" when none), and
// started says whether the kernel accepted one.
func classifyInterrupt(submitted, pty, startedCmd string, started bool) interruptVerdict {
	ranSubmitted := strings.Contains(pty, evaluatedMarker(submitted))
	switch {
	case started && startedCmd == submitted && ranSubmitted:
		return verdictRanSubmitted
	case started && startedCmd != submitted:
		// The start is the shell's own report of what it ran, so a start
		// naming anything else means the shell ran something else — whether
		// or not the marker appears (a front-truncated remainder can still
		// evaluate the marker if the cut lands before it).
		return verdictLostByte
	case started:
		// named the submitted line, which left no trace of having run.
		return verdictKernelMissed
	case ranSubmitted:
		return verdictKernelMissed
	default:
		return verdictNeverStarted
	}
}

// evaluatedMarker is the form the submitted line's marker takes only when
// bash evaluates it: `echo RACE$((1+1))MARK017` prints `RACE2MARK017`, while
// the terminal's echo of the typed line carries `$((1+1))` unevaluated.
func evaluatedMarker(submitted string) string {
	const raw = "RACE$((1+1))MARK"
	i := strings.Index(submitted, raw)
	if i < 0 {
		return "\x00no-marker-in-this-line"
	}
	tail := submitted[i+len(raw):]
	end := 0
	for end < len(tail) && tail[end] >= '0' && tail[end] <= '9' {
		end++
	}
	return "RACE2MARK" + tail[:end]
}

// iterationStart returns the command text of the first start the kernel
// accepted after startsBefore starts, and whether such a start exists.
func iterationStart(evs []kernelEvent, startsBefore int) (string, bool) {
	n := 0
	for _, e := range evs {
		if e.Evt != "start" {
			continue
		}
		n++
		if n <= startsBefore {
			continue
		}
		cmd, _ := e.Body["command"].(string)
		return cmd, true
	}
	return "", false
}

// truncationShape names how the executed text relates to the submitted line,
// so a failure says which defect it saw: the recorded upstream one drops a
// prefix, while any other shape would be a different — possibly ours — defect.
func truncationShape(submitted, executed string) string {
	switch {
	case executed == "":
		return "an empty command text"
	case strings.Contains(submitted, executed):
		return "a front-truncated remainder of the submitted line"
	default:
		return "NOT any part of the submitted line"
	}
}

// TestInterruptOracleNamesTheLostByte pins the verdict that cost CI run
// 35190081902, with that run's own evidence verbatim: the kernel accepted
// `start command:"cho RACE$((1+1))MARK017"` for a line submitted as
// `echo RACE$((1+1))MARK017`, bash's own error named `cho`, and no evaluated
// marker appeared. The old oracle compared "a start arrived" with "the
// submitted line ran", so it reported this as the kernel and the shell
// disagreeing about whether a command ran — indicting the kernel for a defect
// upstream of it — and this test fails under that comparison and passes under
// the four-way one above.
func TestInterruptOracleNamesTheLostByte(t *testing.T) {
	const submitted = "echo RACE$((1+1))MARK017"
	// The echoed line as the pty shows it: `$((1+1))` is still literal.
	echoed := "P# echo RACE$((1+1))MARK017\r\n"
	// bash's error for the command it made of the remainder, plus the eval'd
	// form of a line that did run, for the control cases.
	ranLine := "P# echo RACE$((1+1))MARK017\r\nRACE2MARK017\r\n"

	for _, tc := range []struct {
		name      string
		pty       string
		startedCt string
		started   bool
		want      interruptVerdict
	}{
		{
			name:      "the CI run's lost byte: a start naming a truncated line",
			pty:       echoed + "bash: cho: command not found\r\n",
			startedCt: "cho RACE$((1+1))MARK017",
			started:   true,
			want:      verdictLostByte,
		},
		{
			name:      "a start naming a front-truncated remainder that still evaluates",
			pty:       echoed + "RACE2MARK017\r\n",
			startedCt: "cho RACE$((1+1))MARK017",
			started:   true,
			want:      verdictLostByte,
		},
		{
			name:      "the interrupt won: no start, no evaluation",
			pty:       echoed,
			startedCt: "",
			started:   false,
			want:      verdictNeverStarted,
		},
		{
			name:      "the submitted line ran and the start names it",
			pty:       ranLine,
			startedCt: submitted,
			started:   true,
			want:      verdictRanSubmitted,
		},
		{
			name:      "the submitted line ran with no start recorded",
			pty:       ranLine,
			startedCt: "",
			started:   false,
			want:      verdictKernelMissed,
		},
		{
			name:      "a start naming the submitted line that left no trace",
			pty:       echoed,
			startedCt: submitted,
			started:   true,
			want:      verdictKernelMissed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyInterrupt(submitted, tc.pty, tc.startedCt, tc.started)
			if got != tc.want {
				t.Fatalf("classifyInterrupt(%q, pty=%q, start=%q, started=%v) = %q, want %q",
					submitted, tc.pty, tc.startedCt, tc.started, got, tc.want)
			}
		})
	}
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
