package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/paneview/paneviewtest"
	"github.com/shady2k/nocx/internal/session"
)

type allowPaneApproval struct{}

func (allowPaneApproval) Approve(context.Context, session.ID, string) error { return nil }
func (allowPaneApproval) Forget(session.ID)                                 {}

func TestNewPaneEnrollerRejectsMissingApproval(t *testing.T) {
	if _, err := newPaneEnroller(nil, nil, nil, nil, nil); err == nil {
		t.Fatal("newPaneEnroller accepted a missing approval dependency")
	}
}

// realStore is the product's own store rather than a double: what this seam is
// tested for is that an enrolment actually opens a watch, and a fake store can
// only report that the seam called something.
func newEnroller(t *testing.T) (*paneEnroller, *paneviewtest.Views, *sessionRegistry) {
	t.Helper()
	e, views, sessions, _ := newEnrollerWithWatcher(t)
	return e, views, sessions
}

// The same, plus the watcher — for the tests that assert the observation opens
// and closes with the watch rather than beside it.
func newEnrollerWithWatcher(t *testing.T) (*paneEnroller, *paneviewtest.Views, *sessionRegistry, *paneobserve.Watcher) {
	t.Helper()
	lg := log.NewSlogAdapter(nil)
	views := paneviewtest.NewViews(lg)
	drivers, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	watch := paneobserve.New(lg, views.Store, drivers, paneobserve.Config{})
	sessions := newSessionRegistry()
	e, err := newPaneEnroller(lg, sessions, views.Store, watch, allowPaneApproval{})
	if err != nil {
		t.Fatalf("enroller: %v", err)
	}
	return e, views, sessions, watch
}

// placeSession registers the lane's session and declares that session's
// runtime in the source, because production does the two in one act: a
// session's PTY is opened by the helper that holds it, so the runtime beside
// that PTY answers for the pane from the moment the session exists (the one
// paneview.Source is internal/app/panescreen.go's). Enrolment probes that
// runtime before it claims a watch slot, so a fixture that registered a lane
// and declared no runtime would be asking nocx to watch a pane it has no way
// to read — which is the state the probe exists to refuse.
func placeSession(sessions *sessionRegistry, views *paneviewtest.Views, lane lifecycle.LaneID, sid string, cols, rows int) {
	sessions.register(lane, sid)
	views.Size(sid, cols, rows)
}

// The ordinary case, and every refusal below is paired against it: a lane that
// belongs to a session gets a watch over that session's pane, and a frame read
// comes back at the geometry the SESSION's runtime holds — the size a shell
// reports for itself is not what a frame is measured at (panescreen.go).
func TestEnrolmentOpensTheWatchForTheLanesSession(t *testing.T) {
	e, views, sessions := newEnroller(t)
	placeSession(sessions, views, "lane-1", "sess-1", 120, 40)

	if err := e.Enrol("lane-1", "claude", 120, 40); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if !views.Watched("sess-1") {
		t.Fatal("the lane's session has no watch")
	}
	f, err := views.Frame("sess-1")
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if f.Cols != 120 || f.Rows != 40 {
		t.Errorf("frame size = %dx%d, want the session's 120x40", f.Cols, f.Rows)
	}

	e.Withdraw("lane-1")
	if views.Watched("sess-1") {
		t.Error("the watch outlived the withdrawal: the interval has one end")
	}
}

// A lane the backend cannot place is a refusal, not a silent success. It is a
// real state — a domain established before its lane was registered, or a lane
// whose session has already gone — and answering it with "yes" would tell a
// caller it is orchestrated while nothing is watching.
func TestAnUnplaceableLaneIsRefused(t *testing.T) {
	e, views, _ := newEnroller(t)

	err := e.Enrol("lane-nobody", "claude", 120, 40)
	if err == nil {
		t.Fatal("a lane belonging to no session was enrolled")
	}
	if views.Count() != 0 {
		t.Errorf("a refused enrolment left %d watches behind", views.Count())
	}
}

// Re-enrolling a watched pane is refused rather than silently restarted.
// Restarting would absorb a second open, and one withdrawal could no longer be
// said to close what it claimed: the interval has exactly one owner.
func TestAWatchedPaneIsNotReEnrolled(t *testing.T) {
	e, views, sessions := newEnroller(t)
	placeSession(sessions, views, "lane-1", "sess-1", 120, 40)

	if err := e.Enrol("lane-1", "claude", 120, 40); err != nil {
		t.Fatalf("first enrol: %v", err)
	}
	err := e.Enrol("lane-1", "claude", 80, 24)
	if err == nil {
		t.Fatal("a pane already being watched was enrolled a second time")
	}
	if !strings.Contains(err.Error(), "already") {
		t.Errorf("refusal reads %q; it is shown to a person and must say what happened", err)
	}
}

