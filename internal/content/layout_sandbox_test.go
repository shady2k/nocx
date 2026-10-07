package content_test

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/sandbox"
)

func TestWorkspaceProfileOverrideResetClockAndRenamePreservation(t *testing.T) {
	_, layout := newLayout(t)
	ctx := context.Background()
	seedWorkspace(t, layout, "workspace-profile", "tab-profile", "pane-profile")
	store, ok := layout.(sandbox.WorkspaceProfileStore)
	if !ok {
		t.Fatal("layout does not implement sandbox.WorkspaceProfileStore")
	}

	initial, err := store.GetWorkspaceProfile(ctx, "workspace-profile")
	if err != nil {
		t.Fatal(err)
	}
	if initial.Revision != 0 || initial.Override != nil {
		t.Fatalf("initial workspace profile = %#v", initial)
	}
	override := &sandbox.ProfileRoots{ReadOnlyDirs: []string{"/standard", "/extra"}, ReadWriteDirs: []string{}}
	updated, err := store.UpdateWorkspaceProfile(ctx, "workspace-profile", 0, override)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 1 || updated.Override == nil || len(updated.Override.ReadOnlyDirs) != 2 {
		t.Fatalf("first override = %#v", updated)
	}

	if _, err = layout.RenameWorkspace(ctx, "workspace-profile", "renamed"); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.GetWorkspaceProfile(ctx, "workspace-profile")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Revision != 1 || persisted.Override == nil || persisted.Override.ReadOnlyDirs[0] != "/standard" {
		t.Fatalf("rename lost sparse payload: %#v", persisted)
	}

	reset, err := store.UpdateWorkspaceProfile(ctx, "workspace-profile", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Revision != 2 || reset.Override != nil {
		t.Fatalf("reset-to-inherit = %#v", reset)
	}
	if _, err := store.UpdateWorkspaceProfile(ctx, "workspace-profile", 1, override); !errors.Is(err, sandbox.ErrProfileConflict) {
		t.Fatalf("stale workspace CAS = %v", err)
	}
	if _, err := store.GetWorkspaceProfile(ctx, content.DefaultWorkspaceID); err != nil {
		t.Fatalf("default profile: %v", err)
	}
	if _, err := store.UpdateWorkspaceProfile(ctx, content.DefaultWorkspaceID, 0, override); !errors.Is(err, sandbox.ErrWorkspaceProfileUnsupported) {
		t.Fatalf("default update = %v", err)
	}
}

func TestWorkspaceProfileRejectsUnknownWorkspaceWithoutCreatingIt(t *testing.T) {
	_, layout := newLayout(t)
	store, ok := layout.(sandbox.WorkspaceProfileStore)
	if !ok {
		t.Fatal("layout does not implement sandbox.WorkspaceProfileStore")
	}
	if _, err := store.UpdateWorkspaceProfile(context.Background(), "workspace-missing", 0, &sandbox.ProfileRoots{}); !errors.Is(err, content.ErrNoSuchWorkspace) {
		t.Fatalf("unknown workspace update = %v", err)
	}
	if _, err := store.GetWorkspaceProfile(context.Background(), "workspace-missing"); !errors.Is(err, content.ErrNoSuchWorkspace) {
		t.Fatalf("unknown workspace read = %v", err)
	}
}
