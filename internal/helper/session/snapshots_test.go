package session

// The retained-snapshot ring's own acceptance criteria (nocx-6q1uh.4, spec
// §6.1): a snapshot answers session.target's caller while it is live, and is
// treated as gone — the same way an overwritten ring slot would be — once
// snapshotMaxAge has passed, whether or not anything physically evicted it.

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/sessionruntime"
)

// newTestHostSession builds a minimal hostSession the way
// lease_serialization_test.go's fixtures do — a real runtime and a real
// owner over a scripted process, with no PTY and no shell — plus this
// task's own additions (now, tokens) that finishSpawn wires in production.
func newTestHostSession(t *testing.T, clock *fakeClock) *hostSession {
	t.Helper()
	proc := newScriptedProcess("")
	rt := pumpRuntime(t, proc)
	hs := &hostSession{
		proc:    proc,
		win:     newWindow(2 * creditLimit),
		runtime: rt,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:     clock.Now,
	}
	owner := newSessionOwner(proc, rt, hs.win, hs.log)
	rt.SetReplies(owner)
	hs.owner = owner
	hs.tokens = newTokenBook(rt.Incarnation(), clock.Now)
	owner.SetTokens(hs.tokens)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })
	return hs
}

func TestTakeSnapshotIsRetainedThenAgesOut(t *testing.T) {
	clock := newFakeClock()
	hs := newTestHostSession(t, clock)

	snap, err := hs.takeSnapshot()
	if err != nil {
		t.Fatalf("takeSnapshot: %v", err)
	}
	if snap.ID == 0 {
		t.Fatal("takeSnapshot minted the zero SnapshotID")
	}
	if len(snap.Rows) == 0 {
		t.Fatal("takeSnapshot retained no rows")
	}

	got, ok := hs.retained(snap.ID)
	if !ok || got.ID != snap.ID {
		t.Fatalf("retained(%d) = (id=%d, ok=%v), want the snapshot just taken", snap.ID, got.ID, ok)
	}

	clock.Advance(snapshotMaxAge + time.Millisecond)
	if _, ok := hs.retained(snap.ID); ok {
		t.Fatal("retained still answered a snapshot older than snapshotMaxAge")
	}
}

// TestMintTargetRefusesAnEvictedSnapshot is the plan's own criterion: "An
// intent whose snapshot was evicted is refused snapshot_gone at mint."
func TestMintTargetRefusesAnEvictedSnapshot(t *testing.T) {
	clock := newFakeClock()
	hs := newTestHostSession(t, clock)

	snap, err := hs.takeSnapshot()
	if err != nil {
		t.Fatalf("takeSnapshot: %v", err)
	}
	clock.Advance(snapshotMaxAge + time.Millisecond)

	_, err = hs.mintTarget(snap.ID, sessionruntime.TargetRegion, sessionruntime.RowRange{First: 0, Last: 0})
	if !errors.Is(err, ErrSnapshotGone) {
		t.Fatalf("mintTarget against an evicted snapshot: got %v, want ErrSnapshotGone", err)
	}
}

func TestMintTargetSucceedsAgainstALiveSnapshot(t *testing.T) {
	clock := newFakeClock()
	hs := newTestHostSession(t, clock)

	snap, err := hs.takeSnapshot()
	if err != nil {
		t.Fatalf("takeSnapshot: %v", err)
	}

	tok, err := hs.mintTarget(snap.ID, sessionruntime.TargetRegion, sessionruntime.RowRange{First: 0, Last: 0})
	if err != nil {
		t.Fatalf("mintTarget against a live snapshot: %v", err)
	}
	if err := hs.tokens.Verify(tok); err != nil {
		t.Fatalf("verify a token just minted: %v", err)
	}
}
