package shellintegration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	// The pane says what it is waiting for and how to stop waiting.
	waitForOutput(t, s, "Ctrl+C")
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
		if strings.Contains(out, "AGENT-RAN") && strings.Contains(out, "not orchestrated") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	out := s.output()
	if !strings.Contains(out, "nocx: not orchestrated — this pane has no lifecycle channel") {
		t.Errorf("the absent channel was not reported in the pane; output=%q", out)
	}
	if !strings.Contains(out, "AGENT-RAN") {
		t.Errorf("an absent lifecycle channel stopped the user's agent; output=%q", out)
	}
	if n := strings.Count(out, "AGENT-RAN"); n != 1 {
		t.Errorf("the agent ran %d times, want exactly 1; output=%q", n, out)
	}
	// With no descriptor there is nowhere a frame could be written, so the
	// absence of a withdrawal is structural rather than observable here. The
	// falsifiable withdrawal assertion lives in the refused-enrolment test,
	// which supplies a kernel that records protocol frames.
}
