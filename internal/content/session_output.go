package content

// Session output recording (nocx-22k1c.1): the durable sink for the bytes a
// session produced, written by the BACKEND on its own read path rather than
// by the renderer at freeze time.
//
// ── Why this exists, and why it is not ledger.capture ─────────────────────
//
// nocx-2f0f shipped output capture, and it captures in the RENDERER: the
// terminal freezes a block, serialises its cells and sends them up as
// artifacts. That path is correct for what it does and cannot do this one,
// because a session with no client attached has no renderer to freeze
// anything — and a session with no client attached is now the ordinary case
// (nocx-22k1c: the coordinator outlives the window).
//
// It was also a STALL and not merely a gap. The AD-9 replay ring blocks its
// writer when it is full (transport/ring.go), deliberately, because AD-10
// says throttle the source and never drop; the acks that free the ring come
// from an attached client. With nobody attached, nothing acked, and a
// detached session froze after 256 KiB of output. Recording is what lets the
// backend be the ring's consumer, so the source is never throttled and
// nothing is dropped in flight.
//
// ── Why its own two tables, and why that is not a second store ────────────
//
// Same database, same Policy, same per-command cap knob. A separate
// repository for the same reason APIRunRepository is separate (api_run.go):
// the ledger's `artifacts` hang on an ENTRY — a block a person ran — and one
// table answering two unrelated identities would silently acquire a second
// writer. A session's byte stream is a third identity: it belongs to a pipe,
// not to a command, and it has no entry to hang on precisely BECAUSE nothing
// was there to record one.
//
// ── Geometry-free, so AD-6 is untouched ──────────────────────────────────
//
// A byte stream has no width. Nothing here holds a size, a grid or a VT, and
// nothing here interprets a byte: the store keeps runs of bytes keyed by the
// stream offset the transport already assigns them for replay. Storing what
// the backend already forwards is not sniffing it.

// ── What is wired, by hand, because `deadcode` cannot tell you ───────────
//
// RTA marks every method reached through an interface as reflection-reachable,
// so the ratchet cannot separate a wired write path from an unwired one in
// this package (ledger.go's header says the same, for the same reason). The
// honest statement, as of nocx-22k1c.2, checked with `-whylive`:
//
//	Append  — WIRED. main → App.Run → … → handleOpen → pumpToRing →
//	          recordSessionOutput → Append. It is the seam the whole bead is
//	          about, and the transport test that watches a detached session
//	          outlive the ring is its end-to-end proof.
//	Stance  — WIRED. history.status reads it on every render of the History
//	          settings section, which is where the degrade is stated.
//	Read    — WIRED as of nocx-22k1c.2. main → App.Run → … → sessionSpecs →
//	          sessionOutputHandlers.handleSessionOutput → Read: the
//	          session.output JSON-RPC method is the read surface, and the
//	          transport test that watches a fresh client recover an hour of
//	          output the replay ring had long since discarded is its
//	          end-to-end proof. It was TEST-REACHABLE ONLY for one bead —
//	          `-whylive` answered "reachable only through reflection" — and
//	          that was REPORTED rather than baselined, which is what made it
//	          the next bead instead of a permanent warning. Keep this line
//	          current: it is the only warning the next reader gets.

import (
	"context"
	"errors"
)

// ErrSessionOutputDiscontinuous is returned when an Append does not begin
// exactly where the last one ended. The recorder advances its cursor by what
// it wrote, so a gap here is a caller defect and not a fact about the
// stream — accepting it would put a hole in a recording whose whole value is
// that its offsets line up with what the client received.
var ErrSessionOutputDiscontinuous = errors.New("content: session output: append does not continue the recording")

// SessionOutputStance is the closed vocabulary for whether output produced
// right now would be kept. Closed for the reason HistoryDegradeReason is: the
// renderer picks its own sentence from the code, so a backend rewording never
// changes what a user reads.
type SessionOutputStance string

