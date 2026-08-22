package transport

// The layout chain ON THE WIRE (nocx-isoph.2, design §4.1, §4.4 and §7): the
// frontend asks the backend to create, move and destroy a workspace, a tab
// and a pane, and renders what it is told.
//
// Every test here drives the REAL method over the REAL socket against a REAL
// encrypted store, and the ones about idempotency count ROWS rather than
// reading the absence of an error — "no error" is exactly what a second
// insert would also give if the create quietly overwrote.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

// ── harness ──────────────────────────────────────────────────────────────

func newLayoutStore(t *testing.T) content.ContentDB {
	t.Helper()
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(context.Background(), content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newLayoutWSServer(t *testing.T) (*WSServer, content.ContentDB) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	db := newLayoutStore(t)
	ws := NewWSServer(logger, newRegWithStub(logger), WithContentDB(db))
	if err := ws.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })
	return ws, db
}

// Real UUIDv7s: version nibble 7, RFC 4122 variant. The shape is what the
// wire validates (§7) — never the timestamp inside it, which is guessable by
// construction and is evidence of nothing.
const (
	wsID1   = "0198f2b0-0000-7000-8000-000000000001"
	wsID2   = "0198f2b0-0000-7000-8000-000000000002"
	tabID1  = "0198f2b0-0000-7000-8000-000000000011"
	tabID2  = "0198f2b0-0000-7000-8000-000000000012"
	tabID3  = "0198f2b0-0000-7000-8000-000000000013"
	paneID1 = "0198f2b0-0000-7000-8000-000000000021"
	paneID2 = "0198f2b0-0000-7000-8000-000000000022"
	paneID3 = "0198f2b0-0000-7000-8000-000000000023"
	paneID4 = "0198f2b0-0000-7000-8000-000000000024"
)

// firstTab and firstPane are the members a create carries: creation is always
// creation-with-content (nocx-isoph.3), so there is no call that makes a
// container on its own to write a test against.
func firstTab(id string) map[string]any {
	return map[string]any{"id": id, "position": 0, "layout": "row"}
}

func firstPane(id, cwd string) map[string]any {
	return map[string]any{
		"id": id, "cwd": cwd, "kind": "local", "sizeShare": 1,
	}
}

// aReplacement is the identity of the tab that appears if a close empties the
// application. Pre-minted by the caller, because a tab id and a pane id are
// durable and therefore the frontend's (§7).
func aReplacement() map[string]any {
	return map[string]any{"tabId": tabID3, "paneId": paneID3, "cwd": "/home/user"}
}

// layoutCall sends one request and returns the raw result plus the error
// object, so a test can validate the result against its schema without the
// helper naming a single field.
func layoutCall(t *testing.T, conn *websocket.Conn, method string, params map[string]any, id int) (json.RawMessage, *jsonrpcErrorObj) {
	t.Helper()
	raw := jsonrpcCallWithID(t, conn, method, params, id)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode %s response: %v\nraw: %s", method, err, raw)
	}
	return env.Result, env.Error
}

func mustLayoutCall(t *testing.T, conn *websocket.Conn, method string, params map[string]any, id int) json.RawMessage {
	t.Helper()
	res, rpcErr := layoutCall(t, conn, method, params, id)
	if rpcErr != nil {
		t.Fatalf("%s: unexpected error %+v", method, rpcErr)
	}
	return res
}

func workspaceCount(t *testing.T, db content.ContentDB) int {
	t.Helper()
	all, err := db.Layout().Workspaces(context.Background())
	if err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	return len(all)
}

