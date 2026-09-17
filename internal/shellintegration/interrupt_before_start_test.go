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
// THE ORACLE SEPARATES WHAT THE KERNEL RECORDED FROM WHAT BASH EXECUTED, and
// that is the fix this test needed (CI run 35190081902, ci-backend
// with-secret-service, iteration 17 of 20). It used to ask "did a start
// arrive?" and "did the submitted line's marker evaluate?" and fail when they
// disagreed, which folds two different facts into one verdict: a command DID
// start (the kernel was truthful — it named what bash ran) while the line bash
// ran was not the line submitted. classifyInterrupt below keeps them apart:
//
//	verdictAgreement            the start names the submitted line. Whether
//	                            the marker evaluated is NOT part of this: the
//	                            DEBUG start legitimately fires before a SIGINT
//	                            stops the echo, so an exact start with no
//	                            visible evaluation is agreement, not a fault.
//	verdictUpstreamTruncation   the start is a proper suffix of the submitted
//	                            line and bash's own output is consistent with
//	                            executing it: the recorded upstream defect
//	                            (below). Agreement, counted and logged — the
//	                            kernel reported exactly what ran.
//	verdictNeverStarted         no start, no marker — the interrupt won the
//	                            race, which is the case this test exists to reach
//	verdictKernelMissed         the submitted line RAN (its evaluated marker is
//	                            in the pty) and no start was recorded: the
//	                            kernel's half, and ours. This is the only shape
//	                            that proves a missed start.
//	verdictUnknown              a start that is neither the submitted line nor a
//	                            proper suffix of it: a shape nobody has
//	                            accounted for, and possibly ours.
//
// Only verdictKernelMissed and verdictUnknown fail. A truncated start is
// upstream bash's and cannot be fixed here (see below), so the test counts it,
// names it in the SUMMARY line, and keeps the failures for the two shapes that
// would mean the KERNEL is lying about what ran.
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
// WHAT A TRUNCATED START MEANS, and whose it is, measured rather than
// assumed: upstream bash resumes the READLINE BUFFER at the index an
// interrupted parse had reached and executes the rest of the line.
// yy_readline_get() feeds the parser one character at a time from
// current_readline_line[index++] (parse.y:1505-1562, the READLINE branch,
// bash 5.2); a SIGINT noticed at the parser's next QUIT check calls
// throw_to_top_level(), which calls reset_parser() (sig.c:400-443);
// reset_parser() frees the LEXER's copy — shell_input_line — but leaves
// current_readline_line and current_readline_line_index untouched
// (parse.y:3299-3321, where only shell_input_line is freed), so the reader
// loop resumes mid-line and bash runs a FRONT-TRUNCATED remainder of the
// accepted line.
//
// WHY ONE BYTE IS THE SMALLEST CUT, which is what the CI run shows: the QUIT
// check sits immediately after the character fetch (shell_getc() does
// `c = yy_getc (); QUIT;` — parse.y:2393-2396), so a SIGINT that lands after
// exactly one character of the line has been consumed leaves the index at 1
// and bash executes the line minus its first byte. The two escape sequences
// around the interrupt in that trace are readline's own terminal handling and
// not a re-entry of readline: `\e[?2004l` is rl_deprep_terminal
// (rltty.c:680) at the end of the read, and `\e[?2004h` is
// rl_reset_after_signal (signals.c:578) re-prepping the terminal after
// readline's signal cleanup re-raised SIGINT.
//
// The CI run's own evidence is exactly a one-byte cut: the kernel's start
// named `cho RACE$((1+1))MARK017` and bash's own error was
// `Command 'cho' not found` — the `e` of `echo` was gone, and the start the
// kernel reported was truthful.
//
// The wider reproduction below is supporting evidence for the MECHANISM (a
// front-truncated remainder at an arbitrary cut point), not proof of the
// one-byte case: it shows the resume happening, at cuts 16k-20k bytes into a
// 21 KB line. The one-byte case is the CI's trace plus parse.y:2393-2396.
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
// this task's commit messages, and not as a test: a test that silently stops
// reproducing is worse than none. What is deterministic, and what earns its
// place in the suite, is TestInterruptOracleClassifiesWhatBashExecuted, which
// drives this oracle with that CI run's verbatim evidence.
//
// The other half of this defect — nocx's own trigger for the ordering, a
// Ctrl-C held and flushed right behind a submitted command — is being fixed in
// the frontend under nocx-xn63t.6.12; that is why this file, and no file under
// frontend/, is what changed here.
func TestInterruptRightAfterEnterNeverStartsWithoutASecondPromptReady(t *testing.T) {
	s := startChannelShell(t, "bash", "nocx.bash", bashScript)
	defer s.close()

	neverStarted := 0
	truncations := 0
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
		case verdictAgreement:
			// The kernel's start names what bash executed; nothing to report.
		case verdictUpstreamTruncation:
			// Agreement too — the kernel reported exactly the text bash ran —
			// and the text is not what was submitted. Upstream bash's
			// parse-resume (this file's header), counted and named in the
			// SUMMARY rather than failed: nocx cannot fix bash's parser, and a
			// red on every starved machine teaches people to ignore red.
			truncations++
			t.Logf("iter %02d: %s — bash executed %q, a proper suffix of the submitted line (%s)",
				i, verdict, head(startedCmd, 60), truncationShape(typed, startedCmd))
		case verdictNeverStarted:
			neverStarted++
		case verdictKernelMissed:
			t.Fatalf("iter %d: %s — the submitted line RAN (its evaluated marker is in the pty) and the kernel recorded no start for it; accepted=%v output_tail=%q",
				i, verdict, s.kernel.events(), tail(accepted, 400))
		case verdictUnknown:
			t.Fatalf("iter %d: %s — %d bytes were submitted (%q) and the start the kernel accepted names %d bytes: %q — %s. "+
				"That is neither the submitted line nor a proper suffix of it, so it is a shape this test has not accounted for, and possibly ours; accepted=%v output_tail=%q",
				i, verdict, len(typed), head(typed, 60), len(startedCmd), head(startedCmd, 120),
				truncationShape(typed, startedCmd), s.kernel.events(), tail(accepted, 400))
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
	// enforces elsewhere. The truncation count is the upstream defect's
	// frequency when a machine is starved enough to open the window (the CI
	// container: 1 of 20 iterations), and it is the number to watch if the
	// reachability of this race changes.
	t.Logf("SUMMARY: %d/%d iterations produced a prompt_ready with no start for that command; %d executed a proper suffix of the submitted line (upstream bash — see this file's header)",
		neverStarted, interruptIterations, truncations)
}

