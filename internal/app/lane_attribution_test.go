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

// TestEveryClosedLaneLeavesTheRegistry keeps the registry from growing with
// the run: every lane this test opens is closed, and once its peer returns the
// registry must be back to what it held before. That is also what -count=N
// needs: a second round of the same test must not find the first round's
// entries still there.
func TestEveryClosedLaneLeavesTheRegistry(t *testing.T) {
	const cycles = 8
	before := laneConns.count()
	provider := &fakeLaneProvider{owner: t.Name(), peer: silentLanePeer}
	lanes := make([]*fakeLaneConn, 0, cycles)
	t.Cleanup(func() {
		for _, lane := range lanes {
			_ = lane.Close()
		}
		waittest.WaitFor(t, "this test's lanes to leave the registry", func() bool {
			for _, lane := range lanes {
				if laneRegistered(lane) {
					return false
				}
			}
			return true
		})
	})
	for range cycles {
		lanes = append(lanes, openSilentLane(t, provider))
	}
	if grew := laneConns.count() - before; grew != cycles {
		t.Fatalf("the registry grew by %d for %d open lanes, want %d", grew, cycles, cycles)
	}
	for i, lane := range lanes {
		if err := lane.Close(); err != nil {
			t.Errorf("closing lane %d: %v", i, err)
		}
	}
	waittest.WaitFor(t, "every closed lane to leave the registry", func() bool {
		return laneConns.count() == before
	})
	if grew := laneConns.count() - before; grew != 0 {
		t.Fatalf("%d of this test's lanes are still registered after being closed and their peers returned", grew)
	}
}
