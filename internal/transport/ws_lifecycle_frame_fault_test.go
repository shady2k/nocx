package transport

// A FRAME THE STORE DID NOT COMMIT IS A FRAME THE NEXT COORDINATOR APPLIES,
// ONCE (ADR-0077). The frame's projection — here the start fact's: the
// entry, its execution, the block's artifact — and the coordinator's cursor
// past it are one transaction. A fault after the frame's last row and before
// the cursor, which is where a coordinator killed mid-frame dies, leaves
// neither behind; the process restarts, the next coordinator resumes the
// stream at the stored cursor, and the frame lands exactly once.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

var errFaultAtTheCursor = errors.New("injected: the process dies after the frame's last row")

// applyFrameThroughTheStore applies one frame the way the coordinator does:
// the kernel's ingest inside the store's own frame, the cursor at offset.
// faultAtCursor fails the frame after its last row and before the cursor.
func applyFrameThroughTheStore(t *testing.T, db content.ContentDB, sid string, pub *lifecyclepub.Publisher,
	tID lifecycle.TransportID, env lifecycle.Envelope, offset uint64, faultAtCursor bool,
) error {
	t.Helper()
	return db.Ledger().ApplyLifecycleFrame(context.Background(), sid, offset, func(ctx context.Context) error {
		if err := pub.Ingest(ctx, tID, env); err != nil {
			t.Fatalf("the kernel refused the %s: %v", env.Event.Kind, err)
		}
		if faultAtCursor {
			return errFaultAtTheCursor
		}
		return nil
	})
}

// storedCursor is the cursor the store hands the next process: read the way
// a restarted coordinator reads it, from the pending bindings at Open.
func storedCursor(t *testing.T, path, sid string) (content.ContentDB, uint64) {
	t.Helper()
	db := newLedgerStoreAt(t, path)
	pending, err := db.Reconcile().Pending(context.Background())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	for _, p := range pending {
		if p.SessionID == sid && p.LifecycleApplied != nil {
			return db, *p.LifecycleApplied
		}
	}
	t.Fatalf("the binding for %s carries no cursor", sid)
	return db, 0
}

// immediatePauses is a frame timer under which a hold bound never fires and
// every pause between attempts fires at once: no test waits on a duration.
func immediatePauses(d time.Duration, fire func()) func() bool {
	if d == content.LifecycleFrameMaxHold {
		return func() bool { return true }
	}
	go fire()
	return func() bool { return false }
}

func newRetryingLedgerStoreAt(t *testing.T, path string) content.ContentDB {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i) // newLedgerStoreAt's key, so either opens the file
	}
	db, err := content.Open(context.Background(), content.Config{
		Path:       path,
		Key:        key,
		Budget:     content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger:     log.NewSlogAdapter(nil),
		FrameTimer: immediatePauses,
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// THE SAME COORDINATOR APPLIES A FAILED FRAME AGAIN (the owner's retry,
// 2026-09-30). The start frame's first attempt fails after its last row and
// before the cursor; the store rolls it back and replays the writes the real
// projection made — the entry, its execution, the block's artifact — in a
// fresh transaction, and the frame lands exactly once, cursor included,
// before the next frame is read. The restarted store then hands the next
// coordinator the cursor past it.
func TestAFrameFailedAtTheCursorIsAppliedOnceOnItsNextAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	first := newRetryingLedgerStoreAt(t, path)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, first)
	zero := uint64(0)
	if err := first.Ledger().CreateSession(context.Background(), content.Session{
		ID: sid, WorkspaceID: "ws-lifecycle", LifecycleApplied: &zero,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	e.ws.AttachBlockRows(session.ID(sid))

	const promptEnd, startEnd = 100, 220
	r := repeatFrames{lane: lane, h: h}
	if err := applyFrameThroughTheStore(t, first, sid, pub, "T", r.prompt(2), promptEnd, false); err != nil {
		t.Fatalf("the prompt frame: %v", err)
	}
	if err := applyFrameThroughTheStore(t, first, sid, pub, "T", r.start(3, 0, "make build"), startEnd, true); err != nil {
		t.Fatalf("the start frame, failed once at its cursor = %v, want it applied on the next attempt", err)
	}
	assertOneOfEach(t, first)
	if shape := ledgerShape(t, first); shape == "" {
		t.Fatal("the start frame stored nothing")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}
	if _, cursor := storedCursor(t, path, sid); cursor != startEnd {
		t.Fatalf("the stored cursor is %d, want %d — past the frame its next attempt applied", cursor, startEnd)
	}
}
