package app

// A FRAME THE STORE REFUSES ON EVERY ATTEMPT HALTS THE LEG, AND THE PANE SAYS
// SO (ADR-0077, the owner's decisions of 2026-09-30). The store rolls back
// each attempt and the same coordinator tries three more times; after the
// fourth, nothing of the frame and not the cursor is stored, the leg applies
// nothing more, and the loss reaches the pane's integration axis as
// store-refused. The next coordinator, resuming from the stored cursor,
// applies the frame once.

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/waittest"
)

// immediatePauses is a frame timer under which a hold bound never fires and
// every pause between attempts fires at once: no test waits on a duration.
func immediatePauses(d time.Duration, fire func()) func() bool {
	if d == content.LifecycleFrameMaxHold {
		return func() bool { return true }
	}
	go fire()
	return func() bool { return false }
}

// storeWritingEmitter is the projection's store half, reduced to one recorded
// command per completed attempt. refuse first ensures an environment of a kind
// the store's CHECK refuses — the failure every attempt of the frame meets.
type storeWritingEmitter struct {
	*recordingEmitter
	ledger content.LedgerRepository
	refuse bool
}

func (e *storeWritingEmitter) PublishLifecycle(ctx context.Context, f lifecyclepub.Fact) {
	if f.Attempt != nil && f.Attempt.State == lifecyclepub.AttemptCompleted {
		if e.refuse {
			_ = e.ledger.EnsureEnvironment(ctx, content.Environment{ID: "env-refused", Kind: "refused-by-the-store"})
		}
		_, _ = e.ledger.RecordCompleted(ctx, content.CompletedCommand{
			Client: "retry-test", Env: content.Environment{ID: "local", Kind: content.EnvLocal},
			Cwd: "/", Intent: f.Attempt.ID, Status: content.EntrySuccess, Source: content.SourceUser,
		})
	}
	e.recordingEmitter.PublishLifecycle(ctx, f)
}

type lossRecorder struct {
	mu     sync.Mutex
	causes []lifecyclechannel.LossCause
}

func (r *lossRecorder) report(_ lifecycle.LaneID, cause lifecyclechannel.LossCause) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.causes = append(r.causes, cause)
}

func (r *lossRecorder) seen() []lifecyclechannel.LossCause {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]lifecyclechannel.LossCause(nil), r.causes...)
}

func newStoreKeepingCoordinator(t *testing.T, provider *fakeLaneProvider, db content.ContentDB, refuse bool) (*integratedCoordinator, *lossRecorder) {
	t.Helper()
	c := newIntegratedCoordinator(t, provider)
	pub, ok := c.reg.lifecycle.(*lifecyclepub.Publisher)
	if !ok {
		t.Fatalf("the coordinator's publisher is %T", c.reg.lifecycle)
	}
	pub.SetEmitter(&storeWritingEmitter{recordingEmitter: c.emitter, ledger: db.Ledger(), refuse: refuse})
	c.reg.lifecycleCursors = db.Ledger()
	losses := &lossRecorder{}
	c.reg.lifecycleLoss = losses.report
	return c, losses
}

func TestAFrameTheStoreRefusesOnEveryAttemptHaltsTheLegAndIsAppliedOnceByTheNext(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "content.db")
	open := func() content.ContentDB {
		db, err := content.Open(ctx, content.Config{
			Path:       path,
			Key:        make([]byte, 32),
			Budget:     content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
			FrameTimer: immediatePauses,
		})
		if err != nil {
			t.Fatalf("content.Open: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db := open()

	spawner := &lifecycleSpawner{}
	svc := helperWithIntegratedShells(t, spawner)
	provider := &fakeLaneProvider{peer: sharedHelperPeer(t, svc)}

	first, losses := newStoreKeepingCoordinator(t, provider, db, true)
	binding := openHostedFixture(t, first.coordinator, "pane-1")
	if _, err := db.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: "ws-retry", Name: "retry"},
		content.Tab{ID: "tab-retry", WorkspaceID: "ws-retry", Layout: content.LayoutRow},
		content.Pane{ID: "pane-retry", TabID: "tab-retry", Cwd: "/", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	zero := uint64(0)
	if err := db.Ledger().CreateSession(ctx, content.Session{ID: binding.SessionID, WorkspaceID: "ws-retry", LifecycleApplied: &zero}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	shell, _ := spawner.theShell(t)
	shell.drain()
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
	build := lifecycle.AttemptID("shell-build")
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &build, Command: "make build"}})
	code := 0
	shell.send(t, lifecycle.Event{
		Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{AttemptID: &build, ExitCode: &code, Fence: lifecycle.FenceNonce{7}},
	})

	// Four attempts refused: the leg halts and the pane is told.
	waittest.WaitFor(t, "the first coordinator's leg halted with the loss stated", func() bool {
		for _, c := range losses.seen() {
			if c == lifecyclechannel.LossStoreRefused {
				return true
			}
		}
		return false
	})
	if got := losses.seen(); len(got) != 1 {
		t.Fatalf("losses %v, want exactly the one store-refused halt", got)
	}
	first.quit()
	// The process restarts: the store hands the next coordinator the cursor
	// the last one stored.
	if err := db.Close(); err != nil {
		t.Fatalf("closing the first coordinator's store: %v", err)
	}
	db = open()
	pending, err := db.Reconcile().Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	var cursor *uint64
	for _, p := range pending {
		if p.SessionID == binding.SessionID {
			cursor = p.LifecycleApplied
		}
	}
	if cursor == nil || *cursor == 0 {
		t.Fatalf("the stored cursor is %v, want the end of the start frame — the frames before the refused one were applied", cursor)
	}

	// The next coordinator resumes from the cursor and applies the frame
	// once.
	second, secondLosses := newStoreKeepingCoordinator(t, provider, db, false)
	binding.LifecycleApplied = cursor
	takeBack(t, second, binding)
	waittest.WaitFor(t, "the next coordinator applied the refused frame", func() bool {
		return len(second.emitter.completed()) == 1
	})
	// Exactly once in the store: RecordCompleted mints a new entry each
	// time it runs, so a frame applied twice would leave two.
	entries, err := db.Ledger().ListEntries(ctx, 50)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	recorded := 0
	for _, e := range entries {
		if e.Intent == string(build) {
			recorded++
		}
	}
	if recorded != 1 {
		t.Fatalf("the frame is recorded %d times, want once: %+v", recorded, entries)
	}
	if got := secondLosses.seen(); len(got) != 0 {
		t.Fatalf("the next coordinator reported losses %v, want none", got)
	}
}