const (
	// SessionOutputKept — recording is on; a detached session's output is
	// written to disk and the ring is freed by the recorder.
	SessionOutputKept SessionOutputStance = "kept"
	// SessionOutputHistoryOff — the user turned history off entirely, so
	// there is no sink and a detached session throttles when the ring fills.
	SessionOutputHistoryOff SessionOutputStance = "historyOff"
	// SessionOutputRetentionOff — history is on but command output is not
	// retained, which has the same consequence for the same reason.
	SessionOutputRetentionOff SessionOutputStance = "outputOff"
)

// SessionOutputAppend is one run of bytes at a known stream offset.
type SessionOutputAppend struct {
	// SessionID is the pipe the bytes came out of — transport's session id,
	// which is server-authoritative (AD-7).
	SessionID string
	// Offset is the byte offset of Body[0] in that session's output stream:
	// the SAME coordinate the replay ring keys on and the client acks
	// against, so a recording can be checked against what a client received
	// by offset rather than by eye.
	Offset uint64
	// Body is the bytes. Never interpreted; split into chunks by the store.
	Body []byte
}

// SessionOutputResult is what the store did with one append.
type SessionOutputResult struct {
	// Kept is false when nothing was written because retention is off. It is
	// an ANSWER and not a failure — the caller stops advancing its
	// persistence cursor, and the ring goes back to being freed by client
	// acks alone, which is the degrade this package's Stance describes.
	Kept bool
	// Dropped is how many bytes this append evicted from the recording to
	// stay inside the cap: oldest first, never the head. Zero on the
	// ordinary path.
	Dropped uint64
}

// SessionOutputRun is one contiguous stretch of recorded bytes and the
// stream offset it starts at. A recording is a LIST of runs because the cap
// drops the middle: head and tail are two runs with a hole between them, and
// a single []byte would silently join bytes that are not adjacent.
type SessionOutputRun struct {
	Offset uint64
	Body   []byte
}

// SessionOutputRecording is everything the store kept for one session.
type SessionOutputRecording struct {
	SessionID string
	// Runs are in stream order and never overlap.
	Runs []SessionOutputRun
	// Gaps are the byte ranges the cap dropped, in the ledger's own Gap
	// shape — so "what is missing" is said the same way here as on an
	// artifact.
	Gaps []Gap
	// Truncated is `cap` once anything has been dropped, and nil while the
	// recording is whole.
	Truncated *Truncation
	// Bytes is how much is currently kept across every run.
	Bytes uint64
	// Produced is how many bytes the session has produced in total,
	// including what was dropped — the recording's end offset. It is what
	// makes a hole measurable rather than invisible.
	Produced uint64
}

// SessionOutputRepository owns session_output and session_output_chunks. It
// is the ONE writer of both.
//
// Lifetime: a recording lives as long as the pipe it records. A session is
// server-authoritative, lives inside one backend process and cannot outlive
// it (AD-7, D5), so at store-open no recording names anything live and the
// startup sweep drops them all — the same sentence dropDeadSessions already
// makes about `sessions`, and the same one owner making it. That is why
// these rows are NOT in the budget sweep: its unit is `artifacts.byte_len`
// ordered by `ingest_seq`, and a session recording has neither. Giving it
// one would mean a second meaning for that column or a second ordering
// inside one sweep.
type SessionOutputRepository interface {
	// Append records bytes at their stream offset, evicting the oldest
	// non-head bytes if the recording would exceed the per-command cap. It
	// never fails for being over the cap — exceeding a bound is what the
	// bound is for.
	Append(ctx context.Context, in SessionOutputAppend) (SessionOutputResult, error)
	// Read returns everything kept for a session, in stream order. An
	// unknown session is an empty recording and not an error: nothing was
	// produced, or all of it was dropped, and neither is a fault.
	Read(ctx context.Context, sessionID string) (SessionOutputRecording, error)
	// Stance reports whether output produced right now would be kept, and
	// why not. Read live, per call: the History settings apply without a
	// restart, so an answer cached at startup would be wrong by lunchtime.
	Stance() SessionOutputStance
}