func tabCount(t *testing.T, db content.ContentDB, workspaceID string) int {
	t.Helper()
	all, err := db.Layout().Tabs(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	return len(all)
}

// seedWire creates ws-1 → tab-1 → pane-1 over the socket, the way the
// renderer will.
func seedWire(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	mustLayoutCall(t, conn, "workspaces.create", map[string]any{
		"id": wsID1, "name": "refactor-auth", "position": 0,
		"firstTab": firstTab(tabID1), "firstPane": firstPane(paneID1, "/repos/nocx"),
	}, 900)
}

// ── the retry AD-9 makes ordinary ────────────────────────────────────────

// The same create, twice, because the answer to the first was lost. The
// second must return the FIRST object and leave ONE row — counted in the
// store, because the absence of an error is exactly what an overwrite would
// also produce.
func TestLayoutCreateReplaysTheSameRequestOverTheWire(t *testing.T) {
	ws, db := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	params := map[string]any{
		"id": wsID1, "name": "refactor-auth", "position": 2,
		"firstTab": firstTab(tabID1), "firstPane": firstPane(paneID1, "/repos/nocx"),
	}

	var first, second struct {
		Workspace struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Position int    `json:"position"`
		} `json:"workspace"`
		FirstTab struct {
			ID          string `json:"id"`
			WorkspaceID string `json:"workspaceId"`
		} `json:"firstTab"`
		FirstPane struct {
			ID    string `json:"id"`
			TabID string `json:"tabId"`
		} `json:"firstPane"`
		Replayed bool `json:"replayed"`
	}
	if err := json.Unmarshal(mustLayoutCall(t, conn, "workspaces.create", params, 1), &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if err := json.Unmarshal(mustLayoutCall(t, conn, "workspaces.create", params, 2), &second); err != nil {
		t.Fatalf("decode retry: %v", err)
	}
	if first.Replayed {
		t.Fatal("the first create reported a replay")
	}
	if !second.Replayed {
		t.Fatal("the retry was not reported as a replay")
	}
	if second.Workspace != first.Workspace || second.FirstTab != first.FirstTab || second.FirstPane != first.FirstPane {
		t.Fatalf("retry returned %+v, want the first objects %+v", second, first)
	}
	// The containers the create filled in are on the wire, not left for the
	// renderer to infer.
	if first.FirstTab.WorkspaceID != wsID1 || first.FirstPane.TabID != tabID1 {
		t.Fatalf("create answered %+v, want the tab in %s and the pane in %s", first, wsID1, tabID1)
	}
	if n := workspaceCount(t, db); n != 1 {
		t.Fatalf("workspaces after a retry = %d rows, want 1", n)
	}
	if n := tabCount(t, db, wsID1); n != 1 {
		t.Fatalf("tabs after a retry = %d rows, want 1", n)
	}
}

// And the retry that matters most arrives on a NEW CONNECTION, because "the
// answer was lost" usually means the socket dropped. A create key bound to
// the connection would turn exactly the case AD-9 exists for into a conflict.
func TestLayoutCreateReplaysAcrossAReconnect(t *testing.T) {
	ws, db := newLayoutWSServer(t)
	params := map[string]any{
		"id": tabID2, "workspaceId": wsID1, "position": 1, "layout": "row",
		"firstPane": firstPane(paneID2, "/var"),
	}

	first := connectWS(t, ws)
	seedWire(t, first)
	mustLayoutCall(t, first, "tabs.create", params, 2)
	_ = first.Close()

	again := connectWS(t, ws)
	var retry struct {
		Tab      map[string]any `json:"tab"`
		Replayed bool           `json:"replayed"`
	}
	if err := json.Unmarshal(mustLayoutCall(t, again, "tabs.create", params, 3), &retry); err != nil {
		t.Fatalf("decode retry: %v", err)
	}
	if !retry.Replayed {
		t.Fatal("a retry over a new connection was not a replay — the key must not be bound to the socket")
	}
	if n := tabCount(t, db, wsID1); n != 2 {
		t.Fatalf("tabs after a reconnect retry = %d rows, want 2 (the seed and the one created once)", n)
	}
}

// An id that already belongs to a DIFFERENT object is refused, and nothing
// changes. This is §7 consequence 2's other half: a create never overwrites.
func TestLayoutCreateRefusesAnIDThatMeansSomethingElse(t *testing.T) {
	ws, db := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)

	_, rpcErr := layoutCall(t, conn, "workspaces.create", map[string]any{
		"id": wsID1, "name": "ansible-rollout", "position": 0,
		"firstTab": firstTab(tabID1), "firstPane": firstPane(paneID1, "/repos/nocx"),
	}, 2)
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("aliasing create = %+v, want -32602", rpcErr)
	}
	if n := workspaceCount(t, db); n != 1 {
		t.Fatalf("workspaces after a refused create = %d, want 1", n)
	}
	all, _ := db.Layout().Workspaces(context.Background())
	if all[0].Name != "refactor-auth" {
		t.Fatalf("stored workspace = %+v, want the original untouched", all[0])
	}
}

