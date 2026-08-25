package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
)

// realGrid is the product's own store rather than a double: what this seam is
// tested for is that an enrolment actually opens a grid, and a fake grid can
// only report that the seam called something.
func newEnroller(t *testing.T) (*paneEnroller, *panegrid.Store, *sessionRegistry) {
	t.Helper()
	lg := log.NewSlogAdapter(nil)
	grid := panegrid.New(lg)
	sessions := newSessionRegistry()
	return newPaneEnroller(lg, sessions, grid), grid, sessions
}

// The ordinary case, and every refusal below is paired against it: a lane that
// belongs to a session gets a grid at the geometry it named.
func TestEnrolmentOpensTheGridForTheLanesSession(t *testing.T) {
	e, grid, sessions := newEnroller(t)
	sessions.register("lane-1", "sess-1")

	if err := e.Enrol("lane-1", "claude", 120, 40); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if !grid.Enrolled("sess-1") {
		t.Fatal("the lane's session has no grid")
	}
	f, err := grid.Frame("sess-1")
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if f.Cols != 120 || f.Rows != 40 {
		t.Errorf("grid size = %dx%d, want the enrolment's 120x40", f.Cols, f.Rows)
	}

	e.Withdraw("lane-1")
	if grid.Enrolled("sess-1") {
		t.Error("the grid outlived the withdrawal: the interval has one end")
	}
}

// A lane the backend cannot place is a refusal, not a silent success. It is a
// real state — a domain established before its lane was registered, or a lane
// whose session has already gone — and answering it with "yes" would tell a
// caller it is orchestrated while nothing is watching.
func TestAnUnplaceableLaneIsRefused(t *testing.T) {
	e, grid, _ := newEnroller(t)

	err := e.Enrol("lane-nobody", "claude", 120, 40)
	if err == nil {
		t.Fatal("a lane belonging to no session was enrolled")
	}
	if grid.Count() != 0 {
		t.Errorf("a refused enrolment left %d grids behind", grid.Count())
	}
}

// Re-enrolling a watched pane is refused rather than silently restarted.
// Restarting would discard the grid built so far, and with it the byte-zero
// guarantee that is the only reason to trust a frame at all.
func TestAWatchedPaneIsNotReEnrolled(t *testing.T) {
	e, _, sessions := newEnroller(t)
	sessions.register("lane-1", "sess-1")

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

// The bound the amendment asks for, reached: a grid is a real emulator with a
// real allocation, so the refusal has to name the bound rather than fail
// obscurely.
func TestTheWatchBoundIsRefusedByName(t *testing.T) {
	e, grid, sessions := newEnroller(t)
	for i := 0; i < panegrid.MaxEnrolled; i++ {
		lane := lifecycle.LaneID("lane-" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
		sid := "sess-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		sessions.register(lane, sid)
		if err := e.Enrol(lane, "claude", 80, 24); err != nil {
			t.Fatalf("enrol %d: %v", i, err)
		}
	}
	if grid.Count() != panegrid.MaxEnrolled {
		t.Fatalf("opened %d grids, want %d", grid.Count(), panegrid.MaxEnrolled)
	}
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
	e, _, sessions := newEnroller(t)
	sessions.register("lane-1", "sess-1")
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
