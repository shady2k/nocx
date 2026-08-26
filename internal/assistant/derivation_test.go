package assistant

import "testing"

// The matcher, tested directly: it is a pure function and the end-to-end tests
// cannot reach the case that matters most.
//
// nocx-d6gn4.9 found this against a real session (2026-08-26): the first
// implementation compared the WHOLE argument value against the earlier result,
// which works for session.read's id and can NEVER work for run's command — a
// whole command line does not appear inside a previous command's output. For
// the one tool the experiment cares about most, every edge would have been
// empty and the depth silently understated. Blindness and independence must not
// look alike.

func TestDerivation_APathDiscoveredInAnEarlierResultIsAnEdge(t *testing.T) {
	d := &derivationLog{}
	d.record("entry-ls", "call-ls", "total 4\n-rw-r--r-- 1 dev users 12 Aug 26 17:05 nocx-2026.log\n")

	derived := d.check(`{"sessionId":"session-a","command":"tail -n 5 /var/log/nocx-2026.log"}`, "sessionId")
	edges := derived.Edges
	if len(edges) != 1 || edges[0] != "entry-ls" {
		t.Fatalf("edges = %v, want [entry-ls]: the file name came out of the listing", edges)
	}
}

// TestDerivation_ANameTheUserSuppliedIsNotAnEdge is the null half, and it is
// the case the owner actually ran: "create test.txt then rename it to
// test2.txt" is a SEQUENCE, not a dependency — both names came from the
// question, and the first command's output is empty. Depth one is the right
// answer there, and a matcher that called it two would invent the very demand
// the experiment is trying to measure.
func TestDerivation_ANameTheUserSuppliedIsNotAnEdge(t *testing.T) {
	d := &derivationLog{}
	d.record("entry-echo", "call-echo", `{"exitCode":0,"output":""}`)

	derived := d.check(`{"sessionId":"session-a","command":"ls -l test2.txt && cat test2.txt"}`, "sessionId")
	edges := derived.Edges
	if len(edges) != 0 {
		t.Fatalf("edges = %v, want none: both names came from the person, not from a result", edges)
	}
}

func TestDerivation_ShortTokensDoNotCollideIntoEdges(t *testing.T) {
	d := &derivationLog{}
	// "ls" and "-l" and "5" are everywhere; none of them may draw an edge.
	d.record("entry-noise", "call-noise", "total 5\ndrwxr-xr-x 2 dev users 4096 Aug 26 17:05 ls\n")

	derived := d.check(`{"sessionId":"session-a","command":"ls -l"}`, "sessionId")
	edges := derived.Edges
	if len(edges) != 0 {
		t.Fatalf("edges = %v, want none: short tokens are collisions, not evidence", edges)
	}
}

func TestDerivation_TheResourceArgumentIsNeverEvidence(t *testing.T) {
	d := &derivationLog{}
	// A session.list result echoes the session id it was asked about.
	d.record("entry-list", "call-list", `{"sessionId":"session-a","items":[]}`)

	derived := d.check(`{"sessionId":"session-a","command":"echo hello"}`, "sessionId")
	edges := derived.Edges
	if len(edges) != 0 {
		t.Fatalf("edges = %v, want none: the resource was granted and told, never derived", edges)
	}
}
