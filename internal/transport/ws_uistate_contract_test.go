package transport

// The uistate.* wire contract, from both ends (AGENTS.md rule 5): the DTO
// marshals to something the schema accepts, and the REAL result off the REAL
// socket satisfies it too. The second is the one that matters — a test that
// validates a payload it constructed itself proves the struct is well-formed,
// not that the server sends it.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/uistate"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestUIStateGet_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "uistate.schema.json")

	cases := map[string]uiStateLayout{
		"a fresh profile": {
			Sidebar:   uiStateSidebar{Width: uistate.DefaultSidebarWidth},
			ActiveTab: "",
		},
		"a used profile": {
			Sidebar:   uiStateSidebar{Collapsed: true, ActiveViewID: "ports", Width: 320},
			ActiveTab: "pane-7",
		},
	}
	for name, dto := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(dto)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "uistate.get result ("+name+")")
		})
	}
}

func TestUIStateSet_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "uistate.schema.json")
	raw, err := json.Marshal(uiStateLayout{
		Sidebar:   uiStateSidebar{Collapsed: false, ActiveViewID: "files", Width: 206},
		ActiveTab: "pane-1",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "uistate.set result")
}

// newUIStateWSServer builds a server over a real DocumentStore in a temp
// directory: the store IS what is under test here, and a fake one would prove
// the DTO and nothing about the wire.
func newUIStateWSServer(t *testing.T) (*WSServer, *uistate.Store, func()) {
	t.Helper()
	store := uistate.New(storage.NewDocumentStore(t.TempDir()), nil)
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithUIState(store))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, store, func() { _ = ws.Stop(ctx); _ = store.Close() }
}

func TestUIStateGet_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "uistate.schema.json")
	ws, _, stop := newUIStateWSServer(t)
	defer stop()

	resp := snippetCall(t, connectWS(t, ws), "uistate.get", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "uistate.get result")
}

func TestUIStateSet_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "uistate.schema.json")
	ws, _, stop := newUIStateWSServer(t)
	defer stop()

	resp := snippetCall(t, connectWS(t, ws), "uistate.set", map[string]any{
		"sidebar":   map[string]any{"collapsed": true, "activeViewId": "ports", "width": 320},
		"activeTab": "pane-3",
	}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "uistate.set result")
}

// The round trip a user performs: drag the panel, restart, find it where they
// left it. Two connections against one store, because a restart is exactly a
// new renderer reading what the last one wrote.
func TestUIStateSurvivesOverTheWire(t *testing.T) {
	ws, _, stop := newUIStateWSServer(t)
	defer stop()

	set := snippetCall(t, connectWS(t, ws), "uistate.set", map[string]any{
		"sidebar":   map[string]any{"collapsed": true, "activeViewId": "ports", "width": 320},
		"activeTab": "pane-3",
	}, 1)
	if set.Error != nil {
		t.Fatalf("uistate.set: %+v", set.Error)
	}

	got := snippetCall(t, connectWS(t, ws), "uistate.get", map[string]any{}, 1)
	if got.Error != nil {
		t.Fatalf("uistate.get: %+v", got.Error)
	}
	var layout struct {
		Sidebar struct {
			Collapsed    bool   `json:"collapsed"`
			ActiveViewID string `json:"activeViewId"`
			Width        int    `json:"width"`
		} `json:"sidebar"`
		ActiveTab string `json:"activeTab"`
	}
	if err := json.Unmarshal(got.Result, &layout); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !layout.Sidebar.Collapsed || layout.Sidebar.ActiveViewID != "ports" || layout.Sidebar.Width != 320 {
		t.Fatalf("sidebar came back as %+v, want what was set", layout.Sidebar)
	}
	if layout.ActiveTab != "pane-3" {
		t.Fatalf("activeTab = %q, want pane-3", layout.ActiveTab)
	}
}

