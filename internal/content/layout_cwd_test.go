package content_test

// SetPaneCwd — the one writer of panes.cwd after creation (nocx-zkiv4,
// design §5).
//
// A pane's cwd was written once, at creation, and never revised: layout.go
// said so in as many words, with "until restore needs one" beside it.
// Restore needs one. Without it a restored local pane opens wherever the
// pane was FIRST created rather than where the person left it, which for a
// tab that has been working in a repository all afternoon is the wrong
// directory every time.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestSetPaneCwd_WritesTheDirectoryARestoreWillOpenIn(t *testing.T) {
	ctx := context.Background()
	db, _ := newLedger(t)
	aPaneUnder(t, db, "0198f2b0-0000-7000-8000-00000000e001",
		"0198f2b0-0000-7000-8000-00000000e002", "0198f2b0-0000-7000-8000-00000000e003")
	const paneID = "0198f2b0-0000-7000-8000-00000000e003"

	got, err := db.Layout().SetPaneCwd(ctx, paneID, "/repo/frontend/src")
	if err != nil {
		t.Fatalf("SetPaneCwd: %v", err)
	}
	if got.Cwd != "/repo/frontend/src" {
		t.Fatalf("returned cwd = %q", got.Cwd)
	}

	// Read back through the snapshot, which is what a restore actually reads.
	snap, err := db.Layout().Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	found := false
	for _, p := range snap.Panes {
		if p.ID == paneID {
			found = true
			if p.Cwd != "/repo/frontend/src" {
				t.Fatalf("stored cwd = %q, want /repo/frontend/src", p.Cwd)
			}
		}
	}
	if !found {
		t.Fatal("the pane is not in the snapshot")
	}
}

// Idempotent, because the renderer reports a cwd on every verified OSC 7 and
// most of them say what the last one said: a shell that prints its prompt
// twice in the same directory must not cost two writes.
func TestSetPaneCwd_TheSameDirectoryTwiceIsOneAnswer(t *testing.T) {
	ctx := context.Background()
	db, _ := newLedger(t)
	aPaneUnder(t, db, "0198f2b0-0000-7000-8000-00000000e011",
		"0198f2b0-0000-7000-8000-00000000e012", "0198f2b0-0000-7000-8000-00000000e013")
	const paneID = "0198f2b0-0000-7000-8000-00000000e013"

	first, err := db.Layout().SetPaneCwd(ctx, paneID, "/srv")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := db.Layout().SetPaneCwd(ctx, paneID, "/srv")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first != second {
		t.Fatalf("second call answered differently: %+v vs %+v", first, second)
	}
}

// An id no pane carries is ErrNoSuchPane, never a silent no-op: a renderer
// reporting a cwd for a pane the chain does not hold is a bug somewhere, and
// swallowing it hides which.
func TestSetPaneCwd_UnknownPane(t *testing.T) {
	_, err := newLedger(t)
	_ = err
	db, _ := newLedger(t)
	if _, setErr := db.Layout().SetPaneCwd(context.Background(),
		"0198f2b0-0000-7000-8000-0000000000ff", "/tmp"); !errors.Is(setErr, content.ErrNoSuchPane) {
		t.Fatalf("err = %v, want ErrNoSuchPane", setErr)
	}
}

// PaneCwd — the reader that made the column worth writing twice (nocx-ty5ks).
//
// Restore reads the whole snapshot; a worker's spawn needs ONE pane's
// directory, resolved from its id, because that is what says where the
// coordinator is standing and therefore where the participant's pane opens.

func TestPaneCwd_AnswersWhatTheRendererReported(t *testing.T) {
	ctx := context.Background()
	db, _ := newLedger(t)
	aPaneUnder(t, db, "0198f2b0-0000-7000-8000-00000000f001",
		"0198f2b0-0000-7000-8000-00000000f002", "0198f2b0-0000-7000-8000-00000000f003")
	const paneID = "0198f2b0-0000-7000-8000-00000000f003"

	if _, err := db.Layout().SetPaneCwd(ctx, paneID, "/home/dev/repos/iaam"); err != nil {
		t.Fatalf("SetPaneCwd: %v", err)
	}
	got, err := db.Layout().PaneCwd(ctx, paneID)
	if err != nil {
		t.Fatalf("PaneCwd: %v", err)
	}
	if got != "/home/dev/repos/iaam" {
		t.Fatalf("PaneCwd = %q, want the directory SetPaneCwd wrote", got)
	}
}

// A pane whose shell has never reported one answers "", and that is an
// answer rather than a failure: a pane with no shell integration reaches no
// prompt, and a caller choosing a directory has to be able to act on it.
func TestPaneCwd_APaneNobodyHasReportedForAnswersEmpty(t *testing.T) {
	ctx := context.Background()
	db, _ := newLedger(t)
	const (
		wsID   = "0198f2b0-0000-7000-8000-00000000f011"
		tabID  = "0198f2b0-0000-7000-8000-00000000f012"
		paneID = "0198f2b0-0000-7000-8000-00000000f013"
	)
	// Created with no cwd at all, which is the state of every pane whose
	// shell has not reached a prompt nocx could verify.
	if _, err := db.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: wsID, Name: "work"},
		content.Tab{ID: tabID, WorkspaceID: wsID, Position: 0, Layout: content.LayoutRow},
		content.Pane{ID: paneID, TabID: tabID, Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	got, err := db.Layout().PaneCwd(ctx, paneID)
	if err != nil {
		t.Fatalf("PaneCwd: %v", err)
	}
	if got != "" {
		t.Fatalf("PaneCwd = %q, want no directory at all", got)
	}
}

// And an id no pane carries is the OTHER fact, kept apart from the one above
// for the reason SetPaneCwd keeps them apart: "nobody reported one" and
// "there is no such pane" send a caller to different places.
func TestPaneCwd_UnknownPane(t *testing.T) {
	db, _ := newLedger(t)
	if _, err := db.Layout().PaneCwd(context.Background(),
		"0198f2b0-0000-7000-8000-0000000000fe"); !errors.Is(err, content.ErrNoSuchPane) {
		t.Fatalf("err = %v, want ErrNoSuchPane", err)
	}
}