// interruptVerdict is what one iteration's evidence says happened. Splitting
// these apart is the oracle: the old two-way comparison folded "the kernel's
// start names the line bash ran" and "the line bash ran is the line
// submitted" into one disagreement.
type interruptVerdict string

const (
	// verdictAgreement: the start names the submitted line. The evaluated
	// marker is deliberately NOT part of this: the DEBUG start fires before
	// the command runs, and a SIGINT that stops the echo (or the command)
	// before the marker is printed leaves no marker, which is still
	// agreement between the kernel and the shell.
	verdictAgreement interruptVerdict = "agreement"
	// verdictUpstreamTruncation: the start is a proper suffix of the
	// submitted line and bash's own output is consistent with executing it.
	// The kernel reported exactly what bash ran; bash ran a front-truncated
	// remainder. Upstream bash (this file's header).
	verdictUpstreamTruncation interruptVerdict = "upstream_truncation"
	// verdictNeverStarted: nothing ran — the interrupt won the race, which
	// is the case this test exists to reach.
	verdictNeverStarted interruptVerdict = "never_started"
	// verdictKernelMissed: the submitted line RAN (its evaluated marker is
	// in the pty) with no start recorded. The kernel's half, and ours.
	verdictKernelMissed interruptVerdict = "kernel_missed"
	// verdictUnknown: a start that is neither the submitted line nor a
	// proper suffix of it — a shape nobody has accounted for.
	verdictUnknown interruptVerdict = "unknown"
)

// classifyInterrupt is the whole oracle. submitted is the line as written to
// the pty (no newline), pty is the pty's bytes since the iteration began,
// startedCmd is the command text of the iteration's start ("" when none), and
// started says whether the kernel accepted one.
//
// Only verdictKernelMissed and verdictUnknown are failures: the kernel must
// record every command that ran, and must never name a command that is not
// what ran. A truncated start satisfies both — the kernel named the
// truncation — so it is agreement about what ran, and a statement about what
// was SUBMITTED that only upstream bash can answer for.
func classifyInterrupt(submitted, pty, startedCmd string, started bool) interruptVerdict {
	ranSubmitted := strings.Contains(pty, evaluatedMarker(submitted))
	switch {
	case !started && ranSubmitted:
		// The line ran and no start was recorded: the one shape that proves
		// a missed start.
		return verdictKernelMissed
	case !started:
		return verdictNeverStarted
	case startedCmd == submitted:
		return verdictAgreement
	case startedCmd != "" && len(startedCmd) < len(submitted) &&
		strings.HasSuffix(submitted, startedCmd) && executedConsistentWith(pty, startedCmd):
		return verdictUpstreamTruncation
	default:
		return verdictUnknown
	}
}

// executedConsistentWith reports whether bash's own output agrees with the
// start's command text: when bash reported a command it could not find, that
// command must be this text's first word. Silence is not a contradiction —
// a remainder can begin at a word that exists and runs quietly — but a
// DIFFERENT word would mean the kernel named something bash did not run.
func executedConsistentWith(pty, startedCmd string) bool {
	word, ok := commandNotFoundWord(pty)
	if !ok {
		return true
	}
	return word == firstWord(startedCmd)
}

