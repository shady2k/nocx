package transport

// Taking a helper-hosted session back after the coordinator was replaced
// (nocx-k6p18.30) — the transport's half of it.
//
// WHAT WAS MISSING. The epic's sentence is "quit nocx mid-build on a remote
// host, reopen, and the build is still running". The helper genuinely kept the
// shell alive; the coordinator simply could not find it again. `sessions.live`
// is served straight off the in-process registry, so a new process held
// nothing; the durable binding had exactly one reader, restart reconciliation,
// and a verdict creates no session and opens no connection.
//
// WHY THE RE-ADOPTION IS SERVER-SIDE, and this is the decision worth
// recording. The tempting fix is to relax `judgeClaim`, which refuses a claim
// carried across a restart as `reasonForeignInstance`. That refusal is right
// and stays: an instance id names the PROCESS, and a claim carried across a
// restart could otherwise resolve to a different session than the renderer
// meant. So the renderer never re-adopts anything. The coordinator reads its
// own binding at start, re-attaches to the helper's existing host session, and
// adopts the result under a FRESH instance id — new process, new instance, the
// same authoritative session id, which is AD-7 as amended: the execution
// host's helper minted the id and the server adopts and returns it. The
// renderer then discovers the session through `sessions.live` like any other.
//
// THE INTERVAL, both ends named. From the moment ReadoptHostedSession returns
// nil — the helper has answered the attach and the registry holds the
// session — until that session is closed (the tab, or monitorExit on the
// shell's own exit), the session appears in `sessions.live` with the pane it
// was the pipe of. An attempt that fails at ANY step leaves nothing behind:
// no receiver, no ring, no registry entry, no pump. There is no state in which
// a session is listed as live having failed to be taken back.

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/session"
)

// HostedSessionReattach re-attaches one helper-hosted session and adopts it
// into the registry. It is the coordinator's half — internal/app owns the
// helper connection, the consent decision and the registry insert — and this
// package supplies the only fact that half cannot know: `from`, the offset
// this machine's recording of that session ends at, which is where the
// attachment must resume so the two stretches of one stream share a
// coordinate.
//
// It returns the same shape an ordinary hosted open does, so the wiring below
// is the wiring handleOpen already does and not a second copy of it.
type HostedSessionReattach func(ctx context.Context, from uint64) (HostedSessionOpen, error)

// ReadoptHostedSession installs the transport-owned half of a session taken
// back from a helper: the replay ring at the offset the recording ends at, the
// hole observer, the output pump and the exit monitor.
//
// THE ORDER IS THE ROLLBACK. The receiver is reserved BEFORE the helper is
// asked, because reserving it is the only step that can fail for a reason the
// caller cannot undo — a server already shutting down — and a receiver with no
// session behind it is invisible to every reader (sessions.live iterates the
// REGISTRY; attach requires both). So the failure path is a removeRx and
// nothing else, and no attempt can leave a half-adopted session anywhere.
func (s *WSServer) ReadoptHostedSession(ctx context.Context, sid session.ID, reattach HostedSessionReattach) error {
	if reattach == nil {
		return fmt.Errorf("transport: no re-attachment for session %s", string(sid))
	}
	from := s.recordedThrough(ctx, sid)

	rx := s.getOrCreateRxAt(sid, from)
	if rx == nil {
		return fmt.Errorf("transport: server is shutting down; session %s was not taken back", string(sid))
	}

	hosted, err := reattach(ctx, from)
	if err != nil {
		s.removeRx(sid)
		return err
	}
	if hosted.Session == nil {
		s.removeRx(sid)
		return fmt.Errorf("transport: re-attaching session %s returned no session", string(sid))
	}
	if hosted.Session.ID() != sid {
		// The helper answered about a different session than the binding
		// named. Refused rather than adopted: the id is the one thing the
		// whole reconciliation ordering is built on, and a coordinator that
		// silently accepted a substitution would attach one pane's ring to
		// another pane's shell.
		s.removeRx(sid)
		return fmt.Errorf("transport: re-attaching session %s answered for %s",
			string(sid), string(hosted.Session.ID()))
	}

	// The helper's holes reach the ring from here, BEFORE the pump starts —
	// the same rule and the same reason as handleOpen's: the observer fires on
	// the pump's own goroutine as it reads, so registering it after the pump
	// would be a window in which a hole is dropped instead of recorded. On
	// this path the FIRST thing the attachment reports is usually a hole:
	// everything the host's bounded window reclaimed while no coordinator was
	// listening (nocx-k6p18.25's `hostWindow`).
	if hosted.ObserveOutputHoles != nil {
		ring := rx.ring
		hosted.ObserveOutputHoles(func(lost uint64, reason string) {
			ring.hole(lost, sessionOutputHoleReason(reason))
		})
	}

	// No ack to wait for and no client to race: AD-7's rule that the open
	// result precedes a session's own traffic is about a client that has just
	// been told an id, and nobody has been told anything here. The pump starts
	// immediately so the bytes the host is still producing go to the recording
	// rather than being dropped by the helper's window, which is the thing
	// that was dropping them.
	//
	// Background is deliberate, with the same ownership handleOpen's pump has.
	// Owner: the session and its replay ring, which outlive every WebSocket
	// (AD-9) — and on this path outlive the coordinator too, which is the whole
	// point. Closing event: session teardown, in monitorExit below, which waits
	// on the session's Done and ends the read pump StartOutput started.
	go s.pumpToRing(context.Background(), hosted.Session, rx.ring)
	rx.monitorOnce.Do(func() {
		go s.monitorExit(rx, hosted.Session)
	})
	if hosted.StartLifecycle != nil {
		hosted.StartLifecycle()
	}
	return nil
}

// recordedThrough is where this machine's recording of a session ends: the
// offset the next byte belongs at, and therefore both where the re-attachment
// must resume the HOST's stream and where the ring's own persistence cursor
// starts (the recorder loop reads it from the ring, not from here — one store
// read per re-adoption, not one per session).
//
// It asks the RECORDER and not a second store, because the recorder is the one
// owner of that number — it is the single writer of the recording's tables and
// the only thing that knows what its own retention bound dropped (AD-8, and
// SessionOutputRecorder's own comment says the read and the write are two
// halves of one capability).
//
// Zero on every failure and on a server with no recorder at all, and that is
// the honest answer rather than a degrade to hide. Zero means "the recording
// starts at the beginning", which is true when there is no recording; the
// consequence is a hole recorded from zero to wherever the stream actually
// resumes, which is a statement about what is missing rather than a silent
// splice. A wrong NON-zero guess would do the opposite and claim a recording
// holds bytes it never had.
func (s *WSServer) recordedThrough(ctx context.Context, sid session.ID) uint64 {
	if s.sessionRecorder == nil {
		return 0
	}
	rec, err := s.sessionRecorder.Read(ctx, string(sid))
	if err != nil {
		s.log.Warn("a session's recording could not be read, so it resumes from the beginning; whatever is already recorded will be reported as a hole",
			"session_id", string(sid), "error", err)
		return 0
	}
	return rec.Produced
}