// A malformed id is refused BEFORE anything is written. The shape is
// validated and never believed (§7) — and the store is asserted empty, so a
// refusal that happened after a write would still fail.
func TestLayoutRefusesAMalformedIDBeforeWriting(t *testing.T) {
	ws, db := newLayoutWSServer(t)
	conn := connectWS(t, ws)

	for name, id := range map[string]string{
		"empty":             "",
		"not a uuid":        "workspace-one",
		"uuid v4":           "9f1c2f7e-6a0e-4b5e-8e2a-2f9b6c1d4e77",
		"no dashes":         "0198f2b0000070008000000000000001",
		"sql-ish":           "0198f2b0-0000-7000-8000-000000000001'; DROP TABLE workspaces;--",
		"trailing rubbish":  "0198f2b0-0000-7000-8000-000000000001x",
		"unicode lookalike": "0198f2b0-0000-7000-8000-00000000000а",
	} {
		t.Run(name, func(t *testing.T) {
			_, rpcErr := layoutCall(t, conn, "workspaces.create", map[string]any{
				"id": id, "name": "nope", "position": 0,
				"firstTab": firstTab(tabID1), "firstPane": firstPane(paneID1, "/tmp"),
			}, 1)
			if rpcErr == nil || rpcErr.Code != -32602 {
				t.Fatalf("workspaces.create with id %q = %+v, want -32602", id, rpcErr)
			}
		})
	}
	if n := workspaceCount(t, db); n != 0 {
		t.Fatalf("workspaces after %d refusals = %d, want 0 — nothing may be written before the shape is checked", 7, n)
	}
}

// ── decoration, order and the move ───────────────────────────────────────

func TestLayoutDecorationRoundTripsOverTheWire(t *testing.T) {
	ws, _ := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)

	var renamed struct {
		Tab struct {
			Name   *string `json:"name"`
			Colour *string `json:"colour"`
			Pinned bool    `json:"pinned"`
		} `json:"tab"`
	}
	if err := json.Unmarshal(mustLayoutCall(t, conn, "tabs.rename",
		map[string]any{"id": tabID1, "name": "deploy"}, 10), &renamed); err != nil {
		t.Fatalf("decode rename: %v", err)
	}
	if renamed.Tab.Name == nil || *renamed.Tab.Name != "deploy" {
		t.Fatalf("renamed tab = %+v, want deploy", renamed.Tab)
	}
	// null CLEARS the name: the tab goes back to the label derived from its
	// panes (§4.5), which is a product state and not a no-op.
	if err := json.Unmarshal(mustLayoutCall(t, conn, "tabs.rename",
		map[string]any{"id": tabID1, "name": nil}, 11), &renamed); err != nil {
		t.Fatalf("decode clear: %v", err)
	}
	if renamed.Tab.Name != nil {
		t.Fatalf("cleared name = %q, want null", *renamed.Tab.Name)
	}
	if err := json.Unmarshal(mustLayoutCall(t, conn, "tabs.recolour",
		map[string]any{"id": tabID1, "colour": "#ff8800"}, 12), &renamed); err != nil {
		t.Fatalf("decode recolour: %v", err)
	}
	if renamed.Tab.Colour == nil || *renamed.Tab.Colour != "#ff8800" {
		t.Fatalf("recoloured tab = %+v", renamed.Tab)
	}
	if err := json.Unmarshal(mustLayoutCall(t, conn, "tabs.pin",
		map[string]any{"id": tabID1, "pinned": true}, 13), &renamed); err != nil {
		t.Fatalf("decode pin: %v", err)
	}
	if !renamed.Tab.Pinned {
		t.Fatalf("pinned tab = %+v", renamed.Tab)
	}
}

