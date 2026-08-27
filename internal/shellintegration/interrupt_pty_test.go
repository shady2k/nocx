package shellintegration

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/waittest"
)

// Ctrl-C at a prompt, watched from BOTH sides at once: the pty (what the
// renderer sees) and the lifecycle channel (what the kernel sees). It needs
// both, because the defect below was a command the shell announced on the
// channel and marked on the stream, for a command the user never ran — and
// either side alone reads as noise rather than as a phantom.
//
// Built on channel_exec_test.go's fakeKernel/channelShell rather than on a
// second harness of its own: that one already boots the real hooks against a
// real kernel on a real pty, which is the whole of what this needs.

// TestBashInterruptAnnouncesNoPhantomCommand guards nocx-678o.
//
// extdebug makes the DEBUG trap fire inside functions, so it fires for every
// line of __nocx_prompt_command — and the wrapper suppresses that with two
// guards: a command text starting `__nocx_`, and __nocx_in_prompt_command.
// The status capture at the top of that function used to satisfy neither
// (`local __nocx_exit=$?` begins with `local`, and the flag went up four
// lines later), leaving exactly one unguarded command per prompt cycle.
//
// That is invisible after a real command, because the C-marker latch is
// already disarmed by then. After an INTERRUPT it is not: nothing ran, and
// __nocx_precmd armed the latch at the previous prompt. So Ctrl-C announced
// nocx's own line as the user's command — an OSC 133 C on the stream and a
// start/complete pair on the channel, naming `local __nocx_exit=$?` and
// carrying SIGINT's status (130 on bash 5, 1 on bash 3.2).
//
// What is asserted is the contract on both sides at once, as an interval
// with both ends: an interrupt produces a new prompt and NOTHING claiming a
// command ran, AND a real command afterwards is still reported in full —
// without which, suppressing the C marker altogether would pass.
func TestBashInterruptAnnouncesNoPhantomCommand(t *testing.T) {
	s := startChannelShell(t, "bash", "nocx.bash", bashScript)
	defer s.close()
	s.waitForHandshake()

	// The handshake is not readline. prompt_ready is sent from inside
	// __nocx_prompt_command, which runs BEFORE bash displays the prompt, and
	// the B marker rides PS1 — so B is the first byte that means "readline
	// owns the terminal now". Typing on the handshake alone raced it: the
	// line was echoed by the tty driver, the interrupt landed before readline
	// existed, and the test then failed for its own race while reading
	// exactly like the product defect it was written for.
	waittest.WaitForTimeoutDetail(t, "pty output \x1b]133;B", 15*time.Second,
		func() string { return fmt.Sprintf("output: %q", s.output()) },
		func() bool {
			return strings.Contains(s.output(), "\x1b]133;B")
		})

	promptsBefore := strings.Count(s.output(), "\x1b]133;A")
	cBefore := strings.Count(s.output(), "\x1b]133;C")
	startsBefore := s.kernel.count("start")
	completesBefore := s.kernel.count("complete")
	promptReadyBefore := s.kernel.count("prompt_ready")

	// A partial line typed at the prompt, then the interrupt — a user
	// abandoning what they were typing, which is the shape the enhanced
	// input path sends \x03 for (terminal-content.ts's `cancel`).
	if _, err := s.ptmx.Write([]byte("echo abandoned")); err != nil {
		t.Fatalf("write partial line: %v", err)
	}
	// The line has to reach readline before the interrupt does, or the two
	// race and the test sometimes interrupts an empty prompt instead — a
	// weaker case than the one under test.
	waittest.WaitForTimeoutDetail(t, "pty output echo abandoned", 10*time.Second,
		func() string { return fmt.Sprintf("output: %q", s.output()) },
		func() bool {
			return strings.Contains(s.output(), "echo abandoned")
		})

	// The interrupt, and then the fresh prompt it must leave behind: the
	// shell is alive and back at readline. Anchoring on that (rather than on
	// a sleep) is what makes the assertions below non-vacuous — they run
	// only once the interrupt's whole prompt cycle has been observed on both
	// sides. Why the helper re-sends rather than waits longer is its own
	// comment, and it is the whole of nocx-yjen.
	interruptUntilPrompt(t, s, promptsBefore)
	waittest.WaitForTimeoutDetail(t, "prompt_ready after the interrupt", 15*time.Second,
		func() string {
			return fmt.Sprintf("want %d, have %d; accepted=%v output=%q",
				promptReadyBefore+1, s.kernel.count("prompt_ready"), s.kernel.events(), s.output())
		},
		func() bool {
			return s.kernel.count("prompt_ready") >= promptReadyBefore+1
		})

	if got := strings.Count(s.output(), "\x1b]133;C") - cBefore; got != 0 {
		t.Errorf("the interrupt emitted %d OSC 133 C marker(s) — a command start for a command the user never ran\noutput: %q",
			got, s.output())
	}
	if got := s.kernel.count("start") - startsBefore; got != 0 {
		t.Errorf("the interrupt announced %d start frame(s) to the kernel: %v", got, s.kernel.events())
	}
	if got := s.kernel.count("complete") - completesBefore; got != 0 {
		t.Errorf("the interrupt announced %d completion(s) for a command that never started: %v", got, s.kernel.events())
	}

	// The closing end of the interval: the guard must not have silenced a
	// REAL command. `run` waits for the completion and the prompt after it,
	// so reaching this line at all is most of the assertion.
	s.run("echo AFTERMARK")
	if got := s.kernel.count("start") - startsBefore; got != 1 {
		t.Errorf("the command after the interrupt must be announced exactly once; got %d start frame(s): %v",
			got, s.kernel.events())
	}
	if !strings.Contains(s.output(), "AFTERMARK") {
		t.Errorf("the command after the interrupt never ran; output: %q", s.output())
	}
}

