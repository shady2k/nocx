package content_test

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestSessionGenerationSurvivesRestart(t *testing.T) {
	db, ledger, path := newLedgerAt(t)
	ctx := context.Background()
	if _, err := db.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: "ws-generation", Name: "work"},
		content.Tab{ID: "tab-generation", WorkspaceID: "ws-generation", Layout: content.LayoutRow},
		content.Pane{ID: "pane-generation", TabID: "tab-generation", Cwd: "/", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := ledger.CreateSession(ctx, content.Session{
		ID: "0123456789abcdef0123456789abcdef", WorkspaceID: "ws-generation",
		Host: "build.example.com", Account: "deploy", Generation: "generation-a",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
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
	if len(pending) != 1 || pending[0].Generation != "generation-a" ||
		pending[0].Host != "build.example.com" || pending[0].Account != "deploy" {
		t.Fatalf("pending = %+v, want generation-a at deploy@build.example.com", pending)
	}
}

func TestSessionWithoutGenerationRemainsUnowned(t *testing.T) {
	db, ledger, _ := newLedgerAt(t)
	ctx := context.Background()
	if _, err := db.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: "ws-unowned", Name: "work"},
		content.Tab{ID: "tab-unowned", WorkspaceID: "ws-unowned", Layout: content.LayoutRow},
		content.Pane{ID: "pane-unowned", TabID: "tab-unowned", Cwd: "/", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := ledger.CreateSession(ctx, content.Session{
		ID: "fedcba9876543210fedcba9876543210", WorkspaceID: "ws-unowned",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	pending, err := db.Reconcile().Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Generation != "" {
		t.Fatalf("pending = %+v, want empty generation", pending)
	}
}
