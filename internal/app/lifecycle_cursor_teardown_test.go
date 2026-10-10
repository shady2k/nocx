package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/waittest"
)

// countingCursorStore answers every frame with err, and counts the frames it
// was handed.
type countingCursorStore struct {
	frames atomic.Int64
	err    error
}

func (c *countingCursorStore) ApplyLifecycleFrame(ctx context.Context, _ string, _ uint64, apply func(context.Context) error) error {
	c.frames.Add(1)
	if err := apply(ctx); err != nil {
		return err
	}
	return c.err
}

// THE TEARDOWN RULE (ADR-0077). Once the coordinator has begun stopping, a
// frame is not applied at all — not by the kernel, not by the store — and the
// cursor stays before it, so the next coordinator applies it; the leg ends as
// a handover. A frame already in the store when stopping began, which then
// fails because the sessions it records against are closing, is left the same
// way rather than halting the leg with an error.
func TestAFrameArrivingWhileTheCoordinatorStopsIsLeftForTheNextOne(t *testing.T) {
	stopping := &atomic.Bool{}
	store := &countingCursorStore{}
	cursor := newLifecycleCursor(context.Background(), store, stopping)
	cursor.bind("0123456789abcdef0123456789abcdef", 0)
	cursor.relayed(40, 40)
	stopping.Store(true)

	applied := false
	err := cursor.applyFrame(40, lifecycle.KindComplete, func(context.Context) { applied = true })
	if !errors.Is(err, lifecyclechannel.ErrFrameLeftForNext) {
		t.Fatalf("applyFrame while stopping = %v, want ErrFrameLeftForNext", err)
	}
	if applied || store.frames.Load() != 0 {
		t.Fatalf("while stopping the frame reached the kernel (%v) or the store (%d frames)", applied, store.frames.Load())
	}
	if cursor.applied != 0 {
		t.Fatalf("the cursor moved to %d past a frame it did not apply", cursor.applied)
	}
}

func TestAFrameFailingBecauseTheCoordinatorStopsIsLeftRatherThanHalted(t *testing.T) {
	stopping := &atomic.Bool{}
	store := &countingCursorStore{err: errors.New("content: no such entry")}
	cursor := newLifecycleCursor(context.Background(), store, stopping)
	cursor.bind("0123456789abcdef0123456789abcdef", 0)
	cursor.relayed(40, 40)
	err := cursor.applyFrame(40, lifecycle.KindStart, func(context.Context) {
		stopping.Store(true) // stopping began while the frame was in hand
	})
	if !errors.Is(err, lifecyclechannel.ErrFrameLeftForNext) {
		t.Fatalf("applyFrame = %v, want ErrFrameLeftForNext: a frame failing because its coordinator is stopping is handed over, not halted", err)
	}
	if cursor.applied != 0 {
		t.Fatalf("the cursor moved to %d", cursor.applied)
	}
}

