package content_test

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// THE COORDINATOR'S OWN LIFECYCLE CURSOR (ADR-0077): the stream offset of the
// last lifecycle frame this coordinator applied, kept with the session's
// binding so the NEXT coordinator can say where it needs the helper's
// lifecycle stream from. It crosses a restart, it only moves forward, and a
// binding that never recorded one says so rather than reading as zero.
func TestTheLifecycleCursorASessionAppliedSurvivesTheRestart(t *testing.T) {
	db, ledger, path := newLedgerAt(t)
	ctx := context.Background()
	if _, err := db.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: "ws-cursor", Name: "work"},
		content.Tab{ID: "tab-cursor", WorkspaceID: "ws-cursor", Layout: content.LayoutRow},
		content.Pane{ID: "pane-cursor", TabID: "tab-cursor", Cwd: "/", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	zero := uint64(0)
	const bound = "0123456789abcdef0123456789abcdef"
	const unrecorded = "fedcba9876543210fedcba9876543210"
	if err := ledger.CreateSession(ctx, content.Session{
		ID: bound, WorkspaceID: "ws-cursor", Generation: "generation-a",
		LifecycleApplied: &zero,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := ledger.CreateSession(ctx, content.Session{
		ID: unrecorded, WorkspaceID: "ws-cursor", Generation: "generation-a",
	}); err != nil {
		t.Fatalf("CreateSession without a cursor: %v", err)
	}
	nothing := func(context.Context) error { return nil }
	if err := ledger.ApplyLifecycleFrame(ctx, bound, 412, nothing); err != nil {
		t.Fatalf("ApplyLifecycleFrame: %v", err)
	}
	// A cursor never moves back: a late frame for an offset already passed
	// changes nothing, so the stored value always names the furthest frame
	// whose effect is stored.
	if err := ledger.ApplyLifecycleFrame(ctx, bound, 97, nothing); err != nil {
		t.Fatalf("ApplyLifecycleFrame behind the cursor: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again, err := content.Open(context.Background(), content.Config{Path: path, Key: testKey(), Budget: testBudget})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if closeErr := again.Close(); closeErr != nil {
			t.Errorf("Close after restart: %v", closeErr)
		}
	}()
	pending, err := again.Reconcile().Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	got := map[string]*uint64{}
	for _, p := range pending {
		got[p.SessionID] = p.LifecycleApplied
	}
	if c := got[bound]; c == nil || *c != 412 {
		t.Fatalf("the bound session carried cursor %v, want 412 — the furthest applied frame, kept across the restart", c)
	}
	if c, ok := got[unrecorded]; !ok || c != nil {
		t.Fatalf("a binding that never recorded a cursor carried %v, want none: absent must not read as offset 0", c)
	}
}