func TestLayoutReorderRefusesANonPermutation(t *testing.T) {
	ws, db := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)
	mustLayoutCall(t, conn, "workspaces.create", map[string]any{
		"id": wsID2, "name": "ansible", "position": 1,
		"firstTab": firstTab(tabID2), "firstPane": firstPane(paneID2, "/srv"),
	}, 20)

	var ordered struct {
		Workspaces []struct {
			ID       string `json:"id"`
			Position int    `json:"position"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(mustLayoutCall(t, conn, "workspaces.reorder",
		map[string]any{"ids": []string{wsID2, wsID1}}, 21), &ordered); err != nil {
		t.Fatalf("decode reorder: %v", err)
	}
	// Position 1, not 0: the default workspace keeps 0 and is not a member of
	// the arrangement (nocx-h2xbu), so the user's own workspaces are written
	// after it. The ORDER is what this asserts — ws-2 ahead of ws-1 — and the
	// number is where that order starts.
	if len(ordered.Workspaces) != 2 || ordered.Workspaces[0].ID != wsID2 || ordered.Workspaces[0].Position != 1 {
		t.Fatalf("reordered = %+v, want ws-2 first at position 1", ordered.Workspaces)
	}
	_, rpcErr := layoutCall(t, conn, "workspaces.reorder", map[string]any{"ids": []string{wsID2}}, 22)
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("partial reorder = %+v, want -32602", rpcErr)
	}
	all, _ := db.Layout().Workspaces(context.Background())
	if all[0].ID != wsID2 {
		t.Fatalf("order after a refused reorder = %+v, want the accepted order unchanged", all)
	}
}

// §4.4: only a reference moves. The pane's id and cwd are the same row on the
// other side, which is what makes the round trip lossless.
func TestLayoutPaneMoveChangesOnlyTheReference(t *testing.T) {
	ws, db := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)
	mustLayoutCall(t, conn, "tabs.create", map[string]any{
		"id": tabID2, "workspaceId": wsID1, "position": 1, "layout": "row",
		"firstPane": firstPane(paneID2, "/var"),
	}, 30)

	var moved struct {
		Pane struct {
			ID    string `json:"id"`
			TabID string `json:"tabId"`
			Cwd   string `json:"cwd"`
		} `json:"pane"`
	}
	if err := json.Unmarshal(mustLayoutCall(t, conn, "panes.move",
		map[string]any{"id": paneID1, "tabId": tabID2}, 31), &moved); err != nil {
		t.Fatalf("decode move: %v", err)
	}
	if moved.Pane.ID != paneID1 || moved.Pane.Cwd != "/repos/nocx" {
		t.Fatalf("moved pane = %+v, want the same identity and cwd", moved.Pane)
	}
	if moved.Pane.TabID != tabID2 {
		t.Fatalf("moved pane tab = %q, want %q", moved.Pane.TabID, tabID2)
	}
	// The source tab held only this pane, so nocx-isoph.3's rule takes it:
	// a tab exists only while it holds a member, and the row goes in the same
	// transaction as the move.
	left, _ := db.Layout().Panes(context.Background(), tabID1)
	if len(left) != 0 {
		t.Fatalf("source tab still holds %+v", left)
	}
}

// The close cascades, and the answer names only the id: there is no object
// left to describe.
func TestLayoutCloseRemovesTheContainerAndItsMembers(t *testing.T) {
	ws, db := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)

	var closed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(mustLayoutCall(t, conn, "tabs.close",
		map[string]any{"id": tabID1, "replacement": aReplacement()}, 40), &closed); err != nil {
		t.Fatalf("decode tabs.close: %v", err)
	}
	if closed.ID != tabID1 {
		t.Fatalf("tabs.close = %q, want %q", closed.ID, tabID1)
	}
	if n := tabCount(t, db, wsID1); n != 0 {
		t.Fatalf("tabs after close = %d, want 0", n)
	}
	panes, _ := db.Layout().Panes(context.Background(), tabID1)
	if len(panes) != 0 {
		t.Fatalf("the closed tab's panes survive: %+v", panes)
	}
	// That was the application's last tab, so the replacement was minted in
	// the same transaction and it went to the DEFAULT workspace — never to
	// the one being closed, or closing a workspace would resurrect it.
	replacement, err := db.Layout().Tabs(context.Background(), content.DefaultWorkspaceID)
	if err != nil {
		t.Fatalf("Tabs(default): %v", err)
	}
	if len(replacement) != 1 || replacement[0].ID != tabID3 {
		t.Fatalf("tabs in the default workspace = %+v, want the replacement %s", replacement, tabID3)
	}
	// And the workspace went with its last tab (nocx-isoph.3), so closing it
	// by hand now names a row that is not there — which removes nothing and
	// is not an error.
	if err := json.Unmarshal(mustLayoutCall(t, conn, "workspaces.close",
		map[string]any{"id": wsID1, "replacement": aReplacement()}, 41), &closed); err != nil {
		t.Fatalf("decode workspaces.close: %v", err)
	}
	if closed.ID != wsID1 {
		t.Fatalf("workspaces.close = %q, want %q", closed.ID, wsID1)
	}
}

// The default workspace is not closed (nocx-isoph.3): it never renders, so no
// surface can offer the affordance, and its row is where the replacement tab
// goes and where the ledger records every session nobody named a workspace
// for. The wire says so as a refusal rather than by hiding the method.
func TestLayoutRefusesToCloseTheDefaultWorkspace(t *testing.T) {
	ws, _ := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	_, rpcErr := layoutCall(t, conn, "workspaces.close",
		map[string]any{"id": content.DefaultWorkspaceID, "replacement": aReplacement()}, 1)
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("closing the default workspace = %+v, want -32602", rpcErr)
	}
}

// A close that would empty the application and named no replacement is
// refused WHOLE: nothing is removed, because the alternative is a state no
// surface can render, and minting the id here would put a durable pane id in
// the backend, which §7 refuses.
func TestLayoutRefusesAClosethatWouldEmptyTheApplication(t *testing.T) {
	ws, db := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)

	_, rpcErr := layoutCall(t, conn, "tabs.close", map[string]any{"id": tabID1}, 1)
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("close with no replacement = %+v, want -32602", rpcErr)
	}
	if n := tabCount(t, db, wsID1); n != 1 {
		t.Fatalf("tabs after a refused close = %d, want 1 — the close fails closed", n)
	}
}

// A pane is not dragged between workspaces yet (§12 q. 5): the atomicity
// model for a subtree move is undesigned and the inherited requirement is
// that a partial move fails closed, so the whole move is refused.
func TestLayoutRefusesACrossWorkspaceMove(t *testing.T) {
	ws, db := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)
	mustLayoutCall(t, conn, "workspaces.create", map[string]any{
		"id": wsID2, "name": "ansible", "position": 1,
		"firstTab": firstTab(tabID2), "firstPane": firstPane(paneID2, "/srv"),
	}, 1)

	_, rpcErr := layoutCall(t, conn, "panes.move", map[string]any{"id": paneID1, "tabId": tabID2}, 2)
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("cross-workspace move = %+v, want -32602", rpcErr)
	}
	stayed, _ := db.Layout().Panes(context.Background(), tabID1)
	if len(stayed) != 1 || stayed[0].ID != paneID1 {
		t.Fatalf("panes in the source tab = %+v, want the pane where it was", stayed)
	}
}

// Knowing an id confers no right to use it, and neither does anything else:
// a method naming a row that does not exist is refused, never invented.
func TestLayoutRefusesRowsThatDoNotExist(t *testing.T) {
	ws, _ := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)

	for name, call := range map[string]struct {
		method string
		params map[string]any
	}{
		"rename a workspace that is not there": {"workspaces.rename", map[string]any{"id": wsID2, "name": "x"}},
		"rename a tab that is not there":       {"tabs.rename", map[string]any{"id": tabID2, "name": "x"}},
		"recolour a tab that is not there":     {"tabs.recolour", map[string]any{"id": tabID2, "colour": "#fff"}},
		"pin a tab that is not there":          {"tabs.pin", map[string]any{"id": tabID2, "pinned": true}},
		"tab in a workspace that is not there": {"tabs.create", map[string]any{"id": tabID2, "workspaceId": wsID2, "position": 0, "layout": "row", "firstPane": firstPane(paneID2, "/var")}},
		"pane in a tab that is not there":      {"panes.create", map[string]any{"id": paneID2, "tabId": tabID2, "cwd": "/", "kind": "local", "sizeShare": 1}},
		"create with no first tab":             {"workspaces.create", map[string]any{"id": wsID2, "name": "x", "position": 0, "firstPane": firstPane(paneID2, "/var")}},
		"create with no first pane":            {"tabs.create", map[string]any{"id": tabID2, "workspaceId": wsID1, "position": 0, "layout": "row"}},
		"move a pane that is not there":        {"panes.move", map[string]any{"id": paneID2, "tabId": tabID1}},
		"move into a tab that is not there":    {"panes.move", map[string]any{"id": paneID1, "tabId": tabID2}},
	} {
		t.Run(name, func(t *testing.T) {
			_, rpcErr := layoutCall(t, conn, call.method, call.params, 1)
			if rpcErr == nil || rpcErr.Code != -32602 {
				t.Fatalf("%s = %+v, want -32602", call.method, rpcErr)
			}
		})
	}
}

// ── the prohibition: a tab ROW, never a tab ADDRESS (§4.4) ───────────────

// Every backend→renderer address is a sessionId the renderer resolves, and
// this bead must not change that: a tab holds several panes, so "the tab that
// spoke" is not well defined, and the renderer already knows where a session
// is.
//
// Two halves, because neither alone is the statement. The first is over the
// DECLARED wire: no contract in the repository names a tab as an address —
// the pane object's tabId is the one occurrence and it is a field of an
// object the renderer asked for, which is why it is named here rather than
// waved through by a looser rule. The second is over a REAL notification off
// a real socket, so a payload built outside the schemas cannot slip past.
func TestNoContractDeclaresATabAddress(t *testing.T) {
	entries, err := os.ReadDir(contractDir)
	if err != nil {
		t.Fatalf("read contracts dir: %v", err)
	}
	seen := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".schema.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(contractDir, e.Name())) //nolint:gosec // test-only path under contracts/
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, where := range propertyPaths(doc, "") {
			if !strings.HasSuffix(where, "tabId") {
				continue
			}
			seen++
			// The ONE permitted occurrence: the pane's own reference to the
			// tab holding it, inside the pane object.
			if e.Name() != "panes.create.schema.json" || !strings.Contains(where, "pane") {
				t.Fatalf("%s declares a tabId at %s — the backend gains a tab ROW, never a tab ADDRESS (§4.4)", e.Name(), where)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no tabId found in any contract — the scan is not looking at the right thing")
	}
}

// propertyPaths lists every declared property name in a schema document, as a
// path, at any depth: a $defs, an items, a oneOf branch. A scan that only
// read the top level would miss exactly where an address would be added.
func propertyPaths(node any, path string) []string {
	var out []string
	switch n := node.(type) {
	case map[string]any:
		for key, child := range n {
			if key == "properties" {
				if props, ok := child.(map[string]any); ok {
					for name, sub := range props {
						out = append(out, path+"/"+name)
						out = append(out, propertyPaths(sub, path+"/"+name)...)
					}
					continue
				}
			}
			out = append(out, propertyPaths(child, path+"/"+key)...)
		}
	case []any:
		for i, child := range n {
			out = append(out, propertyPaths(child, fmt.Sprintf("%s/%d", path, i))...)
		}
	}
	return out
}

// The same statement against a real notification. The assertion is negative,
// so it carries its own positive control: the exit notification MUST arrive
// and MUST be addressed by the session id, or the test fails instead of
// passing on an empty observation.
func TestNotificationsAreStillAddressedBySessionID(t *testing.T) {
	ws, _ := newLayoutWSServer(t)
	conn := connectWS(t, ws)
	seedWire(t, conn)
	sid := openSessionOnConn(t, ws, conn, 100)
	mustLayoutCall(t, conn, "tabs.pin", map[string]any{"id": tabID1, "pinned": true}, 101)

	jsonrpcCallWithID(t, conn, "close", map[string]any{"sessionId": sid}, 102)
	raw := readNotification(t, conn, "exit", wantWithin)

	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("exit params: %v", err)
	}
	if got, _ := params["sessionId"].(string); got != sid {
		t.Fatalf("exit addressed %q, want the session id %q", got, sid)
	}
	for _, forbidden := range []string{"tabId", "tab_id", "tab", "paneId", "workspaceId"} {
		if _, has := params[forbidden]; has {
			t.Fatalf("the exit notification carries %q — every backend→renderer address is a sessionId (§4.4)", forbidden)
		}
	}
}
