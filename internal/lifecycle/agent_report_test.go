package lifecycle

import (
	"errors"
	"strings"
	"testing"
)

func reportEvt(rid RequestID, ok bool, summary string) Event {
	return Event{Kind: KindAgentReport, AgentReport: &AgentReport{RequestID: rid, OK: ok, Summary: summary}}
}

// A participant's declaration, as the wire sees it. Like the enrolment beside
// it, an authenticated agent_report produces exactly one answer addressed back
// to the asking domain and carries NO verdict of its own: the seam that
// actually writes the fact into the wave record is the one that decides, and
// the kernel neither holds a record nor knows what one is.
func TestAgentReportProducesAnAnswerEcho(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const L = LaneID("L")
	h := establish(t, k, "T", tp, L, nil)

	outs, err := k.Ingest("T", env(L, h, 2, reportEvt("r-agent-0", true, "read it")))
	if err != nil {
		t.Fatalf("agent_report: %v", err)
	}
	if len(outs) != 1 {
		t.Fatalf("expected exactly one outbound (the answer), got %d", len(outs))
	}
	a := outs[0]
	if a.Envelope.Event.Kind != KindAgentReported || a.Envelope.Event.AgentReported == nil {
		t.Fatalf("outbound must be an agent_reported, got %+v", a.Envelope.Event)
	}
	if a.Envelope.Domain != h.Domain || a.Envelope.Epoch != h.Epoch {
		t.Fatalf("answer must be addressed to the asking domain, got dom=%s epoch=%d", a.Envelope.Domain, a.Envelope.Epoch)
	}
	if a.Envelope.Capability != (Capability{}) {
		t.Fatalf("answer carries the domain capability back to the shell")
	}
	ans := a.Envelope.Event.AgentReported
	if ans.RequestID != "r-agent-0" {
		t.Fatalf("answer must echo the request, got %+v", ans)
	}
	// Defaults to NOT RECORDED, for the reason the enrolment defaults to not
	// enrolled: a declaration that looked accepted while nothing wrote it is
	// the silent degrade the whole record exists to prevent.
	if ans.Recorded {
		t.Fatalf("kernel answer must carry no verdict yet, got %+v", ans)
	}
}

// What the kernel refuses, and it is only what it can judge from the frame:
// the request id's shape and the summary's length. Whether the pane is a
// participant, and whether the record accepted the fact, are the seam's to
// answer — a kernel that decided either would be a second owner of the wave.
func TestAgentReportRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		evt  Event
		want error
	}{
		{"no request id", Event{Kind: KindAgentReport, AgentReport: &AgentReport{OK: true}}, ErrRequestIDShape},
		{"a request id of the wrong shape", reportEvt("has a space", true, ""), ErrRequestIDShape},
		{"a request id past its bound", reportEvt(RequestID(strings.Repeat("r", 65)), true, ""), ErrRequestIDShape},
		{
			"a summary past the frame's share",
			reportEvt("r-agent-0", true, strings.Repeat("x", MaxReportSummaryBytes+1)),
			ErrBadRequest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, _, _ := newTestKernel()
			tp := &fakePort{}
			if err := k.BindTransport("T", tp); err != nil {
				t.Fatal(err)
			}
			const L = LaneID("L")
			h := establish(t, k, "T", tp, L, nil)
			_, err := k.Ingest("T", env(L, h, 2, tc.evt))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// A summary exactly at the bound is accepted. The paired positive for the
// refusal above: a bound with only a negative test cannot tell "refuses too
// much" from "refuses correctly".
func TestAgentReportAcceptsASummaryAtTheBound(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const L = LaneID("L")
	h := establish(t, k, "T", tp, L, nil)
	if _, err := k.Ingest("T", env(L, h, 2,
		reportEvt("r-agent-0", true, strings.Repeat("x", MaxReportSummaryBytes)))); err != nil {
		t.Fatalf("a summary at the bound was refused: %v", err)
	}
}
