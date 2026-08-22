package transport

// layout.read and panes.close on the wire (nocx-isoph.4, design §4.1 and
// §4.4) — the two methods nocx-isoph.2 left the renderer without, and the
// reason it named them the biggest gap it left.
//
// layout.read is what makes the epic's own sentence true: order, activation
// and decoration come from the backend. Without a read, a renderer has to
// remember what it asked for, and what it remembers it owns — so every test
// here asserts against a SECOND connection wherever it can, because that is
// the renderer reloading with the backend still up, which is the epic's
// headline assertion.
//
// panes.close is DeletePane's wire caller, and it exists under that name
// because the renderer's word for it was already taken: pane.close was the
// capture-scoping notification, which is a different act. The notification is
// now secrets.paneClosed, in the domain that already owns a pending capture,
// and the destructive act on the durable object is here.

import (
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/workspace"
)

// The shapes these tests decode into. They name every field the wire
// declares, deliberately: the contract tests own "and nothing else", and
// these own "and it says what the store holds".
type readWire struct {
	DefaultWorkspaceID string `json:"defaultWorkspaceId"`
	Workspaces         []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Position int    `json:"position"`
	} `json:"workspaces"`
	Tabs []struct {
		ID          string  `json:"id"`
		WorkspaceID string  `json:"workspaceId"`
		Name        *string `json:"name"`
		Colour      *string `json:"colour"`
		Position    int     `json:"position"`
		Pinned      bool    `json:"pinned"`
		Layout      string  `json:"layout"`
	} `json:"tabs"`
	Panes []struct {
		ID    string `json:"id"`
		TabID string `json:"tabId"`
		Cwd   string `json:"cwd"`
		Kind  string `json:"kind"`
	} `json:"panes"`
}

func readLayout(t *testing.T, conn *websocket.Conn, id int) readWire {
	t.Helper()
	var out readWire
	if err := json.Unmarshal(mustLayoutCall(t, conn, "layout.read", map[string]any{}, id), &out); err != nil {
		t.Fatalf("decode layout.read: %v", err)
	}
	return out
}

// The whole chain, read back through one call.
func TestLayoutReadAnswersTheWholeChain(t *testing.T) {
	ws, _ := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)
	mustLayoutCall(t, conn, "tabs.create", map[string]any{
		"id": tabID2, "workspaceId": wsID1, "name": "deploy", "colour": "#ff8800",
		"position": 1, "pinned": true, "layout": "column",
		"firstPane": firstPane(paneID2, "/srv"),
	}, 2)

	got := readLayout(t, conn, 3)
	if len(got.Workspaces) != 1 || got.Workspaces[0].ID != wsID1 {
		t.Fatalf("workspaces = %+v, want exactly the seeded one", got.Workspaces)
	}
	if len(got.Tabs) != 2 || got.Tabs[0].ID != tabID1 || got.Tabs[1].ID != tabID2 {
		t.Fatalf("tabs = %+v, want %s then %s", got.Tabs, tabID1, tabID2)
	}
	deploy := got.Tabs[1]
	if deploy.Name == nil || *deploy.Name != "deploy" || deploy.Colour == nil || *deploy.Colour != "#ff8800" ||
		!deploy.Pinned || deploy.Layout != "column" || deploy.WorkspaceID != wsID1 {
		t.Fatalf("the decorated tab read back as %+v", deploy)
	}
	// The tab nobody named comes back with null, not with an empty string:
	// "no name" is a real state, and it is the normal one (§4.5).
	if got.Tabs[0].Name != nil || got.Tabs[0].Colour != nil {
		t.Fatalf("an undecorated tab read back as %+v, want nulls", got.Tabs[0])
	}
	if len(got.Panes) != 2 {
		t.Fatalf("panes = %+v, want two", got.Panes)
	}
	if got.Panes[0].TabID != tabID1 || got.Panes[0].Cwd != "/repos/nocx" || got.Panes[0].Kind != "local" {
		t.Fatalf("the first pane read back as %+v", got.Panes[0])
	}
	if got.DefaultWorkspaceID != string(workspace.Default) {
		t.Fatalf("defaultWorkspaceId = %q, want the backend's own %q", got.DefaultWorkspaceID, workspace.Default)
	}
}

