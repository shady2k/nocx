package app

// The composition-root acceptance for the clean start (nocx-l21ib.4), in the
// owner's words:
//
//	Turn "reopen tabs on startup" off and restart. The window opens on
//	nothing, and what was there is not deleted — it is marked closed. Work
//	in that clean session, turn the setting back on, restart again: the
//	tabs that come back are THAT session's, never the one before it.
//
// Before this, a clean start left the stored rows open and the renderer
// merely declined to draw them, so the next launch with the setting back on
// reopened the session BEFORE the clean one — and every clean start piled its
// leftovers on top of the pile.
//
// Three composition roots over one set of directories, because "a startup" is
// the interval this behaviour lives in and a single root cannot span it.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// The chain a session leaves behind: one workspace, its first tab, that tab's
// first pane. Ids are UUIDv7-shaped because the wire refuses anything else
// (§7 — every layout id is minted by the renderer and untrusted here).
func createWorkspaceOverWS(t *testing.T, conn *websocket.Conn, id int, wsID, tabID, paneID, name string) {
	t.Helper()
	resp := callAppWS(t, conn, "workspaces.create", map[string]any{
		"id": wsID, "name": name, "colour": nil, "position": 0,
		"firstTab": map[string]any{
			"id": tabID, "name": nil, "colour": nil, "position": 0,
			"pinned": false, "layout": "row",
		},
		"firstPane": map[string]any{
			"id": paneID, "cwd": "/srv", "kind": "local",
			"endpoint": nil, "sizeShare": 1,
		},
	}, id)
	if resp.Error != nil {
		t.Fatalf("workspaces.create %s: %+v", wsID, resp.Error)
	}
}

// tabsInWindow is layout.read reduced to the one question this test asks:
// which tabs does the window open on?
func tabsInWindow(t *testing.T, conn *websocket.Conn, id int) []string {
	t.Helper()
	resp := callAppWS(t, conn, "layout.read", map[string]any{}, id)
	if resp.Error != nil {
		t.Fatalf("layout.read: %+v", resp.Error)
	}
	var read struct {
		Tabs []struct {
			ID string `json:"id"`
		} `json:"tabs"`
	}
	if err := json.Unmarshal(resp.Result, &read); err != nil {
		t.Fatalf("decode layout.read: %v (raw %s)", err, resp.Result)
	}
	out := make([]string, 0, len(read.Tabs))
	for _, tab := range read.Tabs {
		out = append(out, tab.ID)
	}
	return out
}

func setRestoreOnStartup(t *testing.T, conn *websocket.Conn, id int, on bool) {
	t.Helper()
	resp := callAppWS(t, conn, "settings.set", map[string]any{
		"key": "restore.onStartup", "value": on,
	}, id)
	if resp.Error != nil {
		t.Fatalf("settings.set restore.onStartup=%v: %+v", on, resp.Error)
	}
}

func TestCleanStart_ClosesTheLeftoversAndReopensOnlyTheLastSession(t *testing.T) {
	storagetest.Isolate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const (
		oldWorkspace = "00000000-0000-7000-8000-00000000a001"
		oldTab       = "00000000-0000-7000-8000-00000000a002"
		oldPane      = "00000000-0000-7000-8000-00000000a003"
		newWorkspace = "00000000-0000-7000-8000-00000000b001"
		newTab       = "00000000-0000-7000-8000-00000000b002"
		newPane      = "00000000-0000-7000-8000-00000000b003"
	)

	// ── the session before the clean start ────────────────────────────────
	a1, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := a1.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}
	conn1 := dialAppWS(t, a1)
	createWorkspaceOverWS(t, conn1, 1, oldWorkspace, oldTab, oldPane, "before")
	if got := tabsInWindow(t, conn1, 2); len(got) != 1 || got[0] != oldTab {
		t.Fatalf("tabs in the first session = %v, want the one just created", got)
	}
	setRestoreOnStartup(t, conn1, 3, false)
	_ = conn1.Close()
	a1.Shutdown(ctx)

	// ── the clean start ───────────────────────────────────────────────────
	a2, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New after the clean restart: %v", err)
	}
	if startErr := a2.Start(ctx); startErr != nil {
		t.Fatalf("Start after the clean restart: %v", startErr)
	}
	conn2 := dialAppWS(t, a2)
	if got := tabsInWindow(t, conn2, 4); len(got) != 0 {
		t.Fatalf("tabs on a clean start = %v, want none — the leftovers were not marked closed", got)
	}
	// Work in the clean session, and ask for the tabs back.
	createWorkspaceOverWS(t, conn2, 5, newWorkspace, newTab, newPane, "after")
	setRestoreOnStartup(t, conn2, 6, true)
	_ = conn2.Close()
	a2.Shutdown(ctx)

	// ── restore back on: the LAST session, and only it ────────────────────
	a3, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New after turning restore back on: %v", err)
	}
	if startErr := a3.Start(ctx); startErr != nil {
		t.Fatalf("Start after turning restore back on: %v", startErr)
	}
	defer a3.Shutdown(ctx)
	conn3 := dialAppWS(t, a3)
	defer func() { _ = conn3.Close() }()
	got := tabsInWindow(t, conn3, 7)
	if len(got) != 1 || got[0] != newTab {
		t.Fatalf("tabs after restore was turned back on = %v, want the clean session's tab alone — "+
			"the session before the clean start came back", got)
	}
}
