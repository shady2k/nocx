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

	"github.com/shady2k/nocx/internal/operation/catalog"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
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

// SessionOutputRecorder is the transport's view of the durable sink: its
// append/skip writer, recovery metadata and recording stance. content's own
// repository satisfies it; a test's fake satisfies it without a database.
type SessionOutputRecorder interface {
	// Append records one run of bytes at its stream offset. A result with
	// Kept false is a refusal — retention is off — and not a failure.
	Append(ctx context.Context, in content.SessionOutputAppend) (content.SessionOutputResult, error)
	// Skip advances the recording across a range that was never offered,
	// records it as a gap carrying `reason`, and leaves the recording
	// appendable. A result with Kept false is a refusal — retention is
	// off — and not a failure.
	Skip(ctx context.Context, sessionID string, resumeAt uint64, reason string) (content.SessionOutputResult, error)
	// RecoveryStatus reads only the recorded end offset and known gap metadata.
	RecoveryStatus(ctx context.Context, sessionID string) (content.SessionOutputRecoveryStatus, error)
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

// holeResult is what one attempt to write a hole to the store came to. Three
// answers and not two, because "the store declined" and "the store failed"
// are different facts with different next moves: the first is retention being
// off, which a person can turn back on and the loop must survive; the second
// is a write failure the product has to hear about.
type holeResult int

const (
	// holeRecorded — the store took it; the recorder resumes past the hole.
	holeRecorded holeResult = iota
	// holeDeferred — retention is off, so there is no recording for a hole
	// to be a hole in. The cursor stays where it is and the whole skip is
	// retried when the setting comes back.
	holeDeferred
	// holeUnrecordable — the store failed. A store that cannot record the
	// hole cannot be trusted with the resume either, because the alternative
	// is a recording that silently joins two stretches that are not
	// adjacent.
	holeUnrecordable
)

// recordHole writes one hole into the recording and moves the ring's
// persistence cursor across it.
//
// ONE FUNCTION FOR TWO CAUSES, deliberately. A recorder meets a hole two
// ways — its own cursor falls behind the ring's oldest byte (nobody was
// there), or the ring is told a stretch never arrived at all (the execution
// host's window took it, nocx-k6p18.25) — and they are one operation with two
// reasons: the store call, the refusal, the failure, the status episode and
// the cursor move are identical, and only the word for the cause and the
// sentence in the log differ. Writing them twice is how the two would drift.
func (s *WSServer) recordHole(
	ctx context.Context, rec SessionOutputRecorder, sid session.ID, ring *outputRing,
	pos, resumeAt uint64, reason, resumed string,
) holeResult {
	res, err := rec.Skip(ctx, string(sid), resumeAt, reason)
	if err != nil {
		// Said where every other history write failure is said.
		if s.historyStatus != nil {
			s.historyStatus.Raise(HistoryDegradeWriteFailed, err.Error())
		}
		s.log.Warn("session recording could not record the hole it fell into; the ring falls back to client acks",
			"session_id", string(sid), "at", pos, "resume_at", resumeAt, "reason", reason, "error", err)
		return holeUnrecordable
	}
	if !res.Kept {
		return holeDeferred
	}
	// A kept skip is a store write that succeeded, so it closes an open
	// write-failure episode and puts the loop back on the ring as its
	// consumer — both of which the append path does for the same reason, and
	// neither of which may wait for the first byte after the hole, since a
	// session that produces nothing more would leave the product asserting a
	// failure that ended.
	ring.setRecording(true)
	if s.historyStatus != nil {
		s.historyStatus.ClearReason(HistoryDegradeWriteFailed)
	}
	s.log.Warn(resumed, "session_id", string(sid), "at", pos, "resume_at", resumeAt, "reason", reason)
	// The store owes nothing before the resume point, so the ring may free up
	// to it. Without this the persistence cursor would sit at an offset the
	// ring has already discarded until the next successful append moved it.
	if err := ring.recordTo(resumeAt); err != nil {
		s.log.Warn("session recording could not advance its cursor across the hole",
			"session_id", string(sid), "at", resumeAt, "error", err)
		return holeUnrecordable
	}
	// A recorded hole is a revision of the same answer the cap's evictions
	// revise (nocx-zg3k3.14.24): the attached client is told its own card's
	// number moved, not left holding the reclaim's.
	s.publishRecoveryRevision(ctx, sid)
	return holeRecorded
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
	opScope := s.beginInternal(ctx, catalog.OpInternalSessionRecorder)
	defer opScope.Complete()
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

	// THE CAP'S LINE IS AN EPISODE'S, NOT A PASS'S (nocx-3an1s.1.7). A
	// session held at its output cap evicts on every append — measured on an
	// installed build at 10 to 200 ms apart — and one line per eviction put
	// 1.3 GB in nocx.log and pushed every other line out of any tail. The
	// episode says it once when it opens, once per interval with the total
	// since the last line, and once more when it ends with what is left;
	// nothing is lost, because every line names a running total.
	//
	// The logger comes from the context so the test that counts these lines
	// reads the one this server was built with (log.From).
	ctx = log.WithLogger(ctx, s.log)
	capDrops := newCapEpisode(capReportInterval, time.Now)
	defer func() {
		if since, total, reported := capDrops.stopped(); reported {
			log.From(ctx).Info("session recording stopped dropping its oldest bytes; the output cap episode is over",
				"session_id", string(sid), "bytes", since, "total", total)
		}
	}()

	// THE RECORDER RESUMES AT THE RING'S PERSISTENCE CURSOR, which is the one
	// place that already holds "how far this session has been written down".
	// For every session this coordinator opened it is zero and always was; for
	// one it took BACK from a helper the ring was created at the offset the
	// previous coordinator's recording ends at, so the two stretches of one
	// stream share a coordinate (nocx-k6p18.30). One rule and no branch.
	//
	// It is NOT the ring's oldest byte, and the difference is a hole nobody
	// would ever hear about. A session that ran with no recorder — history
	// switched off, then on — leaves a ring whose BASE the acks moved and whose
	// persistence cursor is still at zero, and starting at the base would step
	// over those bytes silently instead of recording the `unrecorded` gap that
	// says nobody was listening (nocx-k6p18.2). The two cursors are two facts
	// and this loop needs the second one.
	pos := ring.recordedLocked()
	for {
		data, _, needsReset, hole := ring.snapshot(pos)
		if hole != nil {
			// A stretch of this session's output never reached this machine
			// at all: the execution host reclaimed its own bounded window
			// past the coordinator before the bytes could be carried across
			// (nocx-k6p18.25). The ring carries it as a hole rather than
			// renumbering what came after, so the resume point is the hole's
			// far end and the range recorded is exactly what was lost.
			//
			// The reason is the RING's, carried from the wire, and not this
			// loop's: `unrecorded` below would say nobody was listening,
			// which is false and points a person at a recorder that was
			// running the whole time.
			resumeAt := hole.at + hole.n
			switch outcome := s.recordHole(ctx, rec, sid, ring, pos, resumeAt, hole.reason,
				"session recording crossed a hole the execution host's window left; the hole is recorded and recording resumes"); outcome {
			case holeRecorded:
				pos = resumeAt
			case holeDeferred:
				if s.recorderIdle(ctx, ring) {
					return
				}
			case holeUnrecordable:
				return
			}
			continue
		}
		if needsReset {
			// The recorder's cursor is behind the ring's oldest retained
			// byte, so the bytes between them are gone from both sides:
			// nothing offered them here and nothing can now. That is a HOLE
			// and not the end of the recording (nocx-k6p18.2) — this branch
			// used to return, which cost the whole session for one missed
			// stretch, and a coordinator that can be replaced under a live
			// session makes that stretch ordinary rather than a defect.
			//
			// It resumes at the ring's OLDEST byte and not at `from`, which
			// snapshot reports as the write frontier for a client that has
			// to clear its screen. A recorder has no screen to clear and the
			// bytes in between are real, so taking `from` would widen the
			// hole by everything the ring is still holding.
			//
			// The loop terminates: `base` only ever moves forward, so each
			// pass through here advances `pos` strictly, and a second reset
			// (the base moved again between the two calls) is another hole
			// honestly recorded rather than a spin.
			resumeAt := ring.oldestLocked()
			if resumeAt <= pos {
				// `base` cannot move backwards, so this is unreachable and
				// is checked rather than assumed: looping on it would spin a
				// core, and the cost of being wrong about a ring's cursor is
				// a recording that claims bytes it does not hold.
				s.log.Warn("session recording was told to resume where it already is; recording stops for this session",
					"session_id", string(sid), "at", pos, "resume_at", resumeAt)
				return
			}
			switch outcome := s.recordHole(ctx, rec, sid, ring, pos, resumeAt, content.GapReasonUnrecorded,
				"session recording lost its place in the ring; the hole is recorded and recording resumes"); outcome {
			case holeRecorded:
				pos = resumeAt
			case holeDeferred:
				if s.recorderIdle(ctx, ring) {
					return
				}
			case holeUnrecordable:
				return
			}
			continue
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
			// move: this append is made again when the setting comes back.
			if s.recorderIdle(ctx, ring) {
				return
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
			if since, total, reported := capDrops.evicted(res.Dropped); reported {
				log.From(ctx).Info("session recording dropped its oldest bytes to stay inside the output cap",
					"session_id", string(sid), "bytes", since, "total", total)
				// The recording's answer to "what is missing" just changed,
				// and this loop is the only party that knows it did
				// (nocx-zg3k3.14.24). A reclaim read that answer once, before
				// it attached, so without this the card a person reads keeps
				// stating the number the reclaim caught.
				//
				// It is published on the EPISODE's schedule and not on the
				// pass's for the same reason the line is: reading the
				// recovery status is a store query and the frame it fills is
				// replaced by the next one, so per-pass publishing bought a
				// query and a marshal per eviction to deliver a number
				// nobody was waiting for. The last one still goes out, below.
				s.publishRecoveryRevision(ctx, sid)
			}
		} else if since, total, reported := capDrops.stopped(); reported {
			log.From(ctx).Info("session recording stopped dropping its oldest bytes; the output cap episode is over",
				"session_id", string(sid), "bytes", since, "total", total)
			s.publishRecoveryRevision(ctx, sid)
		}
		pos += uint64(len(data))
		if err := ring.recordTo(pos); err != nil {
			s.log.Warn("session recording could not advance its cursor",
				"session_id", string(sid), "at", pos, "error", err)
			return
		}
	}
}

// recorderIdle parks the loop after the store declined to keep anything, and
// reports whether the session ended while it was parked.
//
// One function for both refusals — an append's and a skip's — because they
// are one fact: retention is off, so nothing this loop does is durable. The
// flag comes down, so the ring goes back to being freed by acks alone, which
// is the degrade the History section states rather than one only a log
// mentions.
//
// The session's end is checked HERE and not at the top of the loop, because
// everywhere else the loop is already waiting on the ring and a close wakes
// it. This is the one wait that is not on the ring, so without it a recorder
// with nothing to keep would go on polling a session that ended.
func (s *WSServer) recorderIdle(ctx context.Context, ring *outputRing) (stop bool) {
	ring.setRecording(false)
	if ring.isClosed() {
		return true
	}
	select {
	case <-ctx.Done():
		return true
	case <-time.After(recorderRetryInterval):
		return false
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

// sessionOutputHoleReason translates the execution host's hole vocabulary
// into the store's. Unknown host reasons remain unknown.
func sessionOutputHoleReason(reason string) string {
	if reason == proto.GapReasonWindow {
		return content.GapReasonHostWindow
	}
	return content.GapReasonUnknown
}
