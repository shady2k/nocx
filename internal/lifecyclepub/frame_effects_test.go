package lifecyclepub_test

// WHAT A FRAME TELLS THE SHELL WAITS FOR THE FRAME'S COMMIT (ADR-0077
// decision 12). The shell's ACCEPT is the publisher's answer to a hello; a
// frame that fails every attempt stored nothing — not even the cursor that
// says the hello was applied — so the next coordinator applies the hello
// again, and a shell told ACCEPT by both would have been told twice. So the
// accept waits in the frame's post-commit queue and is dropped with a frame
// that fails, and the next coordinator's replay of the same hello answers it
// once.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
)

func openFrameStore(t *testing.T) content.ContentDB {
	t.Helper()
	db, err := content.Open(context.Background(), content.Config{
		Path:   filepath.Join(t.TempDir(), "content.db"),
		Key:    make([]byte, 32),
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func countAccepts(p *recordingPort) int {
	n := 0
	for _, k := range p.kinds() {
		if k == lifecycle.KindAccept {
			n++
		}
	}
	return n
}

func TestAnAcceptWaitsForItsFramesCommitAndTheNextCoordinatorSendsItOnce(t *testing.T) {
	ctx := context.Background()
	db := openFrameStore(t)
	// A binding that records no cursor: the frame's last write fails, on
	// every attempt, after the hello's projection has run.
	if err := db.Ledger().CreateSession(ctx, content.Session{ID: "sess-no-cursor", WorkspaceID: content.DefaultWorkspaceID}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	zero := uint64(0)
	if err := db.Ledger().CreateSession(ctx, content.Session{ID: "sess-cursor", WorkspaceID: content.DefaultWorkspaceID, LifecycleApplied: &zero}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	const lane = lifecycle.LaneID("lane-accept")
	first := &recordingPort{}
	pub := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	pub.SetEmitter(&recorder{})
	if err := pub.BindTransport("T", first); err != nil {
		t.Fatalf("BindTransport: %v", err)
	}
	h, err := pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	hello := env(lane, h, 1, helloEvt())
	ferr := db.Ledger().ApplyLifecycleFrame(ctx, "sess-no-cursor", 100, func(fctx context.Context) error {
		return pub.Ingest(fctx, "T", hello)
	})
	if !errors.Is(ferr, content.ErrLifecycleCursorMissing) {
		t.Fatalf("the hello's frame = %v, want it failed at its cursor", ferr)
	}
	if n := countAccepts(first); n != 0 {
		t.Fatalf("the shell was sent %d ACCEPTs for a frame that was never stored, want none", n)
	}

	// The next coordinator adopts the domain and is offered the same hello
	// from the cursor, which never moved past it.
	next := &recordingPort{}
	pub2 := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	pub2.SetEmitter(&recorder{})
	if err := pub2.BindTransport("T2", next); err != nil {
		t.Fatalf("BindTransport: %v", err)
	}
	if _, err := pub2.AdoptDomain(lane, h.Domain, h.Epoch, h.Capability, h.Recovery, "T2"); err != nil {
		t.Fatalf("AdoptDomain: %v", err)
	}
	if err := db.Ledger().ApplyLifecycleFrame(ctx, "sess-cursor", 100, func(fctx context.Context) error {
		return pub2.Ingest(fctx, "T2", hello)
	}); err != nil {
		t.Fatalf("the next coordinator's hello frame: %v", err)
	}
	if n := countAccepts(first) + countAccepts(next); n != 1 {
		t.Fatalf("the shell was sent %d ACCEPTs across both coordinators, want exactly one", n)
	}
}
