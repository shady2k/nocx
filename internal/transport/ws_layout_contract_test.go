package transport

// The layout methods' three contract checks (contracts/README.md,
// nocx-isoph.2). They live in their own file rather than in
// ws_contract_test.go's table because the domain arrived whole — twelve
// methods and three object shapes in one commit — and one file per domain is
// how the next reader finds them.
//
// The third check is the one that matters: a test validating a payload the
// test itself built proves the struct is well-formed, not that the server
// sends it. Only the real result off the real socket does that, and nothing
// in the over-the-wire half names a field — the schema's
// additionalProperties:false plus its required list makes the key set exact
// in both directions, so a field this file forgot cannot pass.

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// ── the DTOs ──────────────────────────────────────────────────────────────

func TestLayoutWorkspaceDTOsConformToContract(t *testing.T) {
	created := loadSchema(t, "workspaces.create.schema.json")
	renamed := loadSchema(t, "workspaces.rename.schema.json")
	reordered := loadSchema(t, "workspaces.reorder.schema.json")
	closed := loadSchema(t, "workspaces.close.schema.json")

	ws := workspaceWire{ID: wsID1, Name: "refactor-auth", Position: 3}
	made := workspaceCreateResponse{
		Workspace: ws,
		FirstTab:  wireTab(content.Tab{ID: tabID1, WorkspaceID: wsID1, Layout: content.LayoutRow}),
		FirstPane: wirePane(content.Pane{ID: paneID1, TabID: tabID1, Cwd: "/srv", Kind: content.PaneLocal, SizeShare: 1}),
	}
	validateJSON(t, created, mustMarshal(made), "workspaces.create DTO (fresh)")
	replayed := made
	replayed.Replayed = true
	validateJSON(t, created, mustMarshal(replayed), "workspaces.create DTO (replay)")
	validateJSON(t, renamed, mustMarshal(workspaceResponse{Workspace: ws}), "workspaces.rename DTO")
	validateJSON(t, reordered, mustMarshal(workspaceListResponse{Workspaces: []workspaceWire{ws}}), "workspaces.reorder DTO")
	// The empty collection must marshal as [] and never null — wireWorkspaces
	// is what guarantees it, so the DTO case goes through it rather than
	// building the slice by hand.
	validateJSON(t, reordered, mustMarshal(workspaceListResponse{Workspaces: wireWorkspaces(nil)}), "workspaces.reorder DTO (empty)")
	validateJSON(t, closed, mustMarshal(closedResponse{ID: wsID1}), "workspaces.close DTO")
}

func TestLayoutTabDTOsConformToContract(t *testing.T) {
	created := loadSchema(t, "tabs.create.schema.json")
	renamed := loadSchema(t, "tabs.rename.schema.json")
	recoloured := loadSchema(t, "tabs.recolour.schema.json")
	pinned := loadSchema(t, "tabs.pin.schema.json")
	reordered := loadSchema(t, "tabs.reorder.schema.json")
	closed := loadSchema(t, "tabs.close.schema.json")

	name, colour, parent := "deploy", "#ff8800", tabID2
	seen := int64(1_750_000_000_000)
	// The two states that must BOTH hold: a tab nobody named or decorated —
	// the normal one, because a tab minted by a drag was never named — and
	// one carrying every optional field.
	bare := wireTab(content.Tab{ID: tabID1, WorkspaceID: wsID1, Layout: content.LayoutRow})
	full := wireTab(content.Tab{
		ID: tabID1, WorkspaceID: wsID1, ParentID: &parent, Name: &name, Colour: &colour,
		Position: 2, Pinned: true, Layout: content.LayoutColumn, SeenAt: &seen,
	})
	pane := wirePane(content.Pane{ID: paneID1, TabID: tabID1, Cwd: "/srv", Kind: content.PaneLocal, SizeShare: 1})
	for what, tab := range map[string]tabWire{"bare": bare, "full": full} {
		validateJSON(t, created, mustMarshal(tabCreateResponse{Tab: tab, FirstPane: pane}), "tabs.create DTO ("+what+")")
		validateJSON(t, renamed, mustMarshal(tabResponse{Tab: tab}), "tabs.rename DTO ("+what+")")
		validateJSON(t, recoloured, mustMarshal(tabResponse{Tab: tab}), "tabs.recolour DTO ("+what+")")
		validateJSON(t, pinned, mustMarshal(tabResponse{Tab: tab}), "tabs.pin DTO ("+what+")")
	}
	validateJSON(t, reordered, mustMarshal(tabListResponse{Tabs: []tabWire{bare, full}}), "tabs.reorder DTO")
	validateJSON(t, reordered, mustMarshal(tabListResponse{Tabs: wireTabs(nil)}), "tabs.reorder DTO (empty)")
	validateJSON(t, closed, mustMarshal(closedResponse{ID: tabID1}), "tabs.close DTO")
}