// The same rule through a real pane: the command's end arrives after the
// first coordinator began stopping. That coordinator applies nothing of it,
// reports no loss, and its cursor stays at the start; the next coordinator,
// resuming there, completes the command exactly once.
func TestACompletionDeliveredDuringShutdownIsAppliedOnceByTheNextCoordinator(t *testing.T) {
	spawner := &lifecycleSpawner{}
	svc := helperWithIntegratedShells(t, spawner)
	provider := &fakeLaneProvider{peer: sharedHelperPeer(t, svc)}
	store := newMemCursorStore()

	first := newCursorKeepingCoordinator(t, provider, store)
	stopping := &atomic.Bool{}
	first.reg.lifecycleStopping = stopping
	losses := &lossRecorder{}
	first.reg.lifecycleLoss = losses.report
	binding := openHostedFixture(t, first.coordinator, "pane-1")
	shell, _ := spawner.theShell(t)
	shell.drain()
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
	build := lifecycle.AttemptID("shell-build")
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &build, Command: "make build"}})
	waittest.WaitFor(t, "the first coordinator stored the start's cursor", func() bool {
		return store.stored(binding.SessionID) == 2
	})

	stopping.Store(true)
	code := 0
	shell.send(t, lifecycle.Event{
		Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{AttemptID: &build, ExitCode: &code, Fence: lifecycle.FenceNonce{3}},
	})
	// The detach waits for any frame in hand: past it, the first
	// coordinator's word on this frame is final.
	first.quit()
	if done := first.emitter.completed(); len(done) != 0 {
		t.Fatalf("the stopping coordinator completed %v: it applied a frame that arrived after it began stopping", completedCommands(done))
	}
	if got := losses.seen(); len(got) != 0 {
		t.Fatalf("the stopping coordinator reported losses %v, want none", got)
	}
	if n := store.stored(binding.SessionID); n != 2 {
		t.Fatalf("the cursor was stored %d times, want 2: it moved past a frame left for the next coordinator", n)
	}

	second := newCursorKeepingCoordinator(t, provider, store)
	takeBack(t, second, store.bindingFor(binding))
	runBarrier(t, shell, second, "echo after")
	if done := completedCommands(second.emitter.completed()); len(done) != 2 || done[0] != "" && done[0] != "make build" || done[1] != "echo after" {
		t.Fatalf("the next coordinator completed %q, want the command that ended during shutdown, once, then the barrier", done)
	}
}

// A FRAME IN HAND WHEN STOPPING BEGAN IS NOT COMMITTED, WHATEVER ITS WRITES
// ANSWERED (nocx-zg3k3.5.11, the loaded bar after the review round:
// "the command's block never closed: entries = []"). Stopping began while
// the start frame was in the store's hands; its projection then found the
// session closing, recorded no entry, and the block's open was ANSWERED —
// no such entry — rather than failed. Nothing failed, so the frame
// committed, the cursor moved past the start, and the next coordinator,
// resuming there, never saw the command begin. The rule is the frame's, not
// its writes': a frame whose projection ran while stopping stores nothing,
// and the cursor stays before it.
func TestAFrameInHandWhenStoppingBeganIsNotCommitted(t *testing.T) {
	ctx := context.Background()
	db, err := content.Open(ctx, content.Config{
		Path:   filepath.Join(t.TempDir(), "content.db"),
		Key:    make([]byte, 32),
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const sid = "0123456789abcdef0123456789abcdef"
	zero := uint64(0)
	if cerr := db.Ledger().CreateSession(ctx, content.Session{ID: sid, WorkspaceID: content.DefaultWorkspaceID, LifecycleApplied: &zero}); cerr != nil {
		t.Fatalf("CreateSession: %v", cerr)
	}
	stopping := &atomic.Bool{}
	cursor := newLifecycleCursor(ctx, db.Ledger(), stopping)
	cursor.bind(sid, 0)
	cursor.relayed(40, 40)
	err = cursor.applyFrame(40, lifecycle.KindStart, func(fctx context.Context) {
		// The projection writes what it can, and answers what it cannot,
		// with no store failure anywhere — and stopping begins meanwhile.
		if werr := db.Ledger().EnsureEnvironment(fctx, content.Environment{ID: "local", Kind: content.EnvLocal}); werr != nil {
			t.Errorf("EnsureEnvironment: %v", werr)
		}
		stopping.Store(true)
	})
	if !errors.Is(err, lifecyclechannel.ErrFrameLeftForNext) {
		t.Fatalf("applyFrame = %v, want ErrFrameLeftForNext", err)
	}
	pending, err := db.Reconcile().Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	for _, p := range pending {
		if p.SessionID == sid && (p.LifecycleApplied == nil || *p.LifecycleApplied != 0) {
			t.Fatalf("the stored cursor is %v, want 0: it moved past a frame left for the next coordinator", p.LifecycleApplied)
		}
	}
	if cursor.applied != 0 {
		t.Fatalf("the cursor moved to %d in memory", cursor.applied)
	}
}
