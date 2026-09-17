//go:build nocx_local_ssh

package session

// A white-box bridge for ssh_owner_test.go (package session_test, which needs
// the real SSH harness in ssh_spawn_harness_test.go and so cannot itself be
// package session): nocx-6q1uh.3's own acceptance tests need to submit an
// intent, and to force a stop with a deadline, against a REALLY spawned
// session — and neither has a wire op yet (session.intent is nocx-6q1uh.4/.5;
// a forced helper-shutdown caller is a later task's, per owner.go's own doc).
// This file is the seam: it exists only in the test binary (the _test.go
// suffix keeps it out of every production build), and every symbol it adds is
// named Test* so it is never mistaken for anything else.

import (
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// TestGrantControl grants control authority on a live session's runtime —
// Admit (which commitIntent calls) requires an epoch to admit an intent
// under, and the wire's own grant op belongs to a different task.
func (s *Service) TestGrantControl(id proto.HostSessionID, principal sessionruntime.Principal) (sessionruntime.Control, error) {
	hs, err := s.find(id)
	if err != nil {
		return sessionruntime.Control{}, err
	}
	return hs.runtime.GrantControl(principal)
}

// TestIncarnation is a live session's current incarnation, for building an
// Intent.At a real Admit will accept.
func (s *Service) TestIncarnation(id proto.HostSessionID) (sessionruntime.Incarnation, error) {
	hs, err := s.find(id)
	if err != nil {
		return sessionruntime.Incarnation{}, err
	}
	return hs.runtime.Incarnation(), nil
}

// TestSubmitIntent submits one intent directly to a live session's owner —
// the exact seam commitIntent's no_read_barrier refusal (spec §5.2) is
// reached through, since no wire op reaches it yet.
func (s *Service) TestSubmitIntent(id proto.HostSessionID, intent sessionruntime.Intent) (sessionruntime.IntentState, error) {
	hs, err := s.find(id)
	if err != nil {
		return sessionruntime.IntentStateNone, err
	}
	done, subErr := hs.owner.submit(ownerItem{kind: itemIntent, intent: &pendingIntent{Intent: intent}})
	if subErr != nil {
		return sessionruntime.IntentStateNone, subErr
	}
	res := <-done
	return res.State, res.Err
}

// TestForceStop calls a live session's owner.stop with a deadline directly
// (spec §5.7's forced path) — no production caller passes one yet (owner.go's
// own doc: "nothing asks for a forced stop yet"), and this is what
// TestAWriterBlockedOnAZeroWindowIsDetachedAndTheSessionCloses drives to
// exercise it against a REAL stuck SSH write. It reports tailLost (owner.stop's
// own return) and whether the writer was detached (owner.writerDetached,
// meaningful only after stop returns).
func (s *Service) TestForceStop(id proto.HostSessionID, deadline time.Time) (tailLost, writerDetached bool, err error) {
	hs, ferr := s.find(id)
	if ferr != nil {
		return false, false, ferr
	}
	tailLost = hs.owner.stop(false, deadline)
	return tailLost, hs.owner.writerDetached(), nil
}

// writeInFlight is the read side of owner.go's inFlightFlag — no production
// caller needs it yet, only this file's own TestWriteInFlight, so it lives
// here rather than beside inFlightFlag's writers (writeStart, completeWrite,
// performDetach) in owner.go/owner_ssh.go.
func (o *sessionOwner) writeInFlight() bool {
	return o.inFlightFlag.Load()
}

// TestWriteInFlight reports whether id's owner currently has a write
// dispatched to its writer (writeInFlight, above): a caller driving a write
// over the real wire, on its own goroutine, has no other way to know the
// write has actually reached the writer rather than still being framed, in
// transit over the socket, or waiting behind stop's own admission check
// (submit's closingSignal case) — and TestForceStop must never be called
// before that is true, or it finds nothing in flight to detach.
func (s *Service) TestWriteInFlight(id proto.HostSessionID) bool {
	hs, ferr := s.find(id)
	if ferr != nil {
		return false
	}
	return hs.owner.writeInFlight()
}

// TestCompletedFence reports how many items id's owner has resolved so far
// (owner.go's completedFence — the same counter inputFence publishes to a
// production caller). TestAWriterBlockedOnAZeroWindowIsDetachedAndTheSession
// Closes reads a fence taken before a chunked write starts and polls this
// against a fence taken after: once the delta reaches the number of chunks
// arithmetic guarantees the far side's untouched window can hold, whichever
// chunk the writer holds next is proven stuck, without ever asking how long
// anything took.
func (s *Service) TestCompletedFence(id proto.HostSessionID) (uint64, error) {
	hs, ferr := s.find(id)
	if ferr != nil {
		return 0, ferr
	}
	return hs.owner.completedFence.Load(), nil
}