// interruptUntilPrompt writes the interrupt the frontend writes — \x03 into
// the pty, terminal-content.ts's `cancel` — and returns when the shell has
// come back to a prompt, re-sending it if the shell did not take the first
// one. Bounded by the same 15 seconds the single wait used; nothing here is
// a longer deadline.
//
// WHY IT IS A LOOP, which is the whole of nocx-yjen. A ^C at a bash prompt
// does not always reach the command loop. Roughly one run in a hundred, with
// the test container starved to a single CPU, the wire shows the interrupt
// being handled and then abandoned:
//
//	^C \e[?2004l \r \e[?2004h        and nothing, ever again
//
// which is readline echoing the signal character, running its signal
// cleanup, and re-prepping the terminal — with bash's abort never following.
// No newline, no PROMPT_COMMAND, no A marker. /proc says the shell is back
// in pselect; the pty's Lflag is readline's own 0x8a31 the whole time; the
// line is still in readline's buffer (a later Enter runs it, minus its first
// character). The shell is not slow, it is waiting for another key — and the
// 15-second timeout the bead recorded was reporting exactly that.
//
// It is NOT nocx's, and that is why this is a test change and not a script
// change. Measured in the same container with the same byte pattern against
// a PLAIN interactive bash carrying no integration at all — no hooks, no
// PROMPT_COMMAND, no DEBUG trap: same absorbed interrupt, same missing
// prompt, same eaten first character, once in 120 rounds. Adding an
// observable that proves readline is idle before the interrupt does not help
// either: driving readline to a clear-screen redraw first (so the read-ahead
// is provably drained and the terminal provably readline's) still hung three
// times in 600 rounds. So there is nothing for the shell scripts to report
// and nothing for them to fix.
//
// What this test owns is the other half: whatever bash does with the
// interrupt, nocx must not announce a command for it. The assertions below
// count deltas from before the FIRST interrupt, so N interrupts are as
// strong a case as one — none of them may emit a C marker or a start frame.
// A slow machine simply sends the interrupt again and still passes, which is
// the property a duration-based wait could never have.
func interruptUntilPrompt(t *testing.T, s *channelShell, promptsBefore int) {
	t.Helper()
	if _, err := s.ptmx.Write([]byte("\x03")); err != nil {
		t.Fatalf("write interrupt: %v", err)
	}
	nextRetry := time.Now().Add(3 * time.Second)
	waittest.WaitForTimeoutDetail(t, "prompt after repeated interrupts", 15*time.Second,
		func() string {
			return fmt.Sprintf("OSC 133 A still %d; accepted=%v output=%q",
				strings.Count(s.output(), "\x1b]133;A"), s.kernel.events(), s.output())
		},
		func() bool {
			if strings.Count(s.output(), "\x1b]133;A") > promptsBefore {
				return true
			}
			if time.Now().After(nextRetry) {
				if _, err := s.ptmx.Write([]byte("\x03")); err != nil {
					t.Fatalf("write retry interrupt: %v", err)
				}
				t.Logf("the shell took no prompt from the interrupt; sending it again")
				nextRetry = time.Now().Add(3 * time.Second)
			}
			return false
		})
}
