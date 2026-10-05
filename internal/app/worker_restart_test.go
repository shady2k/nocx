package app

// The restart record at the composition root (ADR-0079, nocx-xn63t.5.1).
//
// Two things are proved here and neither is the workers package's own proof:
// that a SPAWN really writes a record naming the pane its session is in — the
// link the whole record exists for, and one nothing in internal/workers can
// check because the pane id is minted in this layer — and that the startup
// pass drops what it must and reports what it cannot.
//
// The complete-tuple round trip across a reopen is restart_test.go's, over the
// document store; what is under test here is the WIRING, which is exactly the
// part a record nothing writes would leave green.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/agentrecord"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/workers"
)

// openLayoutStore is the product's own layout chain over a fresh file: the
// restore pass asks THAT chain which panes are open, and a double for it would
// prove only that the pass called the double.
func openLayoutStore(t *testing.T) content.ContentDB {
	t.Helper()
	key := make([]byte, 32)
	db, err := content.Open(context.Background(), content.Config{
		Path:   filepath.Join(t.TempDir(), "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// createTabOverWS adds a tab to an existing workspace, with its first pane —
// the one creation that adds a member to a container that already exists.
func createTabOverWS(t *testing.T, conn *websocket.Conn, id int, workspaceID, tabID, paneID string) {
	t.Helper()
	resp := callAppWS(t, conn, "tabs.create", map[string]any{
		"id": tabID, "workspaceId": workspaceID, "position": 1,
		"layout": "row",
		"firstPane": map[string]any{
			"id": paneID, "cwd": "/", "kind": "local", "sizeShare": 1,
		},
	}, id)
	if resp.Error != nil {
		t.Fatalf("tabs.create %s: %+v", tabID, resp.Error)
	}
}

// A worker spawned through the product's own spawner leaves a record naming
// the pane its session actually holds — the worker-to-pane link — and the
// record survives a reopen of the document, which is a backend restart at this
// layer.
func TestASpawnedWorkerLeavesARestartRecordNamingItsOwnPane(t *testing.T) {
	ctx := context.Background()
	stand := newWorkerStand(t)

	// A restart store the stand's registrar writes through, over a document of
	// its own so the reopen below is a second store and not the same value.
	docs := storage.NewDocumentStore(t.TempDir())
	restarts := workers.NewFileRestartStore(docs, "worker-restarts.json")
	stand.record.SetRestartRecords(restarts)

	p := stand.registerWithEnrolment(t, "read AGENTS.md and report")
	sess, err := stand.reg.Get(session.ID(p.Liveness.SessionID))
	if err != nil {
		t.Fatalf("the participant's session: %v", err)
	}
	paneID := sess.PaneID()
	if paneID == "" {
		t.Fatalf("the participant's session is the pipe of no pane")
	}

	// The pane the record names is a row the LAYOUT CHAIN holds, read back
	// through that chain — the record never becomes a second source of pane
	// identity, and this is what proves the link points at something real.
	if _, paneErr := stand.db.Layout().PaneCwd(ctx, paneID); paneErr != nil {
		t.Fatalf("the pane the session names is not in the layout chain: %v", paneErr)
	}

	// The reopen: a second store over the same document, which is what a
	// restarted backend builds.
	after := workers.NewFileRestartStore(docs, "worker-restarts.json")
	records, err := after.Records(ctx)
	if err != nil {
		t.Fatalf("records after reopen: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records after reopen = %+v, want the one worker that went live", records)
	}
	rec := records[0]
	if rec.Participant != p.ID {
		t.Fatalf("record participant = %q, want %q", rec.Participant, p.ID)
	}
	if rec.PaneID != paneID {
		t.Fatalf("record pane = %q, want the pane the participant's session holds (%q)", rec.PaneID, paneID)
	}
	if rec.Group != p.Group || rec.CoordinatorSession != "sess-coordinator" {
		t.Fatalf("record lost its worker: %+v", rec)
	}

	// AND IT IS NOT REPORTED LIVE. Every record read back describes a process
	// this backend did not start, so the restore says interrupted whatever the
	// record holds.
	restored := workers.Restore(ctx, records, workers.DiskProbe{})
	if len(restored) != 1 || restored[0].State != workers.StateInterrupted {
		t.Fatalf("restorations = %+v, want one interrupted", restored)
	}
	// What the restore says about the record this stand produced is asserted
	// exactly, because it is the honest state of the product today and the
	// sentence a pane will eventually show: this coordinator had no recorded
	// directory (no renderer reports one here) and no resume identity is
	// recorded, because the launch record (nocx-dz9vj) that decides how an
	// agent continues its conversation has not been built. So the record
	// resolves and is refused — never "live", never an empty launch.
	if restored[0].Restorable() {
		t.Fatalf("a record with no launch directory and no resume identity produced a launch: %+v", restored[0].Request)
	}
	if restored[0].Failure.Reason != workers.RestoreCheckoutUnavailable {
		t.Fatalf("failure = %+v, want the missing launch directory named first", restored[0].Failure)
	}
}

// The startup pass: a record whose pane is gone from the window is dropped, one
// whose pane is still open is kept and reported, and one whose checkout is not
// there any more is reported as the failure it is rather than as a worker.
func TestTheStartupRestoreDropsClosedPanesAndReportsWhatItCannotResume(t *testing.T) {
	ctx := context.Background()
	lg := log.NewSlogAdapter(nil)

	db := openLayoutStore(t)
	layoutRepo := db.Layout()
	docs := storage.NewDocumentStore(t.TempDir())
	restarts := workers.NewFileRestartStore(docs, "worker-restarts.json")

	// A window holding one pane, which is the record that must survive.
	if _, err := layoutRepo.CreateWorkspace(ctx,
		content.Workspace{ID: "ws-open", Name: "open", Position: 1},
		content.Tab{ID: "tab-open", Layout: content.LayoutRow},
		content.Pane{ID: "pane-open", TabID: "tab-open", Cwd: t.TempDir(), Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("create the open workspace: %v", err)
	}

	openDir := t.TempDir()
	goneDir := filepath.Join(t.TempDir(), "removed")
	records := []workers.RestartRecord{
		{
			Participant: "p-open", Group: "worker-1", PaneID: "pane-open", TabID: "tab-open",
			Agent: "claude", Cwd: openDir, Resume: workers.ResumeIdentity{Mode: workers.ResumeByID, ID: "conv-1"},
		},
		{
			// The pane was closed while nocx was down: nothing to reopen, so
			// the record is a note about nothing and goes.
			Participant: "p-closed", Group: "worker-1", PaneID: "pane-closed", TabID: "tab-closed",
			Agent: "claude", Cwd: openDir, Resume: workers.ResumeIdentity{Mode: workers.ResumeByID, ID: "conv-2"},
		},
		{
			// The pane is open and the checkout is not: an explicit failure.
			Participant: "p-nocheckout", Group: "worker-1", PaneID: "pane-open", TabID: "tab-open",
			Agent: "claude", Cwd: goneDir, Resume: workers.ResumeIdentity{Mode: workers.ResumeByID, ID: "conv-3"},
		},
	}
	for _, rec := range records {
		if err := restarts.Record(ctx, rec); err != nil {
			t.Fatalf("record %s: %v", rec.Participant, err)
		}
	}

	// The RECORD's probe, not the shipped disk one (nocx-t5e7d): the restore
	// asks the agent record which agents nocx knows and what each of them can
	// resume, and this pass is its first reader.
	agentRecords, err := agentrecord.New(t.TempDir())
	if err != nil {
		t.Fatalf("agentrecord.New: %v", err)
	}
	got := restoreWorkerRecords(ctx, lg, restarts, layoutRepo, agentProbe{store: agentRecords})
	if len(got) != 2 {
		t.Fatalf("restorations = %+v, want the two records whose panes are still open", got)
	}
	byParticipant := map[workers.ParticipantID]workers.Restoration{}
	for _, x := range got {
		byParticipant[x.Record.Participant] = x
	}
	live, ok := byParticipant["p-open"]
	if !ok || !live.Restorable() {
		t.Fatalf("the one complete record = %+v, want a launch request", live)
	}
	if live.Request.PaneID != "pane-open" || live.Request.Cwd != openDir ||
		live.Request.Resume.ID != "conv-1" || live.Request.Agent != "claude" {
		t.Fatalf("the reconstructed launch lost the tuple: %+v", live.Request)
	}
	missing, ok := byParticipant["p-nocheckout"]
	if !ok {
		t.Fatalf("a record with no checkout was dropped instead of reported: %+v", got)
	}
	if missing.Failure == nil || missing.Failure.Reason != workers.RestoreCheckoutUnavailable {
		t.Fatalf("a record whose checkout is gone = %+v, want a checkout-unavailable failure", missing.Failure)
	}

	// And the closed pane's record is GONE from the document, not merely
	// unreported: a restore that skipped it every time would leave it there
	// for ever.
	kept, err := restarts.Records(ctx)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	for _, rec := range kept {
		if rec.Participant == "p-closed" {
			t.Fatalf("the record of a closed pane survived the pass: %+v", rec)
		}
	}
	if len(kept) != 2 {
		t.Fatalf("records after the pass = %+v, want the two open panes' records", kept)
	}
}

// Acceptance 3: the layout restore is unchanged by any of this. Every stored
// tab comes back ONCE, in its prior workspace and order — and it comes back
// from the chain, because the restart record is a note about a pane and never a
// second place tabs are created.
func TestARestartReopensEachStoredTabOnceInItsPriorWorkspaceAndOrder(t *testing.T) {
	storagetest.Isolate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const (
		workspace  = "00000000-0000-7000-8000-00000000c001"
		firstTab   = "00000000-0000-7000-8000-00000000c002"
		firstPane  = "00000000-0000-7000-8000-00000000c003"
		secondTab  = "00000000-0000-7000-8000-00000000c004"
		secondPane = "00000000-0000-7000-8000-00000000c005"
	)

	// The session before the restart: one workspace, two tabs in an order that
	// is stored rather than incidental.
	a1, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := a1.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}
	conn1 := dialAppWS(t, a1)
	createWorkspaceOverWS(t, conn1, 1, workspace, firstTab, firstPane, "work")
	createTabOverWS(t, conn1, 2, workspace, secondTab, secondPane)
	if got := tabsInWindow(t, conn1, 3); len(got) != 2 || got[0] != firstTab || got[1] != secondTab {
		t.Fatalf("tabs in the first session = %v, want [%s %s]", got, firstTab, secondTab)
	}
	_ = conn1.Close()
	a1.Shutdown(ctx)

	// The restart, against the same profile.
	a2, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New after the restart: %v", err)
	}
	if startErr := a2.Start(ctx); startErr != nil {
		t.Fatalf("Start after the restart: %v", startErr)
	}
	defer a2.Shutdown(ctx)
	conn2 := dialAppWS(t, a2)
	defer func() { _ = conn2.Close() }()
	got := tabsInWindow(t, conn2, 4)
	if len(got) != 2 || got[0] != firstTab || got[1] != secondTab {
		t.Fatalf("tabs after the restart = %v, want each stored tab once in its prior order [%s %s]",
			got, firstTab, secondTab)
	}
}

// The AGENT RECORD is what decides whether a persisted worker's agent can be
// launched (nocx-t5e7d). This is the seam nocx-xn63t.5.1 left and said so in
// its own words — "an agent nocx never saw" was a question that could not be
// answered until a record of which agents exist was anybody's fact — so the
// proof is that the pass asks it and acts on the answer: an agent nocx has no
// record of is a named restore failure, and an agent whose record declares the
// resume shape its pane recorded comes back as a launch request.
func TestTheAgentRecordDecidesWhetherAPersistedWorkerCanBeLaunched(t *testing.T) {
	ctx := context.Background()
	lg := log.NewSlogAdapter(nil)

	db := openLayoutStore(t)
	layoutRepo := db.Layout()
	open := map[string]string{}
	for _, id := range []string{"pane-shipped", "pane-unknown"} {
		dir := t.TempDir()
		open[id] = dir
		if _, err := layoutRepo.CreateWorkspace(ctx,
			content.Workspace{ID: "ws-" + id, Name: id, Position: 1},
			content.Tab{ID: "tab-" + id, Layout: content.LayoutRow},
			content.Pane{ID: id, TabID: "tab-" + id, Cwd: dir, Kind: content.PaneLocal, SizeShare: 1},
		); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	docs := storage.NewDocumentStore(t.TempDir())
	restarts := workers.NewFileRestartStore(docs, "worker-restarts.json")
	for _, rec := range []workers.RestartRecord{
		{
			Participant: "p-shipped", Group: "worker-1", PaneID: "pane-shipped", TabID: "tab-shipped",
			Agent: "claude", Cwd: open["pane-shipped"],
			Resume: workers.ResumeIdentity{Mode: workers.ResumeByID, ID: "conv-1"},
		},
		{
			Participant: "p-unknown", Group: "worker-1", PaneID: "pane-unknown", TabID: "tab-unknown",
			// A name nocx has no record of — an agent somebody typed, or one
			// whose record this install has never had. Both are the same
			// answer, and neither may become a launch.
			Agent: "not-an-agent", Cwd: open["pane-unknown"],
			Resume: workers.ResumeIdentity{Mode: workers.ResumeByID, ID: "conv-2"},
		},
	} {
		if err := restarts.Record(ctx, rec); err != nil {
			t.Fatalf("record %s: %v", rec.Participant, err)
		}
	}

	agentRecords, err := agentrecord.New(t.TempDir())
	if err != nil {
		t.Fatalf("agentrecord.New: %v", err)
	}
	got := restoreWorkerRecords(ctx, lg, restarts, layoutRepo, agentProbe{store: agentRecords})
	if len(got) != 2 {
		t.Fatalf("restorations = %+v, want both records whose panes are still open", got)
	}
	byParticipant := map[workers.ParticipantID]workers.Restoration{}
	for _, x := range got {
		byParticipant[x.Record.Participant] = x
	}

	// The agent this build ships, resumed under the shape its record
	// declares: a launch request, which is the half that must still work.
	shipped, ok := byParticipant["p-shipped"]
	if !ok || !shipped.Restorable() {
		t.Fatalf("the shipped agent's record = %+v, want a launch request", shipped)
	}
	if shipped.Request.Agent != "claude" || shipped.Request.Resume.ID != "conv-1" {
		t.Fatalf("the reconstructed launch = %+v, want the recorded agent and identity", shipped.Request)
	}

	// An agent nocx has no record of: an explicit failure naming it, never an
	// empty shell that claims the pane came back.
	unknown, ok := byParticipant["p-unknown"]
	if !ok {
		t.Fatalf("the unknown agent's record vanished instead of being reported")
	}
	if unknown.Restorable() {
		t.Fatalf("an agent nocx has no record of produced a launch: %+v", unknown.Request)
	}
	if unknown.Failure.Reason != workers.RestoreResumeUnavailable {
		t.Fatalf("failure = %+v, want the resume question named", unknown.Failure)
	}
	if !strings.Contains(unknown.Failure.Detail, "not-an-agent") {
		t.Fatalf("detail = %q, want the agent named so a person can act on it", unknown.Failure.Detail)
	}
}
