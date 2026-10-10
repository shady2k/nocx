package lifecycle

import (
	"errors"
	"testing"
)

func resolveLaunchEvt(rid RequestID, agent string) Event {
	return Event{Kind: KindAgentLaunchResolve, AgentLaunchResolve: &AgentLaunchResolve{RequestID: rid, Agent: agent}}
}

func cancelLaunchEvt(ticket string) Event {
	return Event{Kind: KindAgentLaunchCancel, AgentLaunchCancel: &AgentLaunchCancel{Agent: "myagent", Ticket: ticket}}
}

func TestAgentLaunchResolveEchoesOnlyRequestIdentity(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const lane = LaneID("L")
	h := establish(t, k, "T", tp, lane, nil)

	outs, err := k.Ingest("T", env(lane, h, 2, resolveLaunchEvt("r-launch-0", "myagent")))
	if err != nil {
		t.Fatalf("agent_launch_resolve: %v", err)
	}
	if len(outs) != 1 || outs[0].Envelope.Event.Kind != KindAgentLaunchResolved {
		t.Fatalf("outbound = %+v, want one agent_launch_resolved response", outs)
	}
	response := outs[0].Envelope.Event.AgentLaunchResolved
	if response == nil || response.RequestID != "r-launch-0" || response.Agent != "myagent" {
		t.Fatalf("response identity = %+v, want request/agent echo", response)
	}
	if response.Local || response.Ticket != "" || response.Payload != "" {
		t.Fatalf("kernel response carried app-owned launch data: %+v", response)
	}
	if outs[0].Envelope.Domain != h.Domain || outs[0].Envelope.Epoch != h.Epoch {
		t.Fatalf("response addressed outside request domain: %+v", outs[0].Envelope)
	}
	assertState(t, mustState(t, k, lane), LifecyclePromptReady, h.Domain, "", []DomainID{h.Domain})
}

func TestAgentLaunchCancelIsAnAuthenticatedNoopOnLifecycleState(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const lane = LaneID("L")
	h := establish(t, k, "T", tp, lane, nil)

	outs, err := k.Ingest("T", env(lane, h, 2, cancelLaunchEvt("0123456789abcdef0123456789abcdef0123456789A")))
	if err != nil {
		t.Fatalf("agent_launch_cancel: %v", err)
	}
	if len(outs) != 0 {
		t.Fatalf("best-effort cancel produced outbound frames: %+v", outs)
	}
	assertState(t, mustState(t, k, lane), LifecyclePromptReady, h.Domain, "", []DomainID{h.Domain})
}

func TestAgentLaunchProtocolRejectsMalformedRequestsAndTickets(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const lane = LaneID("L")
	h := establish(t, k, "T", tp, lane, nil)
	cases := []struct {
		name string
		evt  Event
		want error
	}{
		{name: "resolve missing request", evt: resolveLaunchEvt("", "myagent"), want: ErrRequestIDShape},
		{name: "resolve malformed agent", evt: resolveLaunchEvt("r-launch-1", "bad;name"), want: ErrBadRequest},
		{name: "cancel missing ticket", evt: cancelLaunchEvt(""), want: ErrBadRequest},
		{name: "cancel malformed ticket", evt: cancelLaunchEvt("not-a-ticket"), want: ErrBadRequest},
		{name: "cancel missing agent", evt: Event{Kind: KindAgentLaunchCancel, AgentLaunchCancel: &AgentLaunchCancel{Ticket: "0123456789abcdef0123456789abcdef0123456789A"}}, want: ErrBadRequest},
		{name: "enrol malformed ticket", evt: Event{Kind: KindAgentEnrol, AgentEnrol: &AgentEnrol{RequestID: "r-enrol-1", Agent: "myagent", Cols: 80, Rows: 24, LaunchTicket: "bad"}}, want: ErrBadRequest},
	}
	seq := uint64(2)
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := k.Ingest("T", env(lane, h, seq, tt.evt))
			seq++
			if !errors.Is(err, tt.want) {
				t.Fatalf("Ingest error = %v, want %v", err, tt.want)
			}
		})
	}
}