// commandNotFoundWord extracts the command word from the two shapes a shell
// reports an unknown command in: bash's own `bash: WORD: command not found`
// and the Ubuntu command-not-found handler's `Command 'WORD' not found`.
func commandNotFoundWord(out string) (string, bool) {
	if i := strings.Index(out, "command not found"); i >= 0 {
		line := out[strings.LastIndex(out[:i], "\n")+1 : i]
		line = strings.TrimSpace(line)
		if k := strings.LastIndex(line, " "); k >= 0 {
			line = line[k+1:]
		}
		return strings.TrimSuffix(line, ":"), true
	}
	if i := strings.Index(out, "not found"); i >= 0 {
		if k := strings.LastIndex(out[:i], "Command '"); k >= 0 {
			seg := out[k+len("Command '"):]
			if end := strings.IndexByte(seg, '\''); end >= 0 {
				return seg[:end], true
			}
		}
	}
	return "", false
}

// firstWord is the command word of a shell-reported command text.
func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
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
// in the same vocabulary classifyInterrupt decides in: a PROPER SUFFIX is the
// recorded upstream defect (bash drops a prefix, and the hook may cut the
// remainder further), and anything else is a shape that defect does not
// produce — so a failure never describes an interior fragment or a prefix as
// a front-truncated remainder.
func truncationShape(submitted, executed string) string {
	switch {
	case executed == "":
		return "an empty command text"
	case executed != submitted && strings.HasSuffix(submitted, executed):
		return "a front-truncated remainder of the submitted line"
	default:
		return "NOT a proper suffix of the submitted line"
	}
}

// TestInterruptOracleClassifiesWhatBashExecuted pins every class the oracle
// above distinguishes, the CI run's case among them, with that run's evidence
// verbatim: the kernel accepted `start command:"cho RACE$((1+1))MARK017"` for
// a line submitted as `echo RACE$((1+1))MARK017`, and bash's own error named
// `cho`. The old oracle compared "a start arrived" with "the submitted line
// ran", so it reported that as the kernel and the shell disagreeing about
// whether a command ran — indicting the kernel for a defect upstream of it —
// and this test fails under that comparison and passes under this one.
//
// The classes exist because each is a different claim about who is wrong: an
// exact start is the kernel and the shell agreeing whatever the marker shows;
// a PROPER SUFFIX is the shell having executed a truncated line while the
// kernel reported it truthfully (upstream, counted elsewhere in this file);
// anything else is a shape nobody has accounted for, and a marker with no
// start is the one shape that proves the kernel missed a command.
func TestInterruptOracleClassifiesWhatBashExecuted(t *testing.T) {
	const submitted = "echo RACE$((1+1))MARK017"
	// The echoed line as the pty shows it: `$((1+1))` is still literal.
	echoed := "P# echo RACE$((1+1))MARK017\r\n"
	// bash's error for the command it made of the remainder, and the eval'd
	// form of a line that really ran, for the control cases.
	truncatedRun := echoed + "bash: cho: command not found\r\n"
	ranLine := "P# echo RACE$((1+1))MARK017\r\nRACE2MARK017\r\n"

	for _, tc := range []struct {
		name      string
		pty       string
		startedCt string
		started   bool
		want      interruptVerdict
	}{
		{
			name:      "the start names the submitted line and the marker evaluated",
			pty:       ranLine,
			startedCt: submitted,
			started:   true,
			want:      verdictAgreement,
		},
		{
			// The DEBUG start fires before the command runs, so an interrupt
			// that stops the echo (or the command) leaves no marker. That is
			// still agreement — and calling it a kernel miss was the defect
			// this oracle carried until now.
			name:      "the start names the submitted line and no marker appeared",
			pty:       echoed,
			startedCt: submitted,
			started:   true,
			want:      verdictAgreement,
		},
		{
			name:      "the CI run's case: a start naming a proper suffix bash then failed to find",
			pty:       truncatedRun,
			startedCt: "cho RACE$((1+1))MARK017",
			started:   true,
			want:      verdictUpstreamTruncation,
		},
		{
			name:      "a proper suffix that ran quietly",
			pty:       echoed,
			startedCt: "RACE$((1+1))MARK017",
			started:   true,
			want:      verdictUpstreamTruncation,
		},
		{
			// Not a suffix of the submitted line: bash never ran this text,
			// so the kernel is reporting a command of its own invention.
			name:      "a differing start that is not a suffix",
			pty:       truncatedRun,
			startedCt: "cho RACE$((1+1))MARK019",
			started:   true,
			want:      verdictUnknown,
		},
		{
			// A PREFIX, not a suffix: the submitted line with its tail cut.
			// The upstream defect cuts the front, so this shape is a
			// different one, and containment is not enough to call it ours.
			name:      "a differing start that is a prefix, not a suffix",
			pty:       echoed,
			startedCt: "echo RACE$((1+1))",
			started:   true,
			want:      verdictUnknown,
		},
		{
			// A suffix, but bash's own output names a different command:
			// the pty and the kernel disagree about what ran.
			name:      "a suffix whose command word contradicts bash's report",
			pty:       echoed + "bash: grep: command not found\r\n",
			startedCt: "cho RACE$((1+1))MARK017",
			started:   true,
			want:      verdictUnknown,
		},
		{
			name:      "the interrupt won: no start, no evaluation",
			pty:       echoed,
			startedCt: "",
			started:   false,
			want:      verdictNeverStarted,
		},
		{
			name:      "the submitted line ran with no start recorded",
			pty:       ranLine,
			startedCt: "",
			started:   false,
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
