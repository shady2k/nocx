package lifecyclepub_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
)

func enrolEvt(rid lifecycle.RequestID, agent string) lifecycle.Event {
	return lifecycle.Event{Kind: lifecycle.KindAgentEnrol, AgentEnrol: &lifecycle.AgentEnrol{
		RequestID: rid, Agent: agent, Cols: 120, Rows: 40,
	}}
}

func withdrawEvt(rid lifecycle.RequestID) lifecycle.Event {
	return lifecycle.Event{Kind: lifecycle.KindAgentWithdraw, AgentWithdraw: &lifecycle.AgentWithdraw{
		RequestID: rid,
	}}
}

// fakeEnroller records what the seam was asked, and can be told to refuse.
type fakeEnroller struct {
	enrolled  []string // "lane/agent/COLSxROWS", in order
	withdrawn []string
	err       error
	// openAt records what the seam had already done at the moment it was
	// called, so a test can assert ORDER rather than merely occurrence.
	openWhenAsked func()
}

func (f *fakeEnroller) Enrol(lane lifecycle.LaneID, agent string, cols, rows int) error {
	if f.openWhenAsked != nil {
		f.openWhenAsked()
	}
	if f.err != nil {
		return f.err
	}
	f.enrolled = append(f.enrolled, fmt.Sprintf("%s/%s/%dx%d", lane, agent, cols, rows))
	return nil
}

func (f *fakeEnroller) Withdraw(lane lifecycle.LaneID) {
	f.withdrawn = append(f.withdrawn, string(lane))
}

func answerFrom(t *testing.T, port *recordingPort, kind lifecycle.EventKind) lifecycle.Envelope {
	t.Helper()
	for i := range port.sent {
		if port.sent[i].Event.Kind == kind {
			return port.sent[i]
		}
	}
	t.Fatalf("no %s delivered to the port; sent kinds=%v", kind, port.kinds())
	return lifecycle.Envelope{}
}

// establishedPub is the common setup: a publisher with an enroller, one
// established domain on lane L, and the port its answers land on.
func establishedPub(t *testing.T, e lifecyclepub.AgentEnroller) (*lifecyclepub.Publisher, *recordingPort, lifecycle.DomainHandle) {
	t.Helper()
	k := lifecycle.New(lifecycle.Options{})
	var opts []lifecyclepub.Option
	if e != nil {
		opts = append(opts, lifecyclepub.WithAgentEnroller(e))
	}
	pub := lifecyclepub.New(k, opts...)
	r := &recorder{}
	pub.SetEmitter(r)
	port := &recordingPort{}
	if err := pub.BindTransport("T", port); err != nil {
		t.Fatal(err)
	}
	h, err := pub.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatal(err)
	}
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	mustAckEstablishment(t, pub, r, "L", h)
	return pub, port, h
}

// The ordinary case, which every refusal below is paired against: an
// authenticated enrolment reaches the seam with the lane it came from and the
// agent it named, and the caller is told yes.
func TestAnAuthenticatedEnrolmentReachesTheSeamAndIsGranted(t *testing.T) {
	e := &fakeEnroller{}
	pub, port, h := establishedPub(t, e)

	mustIngest(t, pub, "T", env("L", h, 2, enrolEvt("r-agent-0", "claude")))

	if len(e.enrolled) != 1 || e.enrolled[0] != "L/claude/120x40" {
		t.Fatalf("seam saw %v, want one enrolment of claude on lane L at 120x40", e.enrolled)
	}
	ans := answerFrom(t, port, lifecycle.KindAgentEnrolled)
	if ans.Domain != h.Domain || ans.Epoch != h.Epoch || ans.Capability != h.Capability {
		t.Fatalf("answer must be addressed to the asking domain, got dom=%s epoch=%d", ans.Domain, ans.Epoch)
	}
	a := ans.Event.AgentEnrolled
	if a == nil || !a.Enrolled || a.RequestID != "r-agent-0" || a.Agent != "claude" {
		t.Fatalf("answer = %+v, want an enrolled claude echoing r-agent-0", a)
	}
	if a.Reason != "" {
		t.Errorf("a granted enrolment carries no reason, got %q", a.Reason)
	}
}

