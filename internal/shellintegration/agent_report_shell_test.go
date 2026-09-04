package shellintegration

// THE DECLARATION, from the agent that made it (nocx-dkawo.12).
//
// The receiving half of agent_report shipped with the wave record and nothing
// in the product could send one: nocx.bash sent agent_enrol and
// agent_withdraw and nothing else, so only one of the two facts ever arrived
// and every worker terminalized as abandoned. These tests are the sender, and
// they run on a real pty against a real shell, because the thing being
// asserted is that an agent a person typed the name of can say what its work
// produced.

import (
	"os"
	"strings"
	"testing"
	"time"
)

// declaringAgent is a stand-in agent that writes a declaration into the drop
// the wrapper opened for it, and prints the path so the test can check the
// wrapper cleaned up after itself.
func declaringAgent(verdict, summary string) string {
	return "#!/bin/sh\n" +
		"echo AGENT-RAN\n" +
		"echo \"DROP=$NOCX_AGENT_REPORT\"\n" +
		"printf '" + verdict + "\\n" + summary + "\\n' > \"$NOCX_AGENT_REPORT\"\n"
}

func TestBashAnAgentCanDeclareWhatItsWorkProduced(t *testing.T) {
	anAgentCanDeclareWhatItsWorkProduced(t, startNestedBashParent)
}

// The zsh twin, for the enrolment pair's reason: a worker that could declare
// in bash and not in zsh would be a wave whose completions depended on which
// login shell the person happens to have.
func TestZshAnAgentCanDeclareWhatItsWorkProduced(t *testing.T) {
	anAgentCanDeclareWhatItsWorkProduced(t, startNestedZshParent)
}

func anAgentCanDeclareWhatItsWorkProduced(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k := newNestedKernel(t)
	s := start(t, k, "claude", declaringAgent("ok", "read AGENTS.md and reported"))

	if _, err := s.ptmx.Write([]byte("claude\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	enrol := waitForEvent(t, k, "agent_enrol")
	rid, _ := enrol.Body["request"].(string)

	report := waitForEvent(t, k, "agent_report")
	if got, _ := report.Body["request"].(string); got != rid {
		t.Errorf("the declaration names request %q, want the enrolment's %q", got, rid)
	}
	if ok, _ := report.Body["ok"].(bool); !ok {
		t.Errorf("the declaration says ok=%v, want true", report.Body["ok"])
	}
	if got, _ := report.Body["summary"].(string); !strings.Contains(got, "read AGENTS.md and reported") {
		t.Errorf("summary = %q, want what the agent wrote", got)
	}

	// INSIDE THE INTERVAL THE ENROLMENT OPENED. A verdict sent after the
	// withdraw would be a report about a pane nocx had already stopped
	// watching, and the backend would have nothing to attach it to.
	withdraw := waitForEvent(t, k, "agent_withdraw")
	if report.Seq <= enrol.Seq || report.Seq >= withdraw.Seq {
		t.Errorf("the declaration (seq %d) is not between the enrolment (%d) and the withdrawal (%d)",
			report.Seq, enrol.Seq, withdraw.Seq)
	}
	if k.rejectedCount() != 0 {
		t.Errorf("the kernel rejected %d frames; a declaration is ordinary authenticated traffic", k.rejectedCount())
	}

	// And the drop is gone. A wrapper that left one behind would litter
	// /tmp once per agent run for the life of the machine.
	drop := dropPathFrom(t, s)
	waitUntil(t, "the drop to be removed", func() bool {
		_, err := os.Stat(drop)
		return os.IsNotExist(err)
	})
}

// A DECLARATION IS NEVER INFERRED. An agent that says nothing has said
// nothing, whatever it exited with — the two facts are independent (D9), and a
// verdict synthesised from an exit status would make "completed" mean nothing
// beyond "exited 0".
func TestBashAnAgentThatSaysNothingDeclaresNothing(t *testing.T) {
	anAgentThatSaysNothingDeclaresNothing(t, startNestedBashParent)
}

func TestZshAnAgentThatSaysNothingDeclaresNothing(t *testing.T) {
	anAgentThatSaysNothingDeclaresNothing(t, startNestedZshParent)
}

func anAgentThatSaysNothingDeclaresNothing(t *testing.T, start nestedParentStarter) {
	t.Helper()
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			// The ordinary case, and the one every agent that has never
			// heard of nocx is in: it exits 0 and writes nothing.
			name: "it exits cleanly and writes nothing",
			body: "#!/bin/sh\necho AGENT-RAN\nexit 0\n",
		},
		{
			// A drop that is not a declaration. Consent is the presence of
			// what we positively recognise, exactly as the enrolment
			// answer's is: a half-written file, or one some other program
			// left behind, must not become a verdict.
			name: "it writes something that is not a verdict",
			body: declaringAgent("maybe", "who knows"),
		},
		{
			name: "it writes an empty drop",
			body: "#!/bin/sh\necho AGENT-RAN\n: > \"$NOCX_AGENT_REPORT\"\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := newNestedKernel(t)
			s := start(t, k, "claude", tc.body)
			if _, err := s.ptmx.Write([]byte("claude\n")); err != nil {
				t.Fatalf("type claude: %v", err)
			}
			waitForEvent(t, k, "agent_enrol")
			// The withdraw is the interval's end, so once it has arrived
			// every frame this run was ever going to send has been sent.
			waitForEvent(t, k, "agent_withdraw")
			if hasEvent(k, "agent_report") {
				t.Fatalf("a declaration was sent for an agent that made none; accepted=%v", k.events())
			}
		})
	}
}

