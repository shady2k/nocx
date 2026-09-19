package transport

// The pane-opened note (nocx-xn63t.1.4): one hook beside the ONE open path,
// told about every pane nocx actually opened, whichever caller opened it.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

type notedOpen struct {
	spec OpenSpec
	sid  session.ID
}

type noteRecorder struct {
	mu    sync.Mutex
	notes []notedOpen
}

func (r *noteRecorder) note(spec OpenSpec, sid session.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, notedOpen{spec: spec, sid: sid})
}

func (r *noteRecorder) seen() []notedOpen {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notedOpen(nil), r.notes...)
}

// Criterion: a successful open tells the note, with the spec it was asked
// for and the session it became — from BOTH callers, because the whole point
// of the hook's position is that a renderer's `open` and the backend's own
// OpenSession share one door and must not diverge in who hears about it. A
// refused open tells it nothing: a pane nocx failed to open is not a pane
// nocx has open.
func TestThePaneOpenedNoteRidesEverySuccessfulOpen(t *testing.T) {
	ctx := context.Background()
	rec := &noteRecorder{}
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithPaneOpenedNote(rec.note))
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	// The backend's own caller.
	const backendCwd = "/repo/main-checkout"
	opened, err := ws.OpenSession(ctx, OpenSpec{Cols: 80, Rows: 24, Cwd: backendCwd})
	if err != nil {
		t.Fatalf("backend open: %v", err)
	}

	// The renderer's caller, over the wire.
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	const rendererCwd = "/repo/linked-checkout"
	const pane = "0198f2b0-0000-7000-8000-0000000000c3"
	raw := jsonrpcCall(t, conn, "open", map[string]any{"cols": 80, "rows": 24, "cwd": rendererCwd, "paneId": pane})
	var resp struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal open response: %v (%s)", err, raw)
	}
	if resp.Error != nil {
		t.Fatalf("the renderer's open refused: %s", raw)
	}

	// And a refusal the note must NOT hear: an ssh open in a build with no
	// resolver wired, the same refusal ws_open_refusal_test.go pins.
	rawRefused := jsonrpcCall(t, conn, "open", map[string]any{
		"kind": "ssh", "host": "myhost", "cols": 80, "rows": 24, "paneId": "0198f2b0-0000-7000-8000-0000000000c4",
	})
	var refused struct {
		Error *struct{} `json:"error"`
	}
	if err := json.Unmarshal(rawRefused, &refused); err != nil {
		t.Fatalf("unmarshal refused response: %v (%s)", err, rawRefused)
	}
	if refused.Error == nil {
		t.Fatalf("the ssh open succeeded where it must refuse: %s", rawRefused)
	}

	notes := rec.seen()
	if len(notes) != 2 {
		t.Fatalf("note heard %d open(s), want exactly the two successful ones: %+v", len(notes), notes)
	}
	if notes[0].spec.Cwd != backendCwd || notes[0].sid != opened.Session.ID() {
		t.Fatalf("backend note = %+v, want cwd %q and session %s", notes[0], backendCwd, opened.Session.ID())
	}
	if notes[1].spec.Cwd != rendererCwd {
		t.Fatalf("renderer note = %+v, want cwd %q", notes[1], rendererCwd)
	}
}

// Criterion: an unwired note is the ordinary shape — the open neither fails
// nor notices.
func TestAnOpenWithoutANoteOpensAnyway(t *testing.T) {
	ctx := context.Background()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	opened, err := ws.OpenSession(ctx, OpenSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if opened.Session == nil {
		t.Fatal("no session")
	}
}
