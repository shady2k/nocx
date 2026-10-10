package app

import "testing"

// The one piece of the LOCAL pane's agent set that is not covered by a heavier
// path (nocx-t5e7d).
//
// WHAT THIS STANDS IN FOR, and why it is not more. The set a local pane's shell
// wraps travels: the record's EnabledNames -> this opener -> the spawn request
// -> the launcher -> the generator. Three of those links have their own tests
// (agentrecord's EnabledNames, helper/session's wire, shellintegration's
// generator), and the fourth — that this opener puts the record's answer into
// the request — is asserted end to end through the NESTED CHILD domain, which
// shares this same closure and goes through the real composition root
// (TestANestedChildOnThisMachineWrapsTheRecordsAgents).
//
// What is left uncovered is the conversion between the two shapes, and it is
// the only production logic on the path without a test of its own: the record
// answers with a LIST, and the helper's params carry a SET, because its D3
// rule refuses a free-form string list — such a list is argv, and a helper that
// accepts argv is a remote shell. So this asserts the conversion, and the
// alternative — a real local pane — costs the embedded helper artifact, a
// daemon, a temporary HOME and a read of the pane's own output to observe a
// function table, which is proportionate to the widget and not to this line.

func TestTheLocalOpenerCarriesTheRecordsAgentsAsASet(t *testing.T) {
	// A caller with no record gets nil, and the launcher renders this build's
	// own set for it: a pane whose shell wraps NOTHING is a terminal that
	// quietly stopped being orchestrated.
	o := &localHelperOpener{}
	if got := o.agents(); got != nil {
		t.Fatalf("an opener with no record answered %v, want nothing at all", got)
	}

	o.agentNames = func() []string { return []string{"claude", "myagent"} }
	got := o.agents()
	if len(got) != 2 || !got["claude"] || !got["myagent"] {
		t.Fatalf("the opener answered %v, want the record's own set", got)
	}

	// An empty answer is nil rather than an empty map: "no agent is offered" is
	// one fact, and a nil map is how the wire says it.
	o.agentNames = func() []string { return nil }
	if got := o.agents(); got != nil {
		t.Fatalf("an empty set was carried as %v, want the zero value", got)
	}
}
