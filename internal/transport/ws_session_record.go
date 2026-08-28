package transport

// The session output recorder (nocx-22k1c.1): the backend consuming its own
// replay ring, so a session with no client attached keeps running and its
// bytes end up on disk.
//
// ── The stall this removes ───────────────────────────────────────────────
//
// ring.write BLOCKS when the ring is full and nothing has been acked. That is
// deliberate and it is AD-10: throttle the source, never drop. But the acks
// come from an ATTACHED CLIENT, and a session that outlives its window has
// none — so nothing acked, and a detached session froze after RingCapacity
// bytes. The daemon stalled the very work it exists to protect.
//
// The fix is not to weaken AD-10, and this does not: the backend becomes the
// ring's consumer. The bytes go to the content store instead of to a socket,
// the ring is freed against a cursor that is nobody's acknowledgement, and
// AD-10 holds as written — nothing dropped, nothing unbounded.
//
// ── What it does not do ──────────────────────────────────────────────────
//
// It does not interpret a byte. A stream has no width, so there is no size,
// no grid and no VT here: the loop moves what the ring already holds into a
// store that keys it by the offset the ring already assigned. AD-6 is
// untouched — storing what you already forward is not sniffing it.

import (
	"context"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
)

// recorderRetryInterval is how long a recorder waits before re-reading the
// stance after the store declined to keep anything.
//
// It is a POLL because the thing it is waiting for has no event: History is a
// live setting (app.go's registry notifier) and a person may turn output
// retention back on at any moment, with nothing in this package told about
// it. The alternative — exiting for the life of the session — would leave the
// product saying recording is on while the sessions already running were
// still stalling, which is the silent contradiction the status surface exists
// to prevent. Two seconds is far below human patience for a settings change
// and costs one timer per session, only while retention is off.
const recorderRetryInterval = 2 * time.Second

// SessionOutputRecorder is the transport's view of the durable sink: the two
// things this package asks of it and nothing else. content's own repository
// satisfies it; a test's fake satisfies it without a database.
type SessionOutputRecorder interface {
	// Append records one run of bytes at its stream offset. A result with
	// Kept false is a refusal — retention is off — and not a failure.
	Append(ctx context.Context, in content.SessionOutputAppend) (content.SessionOutputResult, error)
	// Stance says whether output produced now would be kept, and why not.
	Stance() content.SessionOutputStance
}

// WithSessionOutputRecorder attaches the durable sink for session output.
// Without it the server records nothing, the ring is freed by client acks
// alone, and a detached session throttles once the ring fills — which is the
// pre-recorder behaviour, and is stated in the product through
// history.status rather than only in a log.
func WithSessionOutputRecorder(r SessionOutputRecorder) WSServerOption {
	return func(s *WSServer) { s.sessionRecorder = r }
}

// recordSessionOutput is the recorder's loop: one goroutine per session,
// started by pumpToRing and ending when the ring closes.
//
// The invariant it maintains, with both ends named: from the moment the last
// client detaches until one attaches, the ring's consumer is this loop, and
// the source is never throttled while it is keeping up. It is a CONSUMER of
// the ring and never a subscriber — it reads by offset and moves the
// persistence cursor, which is not an acknowledgement and never becomes one.
func (s *WSServer) recordSessionOutput(ctx context.Context, sid session.ID, ring *outputRing) {
	rec := s.sessionRecorder
	if rec == nil {
		return
	}
	// The flag is what makes trim respect this loop: while it is set, the
	// ring may not free bytes this cursor has not passed. Cleared on every
	// exit, so a recorder that dies hands the ring back to the acks rather
	// than wedging it against a cursor nothing will move again.
	ring.setRecording(true)
	defer ring.setRecording(false)

	var pos uint64
	for {
		data, from, needsReset := ring.snapshot(pos)
		if needsReset {
			// Unreachable while the flag above is set — trim never passes
			// the persistence cursor — and checked rather than assumed
			// because the cost of being wrong is a recording that claims
			// bytes it does not hold. Recording that session stops here;
			// what is already on disk stays, and stays honest.
			s.log.Warn("session recording lost its place in the ring; recording stops for this session",
				"session_id", string(sid), "at", pos, "ring_from", from)
			return
		}
		if len(data) == 0 {
			if ring.waitForData(ctx, pos) {
				return
			}
			if ctx.Err() != nil {
				return
			}
			continue
		}

		res, err := rec.Append(ctx, content.SessionOutputAppend{
			SessionID: string(sid),
			Offset:    pos,
			Body:      data,
		})
		if err != nil {
			// The store refused the write. Say it where the product says
			// every other history write failure — history.status already
			// owns "the store is open and refusing writes", raises once per
			// episode rather than once per loss, and has a closing event.
			if s.historyStatus != nil {
				s.historyStatus.Raise(HistoryDegradeWriteFailed, err.Error())
			}
			s.log.Warn("session recording failed; the ring falls back to client acks",
				"session_id", string(sid), "at", pos, "error", err)
			return
		}
		if !res.Kept {
			// Retention is off. Nothing is durable, so the cursor does not
			// move and the flag comes down: the ring goes back to being
			// freed by acks alone, which is the degrade the History section
			// states. Re-read the stance in a moment — the setting is live.
			ring.setRecording(false)
			// The session's end is checked HERE and not at the top of the
			// loop, because everywhere else the loop is already waiting on
			// the ring and a close wakes it. This is the one wait that is
			// not on the ring, so without this line a recorder that had
			// nothing to keep would go on polling a session that ended.
			if ring.isClosed() {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(recorderRetryInterval):
			}
			continue
		}
		ring.setRecording(true)
		if s.historyStatus != nil {
			// The closing event for an episode this loop opened. ClearReason
			// and not Clear: a runtime success does not disprove a startup
			// degrade, and clearing unconditionally would erase "the content
			// key could not be read" — a sentence still true and one nothing
			// else would ever say again.
			s.historyStatus.ClearReason(HistoryDegradeWriteFailed)
		}
		if res.Dropped > 0 {
			s.log.Info("session recording dropped its oldest bytes to stay inside the output cap",
				"session_id", string(sid), "bytes", res.Dropped)
		}
		pos += uint64(len(data))
		if err := ring.recordTo(pos); err != nil {
			s.log.Warn("session recording could not advance its cursor",
				"session_id", string(sid), "at", pos, "error", err)
			return
		}
	}
}

// sessionRecordingStance is what the product is told about a detached
// session's output: whether it is being kept, and if not, which switch says
// so. A server with no recorder wired is not making a claim about a setting —
// there is no sink at all — and answers with the same "history is off"
// stance, which is what history.status.available already elaborates.
func (s *WSServer) sessionRecordingStance() content.SessionOutputStance {
	if s.sessionRecorder == nil {
		return content.SessionOutputHistoryOff
	}
	return s.sessionRecorder.Stance()
}
