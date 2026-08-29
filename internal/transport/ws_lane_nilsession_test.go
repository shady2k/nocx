package transport

// A resize claimed for a session that is going away (nocx-44349).
//
// The reported crash was a nil pointer dereference at sess.Resize on the lane
// worker — a goroutine nobody recovers, so it killed the whole backend rather
// than one resize. The second argument in the trace was {0x0, 0x0}: a nil
// INTERFACE, so the lane held no session at all at the moment it applied.
//
// The mechanism is not the one the crash first suggests. A close that lands
// between the worker's claim and its apply cannot produce it: the claim reads
// sess and closed in ONE mu hold, and admitClose writes both under the same
// mutex, so a lane that reported itself open handed out a session that was
// still there (TestCloseBetweenClaimAndApplyKeepsTheClaimedSession forces
// exactly that ordering and shows it is survivable).
//
// What produced it is a lane that was OPEN AND HELD NO SESSION — a state that
// only closeLane could publish, by putting a fresh lane in the map and
// admitting its close on the next statement. Another goroutine already
// blocked on lanesMu inside laneFor takes the lock the instant closeLane
// releases it, finds the lane open, enqueues, and starts a worker on a
// session that was never there. That is the ordering these tests force.

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// laneTestOp returns a resize op and the channel its completion arrives on.
// An admitted request must always be answered, so every test here waits for
// it rather than assuming.
func laneTestOp(cols, rows uint16) (*resizeOp, chan error) {
	settled := make(chan error, 1)
	return &resizeOp{
		reported: session.Size{Cols: cols, Rows: rows},
		done: func(err error) {
			select {
			case settled <- err:
			default:
			}
		},
	}, settled
}

func awaitSettled(t *testing.T, settled chan error, what string) error {
	t.Helper()
	select {
	case err := <-settled:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: the admitted resize was never answered", what)
		return nil
	}
}

// --- the defect, forced ----------------------------------------------------

// TestResizeClaimedForALaneWithNoSessionIsAbandoned forces the STATE the
// crash needs instead of hoping to observe the race that reaches it: a lane
// that is open and holds no session, with a resize claimed out of its pending
// slot. Under the old apply that is a dereference of a nil session.Session on
// the worker goroutine, which is not a failed resize but a dead backend.
//
// The lane must survive it and the request must be answered.
func TestResizeClaimedForALaneWithNoSessionIsAbandoned(t *testing.T) {
	lane := newSessionLane("s-no-session", nil, log.NewSlogAdapter(nil), nil)

	op, settled := laneTestOp(100, 30)
	if !lane.enqueue(op) {
		t.Fatal("the lane refused the resize; this test needs it admitted, which is the state the defect needs too")
	}
	if err := awaitSettled(t, settled, "a resize claimed for a lane with no session"); err != nil {
		t.Fatalf("the abandoned resize was answered with an error: %v", err)
	}

	// And the lane keeps serving: abandoning one resize must not end the
	// worker, or the next resize on a live session would never be applied.
	op2, settled2 := laneTestOp(120, 40)
	if !lane.enqueue(op2) {
		t.Fatal("the lane refused a second resize: the worker did not survive the first")
	}
	if err := awaitSettled(t, settled2, "the second resize on the same lane"); err != nil {
		t.Fatalf("the second abandoned resize was answered with an error: %v", err)
	}
}

// TestCloseLaneNeverPublishesAnOpenLane states the structural half of the fix
// as the invariant it is: the lane closeLane puts in the map refuses work
// from the instant it exists, not from the instant closeLane gets round to
// admitting it. There is no observable interval in between.
func TestCloseLaneNeverPublishesAnOpenLane(t *testing.T) {
	l := newClosedLane("s-tombstone", log.NewSlogAdapter(nil))

	op, _ := laneTestOp(100, 30)
	if l.enqueue(op) {
		t.Fatal("a tombstone accepted a resize: it was published open, which is the window the crash came through")
	}
	select {
	case <-l.ctx.Done():
	default:
		t.Fatal("a tombstone's context is not cancelled: an in-flight resize would not be preempted by it")
	}
}

// TestCloseBetweenClaimAndApplyKeepsTheClaimedSession forces the ordering the
// crash report first suggested — the close lands after the worker claimed the
// op and before the resize returns — and shows it is NOT the mechanism: the
// claimed copy keeps the session usable for the whole apply, and the request
// is answered as admitted.
//
// It is here because the correction is the load-bearing part of this fix. A
// reader who believes the close/claim ordering is the bug would "fix" it by
// re-reading l.sess inside apply, which changes nothing and hides the state
// that actually panics.
func TestCloseBetweenClaimAndApplyKeepsTheClaimedSession(t *testing.T) {
	gated := newGatedResizeChannel()
	ws := stallServer(t, gated)
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	sid := openSSHOverSocket(t, ws, conn, 1)
	sess, err := ws.registry.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("registry get: %v", err)
	}

	lane := newSessionLane(session.ID(sid), sess, log.NewSlogAdapter(nil), nil)
	op, settled := laneTestOp(100, 30)
	if !lane.enqueue(op) {
		t.Fatal("a fresh lane refused a resize")
	}

	// The claim has happened and the apply is in flight: the worker is inside
	// sess.Resize and cannot be anywhere else.
	select {
	case <-gated.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the resize never started; the claim/apply ordering was not reached")
	}

	// Close is admitted underneath a running apply. It nils the lane's own
	// reference; the apply's claimed copy is what keeps the session alive.
	lane.admitClose()

	if err := awaitSettled(t, settled, "a resize closed between its claim and its apply"); err != nil {
		t.Fatalf("the cancelled resize was answered with an error: %v", err)
	}
	select {
	case <-gated.returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight resize was never cancelled by the close admission")
	}
	if got := gated.appliedResizes(); len(got) != 0 {
		t.Fatalf("a resize reached the session after close admission: %+v", got)
	}
}

// TestCloseLaneRacingAResizeNeverKillsTheBackend is the race as the product
// runs it, through the two real entry points: monitorExit (or a close
// handler) calling closeLane while a resize handler on another goroutine
// calls laneFor and enqueues.
//
// It is PROBABILISTIC and says so, because a test that is green when the race
// did not happen proves nothing on its own — the deterministic proof is
// TestResizeClaimedForALaneWithNoSessionIsAbandoned above, which forces the
// state instead of waiting for it. What this one adds is the demonstration
// that the two real call sites can reach that state: measured against the
// unfixed lane, 2000 iterations panicked on roughly one run in three, which
// is also why the crash appeared in one CI keyring variant and not the other.
// Its job now is regression cover on the path, at about a second a run.
func TestCloseLaneRacingAResizeNeverKillsTheBackend(t *testing.T) {
	ws := stallServer(t, newLiveChannel())
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	sid := openSSHOverSocket(t, ws, conn, 1)
	sess, err := ws.registry.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("registry get: %v", err)
	}

	for i := range 2000 {
		// A session id with no lane yet: the window only exists when
		// closeLane is the one that creates the map entry.
		raced := session.ID(fmt.Sprintf("%s-raced-%d", sid, i))

		op, settled := laneTestOp(100, 30)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			ws.closeLane(raced)
		}()
		go func() {
			defer wg.Done()
			if !ws.laneFor(raced, sess).enqueue(op) {
				// Refused by the tombstone: the request is the caller's to
				// answer, and that is what the handler does.
				op.done(nil)
			}
		}()
		wg.Wait()

		if err := awaitSettled(t, settled, fmt.Sprintf("iteration %d", i)); err != nil {
			t.Fatalf("iteration %d: the resize was answered with an error: %v", i, err)
		}
	}
}
