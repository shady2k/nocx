package session

// The retained-snapshot ring (nocx-6q1uh.4, spec §6.1): one consistent read
// of a session's screen, held long enough that a coordinator's round trip —
// session.read classifies it, session.target mints a token from the SAME
// frame — describes one instant rather than two independent reads that may
// disagree about what the program had drawn.

import (
	"errors"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// SnapshotID names one retained snapshot within a session's ring. It is
// minted from 1, so the zero value names no snapshot — the same convention
// [sessionruntime.IntentID] uses and for the same reason: a caller that
// forgot to mint one must not be answered as though it had asked for the
// first.
type SnapshotID uint64

// retainedSnapshot is one consistent read of a session's screen: the rows
// with style a target's digest is computed from, the cursor, the identity a
// mint or a commit-point check compares against, and the Frame a coordinator
// classifies with the agent rule. Every field belongs to the SAME read —
// [hostSession.takeSnapshot] is the only place one is built, and it builds
// all of them from one [sessionruntime.Session.Snapshot] call.
type retainedSnapshot struct {
	ID           SnapshotID
	Taken        time.Time
	Identity     sessionruntime.ScreenIdentity
	Rows         []emulator.Row
	Cursor       emulator.Cursor
	Frame        paneview.Frame
	Revision     sessionruntime.Revision
	InputFence   sessionruntime.Fence
	Completeness sessionruntime.Completeness
	// AccessEpoch is the helper session's access epoch at the moment this
	// snapshot was taken (spec §6.1: "stored with every token the snapshot
	// yields"): 1 for a fresh incarnation, raised by session.access-bump
	// (nocx-6q1uh.5, spec §7.2, owner.go's accessEpoch / access.go's
	// applyAccessBump).
	AccessEpoch uint64
}

// snapshotRing is how many of a session's snapshots are retained at once
// (spec §6.1's "a ring of 8"): enough for session.read → classify →
// session.target to still find the frame it asked about, without holding
// every screen a session has ever shown.
const snapshotRing = 8

// snapshotMaxAge is how long a retained snapshot answers [hostSession.retained]
// before it counts as gone, whether or not the ring has since overwritten its
// slot (spec §6.1): a coordinator minting a target from a two-second-old
// frame would bind a token to a screen the program has long since painted
// over.
const snapshotMaxAge = 2 * time.Second

// errSnapshotUnavailable names a session whose screen could not be read at
// all (a closed emulator) — the same honest silence
// [sessionruntime.Session.Snapshot] keeps by answering nil rows, turned into
// an error here because a snapshot with nothing in it is not a snapshot a
// caller can classify or mint a target from.
var errSnapshotUnavailable = errors.New("session: the screen could not be read")

// takeSnapshot performs ONE consistent read of this session's screen —
// through [sessionruntime.Session.Snapshot], which already holds the
// runtime's lock for exactly as long as reading rows, the cursor and the
// buffer identity together takes — and retains it for [snapshotMaxAge] so a
// later [hostSession.retained] can still find it (spec §6.1).
//
// The fence is read BEFORE the screen, never after (spec §5.4,
// [hostSession.readFrame] states the same rule and this is the same
// property): a write that completes in the gap between this line and the
// runtime read is one the snapshot is honestly silent about, and
// under-reporting is the direction the design already tolerates.
//
// It never blocks on a write: [sessionruntime.Session.Snapshot] takes only
// the runtime's own lock, never [hostSession.write]'s, so a session whose
// writer is stuck on a program that never reads its input still answers a
// snapshot of its last ingested output.
func (hs *hostSession) takeSnapshot() (retainedSnapshot, error) {
	fence := hs.owner.inputFence()
	snap := hs.runtime.Snapshot()
	if snap.Rows == nil {
		return retainedSnapshot{}, errSnapshotUnavailable
	}

	frame := paneview.FromRows(
		emulator.Geometry{Cols: snap.Identity.Cols, Rows: snap.Identity.Rows},
		snap.Identity.AltScreen,
		snap.Rows,
		snap.Cursor,
	)
	frame.Revision = snap.Revision
	frame.Completeness = snap.Completeness
	frame.InputFence = fence

	hs.snapMu.Lock()
	defer hs.snapMu.Unlock()
	hs.snapNext++
	id := hs.snapNext
	rs := retainedSnapshot{
		ID:           id,
		Taken:        hs.now(),
		Identity:     snap.Identity,
		Rows:         snap.Rows,
		Cursor:       snap.Cursor,
		Frame:        frame,
		Revision:     snap.Revision,
		InputFence:   fence,
		Completeness: snap.Completeness,
		AccessEpoch:  hs.owner.currentAccessEpoch(),
	}
	hs.snapRing[uint64(id)%snapshotRing] = &rs
	return rs, nil
}

// retained answers the snapshot named id, if the ring still holds its slot
// and it has not aged past [snapshotMaxAge]. Either miss is reported the
// same way — false — because a caller (session.target, nocx-6q1uh.5) treats
// both as `snapshot_gone`: a coordinator minting a target from a stale frame
// would bind a token to a screen the program has since redrawn, whether the
// ring physically overwrote the slot or merely outlived it.
func (hs *hostSession) retained(id SnapshotID) (retainedSnapshot, bool) {
	hs.snapMu.Lock()
	defer hs.snapMu.Unlock()
	slot := hs.snapRing[uint64(id)%snapshotRing]
	if slot == nil || slot.ID != id {
		return retainedSnapshot{}, false
	}
	if hs.now().Sub(slot.Taken) > snapshotMaxAge {
		return retainedSnapshot{}, false
	}
	return *slot, true
}

// mintTarget is the join of the ring and the token book that session.target
// will call (nocx-6q1uh.5 wires the op itself): resolve snapshotId in THIS
// session's ring and mint from it in one call, so no caller can mint against
// a snapshot that looked live a moment ago and is gone by the time it asks.
func (hs *hostSession) mintTarget(id SnapshotID, kind sessionruntime.TargetKind, rows sessionruntime.RowRange) (Token, error) {
	snap, ok := hs.retained(id)
	if !ok {
		return Token{}, ErrSnapshotGone
	}
	return hs.tokens.Mint(snap, kind, rows)
}
