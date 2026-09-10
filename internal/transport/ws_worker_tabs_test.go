package transport

// workers.tabCreated — the notification a connected renderer learns a
// participant's tab from (nocx-ui8q6.3).
//
// The over-the-wire test drives the exact production shape: a session opened
// through ws.OpenSession, precisely as workers.go's Spawn opens a
// participant's, which is what makes replayFrom 0 and attached false in that
// test the REAL answer rather than a stand-in for it — that session has no
// receiver at all, by session_open.go's own design.

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

func TestWorkerTabCreated_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "workers.tabCreated.schema.json")
	name := "codex-1"
	tab := wireTab(content.Tab{ID: tabID1, WorkspaceID: wsID1, Name: &name, Layout: content.LayoutRow})
	pane := wirePane(content.Pane{ID: paneID1, TabID: tabID1, Kind: content.PaneLocal, SizeShare: 1})

	cases := map[string]workerTabCreatedParams{
		// The ordinary spawn: nobody has attached yet.
		"fresh, nobody attached": {
			Tab: tab, FirstPane: pane,
			SessionID: "sess-1", InstanceID: "inst-1", SessionEpoch: 1,
			ReplayFrom: 0, Attached: false,
		},
		// A pane whose ring has already produced bytes and been attached to
		// by the time the notification is built — both must still validate,
		// since nothing in the schema may special-case the ordinary answer.
		"produced output and attached": {
			Tab: tab, FirstPane: pane,
			SessionID: "sess-2", InstanceID: "inst-1", SessionEpoch: 1,
			ReplayFrom: 4096, Attached: true,
		},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			validateJSON(t, schema, mustMarshal(params), "workers.tabCreated DTO")
		})
	}
}

func newWorkerTabAnnounceWS(t *testing.T) *WSServer {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return ws
}

// TestWorkerTabCreated_OverTheWireConformsToContract drives the real path: a
// session opened the way workers.go opens a participant's (ws.OpenSession,
// never `open`), so it has no receiver — and the notification it produces is
// read off a SEPARATE, ordinary renderer connection, because a worker's own
// session is never the audience (it has no subscriber to be one).
func TestWorkerTabCreated_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "workers.tabCreated.schema.json")
	ws := newWorkerTabAnnounceWS(t)
	renderer := connectWS(t, ws)
	t.Cleanup(func() { _ = renderer.Close() })

	opened, err := ws.OpenSession(t.Context(), OpenSpec{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	tab := content.Tab{ID: tabID1, WorkspaceID: wsID1, Layout: content.LayoutRow}
	pane := content.Pane{ID: paneID1, TabID: tabID1, Kind: content.PaneLocal, SizeShare: 1}

	ws.AnnounceWorkerTab(tab, pane, opened.Session)

	raw := readNotification(t, renderer, "workers.tabCreated", wantWithin)
	validateJSON(t, schema, raw, "workers.tabCreated params (real socket)")

	var got workerTabCreatedParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SessionID != string(opened.Session.ID()) {
		t.Errorf("sessionId = %q, want %q", got.SessionID, opened.Session.ID())
	}
	ident := opened.Session.Identity()
	if got.InstanceID != string(ident.InstanceID) {
		t.Errorf("instanceId = %q, want %q", got.InstanceID, ident.InstanceID)
	}
	if got.SessionEpoch != ident.Epoch {
		t.Errorf("sessionEpoch = %d, want %d", got.SessionEpoch, ident.Epoch)
	}
	if got.Tab.ID != tabID1 || got.FirstPane.ID != paneID1 {
		t.Errorf("tab/pane ids = %q/%q, want %q/%q", got.Tab.ID, got.FirstPane.ID, tabID1, paneID1)
	}
	// A session ws.OpenSession minted has no receiver at all — the fact this
	// bead's whole routing decision rests on. Both must read as the
	// unattached, unproduced answer, not merely as "not asserted".
	if got.ReplayFrom != 0 {
		t.Errorf("replayFrom = %d, want 0 (no receiver yet)", got.ReplayFrom)
	}
	if got.Attached {
		t.Error("attached = true, want false (no receiver yet)")
	}
}

// The notification is a BROADCAST — every connected window learns of a new
// participant tab, unlike lifecycle.changed or session.focus which resolve
// one session's own subscriber. Two renderer connections must both receive
// it from one spawn.
func TestWorkerTabCreated_BroadcastsToEveryConnection(t *testing.T) {
	ws := newWorkerTabAnnounceWS(t)
	first := connectWS(t, ws)
	second := connectWS(t, ws)
	t.Cleanup(func() { _ = first.Close() })
	t.Cleanup(func() { _ = second.Close() })

	opened, err := ws.OpenSession(t.Context(), OpenSpec{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	tab := content.Tab{ID: tabID1, WorkspaceID: wsID1, Layout: content.LayoutRow}
	pane := content.Pane{ID: paneID1, TabID: tabID1, Kind: content.PaneLocal, SizeShare: 1}

	ws.AnnounceWorkerTab(tab, pane, opened.Session)

	firstRaw := readNotification(t, first, "workers.tabCreated", wantWithin)
	secondRaw := readNotification(t, second, "workers.tabCreated", wantWithin)
	var a, b workerTabCreatedParams
	if err := json.Unmarshal(firstRaw, &a); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if err := json.Unmarshal(secondRaw, &b); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if a.Tab.ID != tabID1 || b.Tab.ID != tabID1 {
		t.Fatalf("both connections must learn of the same tab, got %q and %q", a.Tab.ID, b.Tab.ID)
	}
}

// With no connection at all the spawn's own success must not be threatened —
// AnnounceWorkerTab must simply return, having logged the drop rather than
// panicked or blocked.
func TestWorkerTabCreated_WithNoConnectionDropsSilently(t *testing.T) {
	ws := newWorkerTabAnnounceWS(t)
	opened, err := ws.OpenSession(t.Context(), OpenSpec{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	tab := content.Tab{ID: tabID1, WorkspaceID: wsID1, Layout: content.LayoutRow}
	pane := content.Pane{ID: paneID1, TabID: tabID1, Kind: content.PaneLocal, SizeShare: 1}

	// Must not panic, hang or return anything for the caller to check —
	// exactly the "best effort" contract FocusSession and PublishLifecycle
	// already document.
	ws.AnnounceWorkerTab(tab, pane, opened.Session)
}
