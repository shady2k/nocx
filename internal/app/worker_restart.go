package app

// The startup restore of the durable worker restart record (ADR-0079,
// nocx-xn63t.5.1): the ONE read of that document, and what this backend does
// with what it finds.
//
// # WHAT IT DOES AND WHAT IT DELIBERATELY DOES NOT
//
// It forgets the records whose pane is no longer in the window, and it
// classifies the rest — never live, always interrupted, either a launch it
// could reconstruct or a failure that names itself.
//
// It does NOT relaunch anything. That is nocx-xn63t.5.2's job, and until the
// launch record (nocx-dz9vj) exists there is no resume invocation to build:
// what a restored tab must run is defined there, not here. What this pass
// guarantees is the half that can be true today and is load-bearing for that
// work — the record survives, it resolves, and a record that cannot be
// restored says so at the moment a backend starts rather than sitting in a
// document until somebody wonders why a tab came back empty.
//
// # WHY THE FORGET IS HERE AND NOT IN THE DOCUMENT
//
// A record outlives its pane by exactly as long as nobody drops it. A pane
// closed while nocx was down has no tab to reopen and no agent to resume into,
// so its record is a note about nothing; and left in place it would be a
// second thing to be wrong about every restart. The layout chain is the one
// owner of which panes are open, so the question is asked there and answered
// with the snapshot every restore already reads — no second reader, and no
// sweep.

import (
	"context"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/workers"
)

// openWindow is the narrow view of the layout chain this pass needs: which
// panes the window holds open right now. It is the existing repository, asked
// through a two-method seam rather than a second store (AD-8).
type openWindow interface {
	Snapshot(ctx context.Context) (content.LayoutSnapshot, error)
}

// restoreWorkerRecords is the startup pass. It is a function rather than a
// method because nothing about it belongs to App: it reads two things and
// writes one, and a test can stand it up over a content store and a document
// without a backend.
//
// EVERY FAILURE IS REPORTED, NEVER SWALLOWED. A record set nocx could not read
// is the one case where saying "nothing to restore" would be the worst answer
// available: the workers really were there, and a person who is told nothing
// concludes nocx lost them rather than that it declined to look.
func restoreWorkerRecords(ctx context.Context, lg log.Logger,
	store workers.RestartStore, window openWindow, probe workers.RestartProbe,
) []workers.Restoration {
	records, err := store.Records(ctx)
	if err != nil {
		lg.Warn("worker restart: the restart record could not be read, so no worker can be resumed after this restart",
			"error", err)
		return nil
	}
	if len(records) == 0 {
		return nil
	}

	kept := forgetRecordsOfClosedPanes(ctx, lg, store, window, records)
	restored := workers.Restore(ctx, kept, probe)
	for _, x := range restored {
		if !x.Restorable() {
			lg.Warn("worker restart: a persisted worker cannot be resumed after this restart",
				"participant", string(x.Record.Participant),
				"pane_id", x.Record.PaneID,
				"agent", x.Record.Agent,
				"reason", string(x.Failure.Reason),
				"detail", x.Failure.Detail)
			continue
		}
		lg.Info("worker restart: a persisted worker is waiting to be relaunched after this restart",
			"participant", string(x.Record.Participant),
			"pane_id", x.Record.PaneID,
			"agent", x.Record.Agent,
			"resume_mode", string(x.Record.Resume.Mode),
			"worktree", x.Request.Worktree.Path)
	}
	return restored
}

// forgetRecordsOfClosedPanes drops the records whose pane is no longer open,
// and answers the ones that stay. A record with no pane at all is kept: this
// pass cannot tell it apart from one whose pane the chain cannot name, and
// dropping it would delete a note over an absence of evidence — the checkout
// probe below is the thing that judges a record's usability.
func forgetRecordsOfClosedPanes(ctx context.Context, lg log.Logger,
	store workers.RestartStore, window openWindow,
	records []workers.RestartRecord,
) (kept []workers.RestartRecord) {
	if window == nil {
		return records
	}
	snap, err := window.Snapshot(ctx)
	if err != nil {
		// The window could not be read, so nothing can be said about which
		// panes are open and NO record is dropped: a sweep that guessed would
		// delete the very records it exists to keep.
		lg.Warn("worker restart: the window could not be read, so no restart record was dropped",
			"error", err)
		return records
	}
	open := make(map[string]bool, len(snap.Panes))
	for _, pane := range snap.Panes {
		open[pane.ID] = true
	}
	for _, rec := range records {
		if rec.PaneID != "" && !open[rec.PaneID] {
			if err := store.Forget(ctx, rec.Participant); err != nil {
				lg.Warn("worker restart: a closed pane's restart record could not be dropped",
					"participant", string(rec.Participant), "pane_id", rec.PaneID, "error", err)
				kept = append(kept, rec)
				continue
			}
			lg.Info("worker restart: a persisted worker's pane is no longer in the window, so its record was dropped",
				"participant", string(rec.Participant), "pane_id", rec.PaneID)
			continue
		}
		kept = append(kept, rec)
	}
	return kept
}
