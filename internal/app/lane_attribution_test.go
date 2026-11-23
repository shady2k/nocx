package app

// The stand's leak report names the test that opened a fake lane, and the
// registry keeps only lanes that have not ended (nocx-y3kfq).
//
// A lane's author has to be named where the fixture is built. A test that
// drives `open` over the socket reaches the fake lane through the server's
// readLoop, on a goroutine that holds no test's frame, and a synchronous
// t.Run subtest's frame reads TestX.func1 rather than its t.Name() — the two
// ways the stack-walking answer was wrong for exactly the leaks the report
// exists to name. Both are asserted here against the function TestMain prints
// from.

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/waittest"
)

// silentLanePeer is the smallest helper peer: it says nothing and ends when
// the lane's stdin closes, so a lane made with it is open exactly until
// somebody closes it.
func silentLanePeer(in io.Reader, _ io.Writer) int {
	_, _ = io.Copy(io.Discard, in)
	return 0
}

// openSilentLane opens one lane on provider and returns it.
func openSilentLane(t *testing.T, provider *fakeLaneProvider) *fakeLaneConn {
	t.Helper()
	if _, err := provider.LaneConn(context.Background(), "host.example",
		proto.Machine{}, proto.GenerationID(syntheticArtifactHash)); err != nil {
		t.Fatalf("opening a fake lane: %v", err)
	}
	return provider.lane(provider.laneCount() - 1)
}

// laneRegistered reports whether the registry still holds lane.
func laneRegistered(lane *fakeLaneConn) bool {
	for _, c := range laneConns.liveLanes() {
		if c == lane {
			return true
		}
	}
	return false
}

// TestALaneOpenedOffTheTestGoroutineIsReportedByItsTest is the report's own
// check: a named subtest opens a lane from a background goroutine — the shape
// of an open that arrives over the socket — and leaves it open while the
// report is read. The line must carry this subtest's t.Name(), the name a
// reader searches for, and neither "?" (the creating goroutine holds no test
// frame) nor "func1" (the closure, which is what a stack walk finds inside a
// t.Run body).
func TestALaneOpenedOffTheTestGoroutineIsReportedByItsTest(t *testing.T) {
	t.Run("opened on a background goroutine", func(t *testing.T) {
		provider := &fakeLaneProvider{owner: t.Name(), peer: silentLanePeer}
		opened := make(chan *fakeLaneConn, 1)
		go func() {
			_, err := provider.LaneConn(context.Background(), "host.example",
				proto.Machine{}, proto.GenerationID(syntheticArtifactHash))
			if err != nil {
				t.Errorf("opening a fake lane off this goroutine: %v", err)
				opened <- nil
				return
			}
			opened <- provider.lane(provider.laneCount() - 1)
		}()
		lane := <-opened
		if lane == nil {
			t.FailNow()
		}
		// The lane is ENDED before the subtest returns, and the cleanup waits
		// for its peer, so this test's deliberate open never reaches the real
		// suite's report.
		t.Cleanup(func() {
			_ = lane.Close()
			waittest.WaitFor(t, "the closed lane to leave the registry", func() bool {
				return !laneRegistered(lane)
			})
		})

		want := "  a fake lane opened by " + t.Name() + " never ended: it was closed 0 time(s), " +
			"and its helper peer is still running"
		if got := lane.leakLine(); got != want {
			t.Fatalf("the leak line = %q, want %q", got, want)
		}
		report := laneLeakReport()
		for _, line := range report {
			if line == want {
				return
			}
		}
		t.Fatalf("the leak report does not carry this subtest's line; it says:\n%s", strings.Join(report, "\n"))
	})
}

// noneRegistered reports whether the registry has dropped every lane of lanes.
func noneRegistered(lanes []*fakeLaneConn) bool {
	for _, lane := range lanes {
		if laneRegistered(lane) {
			return false
		}
	}
	return true
}

// TestEveryClosedLaneLeavesTheRegistry keeps the registry from growing with
// the run: every lane this test opens is registered while its peer runs, and
// gone once it is closed and its peer has returned. That is also what
// -count=N needs: a second round of the same test must not find the first
// round's entries still there.
//
// The assertion is about the eight POINTERS this test opened, never about the
// registry's size: another test's lane can be registered when this one starts
// and be forgotten while it runs, so a count makes the same registry state
// pass or fail depending on who else is alive (nocx-y3kfq).
func TestEveryClosedLaneLeavesTheRegistry(t *testing.T) {
	const cycles = 8
	provider := &fakeLaneProvider{owner: t.Name(), peer: silentLanePeer}
	lanes := make([]*fakeLaneConn, 0, cycles)
	t.Cleanup(func() {
		for _, lane := range lanes {
			_ = lane.Close()
		}
		waittest.WaitFor(t, "this test's lanes to leave the registry", func() bool {
			return noneRegistered(lanes)
		})
	})
	for range cycles {
		lanes = append(lanes, openSilentLane(t, provider))
	}
	for i, lane := range lanes {
		if !laneRegistered(lane) {
			t.Fatalf("lane %d that this test opened is not in the registry", i)
		}
	}
	for i, lane := range lanes {
		if err := lane.Close(); err != nil {
			t.Errorf("closing lane %d: %v", i, err)
		}
	}
	waittest.WaitFor(t, "every closed lane to leave the registry", func() bool {
		return noneRegistered(lanes)
	})
}

// TestAnEndedLaneIsNotReportedWhileItIsStillRegistered covers the window
// between the peer's end and the registry's forget: the peer has returned and
// closed its lane's ended channel, and forget has not dropped the entry yet.
// laneLeakReport must not name that lane — a lane whose peer is gone is not a
// leak — or the report claims a lane is still running as it ends
// (nocx-y3kfq).
//
// The state is registered directly rather than waited for: the two statements
// that produce it run back to back on the peer's goroutine, so nothing outside
// can hold them apart, and a report read in that gap by luck is a test of the
// scheduler rather than of the filter.
func TestAnEndedLaneIsNotReportedWhileItIsStillRegistered(t *testing.T) {
	lane := &fakeLaneConn{exited: make(chan struct{}), openedBy: t.Name()}
	close(lane.exited)
	laneConns.add(lane)
	t.Cleanup(func() { laneConns.forget(lane) })

	if !laneRegistered(lane) {
		t.Fatal("the ended lane is not in the registry, so the window the report has to survive is not under test")
	}
	ended := lane.leakLine()
	for _, line := range laneLeakReport() {
		if line == ended {
			t.Fatalf("laneLeakReport names a lane whose peer has returned as still running: %q", line)
		}
	}
}
