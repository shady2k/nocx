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
	db, ledger, path := newLedgerAt(t)
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
	// The carry-over set is read at Open, from the rows a PREVIOUS incarnation
	// left; a session created after that runs is not in it. So this restarts,
	// exactly as its sibling above does — without the restart the assertion
	// would be about the wrong incarnation.
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
	if len(pending) != 1 || pending[0].Generation != "" {
		t.Fatalf("pending = %+v, want empty generation", pending)
	}
}

// THE ROUTE BACK SURVIVES THE RESTART TOO (nocx-k6p18.30). Generation, host and
// account are what a VERDICT needs; taking the session back needs the three
// facts a verdict never asks for — which pane it was the pipe of, which saved
// connection reaches its host, and where the helper binary the bridge execs
// lives — plus the consent key of the machine it runs on.
//
// It is asserted through a REOPEN and not off the struct that was written,
// because the whole failure this bead's parent chain paid for was a write path
// nothing read (nocx-1u0am): a test that read back its own value would prove
// the struct is well-formed, not that the payload carries it.
func TestTheRouteBackToASessionSurvivesRestart(t *testing.T) {
	db, ledger, path := newLedgerAt(t)
	ctx := context.Background()
	if _, err := db.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: "ws-route", Name: "work"},
		content.Tab{ID: "tab-route", WorkspaceID: "ws-route", Layout: content.LayoutRow},
		content.Pane{ID: "pane-route", TabID: "tab-route", Cwd: "/", Kind: content.PaneSSH, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := ledger.CreateSession(ctx, content.Session{
		ID: "abcdef0123456789abcdef0123456789", WorkspaceID: "ws-route",
		Host: "build.example.com", Account: "deploy", Generation: "generation-a",
		PaneID: "pane-route", ProfileID: "profile-7",
		HelperCommand: "/home/deploy/.nocx/helper/generation-a/nocx-helper",
		Fingerprint:   "SHA256:build-machine",
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
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want exactly one", pending)
	}
	got := pending[0]
	if got.PaneID != "pane-route" {
		t.Fatalf("pane = %q, want pane-route — a session taken back with no pane is an entry no "+
			"restored pane can claim", got.PaneID)
	}
	if got.ProfileID != "profile-7" {
		t.Fatalf("profile = %q, want profile-7 — without it there is no route to the host", got.ProfileID)
	}
	if got.HelperCommand != "/home/deploy/.nocx/helper/generation-a/nocx-helper" {
		t.Fatalf("helper command = %q, want the path the install recorded", got.HelperCommand)
	}
	if got.Fingerprint != "SHA256:build-machine" {
		t.Fatalf("fingerprint = %q, want the consent key of the machine it runs on", got.Fingerprint)
	}
}

// The paired negative, and it is the one that keeps the route from being
// guessed at: a session written with no route comes back with none, rather
// than with an empty string that some later reader treats as a value.
func TestASessionWithNoRouteComesBackWithNone(t *testing.T) {
	db, ledger, path := newLedgerAt(t)
	ctx := context.Background()
	if _, err := db.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: "ws-noroute", Name: "work"},
		content.Tab{ID: "tab-noroute", WorkspaceID: "ws-noroute", Layout: content.LayoutRow},
		content.Pane{ID: "pane-noroute", TabID: "tab-noroute", Cwd: "/", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := ledger.CreateSession(ctx, content.Session{
		ID: "99999999999999999999999999999999", WorkspaceID: "ws-noroute",
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
	defer func() { _ = again.Close() }()
	pending, err := again.Reconcile().Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want exactly one", pending)
	}
	if got := pending[0]; got.PaneID != "" || got.ProfileID != "" || got.HelperCommand != "" || got.Fingerprint != "" {
		t.Fatalf("a session written with no route came back with one: %+v", got)
	}
}
