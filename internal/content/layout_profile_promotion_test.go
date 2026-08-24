package content_test

// The copy-on-write promotion path (design 2026-08-23 §4.4): a named
// workspace without an explicit profile receives a revision-1 profile
// initialized from the current standard profile plus the promoted path. The
// composition root's AccessGrantStore wires these two seams together; this
// test pins the semantics the seams must compose into.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/settings"
)

func TestWorkspacePromotionCopyOnWriteFromStandardProfile(t *testing.T) {
	db, _ := newTestStore(t)
	layout := db.Layout()
	ctx := context.Background()
	if _, err := layout.CreateWorkspace(ctx,
		content.Workspace{ID: "ws-1", Name: "ws-1"},
		aTab("tab-1", "ws-1"), aPane("pane-1", "tab-1", "/srv")); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	base := t.TempDir()
	stdW := filepath.Join(base, "std-w")
	stdR := filepath.Join(base, "std-r")
	promoted := filepath.Join(base, "promoted")
	for _, d := range []string{stdW, stdR, promoted} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	stdWCanon, _ := filepath.EvalSymlinks(stdW)
	stdRCanon, _ := filepath.EvalSymlinks(stdR)
	promotedCanon, _ := filepath.EvalSymlinks(promoted)

	// The standard profile is the copy-on-write seed; the promoted path is
	// appended to the writable list (readWrite decision).
	writable := []string{stdW, promoted}
	readOnly := []string{stdR}
	canonWritable, canonReadOnly, err := settings.CanonicalizeSandboxProfile(writable, readOnly)
	if err != nil {
		t.Fatalf("CanonicalizeSandboxProfile: %v", err)
	}

	revision, err := layout.SetWorkspaceSandboxProfile(ctx, "ws-1", 0, content.WorkspaceSandboxProfile{
		SchemaVersion: 1,
		WritablePaths: canonWritable,
		ReadOnlyPaths: canonReadOnly,
	})
	if err != nil {
		t.Fatalf("SetWorkspaceSandboxProfile: %v", err)
	}
	if revision != 1 {
		t.Fatalf("revision = %d, want 1", revision)
	}
	prof, err := layout.WorkspaceSandboxProfile(ctx, "ws-1")
	if err != nil || prof == nil {
		t.Fatalf("read profile = %#v, %v", prof, err)
	}
	if len(prof.WritablePaths) != 2 ||
		prof.WritablePaths[0] != stdWCanon || prof.WritablePaths[1] != promotedCanon {
		t.Fatalf("writable = %v, want [%s %s]", prof.WritablePaths, stdWCanon, promotedCanon)
	}
	if len(prof.ReadOnlyPaths) != 1 || prof.ReadOnlyPaths[0] != stdRCanon {
		t.Fatalf("readOnly = %v, want [%s]", prof.ReadOnlyPaths, stdRCanon)
	}

	// A second promotion into the now-explicit profile appends at revision 1.
	second := filepath.Join(base, "second")
	if mkdirErr := os.MkdirAll(second, 0o750); mkdirErr != nil {
		t.Fatalf("mkdir: %v", mkdirErr)
	}
	secondCanon, _ := filepath.EvalSymlinks(second)
	canonWritable, canonReadOnly, err = settings.CanonicalizeSandboxProfile(
		append(append([]string(nil), prof.WritablePaths...), second), prof.ReadOnlyPaths)
	if err != nil {
		t.Fatalf("CanonicalizeSandboxProfile(second): %v", err)
	}
	rev2, err := layout.SetWorkspaceSandboxProfile(ctx, "ws-1", 1, content.WorkspaceSandboxProfile{
		SchemaVersion: 1, WritablePaths: canonWritable, ReadOnlyPaths: canonReadOnly,
	})
	if err != nil || rev2 != 2 {
		t.Fatalf("second promotion = %d, %v; want 2, nil", rev2, err)
	}
	prof, _ = layout.WorkspaceSandboxProfile(ctx, "ws-1")
	if len(prof.WritablePaths) != 3 || prof.WritablePaths[2] != secondCanon {
		t.Fatalf("after second promotion writable = %v", prof.WritablePaths)
	}
}