// layout.read's DTO, in the two states that must both hold: a populated
// snapshot, and the empty one a fresh profile is in — whose collections must
// marshal as [] and never null.
func TestLayoutReadDTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "layout.read.schema.json")
	name, colour := "deploy", "#ff8800"
	full := layoutReadResponse{
		DefaultWorkspaceID: "workspace:default",
		Workspaces:         wireWorkspaces([]content.Workspace{{ID: wsID1, Name: "refactor-auth"}}),
		Tabs: wireTabs([]content.Tab{
			{ID: tabID1, WorkspaceID: wsID1, Layout: content.LayoutRow},
			{ID: tabID2, WorkspaceID: wsID1, Name: &name, Colour: &colour, Position: 1, Pinned: true, Layout: content.LayoutColumn},
		}),
		Panes: wirePanes([]content.Pane{{ID: paneID1, TabID: tabID1, Cwd: "/repos/nocx", Kind: content.PaneLocal, SizeShare: 1}}, map[string]struct{}{paneID1: {}}),
	}
	validateJSON(t, schema, mustMarshal(full), "layout.read DTO (populated)")
	empty := layoutReadResponse{
		DefaultWorkspaceID: "workspace:default",
		Workspaces:         wireWorkspaces(nil),
		Tabs:               wireTabs(nil),
		Panes:              wirePanes(nil, nil),
	}
	validateJSON(t, schema, mustMarshal(empty), "layout.read DTO (empty)")
	if got := string(mustMarshal(empty)); got != `{"defaultWorkspaceId":"workspace:default","workspaces":[],"tabs":[],"panes":[]}` {
		t.Fatalf("an empty snapshot marshals as %s, want [] for every collection", got)
	}
}

func TestLayoutPaneDTOsConformToContract(t *testing.T) {
	created := loadSchema(t, "panes.create.schema.json")
	moved := loadSchema(t, "panes.move.schema.json")
	validateJSON(t, loadSchema(t, "panes.close.schema.json"),
		mustMarshal(closedResponse{ID: paneID1}), "panes.close DTO")

	endpoint := "deploy@srv-01:22"
	local := wirePane(content.Pane{ID: paneID1, TabID: tabID1, Cwd: "/repos/nocx", Kind: content.PaneLocal, SizeShare: 1})
	remote := wirePane(content.Pane{ID: paneID2, TabID: tabID1, Cwd: "/srv", Kind: content.PaneSSH, Endpoint: &endpoint, SizeShare: 0.5})
	for what, pane := range map[string]paneWire{"local": local, "ssh": remote} {
		validateJSON(t, created, mustMarshal(paneCreateResponse{Pane: pane}), "panes.create DTO ("+what+")")
		validateJSON(t, moved, mustMarshal(paneResponse{Pane: pane}), "panes.move DTO ("+what+")")
	}
}

