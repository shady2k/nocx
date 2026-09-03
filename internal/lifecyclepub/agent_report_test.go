package lifecyclepub_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
)

func reportEvt(rid lifecycle.RequestID, ok bool, summary string) lifecycle.Event {
	return lifecycle.Event{Kind: lifecycle.KindAgentReport, AgentReport: &lifecycle.AgentReport{
		RequestID: rid, OK: ok, Summary: summary,
	}}
}

// fakeReporter records what the seam was asked, and can be told to refuse.
type fakeReporter struct {
	reported []string // "lane/ok/summary", in order
	err      error
}

func (f *fakeReporter) Report(lane lifecycle.LaneID, ok bool, summary string) error {
	if f.err != nil {
		return f.err
	}
	f.reported = append(f.reported, fmt.Sprintf("%s/%t/%s", lane, ok, summary))
	return nil
}

// reportingPub is establishedPub with a reporter instead of an enroller.
func reportingPub(t *testing.T, r lifecyclepub.AgentReporter) (*lifecyclepub.Publisher, *recordingPort, lifecycle.DomainHandle) {
	t.Helper()
	k := lifecycle.New(lifecycle.Options{})
	var opts []lifecyclepub.Option
	if r != nil {
		opts = append(opts, lifecyclepub.WithAgentReporter(r))
	}
	pub := lifecyclepub.New(k, opts...)
	rec := &recorder{}
	pub.SetEmitter(rec)
	port := &recordingPort{}
	if err := pub.BindTransport("T", port); err != nil {
		t.Fatal(err)
	}
	h, err := pub.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatal(err)
	}
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	mustAckEstablishment(t, pub, rec, "L", h)
	return pub, port, h
}

// The ordinary case: an authenticated declaration reaches the seam with the
// lane it came from and the verdict it carried, and the participant is told it
// was recorded.
func TestAnAuthenticatedReportReachesTheSeamAndIsRecorded(t *testing.T) {
	r := &fakeReporter{}
	pub, port, h := reportingPub(t, r)

	mustIngest(t, pub, "T", env("L", h, 2, reportEvt("r-agent-1", true, "read it")))

	if len(r.reported) != 1 || r.reported[0] != "L/true/read it" {
		t.Fatalf("seam saw %v, want one successful report on lane L", r.reported)
	}
	ans := answerFrom(t, port, lifecycle.KindAgentReported)
	if ans.Domain != h.Domain || ans.Epoch != h.Epoch {
		t.Fatalf("answer must be addressed to the asking domain, got dom=%s epoch=%d", ans.Domain, ans.Epoch)
	}
	// By the tuple, and by nothing secret (nocx-aqz7o).
	if ans.Capability != (lifecycle.Capability{}) {
		t.Fatal("the answer carries the domain capability back down the inherited descriptor")
	}
	a := ans.Event.AgentReported
	if a == nil || !a.Recorded || a.RequestID != "r-agent-1" {
		t.Fatalf("answer = %+v, want a recorded declaration echoing r-agent-1", a)
	}
	if a.Reason != "" {
		t.Errorf("a recorded declaration carries no reason, got %q", a.Reason)
	}
}

// Every silent path is a REFUSAL, and each one says why in a sentence the
// participant can print. This is D4 at the second fact's carrier: a
// declaration that looked accepted while nothing wrote it is exactly the
// silent degrade the wave record exists to prevent.
func TestEverySilentReportPathIsARefusalThatSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reporter lifecyclepub.AgentReporter
		want     string
	}{
		{
			name:     "no seam wired at all",
			reporter: nil,
			want:     "this backend is not wired to record what an agent produced",
		},
		{
			name:     "a seam that refused",
			reporter: &fakeReporter{err: errors.New("this pane is not part of a wave")},
			want:     "this pane is not part of a wave",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub, port, h := reportingPub(t, tc.reporter)
			mustIngest(t, pub, "T", env("L", h, 2, reportEvt("r-agent-1", true, "read it")))

			a := answerFrom(t, port, lifecycle.KindAgentReported).Event.AgentReported
			if a == nil {
				t.Fatalf("no answer delivered")
			}
			if a.Recorded {
				t.Fatalf("a declaration nothing recorded was answered as recorded")
			}
			if a.Reason != tc.want {
				t.Fatalf("reason = %q, want %q", a.Reason, tc.want)
			}
		})
	}
}

// A failure verdict travels as a failure. It is a separate case from the
// success above because OK is the one field a truncated frame decodes to
// false, so a test that only ever sent true could not tell a carried false
// from a dropped one.
func TestAFailureVerdictReachesTheSeamAsAFailure(t *testing.T) {
	r := &fakeReporter{}
	pub, _, h := reportingPub(t, r)
	mustIngest(t, pub, "T", env("L", h, 2, reportEvt("r-agent-1", false, "could not build")))
	if len(r.reported) != 1 || r.reported[0] != "L/false/could not build" {
		t.Fatalf("seam saw %v, want one failed report", r.reported)
	}
}