// The stored value is what comes back, not the sent one. A renderer that never
// learns its width was clamped holds a number nobody will ever read back —
// which is the defect that bought the whole contracts directory.
func TestUIStateSetAnswersWithTheStoredWidth(t *testing.T) {
	ws, _, stop := newUIStateWSServer(t)
	defer stop()

	resp := snippetCall(t, connectWS(t, ws), "uistate.set", map[string]any{
		"sidebar":   map[string]any{"collapsed": false, "activeViewId": "", "width": 99999},
		"activeTab": "",
	}, 1)
	if resp.Error != nil {
		t.Fatalf("uistate.set: %+v", resp.Error)
	}
	var layout struct {
		Sidebar struct {
			Width int `json:"width"`
		} `json:"sidebar"`
	}
	if err := json.Unmarshal(resp.Result, &layout); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if layout.Sidebar.Width != uistate.MaxSidebarWidth {
		t.Fatalf("width came back %d, want it clamped to %d", layout.Sidebar.Width, uistate.MaxSidebarWidth)
	}
}

// The unwired configuration — cmd/devharness and the dev-web harness build a
// server without the store. The methods must say so rather than pretending.
func TestUIStateUnwiredIsMethodNotFound(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	resp := snippetCall(t, connectWS(t, ws), "uistate.get", map[string]any{}, 1)
	if resp.Error == nil {
		t.Fatal("an unwired uistate answered a result")
	}
	if resp.Error.Code != -32601 {
		t.Fatalf("error code = %d, want -32601", resp.Error.Code)
	}
}

// --- the window half (ADR-0098) -----------------------------------------
//
// Geometry crosses the wire on its own methods because no one process owns
// both ends any more: the shell has the window, the coordinator has the
// document, and the renderer is the relay between them. The same two checks
// as the layout half: the DTO marshals to what the schema accepts, and the
// REAL result off the REAL socket satisfies it too.

func TestUIStateWindowGet_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "uistate.window.schema.json")

	cases := map[string]uiStateWindow{
		"never saved": {},
		"a placed window": {
			Width: 1280, Height: 800, X: 40, Y: 60,
			Displays: "1:1920x1080p",
		},
		"a maximised window": {
			Width: 1280, Height: 800, X: 40, Y: 60, Maximised: true,
			Displays: "2:1920x1080p,2560x1440",
		},
	}
	for name, dto := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(dto)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "uistate.window.get result ("+name+")")
		})
	}
}

func TestUIStateWindowGet_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "uistate.window.schema.json")
	ws, _, stop := newUIStateWSServer(t)
	defer stop()

	resp := snippetCall(t, connectWS(t, ws), "uistate.window.get", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "uistate.window.get result")
}

// TestUIStateWindowObserve_OverTheWireRecordsAndFolds is the write half: a
// notification off the real socket reaches the store, the store's own fold
// keeps the recorded normal geometry under a maximised sample, and a read
// back over the same socket answers the folded state.
func TestUIStateWindowObserve_OverTheWireRecordsAndFolds(t *testing.T) {
	schema := loadSchema(t, "uistate.window.schema.json")
	ws, store, stop := newUIStateWSServer(t)
	defer stop()

	conn := connectWS(t, ws)

	// Seed the normal geometry the way a running app would have: one observe
	// while the window is normal.
	writeNotification(t, conn, "uistate.window.observe", map[string]any{
		"width": 1280, "height": 800, "x": 40, "y": 60, "displays": "1:1920x1080p",
	})
	waittest.WaitFor(t, "the first window sample to reach the store", func() bool {
		return store.Window().Width == 1280
	})

	// A maximised sample moves only its flag: the maximised size must not
	// become the window size, or the next launch would restore a window that
	// merely looks maximised and unmaximises to the wrong place.
	writeNotification(t, conn, "uistate.window.observe", map[string]any{
		"width": 1920, "height": 1080, "x": 0, "y": 0,
		"maximised": true, "displays": "1:1920x1080p",
	})
	waittest.WaitFor(t, "the maximised sample to fold into the store", func() bool {
		w := store.Window()
		return w.Maximised && w.Width == 1280
	})

	resp := snippetCall(t, conn, "uistate.window.get", map[string]any{}, 2)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "uistate.window.get result after observe")

	var got struct {
		Width     int  `json:"width"`
		Height    int  `json:"height"`
		Maximised bool `json:"maximised"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got.Width != 1280 || got.Height != 800 || !got.Maximised {
		t.Errorf("read back %+v, want 1280x800 maximised", got)
	}
}

// writeNotification sends one JSON-RPC notification (no id) on the real
// socket. Notifications are never answered, so the caller observes the effect
// through the state they change — the same rule the product's own relays
// follow.
func writeNotification(t *testing.T, conn *websocket.Conn, method string, params any) {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write notification: %v", err)
	}
}