// A DTO the schema must REFUSE, so the check is known to be able to fail: a
// local pane whose endpoint is the empty string rather than null. The empty
// string is a real value meaning the local machine, which is exactly the
// ambiguity the nullable type removes — and the validator refuses it on the
// way in for the same reason.
func TestLayoutContractRefusesAnUnstatedShape(t *testing.T) {
	schema := loadSchema(t, "panes.create.schema.json")
	var payload map[string]any
	if err := json.Unmarshal(mustMarshal(paneCreateResponse{
		Pane: wirePane(content.Pane{ID: paneID1, TabID: tabID1, Cwd: "/", Kind: content.PaneLocal, SizeShare: 1}),
	}), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	pane, _ := payload["pane"].(map[string]any)
	pane["kind"] = "container"
	raw, _ := json.Marshal(payload)
	if err := validateJSONErr(schema, raw); err == nil {
		t.Fatal("the schema accepted kind=container — a pane is local or ssh (§5), and a contract that accepts anything is theatre")
	}
}

// ── the real results, off the real socket ────────────────────────────────

// Every layout method, driven end to end, with each result validated against
// its own schema. The order is the order a renderer would use, because the
// later calls need the rows the earlier ones made — which is also what makes
// this one test rather than twelve.
func TestLayoutOverTheWireConformsToContract(t *testing.T) {
	ws, _ := newLayoutWSServer(t)
	conn := connectWS(t, ws)

	create := map[string]any{
		"id": wsID1, "name": "refactor-auth", "position": 0,
		"firstTab": firstTab(tabID1), "firstPane": firstPane(paneID1, "/repos/nocx"),
	}
	validateJSON(t, loadSchema(t, "workspaces.create.schema.json"),
		mustLayoutCall(t, conn, "workspaces.create", create, 1),
		"workspaces.create result")
	// The replay, off the socket: the same shape, and the field that says so.
	validateJSON(t, loadSchema(t, "workspaces.create.schema.json"),
		mustLayoutCall(t, conn, "workspaces.create", create, 2),
		"workspaces.create result (replay)")
	validateJSON(t, loadSchema(t, "workspaces.create.schema.json"),
		mustLayoutCall(t, conn, "workspaces.create", map[string]any{
			"id": wsID2, "name": "ansible", "position": 1,
			"firstTab": firstTab(tabID2), "firstPane": firstPane(paneID2, "/srv"),
		}, 3),
		"workspaces.create result (second)")
	validateJSON(t, loadSchema(t, "workspaces.rename.schema.json"),
		mustLayoutCall(t, conn, "workspaces.rename", map[string]any{"id": wsID2, "name": "ansible-rollout"}, 4),
		"workspaces.rename result")
	validateJSON(t, loadSchema(t, "workspaces.reorder.schema.json"),
		mustLayoutCall(t, conn, "workspaces.reorder", map[string]any{"ids": []string{wsID2, wsID1}}, 5),
		"workspaces.reorder result")

	// A decorated tab whose lineage parent is the workspace's first, so
	// parentId is exercised as a real value and not only as null. The bare
	// case — no name, no colour, no parent, which is the normal one — is the
	// firstTab the workspace was created with above.
	validateJSON(t, loadSchema(t, "tabs.create.schema.json"),
		mustLayoutCall(t, conn, "tabs.create", map[string]any{
			"id": tabID3, "workspaceId": wsID1, "parentId": tabID1,
			"name": "deploy", "colour": "#ff8800", "position": 1, "pinned": true, "layout": "column",
			"firstPane": firstPane(paneID3, "/var"),
		}, 7),
		"tabs.create result (decorated)")
	validateJSON(t, loadSchema(t, "tabs.rename.schema.json"),
		mustLayoutCall(t, conn, "tabs.rename", map[string]any{"id": tabID3, "name": nil}, 8),
		"tabs.rename result (cleared)")
	validateJSON(t, loadSchema(t, "tabs.recolour.schema.json"),
		mustLayoutCall(t, conn, "tabs.recolour", map[string]any{"id": tabID1, "colour": "#00aaff"}, 9),
		"tabs.recolour result")
	validateJSON(t, loadSchema(t, "tabs.pin.schema.json"),
		mustLayoutCall(t, conn, "tabs.pin", map[string]any{"id": tabID1, "pinned": true}, 10),
		"tabs.pin result")
	validateJSON(t, loadSchema(t, "tabs.reorder.schema.json"),
		mustLayoutCall(t, conn, "tabs.reorder",
			map[string]any{"workspaceId": wsID1, "ids": []string{tabID3, tabID1}}, 11),
		"tabs.reorder result")

	// The SPLIT: a second pane into a tab that already exists. A tab's first
	// pane arrives with the tab, so this is the only shape panes.create has.
	validateJSON(t, loadSchema(t, "panes.create.schema.json"),
		mustLayoutCall(t, conn, "panes.create", map[string]any{
			"id": paneID4, "tabId": tabID1, "cwd": "/srv", "kind": "ssh",
			"endpoint": "deploy@srv-01:22", "sizeShare": 0.5,
		}, 13),
		"panes.create result (ssh)")
	validateJSON(t, loadSchema(t, "panes.move.schema.json"),
		mustLayoutCall(t, conn, "panes.move", map[string]any{"id": paneID4, "tabId": tabID3}, 14),
		"panes.move result")

	// The read, off the socket, against a chain that now has every shape in
	// it: two workspaces, a decorated tab and an undecorated one, a local
	// pane and an ssh pane. This is the check that matters most for this
	// method — a snapshot the test built would prove the struct is
	// well-formed, not that the server sends what a reloaded renderer draws
	// itself from.
	validateJSON(t, loadSchema(t, "layout.read.schema.json"),
		mustLayoutCall(t, conn, "layout.read", map[string]any{}, 15),
		"layout.read result (populated)")

	validateJSON(t, loadSchema(t, "panes.close.schema.json"),
		mustLayoutCall(t, conn, "panes.close",
			map[string]any{"id": paneID4, "replacement": aReplacement()}, 16),
		"panes.close result")
	validateJSON(t, loadSchema(t, "tabs.close.schema.json"),
		mustLayoutCall(t, conn, "tabs.close",
			map[string]any{"id": tabID3, "replacement": aReplacement()}, 17),
		"tabs.close result")
	validateJSON(t, loadSchema(t, "workspaces.close.schema.json"),
		mustLayoutCall(t, conn, "workspaces.close",
			map[string]any{"id": wsID2, "replacement": aReplacement()}, 18),
		"workspaces.close result")
}

// The reorder answers with an EMPTY collection nowhere, but the empty answer
// is still reachable and must be [] rather than null: a workspace with no
// tabs, reordered — which is refused — is not it, so the shape is driven
// where it genuinely occurs, on a tab list read back after the only member
// was closed. Asserted off the socket because a nil slice marshalling as null
// is precisely the defect that no Go-side test of the struct would see.
func TestLayoutEmptyCollectionsAreNeverNullOverTheWire(t *testing.T) {
	ws, db := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)

	empty, err := db.Layout().Tabs(t.Context(), wsID2)
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	raw := mustMarshal(tabListResponse{Tabs: wireTabs(empty)})
	if got := string(raw); got != `{"tabs":[]}` {
		t.Fatalf("an empty tab list marshals as %s, want {\"tabs\":[]}", got)
	}
	validateJSON(t, loadSchema(t, "tabs.reorder.schema.json"), raw, "tabs.reorder result (empty)")
}