// THE EPIC'S HEADLINE, at this seam: decorate on one connection, drop it, and
// find the decoration from a NEW one. A renderer reload is exactly this — the
// backend never restarted, and the colour, the name, the pinning and the
// order come back because they were never in the renderer.
func TestLayoutReadSurvivesTheRendererGoingAway(t *testing.T) {
	ws, _ := newLayoutWSServer(t)
	first := connectWS(t, ws)
	seedWire(t, first)
	mustLayoutCall(t, first, "tabs.create", map[string]any{
		"id": tabID2, "workspaceId": wsID1, "position": 1, "layout": "row",
		"firstPane": firstPane(paneID2, "/srv"),
	}, 2)
	mustLayoutCall(t, first, "tabs.recolour", map[string]any{"id": tabID2, "colour": "#00aaff"}, 3)
	mustLayoutCall(t, first, "tabs.rename", map[string]any{"id": tabID2, "name": "release"}, 4)
	mustLayoutCall(t, first, "tabs.pin", map[string]any{"id": tabID2, "pinned": true}, 5)
	mustLayoutCall(t, first, "tabs.reorder",
		map[string]any{"workspaceId": wsID1, "ids": []string{tabID2, tabID1}}, 6)
	_ = first.Close()

	again := connectWS(t, ws)
	got := readLayout(t, again, 1)
	if len(got.Tabs) != 2 {
		t.Fatalf("tabs after the reload = %+v, want two", got.Tabs)
	}
	// All four survive: the order, the colour, the name and the pinning.
	if got.Tabs[0].ID != tabID2 || got.Tabs[1].ID != tabID1 {
		t.Fatalf("order after the reload = %s, %s; want the reordered %s first", got.Tabs[0].ID, got.Tabs[1].ID, tabID2)
	}
	head := got.Tabs[0]
	if head.Colour == nil || *head.Colour != "#00aaff" {
		t.Fatalf("colour after the reload = %+v", head.Colour)
	}
	if head.Name == nil || *head.Name != "release" {
		t.Fatalf("name after the reload = %+v", head.Name)
	}
	if !head.Pinned {
		t.Fatal("pinning did not survive the reload")
	}
	if head.Position != 0 || got.Tabs[1].Position != 1 {
		t.Fatalf("positions after the reload = %d, %d; want 0, 1", head.Position, got.Tabs[1].Position)
	}
}

