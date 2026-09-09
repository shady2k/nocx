package lifecycle

import (
	"errors"
	"testing"
)

func enrolEvt(rid RequestID, agent string) Event {
	return Event{Kind: KindAgentEnrol, AgentEnrol: &AgentEnrol{RequestID: rid, Agent: agent, Cols: 120, Rows: 40}}
}

func withdrawEvt(rid RequestID) Event {
	return Event{Kind: KindAgentWithdraw, AgentWithdraw: &AgentWithdraw{RequestID: rid}}
}

// The enrolment act, as the wire sees it. An authenticated agent_enrol
// produces exactly one outbound answer addressed back to the asking domain,
// echoing the request id so a stale answer can never satisfy a newer ask, and
// carrying no verdict of its own — the seam that actually opens the grid is
// the one that decides, and the kernel neither keeps a grid nor knows what one
// is.
func TestAgentEnrolProducesAnAnswerEcho(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const L = LaneID("L")
	h := establish(t, k, "T", tp, L, nil)

	outs, err := k.Ingest("T", env(L, h, 2, enrolEvt("r-agent-0", "claude")))
	if err != nil {
		t.Fatalf("agent_enrol: %v", err)
	}
	if len(outs) != 1 {
		t.Fatalf("expected exactly one outbound (the answer), got %d", len(outs))
	}
	a := outs[0]
	if a.Envelope.Event.Kind != KindAgentEnrolled || a.Envelope.Event.AgentEnrolled == nil {
		t.Fatalf("outbound must be an agent_enrolled, got %+v", a.Envelope.Event)
	}
	if a.Envelope.Domain != h.Domain || a.Envelope.Epoch != h.Epoch {
		t.Fatalf("answer must be addressed to the asking domain, got dom=%s epoch=%d", a.Envelope.Domain, a.Envelope.Epoch)
	}
	// Addressed by the tuple, and by nothing secret: the answer travels the
	// descriptor every descendant of the shell inherits (nocx-aqz7o).
	if a.Envelope.Capability != (Capability{}) {
		t.Fatalf("answer carries the domain capability back to the shell")
	}
	ans := a.Envelope.Event.AgentEnrolled
	if ans.RequestID != "r-agent-0" || ans.Agent != "claude" {
		t.Fatalf("answer must echo the request, got %+v", ans)
	}
	// The verdict belongs to the seam that opens the grid, and it defaults to
	// REFUSED: D4 says failure is closed, so an answer nobody filled in must
	// read as "not orchestrated" rather than as consent.
	if ans.Enrolled {
		t.Fatalf("kernel answer must carry no verdict yet, got %+v", ans)
	}
	// Nothing about the lane moved. Enrolment is not lifecycle state: it
	// cannot open, complete or alter an execution attempt (ADR-0024
	// decision 1, and the AD-6 amendment's second constraint).
	st := mustState(t, k, L)
	assertState(t, st, LifecyclePromptReady, h.Domain, "", []DomainID{h.Domain})
}

// Withdraw is the other end of the interval and is answered the same way, so
// the shell can tell "closed" from "never arrived" — an interval whose close
// is fire-and-forget is an interval with one end.
func TestAgentWithdrawProducesAnAnswerEcho(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const L = LaneID("L")
	h := establish(t, k, "T", tp, L, nil)

	if _, err := k.Ingest("T", env(L, h, 2, enrolEvt("r-agent-0", "claude"))); err != nil {
		t.Fatalf("agent_enrol: %v", err)
	}
	outs, err := k.Ingest("T", env(L, h, 3, withdrawEvt("r-agent-0")))
	if err != nil {
		t.Fatalf("agent_withdraw: %v", err)
	}
	if len(outs) != 1 || outs[0].Envelope.Event.Kind != KindAgentWithdrawn {
		t.Fatalf("expected one agent_withdrawn outbound, got %+v", outs)
	}
	if got := outs[0].Envelope.Event.AgentWithdrawn.RequestID; got != "r-agent-0" {
		t.Errorf("withdrawn echo request id = %q, want %q", got, "r-agent-0")
	}
}

// Validation, and the reason it is here rather than at the seam: a malformed
// frame must be refused before anything downstream is asked to act on it, and
// the refusal must move no state at all.
func TestAgentEnrolValidation(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const L = LaneID("L")
	h := establish(t, k, "T", tp, L, nil)

	cases := []struct {
		name string
		evt  Event
		want error
	}{
		{"no request id", enrolEvt("", "claude"), ErrRequestIDShape},
		{"request id out of shape", enrolEvt("r agent/0", "claude"), ErrRequestIDShape},
		{"no agent named", enrolEvt("r-agent-1", ""), ErrBadRequest},
		// The agent name is spliced into nothing and quoted by nobody, but it
		// is a name a person will read on a refusal, and an unbounded one from
		// a malformed frame is a log line nobody can use.
		{"agent name out of shape", enrolEvt("r-agent-1", "claude; rm -rf /"), ErrBadRequest},
		// A pane with no size is not a pane, and a pane the size of a building
		// is a frame asking the backend to allocate one.
		{"no geometry", Event{Kind: KindAgentEnrol, AgentEnrol: &AgentEnrol{RequestID: "r-agent-1", Agent: "claude"}}, ErrBadRequest},
		{"geometry out of bounds", Event{Kind: KindAgentEnrol, AgentEnrol: &AgentEnrol{RequestID: "r-agent-1", Agent: "claude", Cols: 100000, Rows: 40}}, ErrBadRequest},
	}
	seq := uint64(2)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := k.Ingest("T", env(L, h, seq, c.evt))
			seq++
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			st := mustState(t, k, L)
			assertState(t, st, LifecyclePromptReady, h.Domain, "", []DomainID{h.Domain})
		})
	}
}

// The kernel-originated answers may never be ingested FROM a shell. Same rule
// as accept, refresh_request and domain_grant, and for the same reason: a
// shell that could send its own answer could tell nocx it is orchestrated.
func TestAgentAnswersAreNotIngestible(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const L = LaneID("L")
	h := establish(t, k, "T", tp, L, nil)

	for _, evt := range []Event{
		{Kind: KindAgentEnrolled, AgentEnrolled: &AgentEnrolled{RequestID: "r", Agent: "claude", Enrolled: true}},
		{Kind: KindAgentWithdrawn, AgentWithdrawn: &AgentWithdrawn{RequestID: "r"}},
	} {
		if _, err := k.Ingest("T", env(L, h, 2, evt)); !errors.Is(err, ErrIllegalEvent) {
			t.Errorf("ingesting %s = %v, want ErrIllegalEvent", evt.Kind, err)
		}
	}
}
