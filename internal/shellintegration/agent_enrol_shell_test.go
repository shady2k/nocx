package shellintegration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/waittest"
)

// fakeAgentBody is a stand-in for a coding agent: it prints a sentinel and
// exits. The sentinel is what proves the user's program actually ran, which is
// the half of every assertion below that is about the person rather than about
// the protocol.
const fakeAgentBody = "#!/bin/sh\necho AGENT-RAN\n"

// waitForEvent blocks until an event of this kind is accepted, and returns it.
func waitForEvent(t *testing.T, k *nestedKernel, evt string) kernelEvent {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range k.events() {
			if e.Evt == evt {
				return e
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the kernel never accepted %q; accepted=%v", evt, k.events())
	return kernelEvent{}
}

func hasEvent(k *nestedKernel, evt string) bool {
	for _, e := range k.events() {
		if e.Evt == evt {
			return true
		}
	}
	return false
}

// THE ENROLMENT ACT, end to end on a real pty: the user types the agent's name
// in a nocx pane, and the pane is enrolled for exactly as long as the agent
// runs.
//
// Both ends are asserted, and that is the point rather than thoroughness. The
// AD-6 amendment permits a backend grid only inside an interval, and AGENTS.md
// names the failure this guards: an invariant written with a start and no
// close buys a test that guards only the start. So the enrolment is asserted
// BEFORE the agent's output and the withdrawal AFTER it, in that order, on the
// same request id.
func TestBashAgentWrapperEnrolsForTheAgentsLifetimeOnly(t *testing.T) {
	agentWrapperEnrolsForTheAgentsLifetimeOnly(t, startNestedBashParent)
}

// The zsh twin. macOS's default login shell is zsh, so a wrapper that works
// only in bash works for nobody on the platform this product is built for
// first — and the two scripts are separate implementations of one protocol,
// which is exactly the shape where a parity test earns its keep.
func TestZshAgentWrapperEnrolsForTheAgentsLifetimeOnly(t *testing.T) {
	agentWrapperEnrolsForTheAgentsLifetimeOnly(t, startNestedZshParent)
}

type nestedParentStarter func(*testing.T, *nestedKernel, string, string) *channelShell

func agentWrapperEnrolsForTheAgentsLifetimeOnly(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k := newNestedKernel(t)
	// No explicit close: startNestedBashParent registers its own cleanup, and
	// channelShell.close is for the listener-based harness (it dereferences a
	// listener this one does not have).
	s := start(t, k, "claude", fakeAgentBody)

	if hasEvent(k, "agent_enrol") {
		t.Fatal("a pane was enrolled before anybody asked; enrolment must be an act")
	}

	// The control, in the same test: an ordinary command in an orchestrated
	// pane enrols nothing. Without it a green run could mean "the wrapper
	// enrols" or "this pane enrols whatever it is asked to run".
	s.run("echo ordinary")
	if hasEvent(k, "agent_enrol") {
		t.Fatal("an ordinary command enrolled the pane; only the agent wrapper may")
	}

	if _, err := s.ptmx.Write([]byte("claude\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}

	enrol := waitForEvent(t, k, "agent_enrol")
	if got, _ := enrol.Body["agent"].(string); got != "claude" {
		t.Errorf("enrolment named agent %q, want claude", got)
	}
	rid, _ := enrol.Body["request"].(string)
	if rid == "" {
		t.Error("the enrolment carried no request id, so no answer can be matched to it")
	}
	// The geometry is what the grid starts at, and a grid at zero columns
	// answers about nothing. The exact size is the pty's and not this test's
	// to pin; that it is a size at all is the assertion.
	cols, _ := enrol.Body["cols"].(float64)
	rows, _ := enrol.Body["rows"].(float64)
	if cols <= 0 || rows <= 0 {
		t.Errorf("enrolment geometry = %vx%v, want a real size", cols, rows)
	}

	// The user's program ran. Without this the test would pass just as well
	// for a wrapper that enrolled a pane and never started the agent.
	waitFor := time.Now().Add(10 * time.Second)
	for time.Now().Before(waitFor) && !strings.Contains(s.output(), "AGENT-RAN") {
		time.Sleep(25 * time.Millisecond)
	}
	if !strings.Contains(s.output(), "AGENT-RAN") {
		t.Fatalf("the agent never ran; output=%q", s.output())
	}

	withdraw := waitForEvent(t, k, "agent_withdraw")
	if got, _ := withdraw.Body["request"].(string); got != rid {
		t.Errorf("withdrawal names request %q, want the enrolment's %q — an interval whose ends do not match is two intervals", got, rid)
	}
	if withdraw.Seq <= enrol.Seq {
		t.Errorf("the withdrawal (seq %d) did not follow the enrolment (seq %d)", withdraw.Seq, enrol.Seq)
	}
	if k.rejectedCount() != 0 {
		t.Errorf("the kernel rejected %d frames; the enrolment pair must be ordinary authenticated traffic", k.rejectedCount())
	}
}

// FAILURE IS CLOSED AND VISIBLE, and the agent still runs.
//
// "No enrolment, no orchestration, and the pane says so" (D4) has two halves
// that are easy to collapse into one. It does not mean nocx declines to start
// the program the user asked for — a terminal that refuses a command because a
// feature of its own is unavailable is worse than one without the feature. It
// means the refusal reaches the person, in the pane, and not only a log line
// nobody reads, which is the silent-degrade shape AGENTS.md names.
func TestBashAgentWrapperSaysSoWhenEnrolmentIsRefused(t *testing.T) {
	agentWrapperSaysSoWhenEnrolmentIsRefused(t, startNestedBashParent)
}

func TestZshAgentWrapperSaysSoWhenEnrolmentIsRefused(t *testing.T) {
	agentWrapperSaysSoWhenEnrolmentIsRefused(t, startNestedZshParent)
}

func agentWrapperSaysSoWhenEnrolmentIsRefused(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k := newNestedKernel(t)
	k.refuseEnrolment = true
	k.enrolReason = "nocx is already watching too many panes"
	// No explicit close: startNestedBashParent registers its own cleanup, and
	// channelShell.close is for the listener-based harness (it dereferences a
	// listener this one does not have).
	s := start(t, k, "claude", fakeAgentBody)

	if _, err := s.ptmx.Write([]byte("claude\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	waitForEvent(t, k, "agent_enrol")

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := s.output()
		if strings.Contains(out, "AGENT-RAN") && strings.Contains(out, "not orchestrated") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	out := s.output()
	if !strings.Contains(out, "not orchestrated") {
		t.Errorf("the refusal never reached the pane; output=%q", out)
	}
	if !strings.Contains(out, k.enrolReason) {
		t.Errorf("the refusal reached the pane without the backend's reason; output=%q", out)
	}
	if !strings.Contains(out, "AGENT-RAN") {
		t.Errorf("a refused enrolment stopped the user's agent from running; output=%q", out)
	}
	// And the agent ran EXACTLY once. A wrapper that falls back by running the
	// command a second time is the defect nocx-tyyo names on the grant path.
	if n := strings.Count(out, "AGENT-RAN"); n != 1 {
		t.Errorf("the agent ran %d times, want exactly 1; output=%q", n, out)
	}
	// Nothing was enrolled, so nothing may be withdrawn: a withdrawal for an
	// interval that never opened would close somebody else's.
	if hasEvent(k, "agent_withdraw") {
		t.Error("a refused enrolment still sent a withdrawal")
	}
}

// ── The question comes first (nocx-cyhfw) ────────────────────────────────
//
// The first start of an agent nobody has answered for used to be REFUSED the
// moment the question was raised, and the wrapper did what it does with any
// refusal: it started the agent without tools while the dialog was still on
// screen, and told the person to start it again after answering. The owner's
// rule is the other way round — ask first, then start.

// questionAgentBody is an agent that says whether the question had closed when
// it started. The test creates the flag immediately BEFORE it closes the
// question, so an agent started while the person was still reading prints the
// BEFORE line: "the agent waited" is an assertion about order, not about how
// long the test looked away.
func questionAgentBody(flag string) string {
	return "#!/bin/sh\nif [ -e '" + flag + "' ]; then echo AGENT-RAN-AFTER-ANSWER; else echo AGENT-RAN-BEFORE-ANSWER; fi\n"
}

func waitForOutput(t *testing.T, s *channelShell, text string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.output(), text) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the pane never showed %q; output=%q", text, s.output())
}

func eventsOf(k *nestedKernel, evt string) []kernelEvent {
	var out []kernelEvent
	for _, e := range k.events() {
		if e.Evt == evt {
			out = append(out, e)
		}
	}
	return out
}

// raiseQuestion types the agent's name into a pane whose kernel answers with a
// question, and returns once the pane says it is waiting on it.
func raiseQuestion(t *testing.T, start nestedParentStarter, configure func(*nestedKernel)) (*nestedKernel, *channelShell, string) {
	t.Helper()
	k := newNestedKernel(t)
	k.pendingEnrolment = true
	if configure != nil {
		configure(k)
	}
	flag := filepath.Join(t.TempDir(), "question-closed")
	s := start(t, k, "claude", questionAgentBody(flag))
	if _, err := s.ptmx.Write([]byte("claude\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	waitForEvent(t, k, "agent_enrol")
	// The consent dialog already asks the question, so the terminal does not
	// repeat that instruction.
	if out := s.output(); strings.Contains(out, "answer it in nocx") {
		t.Fatalf("the terminal repeated the consent question; output=%q", out)
	}
	return k, s, flag
}

func closeQuestionWith(t *testing.T, k *nestedKernel, flag, reason string) {
	t.Helper()
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	k.closeQuestion(reason)
}

func TestBashAgentWrapperWaitsForTheAnswerBeforeTheAgentStarts(t *testing.T) {
	agentWrapperWaitsForTheAnswerBeforeTheAgentStarts(t, startNestedBashParent)
}

func TestZshAgentWrapperWaitsForTheAnswerBeforeTheAgentStarts(t *testing.T) {
	agentWrapperWaitsForTheAnswerBeforeTheAgentStarts(t, startNestedZshParent)
}

func agentWrapperWaitsForTheAnswerBeforeTheAgentStarts(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k, s, flag := raiseQuestion(t, start, nil)
	if out := s.output(); strings.Contains(out, "not orchestrated") {
		t.Fatalf("a question on screen was reported as a refusal; output=%q", out)
	}

	closeQuestionWith(t, k, flag, "")
	waitForOutput(t, s, "AGENT-RAN-")
	withdraw := waitForEvent(t, k, "agent_withdraw")

	out := s.output()
	if strings.Contains(out, "AGENT-RAN-BEFORE-ANSWER") {
		t.Fatalf("the agent started while the question was still on screen; output=%q", out)
	}
	if n := strings.Count(out, "AGENT-RAN-AFTER-ANSWER"); n != 1 {
		t.Errorf("the agent ran %d times after the answer, want exactly 1; output=%q", n, out)
	}
	if strings.Contains(out, "not orchestrated") {
		t.Errorf("an answered question left the agent unorchestrated; output=%q", out)
	}
	// Twice: once to raise the question, once to be admitted by its answer.
	// The interval is the SECOND enrolment's, so that is what is withdrawn.
	enrols := eventsOf(k, "agent_enrol")
	if len(enrols) != 2 {
		t.Fatalf("the pane sent %d enrolments, want 2 — the question, then the admission", len(enrols))
	}
	if got, want := withdraw.Body["request"], enrols[1].Body["request"]; got != want {
		t.Errorf("the withdrawal names %v, want the enrolment that opened the interval, %v", got, want)
	}
}

func TestBashAgentWrapperStartsWithoutToolsAfterANo(t *testing.T) {
	agentWrapperStartsWithoutToolsAfterANo(t, startNestedBashParent)
}

func TestZshAgentWrapperStartsWithoutToolsAfterANo(t *testing.T) {
	agentWrapperStartsWithoutToolsAfterANo(t, startNestedZshParent)
}

// A no is the owner's "start it without tools": the agent the person typed
// still runs, after the answer, and the pane says why it has no tools.
func agentWrapperStartsWithoutToolsAfterANo(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k, s, flag := raiseQuestion(t, start, func(k *nestedKernel) {
		k.refuseAfterQuestion = true
		k.enrolReason = "agent approval was denied for claude"
	})

	closeQuestionWith(t, k, flag, "")
	waitForOutput(t, s, "AGENT-RAN-")

	out := s.output()
	if strings.Contains(out, "AGENT-RAN-BEFORE-ANSWER") {
		t.Fatalf("the agent started while the question was still on screen; output=%q", out)
	}
	if n := strings.Count(out, "AGENT-RAN-AFTER-ANSWER"); n != 1 {
		t.Errorf("the agent ran %d times after the answer, want exactly 1; output=%q", n, out)
	}
	if !strings.Contains(out, "nocx: not orchestrated — agent approval was denied for claude") {
		t.Errorf("the pane did not say why the agent has no tools; output=%q", out)
	}
	if n := len(eventsOf(k, "agent_enrol")); n != 2 {
		t.Errorf("the pane sent %d enrolments, want 2 — the question, then the one the answer refused", n)
	}
	if hasEvent(k, "agent_withdraw") {
		t.Error("a refused enrolment still sent a withdrawal")
	}
}

func TestBashAgentWrapperStartsWithoutToolsWhenNobodyCouldBeAsked(t *testing.T) {
	agentWrapperStartsWithoutToolsWhenNobodyCouldBeAsked(t, startNestedBashParent)
}

func TestZshAgentWrapperStartsWithoutToolsWhenNobodyCouldBeAsked(t *testing.T) {
	agentWrapperStartsWithoutToolsWhenNobodyCouldBeAsked(t, startNestedZshParent)
}

// A question that closes with a sentence was never answered: the pane prints
// the sentence and starts the agent without tools, and does NOT enrol again —
// that would raise the same undeliverable question and wait on it for ever.
func agentWrapperStartsWithoutToolsWhenNobodyCouldBeAsked(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k, s, flag := raiseQuestion(t, start, nil)

	closeQuestionWith(t, k, flag, "nocx could not ask whether claude may use its tools: no window is open")
	waitForOutput(t, s, "AGENT-RAN-")

	out := s.output()
	if n := strings.Count(out, "AGENT-RAN-AFTER-ANSWER"); n != 1 {
		t.Errorf("the agent ran %d times, want exactly 1; output=%q", n, out)
	}
	if !strings.Contains(out, "nocx: not orchestrated — nocx could not ask whether claude may use its tools: no window is open") {
		t.Errorf("the pane did not say why the agent has no tools; output=%q", out)
	}
	if n := len(eventsOf(k, "agent_enrol")); n != 1 {
		t.Errorf("the pane sent %d enrolments, want 1 — an unaskable question is not raised again", n)
	}
	if hasEvent(k, "agent_withdraw") {
		t.Error("an enrolment that never opened still sent a withdrawal")
	}
}

func TestBashAgentWrapperCancelsTheLaunchOnCtrlC(t *testing.T) {
	agentWrapperCancelsTheLaunchOnCtrlC(t, startNestedBashParent)
}

func TestZshAgentWrapperCancelsTheLaunchOnCtrlC(t *testing.T) {
	agentWrapperCancelsTheLaunchOnCtrlC(t, startNestedZshParent)
}

// Ctrl+C while the question is up is the owner's "cancel": the agent does not
// start, the command reports the interrupt, and the answer arriving afterwards
// starts nothing — the person already said they did not want that launch.
func agentWrapperCancelsTheLaunchOnCtrlC(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k, s, flag := raiseQuestion(t, start, nil)

	if _, err := s.ptmx.Write([]byte{0x03}); err != nil {
		t.Fatalf("press Ctrl+C: %v", err)
	}
	if _, err := s.ptmx.Write([]byte("echo \"RC=$?\"\n")); err != nil {
		t.Fatalf("type echo: %v", err)
	}
	waitForOutput(t, s, "RC=130")
	// The status alone cannot tell the cancel from a shell that aborted the
	// wrapper on an untrapped interrupt, which bash also reports as 130.
	if out := s.output(); !strings.Contains(out, "nocx: cancelled — claude was not started") {
		t.Errorf("the pane did not say the launch was cancelled; output=%q", out)
	}

	closeQuestionWith(t, k, flag, "")
	// Arithmetic in the line, so the pane's echo of what was typed cannot
	// satisfy the wait for what the shell printed.
	if _, err := s.ptmx.Write([]byte("echo \"STILL-HERE-$((20+22))\"\n")); err != nil {
		t.Fatalf("type echo: %v", err)
	}
	waitForOutput(t, s, "STILL-HERE-42")

	out := s.output()
	if strings.Contains(out, "AGENT-RAN") {
		t.Fatalf("a cancelled launch started the agent; output=%q", out)
	}
	if n := len(eventsOf(k, "agent_enrol")); n != 1 {
		t.Errorf("the pane sent %d enrolments, want 1 — a cancelled launch enrols nothing more", n)
	}
	if hasEvent(k, "agent_withdraw") {
		t.Error("a cancelled launch sent a withdrawal for an interval that never opened")
	}
}

func TestBashAgentWrapperCancelsTheLaunchOnCtrlCDuringResolution(t *testing.T) {
	agentWrapperCancelsTheLaunchOnCtrlCDuringResolution(t, startNestedBashParent)
}

func TestZshAgentWrapperCancelsTheLaunchOnCtrlCDuringResolution(t *testing.T) {
	agentWrapperCancelsTheLaunchOnCtrlCDuringResolution(t, startNestedZshParent)
}

// Ctrl+C CANCELS THE LAUNCH FROM THE MOMENT THE WRAPPER STARTS ASKING what to
// launch, not only from the moment it enrols (nocx-xn63t.6.30).
//
// `__nocx_agent_launch_resolve` runs EARLIER than the enrolment in a local pane
// and waits the same way, and the person who typed an agent's name and changed
// their mind cannot tell which round trip they interrupted. With no trap in that
// window the interrupt aborts the wrapper instead: no cancel sentence, and
// whatever status the shell leaves. So the sentence AND the status 130 are
// asserted here, at the same seam and with the same words as the enrolment's
// cancel tests, one round trip earlier.
//
// The answer is HELD by the kernel rather than raced for: the interrupt is
// written while the shell is provably inside that wait, because the wait cannot
// end until this test releases the answer. That is what makes the failure
// deterministic on a loaded runner as well as on this one.
func agentWrapperCancelsTheLaunchOnCtrlCDuringResolution(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k := newNestedKernel(t)
	k.holdLaunchAnswer = true
	// A literal answer, which is what a worker's pane is resolved with: it
	// carries no ticket, so the ticket's own cancel belongs to the enrolment's
	// window (TestBashAgentWrapperCancelsTheLaunchOnCtrlCBeforeTheQuestionArrives
	// and its zsh twin). What this window can be held to is the sentence and the
	// status, and that no agent starts.
	k.launchLiteral = true
	s := start(t, k, "claude", fakeAgentBody)
	if _, err := s.ptmx.Write([]byte("claude\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	waitForEvent(t, k, "agent_launch_resolve")
	if _, err := s.ptmx.Write([]byte{0x03}); err != nil {
		t.Fatalf("press Ctrl+C: %v", err)
	}
	// The terminal echoes ^C as it raises the signal, so once it is on screen
	// the interrupt has reached the shell. The answer then goes out: a wrapper
	// that survived the interrupt reads it, one that was aborted never does, and
	// neither has to sit out the resolution's own bound.
	waitForOutput(t, s, "^C")
	k.sendHeldLaunchAnswer()
	// Arithmetic in the line, so the pane's echo of what was typed cannot
	// satisfy the wait for what the shell printed.
	if _, err := s.ptmx.Write([]byte("echo \"RC=$(($?+0))-END\"\n")); err != nil {
		t.Fatalf("type echo: %v", err)
	}
	waitForOutput(t, s, "-END\r\n")
	out := s.output()
	if !strings.Contains(out, "RC=130-END") {
		t.Fatalf("the cancelled launch did not report the interrupt; output=%q", out)
	}
	// The status alone cannot tell the cancel from a shell that aborted the
	// wrapper on an untrapped interrupt: bash reports 130 for both, and zsh
	// does not. The sentence is what only the cancel path prints.
	if !strings.Contains(out, "nocx: cancelled — claude was not started") {
		t.Errorf("the pane did not say the launch was cancelled; output=%q", out)
	}
	if strings.Contains(out, "AGENT-RAN") {
		t.Fatalf("a cancelled launch started the agent; output=%q", out)
	}
	if enrolments := eventsOf(k, "agent_enrol"); len(enrolments) != 0 {
		t.Errorf("a launch cancelled while it was being resolved enrolled %d time(s): %v", len(enrolments), enrolments)
	}
}

func TestBashAgentWrapperCancelsTheLaunchOnCtrlCBeforeTheQuestionArrives(t *testing.T) {
	agentWrapperCancelsTheLaunchOnCtrlCBeforeTheQuestionArrives(t, startNestedBashParent)
}

func TestZshAgentWrapperCancelsTheLaunchOnCtrlCBeforeTheQuestionArrives(t *testing.T) {
	agentWrapperCancelsTheLaunchOnCtrlCBeforeTheQuestionArrives(t, startNestedZshParent)
}

// Ctrl+C pressed after the enrolment left the pane but before nocx's "a
// question is up" came back is the same cancel as one pressed while the
// question is up: the person cannot tell the two moments apart, so the pane
// says the agent was not started and the command reports the interrupt.
func agentWrapperCancelsTheLaunchOnCtrlCBeforeTheQuestionArrives(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k, s, flag := raiseQuestion(t, start, func(k *nestedKernel) { k.holdEnrolAnswer = true })
	pressCtrlCBeforeTheAnswer(t, k, s)

	closeQuestionWith(t, k, flag, "")
	if _, err := s.ptmx.Write([]byte("echo \"STILL-HERE-$((20+22))\"\n")); err != nil {
		t.Fatalf("type echo: %v", err)
	}
	waitForOutput(t, s, "STILL-HERE-42")

	out := s.output()
	if strings.Contains(out, "AGENT-RAN") {
		t.Fatalf("a cancelled launch started the agent; output=%q", out)
	}
	if n := len(eventsOf(k, "agent_enrol")); n != 1 {
		t.Errorf("the pane sent %d enrolments, want 1 — a cancelled launch enrols nothing more", n)
	}
	if hasEvent(k, "agent_withdraw") {
		t.Error("a cancelled launch sent a withdrawal for an interval that never opened")
	}
}

func TestBashAgentWrapperCancelsAnAdmittedLaunchOnCtrlCBeforeTheAnswerArrives(t *testing.T) {
	agentWrapperCancelsAnAdmittedLaunchOnCtrlCBeforeTheAnswerArrives(t, startNestedBashParent)
}

func TestZshAgentWrapperCancelsAnAdmittedLaunchOnCtrlCBeforeTheAnswerArrives(t *testing.T) {
	agentWrapperCancelsAnAdmittedLaunchOnCtrlCBeforeTheAnswerArrives(t, startNestedZshParent)
}

// The same Ctrl+C when the answer on its way admits the agent: nothing runs,
// and the interval the admission opened is closed rather than left open with
// no agent in it.
func agentWrapperCancelsAnAdmittedLaunchOnCtrlCBeforeTheAnswerArrives(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k := newNestedKernel(t)
	k.holdEnrolAnswer = true
	flag := filepath.Join(t.TempDir(), "question-closed")
	s := start(t, k, "claude", questionAgentBody(flag))
	if _, err := s.ptmx.Write([]byte("claude\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	enrol := waitForEvent(t, k, "agent_enrol")
	pressCtrlCBeforeTheAnswer(t, k, s)

	withdraw := waitForEvent(t, k, "agent_withdraw")
	if withdraw.Body["request"] != enrol.Body["request"] {
		t.Errorf("the withdrawal closed request %v, want the admitted %v", withdraw.Body["request"], enrol.Body["request"])
	}
	if out := s.output(); strings.Contains(out, "AGENT-RAN") {
		t.Fatalf("a cancelled launch started the agent; output=%q", out)
	}
}

// pressCtrlCBeforeTheAnswer presses Ctrl+C while the kernel holds its answer to
// the enrolment, then lets the answer go, and requires the cancel: the
// interrupt status and the pane's sentence.
func pressCtrlCBeforeTheAnswer(t *testing.T, k *nestedKernel, s *channelShell) {
	t.Helper()
	if _, err := s.ptmx.Write([]byte{0x03}); err != nil {
		t.Fatalf("press Ctrl+C: %v", err)
	}
	// The terminal echoes ^C as it raises the signal, so once it is on screen
	// the interrupt has reached the shell and the answer can follow it.
	waitForOutput(t, s, "^C")
	k.sendHeldEnrolAnswer()
	// Arithmetic in the line, so the pane's echo of what was typed cannot
	// satisfy the wait for what the shell printed.
	if _, err := s.ptmx.Write([]byte("echo \"RC=$(($?+0))-END\"\n")); err != nil {
		t.Fatalf("type echo: %v", err)
	}
	waitForOutput(t, s, "-END\r\n")
	out := s.output()
	if !strings.Contains(out, "RC=130-END") {
		t.Fatalf("the cancelled launch did not report the interrupt; output=%q", out)
	}
	if !strings.Contains(out, "nocx: cancelled — claude was not started") {
		t.Errorf("the pane did not say the launch was cancelled; output=%q", out)
	}
}

// NO CHANNEL IS ALSO CLOSED AND VISIBLE. This is distinct from a live channel
// whose enrolment is refused: the integration is sourced, but the lifecycle
// configuration is absent, so the wrapper must take its conventional branch
// without attempting an enrolment or a withdrawal.
func TestBashAgentWrapperSaysSoWhenLifecycleChannelIsAbsent(t *testing.T) {
	agentWrapperSaysSoWhenLifecycleChannelIsAbsent(t, "bash", "nocx.bash", bashScript)
}

func TestZshAgentWrapperSaysSoWhenLifecycleChannelIsAbsent(t *testing.T) {
	agentWrapperSaysSoWhenLifecycleChannelIsAbsent(t, "zsh", "nocx.zsh", zshScript)
}

func agentWrapperSaysSoWhenLifecycleChannelIsAbsent(t *testing.T, shell, scriptName, script string) {
	t.Helper()
	s := startChannelShellNoChannel(t, shell, scriptName, script)
	t.Cleanup(s.close)

	if _, err := s.ptmx.Write([]byte("claude\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := s.output()
		if strings.Contains(out, "nocx: not orchestrated — this pane has no lifecycle channel") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	out := s.output()
	if !strings.Contains(out, "nocx: not orchestrated — this pane has no lifecycle channel") {
		t.Errorf("the absent channel was not reported in the pane; output=%q", out)
	}
	waitForOutput(t, s, "AGENT-RAN")
	// With no descriptor there is nowhere a frame could be written, so the
	// absence of a withdrawal is structural rather than observable here. The
	// falsifiable withdrawal assertion lives in the refused-enrolment test,
	// which supplies a kernel that records protocol frames.
}

// waitUntil waits on an observable condition, never on a duration.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitForRecordedArgv waits until the worker fixture has written its WHOLE argv
// record, and returns that record's lines.
//
// The marker the fixture prints is not the state its caller reads
// (nocx-xn63t.6.57): `printf 'WORKER-AGENT-RAN\n'` comes before
// `printf '%s\n' "$#" "$@" > argv.txt`, so a wait on the marker alone is
// satisfied while the record does not exist yet, and the caller's next statement
// reads a file the agent has not written. That is exactly what CI run 39176833066
// reported, and no bound would have cured it: the wait was never short of time,
// it was short of a reason to keep looking.
func waitForRecordedArgv(t *testing.T, argvFile string, s *channelShell) []string {
	t.Helper()
	return waitForWholeFile(t, "the worker agent to record its whole argv", argvFile,
		func(raw []byte) ([]string, bool) { return completeArgvRecord(string(raw)) },
		func() string {
			raw, err := os.ReadFile(argvFile) //nolint:gosec // argvFile is inside this test's temp directory
			return fmt.Sprintf("argv.txt held %q (read error %v); output=%q", raw, err, s.output())
		})
}

// waitForWholeFile waits until path holds the WHOLE of what its fixture writes,
// and returns what `whole` made of it.
//
// A reader can catch three shapes mid-write: no file yet, a file that exists and
// is EMPTY (`>` and `cp` both create the destination before the bytes arrive),
// and a PREFIX of the finished content. They are one class of wait and they are
// why this helper exists once rather than beside each fixture — nocx-xn63t.6.57
// took the missing file and nocx-xn63t.6.58 the empty one, in this same file,
// failing with two different messages for one reason: the wait was never short
// of time, it was short of a reason to keep looking.
//
// `whole` decides that reason, from the file's OWN framing and never from the
// values the test is about: a complete but WRONG file must reach the assertions,
// which then say what is wrong in their own words, rather than time out on a
// question this wait never asked.
func waitForWholeFile[T any](t *testing.T, what, path string, whole func([]byte) (T, bool), detail func() string) T {
	t.Helper()
	var got T
	waittest.WaitForTimeoutDetail(t, what, 10*time.Second, detail, func() bool {
		raw, err := os.ReadFile(path) //nolint:gosec // path is inside this test's temp directory
		if err != nil {
			return false
		}
		v, ok := whole(raw)
		got = v
		return ok
	})
	return got
}

// completeJSONDocument reports whether raw is a WHOLE JSON document, decided from
// the document's framing alone: the outermost object or array is closed, and only
// whitespace follows it. Values are deliberately not read — a document that is
// whole and wrong belongs to the assertions after the wait.
//
// Why framing and not "does it decode": a decode would make the wait answer the
// question the test is asking, so a product that wrote a malformed document would
// look like a slow fixture and time out instead of failing where the defect is.
func completeJSONDocument(raw []byte) bool {
	depth := 0
	inString := false
	escaped := false
	closed := false
	for _, b := range raw {
		switch {
		case closed:
			if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
				return false
			}
		case inString:
			switch {
			case escaped:
				escaped = false
			case b == '\\':
				escaped = true
			case b == '"':
				inString = false
			}
		default:
			switch b {
			case '"':
				inString = true
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth < 0 {
					return false
				}
				closed = depth == 0
			}
		}
	}
	return closed
}

// completeArgvRecord reports whether raw is the WHOLE record the fixture writes —
// `printf '%s\n' "$#" "$@"` — and returns its lines.
//
// A reader can catch three shapes mid-write: no file (the `>` redirect has not
// run), an empty file (the redirect truncated it and printf has not written yet),
// and a PREFIX of the whole record. The record's own first line is the number of
// lines that follow, which is what makes a prefix recognisable: the record is
// complete exactly when that count is satisfied, and only then. Nothing here
// inspects the product's arguments — a wrapper that passed the wrong ones still
// records a consistent file, and the assertions after this wait then say so in
// their own words instead of the wait timing out on a question it never asked.
func completeArgvRecord(raw string) ([]string, bool) {
	if !strings.HasSuffix(raw, "\n") {
		return nil, false
	}
	lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	count, err := strconv.Atoi(lines[0])
	if err != nil || count != len(lines)-1 {
		return nil, false
	}
	return lines, true
}

func TestBashAgentWrapperRunsTheResolvedRecordArgsAndEnvironment(t *testing.T) {
	agentWrapperRunsTheResolvedRecordArgsAndEnvironment(t, startNestedBashParent)
}

func TestZshAgentWrapperRunsTheResolvedRecordArgsAndEnvironment(t *testing.T) {
	agentWrapperRunsTheResolvedRecordArgsAndEnvironment(t, startNestedZshParent)
}

func agentWrapperRunsTheResolvedRecordArgsAndEnvironment(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k := newNestedKernel(t)
	body := "#!/bin/sh\nprintf 'LAUNCH:%s|%s|%s|%s|%s\\n' \"$NOCX_AGENT_ENV\" \"$1\" \"$2\" \"$3\" \"$4\"\n"
	s := start(t, k, "claude", body)
	k.launchArgs = []string{"record-one", "record two"}
	k.launchEnv = []string{"NOCX_AGENT_ENV=record env"}
	if _, err := s.ptmx.Write([]byte("claude user-one 'user two'\n")); err != nil {
		t.Fatalf("type configured agent: %v", err)
	}
	resolve := waitForEvent(t, k, "agent_launch_resolve")
	panePath, pathOK := resolve.Body["pane_path"].(string)
	paneCwd, cwdOK := resolve.Body["pane_cwd"].(string)
	if !pathOK || panePath == "" || !cwdOK || !filepath.IsAbs(paneCwd) {
		t.Fatalf("shell omitted pane resolution context: %+v", resolve.Body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !hasEvent(k, "agent_enrol") {
		time.Sleep(25 * time.Millisecond)
	}
	if !hasEvent(k, "agent_enrol") {
		t.Fatalf("launch resolution did not reach enrolment; shell output=%q", s.output())
	}
	enrol := waitForEvent(t, k, "agent_enrol")
	if resolve.Seq >= enrol.Seq {
		t.Fatalf("enrolment preceded resolution: resolve=%d enrol=%d", resolve.Seq, enrol.Seq)
	}
	ticket, _ := enrol.Body["ticket"].(string)
	if len(ticket) != 43 {
		t.Fatalf("agent_enrol did not carry the resolved opaque ticket: %v", enrol.Body)
	}
	want := "LAUNCH:record env|record-one|record two|user-one|user two"
	waitUntil(t, "configured command, args and environment", func() bool { return strings.Contains(s.output(), want) })
	wdr := waitForEvent(t, k, "agent_withdraw")
	if wdr.Seq <= enrol.Seq {
		t.Fatalf("withdrawal preceded enrolment: withdraw=%d enrol=%d", wdr.Seq, enrol.Seq)
	}
}

func TestBashAgentWrapperUsesResolvedRecordAfterRefusalWithoutTools(t *testing.T) {
	agentWrapperUsesResolvedRecordAfterRefusalWithoutTools(t, startNestedBashParent)
}

func TestZshAgentWrapperUsesResolvedRecordAfterRefusalWithoutTools(t *testing.T) {
	agentWrapperUsesResolvedRecordAfterRefusalWithoutTools(t, startNestedZshParent)
}

func agentWrapperUsesResolvedRecordAfterRefusalWithoutTools(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k := newNestedKernel(t)
	k.refuseEnrolment = true
	body := "#!/bin/sh\nprintf 'REFUSED:%s|%s|%s|%s|%s|%s\\n' \"$NOCX_AGENT_ENV\" \"$#\" \"$1\" \"$2\" \"$3\" \"$4\"\n"
	s := start(t, k, "claude", body)
	k.launchArgs = []string{"record-one", "record two"}
	k.launchEnv = []string{"NOCX_AGENT_ENV=record env"}
	if _, err := s.ptmx.Write([]byte("claude user-one 'user two'\n")); err != nil {
		t.Fatalf("type configured agent: %v", err)
	}
	enrol := waitForEvent(t, k, "agent_enrol")
	if ticket, _ := enrol.Body["ticket"].(string); len(ticket) != 43 {
		t.Fatalf("enrolment omitted launch ticket: %v", enrol.Body)
	}
	want := "REFUSED:record env|4|record-one|record two|user-one|user two"
	waitUntil(t, "recorded launch without tools", func() bool { return strings.Contains(s.output(), want) })
	if !hasEvent(k, "agent_launch_cancel") {
		t.Fatal("the refused invocation left its local launch ticket live")
	}
	if hasEvent(k, "agent_withdraw") {
		t.Fatal("a refused enrolment withdrew an interval it never opened")
	}
}

func TestBashAgentWrapperWithdrawsAfterAgentFailure(t *testing.T) {
	agentWrapperWithdrawsAfterAgentFailure(t, startNestedBashParent)
}

func TestZshAgentWrapperWithdrawsAfterAgentFailure(t *testing.T) {
	agentWrapperWithdrawsAfterAgentFailure(t, startNestedZshParent)
}

func agentWrapperWithdrawsAfterAgentFailure(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k := newNestedKernel(t)
	s := start(t, k, "claude", "#!/bin/sh\necho AGENT-FAILED\nexit 23\n")
	if _, err := s.ptmx.Write([]byte("claude\n")); err != nil {
		t.Fatalf("type configured agent: %v", err)
	}
	enrol := waitForEvent(t, k, "agent_enrol")
	waitUntil(t, "failed agent output", func() bool { return strings.Contains(s.output(), "AGENT-FAILED") })
	withdraw := waitForEvent(t, k, "agent_withdraw")
	if withdraw.Seq <= enrol.Seq {
		t.Fatalf("failed agent's interval closed out of order: enrol=%d withdraw=%d", enrol.Seq, withdraw.Seq)
	}
}

func TestBashAgentWrapperDoesNotRunTypedNameAfterMalformedLocalResolution(t *testing.T) {
	agentWrapperDoesNotRunTypedNameAfterMalformedLocalResolution(t, startNestedBashParent)
}

func TestZshAgentWrapperDoesNotRunTypedNameAfterMalformedLocalResolution(t *testing.T) {
	agentWrapperDoesNotRunTypedNameAfterMalformedLocalResolution(t, startNestedZshParent)
}

func agentWrapperDoesNotRunTypedNameAfterMalformedLocalResolution(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k := newNestedKernel(t)
	k.launchMalformed = true
	s := start(t, k, "claude", "#!/bin/sh\nprintf 'TYPED-ID-RAN\\n'\n")
	if _, err := s.ptmx.Write([]byte("claude --user-arg\n")); err != nil {
		t.Fatalf("type agent name: %v", err)
	}
	waitForEvent(t, k, "agent_launch_resolve")
	waitUntil(t, "local resolution refusal", func() bool {
		return strings.Contains(s.output(), "nocx: not started —")
	})
	if strings.Contains(s.output(), "TYPED-ID-RAN") || hasEvent(k, "agent_enrol") {
		t.Fatalf("malformed local resolution fell back to the typed ID or enrolled: output=%q events=%v", s.output(), k.accepted)
	}
}

// A WORKER'S LAUNCH STAYS LITERAL AND STILL GETS NOCX'S TOOLS
// (nocx-xn63t.6.49). These are the two facts that must hold at once, and the
// owner's stand is what a single field answering both of them cost: a worker
// spawned through `workers.spawn {command: claude}` ran with argv `claude` and
// no --mcp-config, so it could not see `mcp__nocx__workers_report` and its
// coordinator had to read its screen instead of receiving its report.
//
// Driven at the seam a spawned worker actually reaches: a real shell on a real
// pty, the pane's own tool socket and helper path exported the way a spawned
// pane's environment carries them, and a frame shaped exactly as the
// backend's worker-lane answer is (agent_launch.go: Local false, ToolSurface
// true). What is asserted is the program that ran — its whole argv, and the
// environment the record would have added — not the shell's variables.
func TestBashAgentWrapperPointsAWorkerLaunchAtTheToolSurface(t *testing.T) {
	agentWrapperPointsAWorkerLaunchAtTheToolSurface(t, startNestedBashParent)
}

func TestZshAgentWrapperPointsAWorkerLaunchAtTheToolSurface(t *testing.T) {
	agentWrapperPointsAWorkerLaunchAtTheToolSurface(t, startNestedZshParent)
}

func agentWrapperPointsAWorkerLaunchAtTheToolSurface(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k := newNestedKernel(t)
	k.launchLiteral = true
	k.launchToolSurface = true
	// A record that must NOT reach the worker. The fixture carries it so a
	// shell that applied it would be caught by the argv and environment
	// assertions below rather than merely unobserved — the literal answer has
	// no payload, so the only way it could arrive is the wrapper inventing it.
	k.launchArgs = []string{"record-one", "record two"}
	k.launchEnv = []string{"NOCX_AGENT_ENV=record env"}

	socket := filepath.Join(t.TempDir(), "tool.sock")
	helper := filepath.Join(t.TempDir(), "nocx-helper")
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	body := "#!/bin/sh\n" +
		"printf 'WORKER-AGENT-RAN\\n'\n" +
		"printf 'RECORD_ENV=%s\\n' \"${NOCX_AGENT_ENV:-unset}\"\n" +
		"printf '%s\\n' \"$#\" \"$@\" > " + ShellQuote(argvFile) + "\n"
	s := start(t, k, "claude", body)

	line := "export NOCX_TOOL_SOCKET=" + ShellQuote(socket) +
		" NOCX_AGENT_HELPER_PATH=" + ShellQuote(helper) +
		"; claude --model test 'two words'\n"
	if _, err := s.ptmx.Write([]byte(line)); err != nil {
		t.Fatalf("type worker command: %v", err)
	}
	resolve := waitForEvent(t, k, "agent_launch_resolve")
	enrol := waitForEvent(t, k, "agent_enrol")
	if enrol.Seq <= resolve.Seq {
		t.Fatalf("enrolment preceded resolution: resolve=%d enrol=%d", resolve.Seq, enrol.Seq)
	}
	if _, hasTicket := enrol.Body["ticket"]; hasTicket {
		t.Fatalf("a worker launch used a local agent record: %v", enrol.Body)
	}
	// THE MARKER IS NOT THE STATE (nocx-xn63t.6.57). The fixture prints
	// WORKER-AGENT-RAN, then the record's environment line, and only then writes
	// argv.txt; a wait on the marker alone was satisfied while the file this
	// test reads did not exist yet, which is the failure CI run 39176833066
	// reported — `the worker agent recorded no argv: open .../003/argv.txt: no
	// such file or directory`, with WORKER-AGENT-RAN in the same message. The
	// marker is still waited for, because the typed program having run is a fact
	// worth asserting; the record is a second fact and is waited for on its own.
	waitUntil(t, "the worker agent to run", func() bool {
		return strings.Contains(s.output(), "WORKER-AGENT-RAN")
	})
	argv := waitForRecordedArgv(t, argvFile, s)
	if len(argv) < 6 {
		t.Fatalf("worker argv = %q, want the typed arguments plus --mcp-config and its file", argv)
	}
	wantFirst := []string{"5", "--model", "test", "two words", "--mcp-config"}
	for i, want := range wantFirst {
		if argv[i] != want {
			t.Fatalf("worker argv = %q, want %q at position %d", argv, want, i)
		}
	}
	configPath := argv[5]
	if !strings.Contains(configPath, "/nocx-agent-launch.") || !strings.HasSuffix(configPath, "/mcp.json") {
		t.Fatalf("the tool surface was passed as %q, want a staged nocx launch config", configPath)
	}
	if strings.Contains(strings.Join(argv, " "), "record-one") || strings.Contains(strings.Join(argv, " "), "record two") {
		t.Fatalf("the record's arguments reached the worker: argv=%q", argv)
	}
	// THE INTERVAL'S OTHER END: the staged directory is gone once the agent has
	// returned, because it carried the pane's bearer. The agent has run and
	// exited by now; wait for the withdrawal that closes the interval first.
	waitForEvent(t, k, "agent_withdraw")
	waitUntil(t, "the launch directory to be swept", func() bool {
		_, statErr := os.Stat(filepath.Dir(configPath))
		return os.IsNotExist(statErr)
	})

	s.waitForPromptAfterComplete()
	s.waitForRenderedThroughPrompt()
	out := strings.ReplaceAll(s.output(), "\r", "")
	if strings.Contains(out, "tool surface unavailable") {
		t.Fatalf("a worker pane with a configured socket refused its tool surface: %q", out)
	}
	if !strings.Contains(out, "RECORD_ENV=unset") {
		t.Fatalf("the record's environment reached the worker: %q", out)
	}
}

// The staged file itself is asserted here rather than in the test above so the
// failure reads as one question: what did the worker get pointed at? It is the
// same frame and the same fixture; only the reading differs, because the file
// is gone by the time the worker has returned (the sweep is the interval's
// other end, and the test above proves that).
func TestBashAgentWrapperStagesTheWorkerToolSurfaceFromThePanesOwnPaths(t *testing.T) {
	agentWrapperStagesTheWorkerToolSurfaceFromThePanesOwnPaths(t, startNestedBashParent)
}

func TestZshAgentWrapperStagesTheWorkerToolSurfaceFromThePanesOwnPaths(t *testing.T) {
	agentWrapperStagesTheWorkerToolSurfaceFromThePanesOwnPaths(t, startNestedZshParent)
}

func agentWrapperStagesTheWorkerToolSurfaceFromThePanesOwnPaths(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k := newNestedKernel(t)
	k.launchLiteral = true
	k.launchToolSurface = true

	socket := filepath.Join(t.TempDir(), "tool.sock")
	helper := filepath.Join(t.TempDir(), "nocx-helper")
	configFile := filepath.Join(t.TempDir(), "mcp.json")
	// THE FIXTURE HOLDS THE COPY WHERE `cp` ITSELF WOULD (nocx-xn63t.6.58): the
	// staged file is created first and filled second, so the file existing is a
	// state the test can catch and the COPY BEING WHOLE is a later one. `cp`
	// does both without a pause and usually finishes before a reader arrives,
	// which is why this window has only ever fired on a loaded runner; the gate
	// below opens it deterministically instead — the release is written by the
	// test, never slept for.
	release := filepath.Join(t.TempDir(), "release-the-copy")
	releaseTheCopy := func() {
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Fatalf("release the fixture's copy: %v", err)
		}
	}
	// However this test ends, the fixture stops waiting rather than holding the
	// shell in its loop.
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	body := "#!/bin/sh\n" +
		"while [ \"$#\" -gt 0 ]; do\n" +
		"  if [ \"$1\" = --mcp-config ]; then\n" +
		"    shift\n" +
		"    : > " + ShellQuote(configFile) + "\n" +
		"    while [ ! -e " + ShellQuote(release) + " ]; do sleep 0.05; done\n" +
		"    cp \"$1\" " + ShellQuote(configFile) + "\n" +
		"  fi\n" +
		"  shift\n" +
		"done\n" +
		"echo WORKER-AGENT-RAN\n"
	s := start(t, k, "claude", body)
	line := "export NOCX_TOOL_SOCKET=" + ShellQuote(socket) +
		" NOCX_AGENT_HELPER_PATH=" + ShellQuote(helper) +
		"; claude --model test\n"
	if _, err := s.ptmx.Write([]byte(line)); err != nil {
		t.Fatalf("type worker command: %v", err)
	}
	waitForEvent(t, k, "agent_enrol")
	// THE EMPTY FILE IS A STATE THIS WAIT MAY PASS THROUGH, NOT ONE IT STOPS AT
	// (nocx-xn63t.6.58). This waits for the fixture to be inside the window —
	// the file created and not yet filled — and only then opens the gate, so the
	// read below is guaranteed to cross the empty state instead of reaching it
	// by luck. It is not the wait that answers the test's question.
	waittest.WaitForTimeoutDetail(t, "the worker agent to create its config file", 10*time.Second,
		func() string {
			_, statErr := os.Stat(configFile)
			return fmt.Sprintf("the staged config was not created (stat error %v); output=%q", statErr, s.output())
		},
		func() bool {
			_, statErr := os.Stat(configFile)
			return statErr == nil
		})
	releaseTheCopy()
	raw := waitForWholeFile(t, "the fixture to finish copying the staged config", configFile,
		func(raw []byte) ([]byte, bool) { return raw, completeJSONDocument(raw) },
		func() string {
			raw, err := os.ReadFile(configFile) //nolint:gosec // configFile is inside this test's temp directory
			return fmt.Sprintf("the staged config held %q (read error %v), which is not a whole JSON document; output=%q",
				raw, err, s.output())
		})
	var config stagedMCPConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("decode the staged config: %v; raw=%q", err, raw)
	}
	server, ok := config.MCPServers["nocx"]
	if !ok || len(config.MCPServers) != 1 {
		t.Fatalf("MCP servers = %#v, want exactly one server named nocx", config.MCPServers)
	}
	if server.Command != helper {
		t.Fatalf("the worker's tool surface names %q, want this pane's helper %q", server.Command, helper)
	}
	wantArgs := []string{"mcp", "--socket", socket}
	if len(server.Args) != len(wantArgs) {
		t.Fatalf("MCP args = %#v, want %#v", server.Args, wantArgs)
	}
	for i := range wantArgs {
		if server.Args[i] != wantArgs[i] {
			t.Fatalf("MCP args = %#v, want %#v", server.Args, wantArgs)
		}
	}
	// The wrapper is still running after the copy above: let the interval close
	// before this test does (the reason the worker-literal test states in full).
	waitForEvent(t, k, "agent_withdraw")
}

func TestBashAgentWrapperKeepsServerClassifiedWorkerCommandLiteral(t *testing.T) {
	k := newNestedKernel(t)
	k.launchLiteral = true
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	body := "#!/bin/sh\nprintf '%s\\n' \"$#\" \"$@\" > " + ShellQuote(argvFile) + "\n"
	s := startNestedBashParent(t, k, "claude", body)
	if _, err := s.ptmx.Write([]byte("claude --model test 'two words'\n")); err != nil {
		t.Fatalf("type worker command: %v", err)
	}
	waitForEvent(t, k, "agent_launch_resolve")
	enrol := waitForEvent(t, k, "agent_enrol")
	if _, hasTicket := enrol.Body["ticket"]; hasTicket {
		t.Fatalf("server-classified worker launch received a record ticket: %v", enrol.Body)
	}
	want := "3\n--model\ntest\ntwo words\n"
	waitUntil(t, "the literal external command and its arguments", func() bool {
		got, err := os.ReadFile(argvFile) //nolint:gosec // argvFile is inside this test's temp directory
		return err == nil && string(got) == want
	})
	got, err := os.ReadFile(argvFile) //nolint:gosec // argvFile is inside this test's temp directory
	if err != nil || string(got) != want {
		t.Fatalf("external argv = %q (err %v), want %q", got, err, want)
	}
	// THE INTERVAL'S OTHER END, and this test does not get to skip it. The
	// program above returns as soon as the file is written, while the wrapper
	// is still running: it withdraws, and the kernel answers that withdrawal.
	// A test that finished first tore the socketpair down under the answer and
	// the fixture's own write failed inside a goroutine, which is a panic and
	// not a failure ("Fail in goroutine after ... has completed"). Waiting on
	// the frame is waiting on an observable, never on a duration, which is the
	// rule every other test in this file already follows.
	waitForEvent(t, k, "agent_withdraw")
}

// A SUBCOMMAND THE RECORD MARKS AS NOT A LAUNCH REACHES THE CLI UNCHANGED
// (nocx-3an1s.1.3, GitHub #541).
//
// The owner ran `claude update` in a pane and Claude answered "unknown option
// '--mcp-config'": the wrapper appended its tool-surface argument to every
// invocation of the agent, maintenance subcommands included. A subcommand is
// not a launch, so its argv is the program's own — no surface, no staged
// config, and no enrolment, because a maintenance command is not an agent
// session. The record owns the list of such words; the resolve answer carries
// it; the wrapper reads it.
//
// BOTH HALVES ARE ASSERTED, because either alone is satisfiable by doing
// nothing: "update carries no --mcp-config" passes if the wrapper never adds
// one, and "a plain launch carries it" passes if it always does.
func TestBashAgentWrapperRunsANonLaunchSubcommandUnchanged(t *testing.T) {
	agentWrapperRunsANonLaunchSubcommandUnchanged(t, startNestedBashParent)
}

// The zsh twin: the two scripts are separate implementations of one protocol,
// and the generated wrapper is one block per shell.
func TestZshAgentWrapperRunsANonLaunchSubcommandUnchanged(t *testing.T) {
	agentWrapperRunsANonLaunchSubcommandUnchanged(t, startNestedZshParent)
}

func agentWrapperRunsANonLaunchSubcommandUnchanged(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k := newNestedKernel(t)
	// What the shipped Claude record now says, and what the backend's answer
	// carries as non_launch_subcommands.
	k.launchNonLaunch = []string{"update"}
	s := start(t, k, "claude", inspectingAgentBody)

	if _, err := s.ptmx.Write([]byte("export NOCX_TOOL_SOCKET=/tmp/nocx-tool.sock NOCX_AGENT_HELPER_PATH=/opt/nocx-helper; claude update\n")); err != nil {
		t.Fatalf("type the maintenance subcommand: %v", err)
	}
	waitForEvent(t, k, "agent_launch_resolve")
	waitUntil(t, "the maintenance subcommand to run", func() bool {
		return strings.Contains(s.output(), "AGENT-RAN")
	})
	s.waitForPromptAfterComplete()
	s.waitForRenderedThroughPrompt()
	out := strings.ReplaceAll(s.output(), "\r", "")
	if !strings.Contains(out, "ARG_0=update") {
		t.Fatalf("the person's argv did not reach the program: %q", out)
	}
	if strings.Contains(out, "--mcp-config") || strings.Contains(out, "MCP_PATH=") {
		t.Fatalf("a maintenance subcommand was given the tool surface: %q", out)
	}
	if strings.Contains(out, "ARG_1=") {
		t.Fatalf("a maintenance subcommand received arguments it was not typed with: %q", out)
	}
	if hasEvent(k, "agent_enrol") {
		t.Fatalf("a maintenance subcommand was enrolled as an agent launch: %v", k.events())
	}
	if !hasEvent(k, "agent_launch_cancel") {
		t.Fatalf("the maintenance invocation left its launch ticket live: %v", k.events())
	}

	// …and the same wrapper still points a real launch at the surface.
	if _, err := s.ptmx.Write([]byte("claude\n")); err != nil {
		t.Fatalf("type the agent: %v", err)
	}
	waitForEvent(t, k, "agent_enrol")
	waitUntil(t, "the plain launch to receive the tool surface", func() bool {
		return strings.Contains(strings.ReplaceAll(s.output(), "\r", ""), "ARG_0=--mcp-config")
	})
	s.waitForPromptAfterComplete()
	out = strings.ReplaceAll(s.output(), "\r", "")
	if !strings.Contains(out, "MCP_PATH=") {
		t.Fatalf("a plain launch lost its tool surface: %q", out)
	}
	// The interval's other end: the program exits and the wrapper withdraws.
	waitForEvent(t, k, "agent_withdraw")
}