// A fresh profile: no rows anywhere. The answer is a snapshot of nothing, and
// its collections are [] rather than null — the renderer's first .map assumes
// it, and this is the nocx-25k9.14 class of defect that only shows off the
// socket.
func TestLayoutReadOnAFreshProfileAnswersEmptyCollections(t *testing.T) {
	ws, _ := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	raw := mustLayoutCall(t, conn, "layout.read", map[string]any{}, 1)
	var probe struct {
		Workspaces *[]json.RawMessage `json:"workspaces"`
		Tabs       *[]json.RawMessage `json:"tabs"`
		Panes      *[]json.RawMessage `json:"panes"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if probe.Workspaces == nil || probe.Tabs == nil || probe.Panes == nil {
		t.Fatalf("an empty layout answered null collections: %s", raw)
	}
	if len(*probe.Workspaces)+len(*probe.Tabs)+len(*probe.Panes) != 0 {
		t.Fatalf("a fresh profile answered %s, want nothing", raw)
	}
}

// The renderer's first tab goes into the DEFAULT workspace, whose id it
// learns from the read rather than from a constant of its own. That id names
// no row on a fresh profile, and the create must work anyway — otherwise the
// renderer is refused with nowhere to go, since workspaces.create takes only
// a UUIDv7 and the default's id is not one.
func TestLayoutCreateTabInTheDefaultWorkspaceOverTheWire(t *testing.T) {
	ws, _ := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	def := readLayout(t, conn, 1).DefaultWorkspaceID

	mustLayoutCall(t, conn, "tabs.create", map[string]any{
		"id": tabID1, "workspaceId": def, "position": 0, "layout": "row",
		"firstPane": firstPane(paneID1, "/repos/nocx"),
	}, 2)

	got := readLayout(t, conn, 3)
	if len(got.Tabs) != 1 || got.Tabs[0].WorkspaceID != def {
		t.Fatalf("tabs = %+v, want one in the default workspace", got.Tabs)
	}
	if len(got.Workspaces) != 1 || got.Workspaces[0].ID != def {
		t.Fatalf("workspaces = %+v, want the default minted on demand", got.Workspaces)
	}
}

// A pane may be opened with an EMPTY cwd, and it means what replacement.cwd
// has always meant: the backend process's own directory, which is what an
// unconfigured local shell inherits. The renderer does not know where a shell
// will land at the moment it opens the tab — the cwd arrives from the session
// a round trip later — so requiring one here would only buy a path the
// renderer invented.
func TestLayoutCreateAcceptsAPaneWithNoCwdYet(t *testing.T) {
	ws, _ := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	def := readLayout(t, conn, 1).DefaultWorkspaceID

	mustLayoutCall(t, conn, "tabs.create", map[string]any{
		"id": tabID1, "workspaceId": def, "position": 0, "layout": "row",
		"firstPane": map[string]any{"id": paneID1, "cwd": "", "kind": "local", "sizeShare": 1},
	}, 2)

	got := readLayout(t, conn, 3)
	if len(got.Panes) != 1 || got.Panes[0].Cwd != "" {
		t.Fatalf("panes = %+v, want one with no cwd", got.Panes)
	}
}

// ── panes.close ──────────────────────────────────────────────────────────

// The split's other direction: a second pane in a tab, removed. The tab stays
// because it still holds one.
func TestPanesCloseRemovesOnePaneAndLeavesItsTab(t *testing.T) {
	ws, _ := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)
	mustLayoutCall(t, conn, "panes.create", map[string]any{
		"id": paneID2, "tabId": tabID1, "cwd": "/srv", "kind": "local",
		"sizeShare": 0.5,
	}, 2)

	var closed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(mustLayoutCall(t, conn, "panes.close",
		map[string]any{"id": paneID2, "replacement": aReplacement()}, 3), &closed); err != nil {
		t.Fatalf("decode panes.close: %v", err)
	}
	if closed.ID != paneID2 {
		t.Fatalf("panes.close answered %q, want the closed pane %q", closed.ID, paneID2)
	}

	got := readLayout(t, conn, 4)
	if len(got.Panes) != 1 || got.Panes[0].ID != paneID1 {
		t.Fatalf("panes = %+v, want only %s", got.Panes, paneID1)
	}
	if len(got.Tabs) != 1 || got.Tabs[0].ID != tabID1 {
		t.Fatalf("tabs = %+v, want the tab to survive its second pane", got.Tabs)
	}
}

// The last pane in the application: the tab goes with it, its workspace goes
// with the tab, and the replacement the params carried is minted — all in one
// transaction, and all of it visible to the read that follows. This is the
// answer to "a close cannot even tell the renderer whether the replacement
// was minted": it asks.
func TestPanesCloseOfTheLastPaneMintsTheReplacement(t *testing.T) {
	ws, _ := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)

	mustLayoutCall(t, conn, "panes.close", map[string]any{"id": paneID1, "replacement": aReplacement()}, 2)

	got := readLayout(t, conn, 3)
	if len(got.Tabs) != 1 || got.Tabs[0].ID != tabID3 {
		t.Fatalf("tabs = %+v, want the replacement %s", got.Tabs, tabID3)
	}
	if got.Tabs[0].WorkspaceID != got.DefaultWorkspaceID {
		t.Fatalf("the replacement landed in %q, want the default workspace", got.Tabs[0].WorkspaceID)
	}
	if len(got.Panes) != 1 || got.Panes[0].ID != paneID3 {
		t.Fatalf("panes = %+v, want the replacement's %s", got.Panes, paneID3)
	}
	// The workspace the closed tab was in went with it: a container exists
	// only while it holds a member (nocx-isoph.3).
	for _, w := range got.Workspaces {
		if w.ID == wsID1 {
			t.Fatalf("workspace %s outlived its last tab", wsID1)
		}
	}
}

// A close that would empty the application and names no replacement is
// refused WHOLE, and the refusal is invalid-params rather than a server
// fault: it is something the renderer can fix by sending one.
func TestPanesCloseWithNoReplacementRemovesNothing(t *testing.T) {
	ws, _ := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)

	_, rpcErr := layoutCall(t, conn, "panes.close", map[string]any{"id": paneID1}, 2)
	if rpcErr == nil {
		t.Fatal("a close that would leave no tab at all was accepted with no replacement")
	}
	if rpcErr.Code != -32602 {
		t.Fatalf("refusal code = %d, want -32602", rpcErr.Code)
	}
	got := readLayout(t, conn, 3)
	if len(got.Panes) != 1 || len(got.Tabs) != 1 {
		t.Fatalf("the refused close removed something: %+v", got)
	}
}

// The id is a shape and the shape is checked, here as everywhere else in this
// family (§7): a v4 is refused before the handler runs, which is the whole
// reason the renderer needed a v7 minter of its own.
func TestPanesCloseRefusesAnIDThatIsNotAUUIDv7(t *testing.T) {
	ws, _ := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)

	_, rpcErr := layoutCall(t, conn, "panes.close",
		map[string]any{"id": "3f2504e0-4f89-41d3-9a0c-0305e82c3301"}, 2)
	if rpcErr == nil {
		t.Fatal("panes.close accepted a UUIDv4")
	}
	if rpcErr.Code != -32602 {
		t.Fatalf("refusal code = %d, want -32602", rpcErr.Code)
	}
}