// THE BYTE-ZERO GUARANTEE, asserted as an order rather than as an occurrence.
// The caller launches the agent the instant it reads "enrolled", so a grid
// opened after the answer went out is a grid that missed the start of the
// process — and a frame whose earlier state was never seen is exactly what the
// amendment says a grid must never be.
func TestTheGridIsOpenBeforeTheCallerIsTold(t *testing.T) {
	var order []string
	e := &fakeEnroller{openWhenAsked: func() { order = append(order, "grid opened") }}
	pub, port, h := establishedPub(t, e)
	port.onSend = func(env lifecycle.Envelope) {
		if env.Event.Kind == lifecycle.KindAgentEnrolled {
			order = append(order, "caller told")
		}
	}

	mustIngest(t, pub, "T", env("L", h, 2, enrolEvt("r-agent-0", "claude")))

	if len(order) != 2 || order[0] != "grid opened" || order[1] != "caller told" {
		t.Fatalf("order = %v, want the grid opened before the caller was told", order)
	}
}

// Failure is closed, and this is the case that matters most because it is the
// one nobody writes on purpose: a backend with no enroller wired at all. It
// must read as "not orchestrated", never as consent — the opposite of the
// grant path beside it, which answers an unwired builder with an empty
// bootstrap and lets the command run conventionally.
func TestAnUnwiredBackendRefusesEveryEnrolment(t *testing.T) {
	pub, port, h := establishedPub(t, nil)

	mustIngest(t, pub, "T", env("L", h, 2, enrolEvt("r-agent-0", "claude")))

	a := answerFrom(t, port, lifecycle.KindAgentEnrolled).Event.AgentEnrolled
	if a == nil || a.Enrolled {
		t.Fatalf("answer = %+v, want a refusal", a)
	}
	if a.Reason == "" {
		t.Error("a refusal with no reason cannot be shown to the person it refuses")
	}
}

// A seam that refuses — the pane bound is reached, the store is gone, the
// pane is already watched — refuses the caller, with the seam's own words.
func TestASeamRefusalReachesTheCallerWithItsReason(t *testing.T) {
	e := &fakeEnroller{err: errors.New("too many panes are already watched")}
	pub, port, h := establishedPub(t, e)

	mustIngest(t, pub, "T", env("L", h, 2, enrolEvt("r-agent-0", "claude")))

	a := answerFrom(t, port, lifecycle.KindAgentEnrolled).Event.AgentEnrolled
	if a == nil || a.Enrolled {
		t.Fatalf("answer = %+v, want a refusal", a)
	}
	if a.Reason != "too many panes are already watched" {
		t.Errorf("reason = %q, want the seam's own", a.Reason)
	}
}

// The other end of the interval. Withdraw is answered too, so the caller can
// tell a close that happened from one that never arrived.
func TestWithdrawClosesTheIntervalAndIsAnswered(t *testing.T) {
	e := &fakeEnroller{}
	pub, port, h := establishedPub(t, e)

	mustIngest(t, pub, "T", env("L", h, 2, enrolEvt("r-agent-0", "claude")))
	mustIngest(t, pub, "T", env("L", h, 3, withdrawEvt("r-agent-0")))

	if len(e.withdrawn) != 1 || e.withdrawn[0] != "L" {
		t.Fatalf("seam saw withdrawals %v, want one for lane L", e.withdrawn)
	}
	w := answerFrom(t, port, lifecycle.KindAgentWithdrawn).Event.AgentWithdrawn
	if w == nil || w.RequestID != "r-agent-0" {
		t.Fatalf("withdrawn answer = %+v, want it echoing r-agent-0", w)
	}
}
