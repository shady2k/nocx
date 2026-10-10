package transport

// THE PER-SESSION END HOLD (nocx-zg3k3.5.11 Round 4).
//
// A re-adopted session whose helper retained a lifecycle window replays that
// window through its lane: the command's start, its rows, its end, the
// domain's close. The shell's exit carry rides a DIFFERENT stream — the
// pane's output — and can reach Done while the replay is still in flight;
// the teardown then unregisters the lane, and every frame the window still
// held is dropped with "no lane" (the Round-2 probe chain). REVIEW-2's
// decision: the helper's facts apply in the order the helper recorded them,
// so the exit may not overtake a replayed fact that preceded it.
//
// The hold is that ordering, owned by the transport. The re-adopt arms it
// with the lifecycle leg's drain signal: the leg's APPLIED cursor reaching
// the window's head, or the leg's adapter stopping, whichever first
// (internal/app's lifecycleCursor, ADR-0077) — applied, not merely read,
// because bytes the bridge has read are not yet facts the kernel has
// published. A leg that was not adopted keeps the attachment's ingest drain
// (AttachedSession.LifecycleDrained). monitorExit waits it after Done; the
// boundary consults settle only behind it. The bounds that keep a dead
// replay from hanging the exit: the drain closes when the leg stops, and
// NoteIntegrationLoss releases every hold for the session when the lifecycle
// channel is lost. A waiter
// selects the two channels directly — there is no third goroutine holding
// the hold's state, and no muxer to leak.

import (
	"sync"
	"sync/atomic"

	"github.com/shady2k/nocx/internal/session"
)

// sessionEndHold is one armed hold. drained is the caller's signal that the
// replay has applied everything the window held; released is the transport's
// own escape (integration loss, or the teardown consuming the hold). A
// waiter selects the two — whichever closes first lifts the hold.
type sessionEndHold struct {
	drained  <-chan struct{}
	released chan struct{}
	release  sync.Once
	// waiting counts the settles parked behind this hold right now — the
	// observable that says an end is being kept back, for a test and a log
	// line alike.
	waiting atomic.Int32
}

func (h *sessionEndHold) releaseNow() { h.release.Do(func() { close(h.released) }) }

// HoldSessionEndFor arms the hold for sid: the session's end waits for
// drained. Arming twice replaces the previous hold (releasing it), so a
// re-adoption that re-arms never leaves a stale drain pinning the end.
func (s *WSServer) HoldSessionEndFor(sid session.ID, drained <-chan struct{}) {
	if drained == nil {
		return
	}
	s.endHoldMu.Lock()
	defer s.endHoldMu.Unlock()
	if prev, ok := s.endHolds[sid]; ok {
		prev.releaseNow()
	}
	if s.endHolds == nil {
		s.endHolds = make(map[session.ID]*sessionEndHold)
	}
	s.endHolds[sid] = &sessionEndHold{
		drained:  drained,
		released: make(chan struct{}),
	}
}

// waitSessionEnd answers the armed hold, or nil when none is armed and the
// end may proceed as it always did.
func (s *WSServer) waitSessionEnd(sid session.ID) *sessionEndHold {
	s.endHoldMu.Lock()
	defer s.endHoldMu.Unlock()
	return s.endHolds[sid]
}

// awaitSessionEnd parks the caller behind the armed hold for sid until it
// lifts; with no hold armed it returns at once. It is how a settle that runs
// on a goroutine of its own — the helper's session-end report, reaching
// HelperSessionEnded from the attachment's end rather than through
// monitorExit — keeps the same order the hold keeps for monitorExit: the
// helper's facts apply in the order it recorded them, so a completion the
// replay still owes is applied before the session's end settles what is
// left (nocx-zg3k3.5.11). Never call it from the lifecycle leg's own pump:
// the hold is waiting for that pump.
func (s *WSServer) awaitSessionEnd(sid session.ID) {
	hold := s.waitSessionEnd(sid)
	if hold == nil {
		return
	}
	hold.waiting.Add(1)
	defer hold.waiting.Add(-1)
	select {
	case <-hold.drained:
	case <-hold.released:
	}
}

// settleWhenEndHoldLifts defers one boundary consult behind the hold: it
// answers whether the consult was deferred (true — the caller returns, the
// settle re-runs from the lift), or ran with no hold in the way (false).
func (s *WSServer) settleWhenEndHoldLifts(sid session.ID) bool {
	hold := s.waitSessionEnd(sid)
	if hold == nil {
		return false
	}
	go func() {
		select {
		case <-hold.drained:
		case <-hold.released:
		}
		s.settleAdoptedTerminalDomains(sid)
	}()
	return true
}

// releaseSessionEndHolds lifts every hold for sid — the bound that keeps a
// dead replay from hanging the exit: when the lifecycle channel is lost
// (NoteIntegrationLoss) or the session's teardown runs, nothing is waited
// for any more.
func (s *WSServer) releaseSessionEndHolds(sid session.ID) {
	s.endHoldMu.Lock()
	defer s.endHoldMu.Unlock()
	for id, hold := range s.endHolds {
		if id == sid {
			hold.releaseNow()
		}
	}
}

// dropSessionEndHold releases and forgets the hold, called by the teardown
// that consumed it so an ended session leaves nothing in the map.
func (s *WSServer) dropSessionEndHold(sid session.ID) {
	s.endHoldMu.Lock()
	hold, ok := s.endHolds[sid]
	delete(s.endHolds, sid)
	s.endHoldMu.Unlock()
	if ok {
		hold.releaseNow()
	}
}
