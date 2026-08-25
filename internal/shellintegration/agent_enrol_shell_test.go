package shellintegration

import (
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