// A worker that says it did NOT succeed is a different and better thing than
// one that says nothing, and the wire carries the difference.
func TestBashAnAgentCanDeclareThatItFailed(t *testing.T) {
	k := newNestedKernel(t)
	s := startNestedBashParent(t, k, "claude", declaringAgent("fail", "could not build"))
	if _, err := s.ptmx.Write([]byte("claude\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	waitForEvent(t, k, "agent_enrol")
	report := waitForEvent(t, k, "agent_report")
	if ok, _ := report.Body["ok"].(bool); ok {
		t.Fatalf("a failure was declared as ok=%v", report.Body["ok"])
	}
	if got, _ := report.Body["summary"].(string); !strings.Contains(got, "could not build") {
		t.Fatalf("summary = %q, want what the agent wrote", got)
	}
}

// "AND THE PANE SAYS SO" (D4), for a declaration that was not kept. An agent
// that reported into nowhere must not think it was heard, and a log line the
// person never reads is the silent degrade AGENTS.md names.
func TestBashADeclarationThatWasNotRecordedIsSaidInThePane(t *testing.T) {
	k := newNestedKernel(t)
	k.refuseReport = true
	k.reportReason = "this pane is not part of a wave"
	s := startNestedBashParent(t, k, "claude", declaringAgent("ok", "did the thing"))
	if _, err := s.ptmx.Write([]byte("claude\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	waitForEvent(t, k, "agent_enrol")
	waitForEvent(t, k, "agent_report")
	waitUntil(t, "the pane to say the declaration was not recorded", func() bool {
		return strings.Contains(s.output(), "was not recorded")
	})
	if !strings.Contains(s.output(), "not part of a wave") {
		t.Fatalf("the pane does not say WHY: output=%q", s.output())
	}
}

// dropPathFrom reads the drop's path out of what the fake agent printed.
func dropPathFrom(t *testing.T, s *channelShell) string {
	t.Helper()
	var line string
	waitUntil(t, "the agent to print the drop path", func() bool {
		for _, l := range strings.Split(s.output(), "\n") {
			if i := strings.Index(l, "DROP=/"); i >= 0 {
				line = strings.TrimSpace(l[i+len("DROP="):])
				return line != ""
			}
		}
		return false
	})
	// The wrapper must have handed the agent a path at all: an empty one
	// means mktemp failed and no agent on that machine could ever declare.
	if line == "" {
		t.Fatalf("the wrapper gave the agent no drop to write into")
	}
	return line
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