// The bound the amendment asks for, reached: what a caller looping over
// enrolments exhausts is the helper's willingness to answer a frame read per
// pane, so the refusal has to name the bound rather than fail obscurely.
func TestTheWatchBoundIsRefusedByName(t *testing.T) {
	e, views, sessions := newEnroller(t)
	for i := range paneview.MaxWatched {
		lane := lifecycle.LaneID("lane-" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
		sid := "sess-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		placeSession(sessions, views, lane, sid, 80, 24)
		if err := e.Enrol(lane, "claude", 80, 24); err != nil {
			t.Fatalf("enrol %d: %v", i, err)
		}
	}
	if views.Count() != paneview.MaxWatched {
		t.Fatalf("opened %d watches, want %d", views.Count(), paneview.MaxWatched)
	}
	// The last lane's session is deliberately left with no runtime in the
	// source. The bound is checked BEFORE the pane is probed, so an
	// implementation that asked for a screen first would refuse this enrolment
	// for the other reason and fail the message assertion below.
	sessions.register("lane-over", "sess-over")
	err := e.Enrol("lane-over", "claude", 80, 24)
	if err == nil {
		t.Fatal("the watch bound was exceeded")
	}
	if !strings.Contains(err.Error(), "already watching") {
		t.Errorf("refusal reads %q; it must say the bound was reached", err)
	}
}

// Withdrawing something that was never enrolled is not an error. A caller
// racing a session teardown should not have to find out who won, and the
// backend closes the same interval again when the session's output ends.
func TestWithdrawingAnUnwatchedPaneIsQuiet(t *testing.T) {
	e, views, sessions := newEnroller(t)
	placeSession(sessions, views, "lane-1", "sess-1", 80, 24)
	e.Withdraw("lane-1")    // never enrolled
	e.Withdraw("lane-none") // not even placeable
}

// The seam satisfies the interface the publisher wires it behind. Without this
// the composition compiles only because app.go names it, and a signature drift
// would be found at the composition root rather than here.
func TestPaneEnrollerRefusalsAreErrorsThePublisherCanShow(t *testing.T) {
	e, _, _ := newEnroller(t)
	err := e.Enrol("nowhere", "claude", 80, 24)
	if err == nil || errors.Unwrap(err) != nil {
		// A wrapped error would carry an internal chain into a sentence a
		// person reads in their own pane.
		t.Fatalf("refusal = %v, want a plain sentence", err)
	}
}

// The enrolment act opens the OBSERVATION as well as the watch, and the pane is
// classified without waiting for another byte. A pane already has a screen by
// the time its agent asks to be watched; a watcher that waited for the next
// chunk would leave a settled agent invisible for as long as it stayed settled,
// which is exactly the state the indicator most needs to show.
func TestEnrolmentOpensTheObservationAndTheFirstSweepReportsThePane(t *testing.T) {
	e, views, sessions, watch := newEnrollerWithWatcher(t)
	placeSession(sessions, views, "lane-1", "sess-1", 40, 14)
	var got []paneobserve.Observation
	watch.SetEmitter(func(o paneobserve.Observation) { got = append(got, o) })

	if err := e.Enrol("lane-1", "claude", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	rule := strings.Repeat("─", 40)
	views.Feed("sess-1", []byte("\x1b[2J\x1b[7;1H  0 tokens\x1b[8;1H"+rule+
		"\x1b[9;1H❯ \x1b[10;1H"+rule+"\x1b[12;1H  ⏵⏵ auto mode on\x1b[9;3H"))
	watch.Sweep()

	if len(got) != 1 {
		t.Fatalf("observations = %+v, want one for the enrolled pane", got)
	}
	if got[0].PaneID != "sess-1" || got[0].Agent != "claude" {
		t.Errorf("observation = %+v, want sess-1/claude", got[0])
	}
}

// And closes it. The interval has both ends here too: a withdrawn pane is not
// observed, and the observation does not outlive the watch it reads through.
func TestWithdrawalClosesTheObservationWithTheWatch(t *testing.T) {
	e, views, sessions, watch := newEnrollerWithWatcher(t)
	placeSession(sessions, views, "lane-1", "sess-1", 40, 14)
	var got []paneobserve.Observation
	watch.SetEmitter(func(o paneobserve.Observation) { got = append(got, o) })

	if err := e.Enrol("lane-1", "claude", 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	e.Withdraw("lane-1")
	// The withdrawal SAYS SO rather than going quiet. A worker that finished
	// and simply stopped being reported leaves its tab showing whatever its
	// title last said — which for an agent that exits without repainting is
	// "working", forever.
	if len(got) != 1 || got[0].State != agentdriver.StateExited {
		t.Fatalf("after the withdrawal: %+v, want one exited observation", got)
	}
	got = nil
	watch.Sweep()
	if len(got) != 0 {
		t.Fatalf("a withdrawn pane was still classified: %+v", got)
	}
	// And a client attaching afterwards learns what became of the pane.
	o, ok := watch.Snapshot("sess-1")
	if !ok || o.State != agentdriver.StateExited {
		t.Fatalf("snapshot after withdrawal = %+v/%v, want an exited observation", o, ok)
	}
}

// A refused enrolment opens NOTHING. The watch is refused and the observation
// must be refused with it, or nocx would go on reporting a state for a pane it
// declined to watch — a claim with no evidence behind it.
func TestARefusedEnrolmentOpensNoObservation(t *testing.T) {
	e, _, _, watch := newEnrollerWithWatcher(t)
	var got []paneobserve.Observation
	watch.SetEmitter(func(o paneobserve.Observation) { got = append(got, o) })

	if err := e.Enrol("lane-unknown", "claude", 40, 14); err == nil {
		t.Fatal("a lane that maps to no session was enrolled")
	}
	watch.Sweep()
	if len(got) != 0 {
		t.Fatalf("a refused enrolment produced %+v", got)
	}
}
