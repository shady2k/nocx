package app

// workers.close closes the worker's tab (nocx-xn63t.4.6).
//
// WHAT THE OWNER SAW (2026-09-17, dev stand, build ab0534d9): a coordinator
// called workers.close on a finished claude worker, the call answered ok, and
// the worker's tab stayed in the strip — carrying a disconnected-plug mark
// and "The connection is gone — Reconnecting opens a NEW shell" over it —
// until the window was reloaded. Stage 4 of the herdr replacement promises
// the opposite: closing a worker closes its tab.
//
// The cause was an asymmetry with one missing half. workers.spawn MINTS the
// tab (content.CreateTabAfter, nocx-tdiqs, placed immediately after its
// coordinator's) and announces it with workers.tabCreated; nothing ever wrote
// its closing. internal/app's own close path ended the participant's SESSION
// and left the tab in the window, which is a tab with nothing behind it.
//
// WHAT THE TESTS BELOW WATCH, and in which order the product performs it: the
// content store first (the tab is out of the window and the strip's positions
// are dense), the wire second (a connected window is told which tab left, on
// the same broadcast the spawn side already uses), and the record last (a
// FINISHED worker — the case the owner hit — still reaches its tab, and a tab
// the person closed themselves is not an error).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

// serve starts the stand's transport. The product starts it at boot; the
// stand leaves it stopped, because everything else in this package works
// against the server's own methods and a listener per test would be paid for
// by thirty tests that never read a frame. The two tests below do read one,
// and the stand's own teardown already stops what this starts.
func (w *workerStand) serve(t *testing.T) {
	t.Helper()
	if err := w.tp.Start(context.Background()); err != nil {
		t.Fatalf("start the stand's transport: %v", err)
	}
}

// workerTestDial opens one renderer connection to the stand's server, the way
// a renderer does: the /session path with the token in the subprotocol.
//
// It is written here rather than borrowed from launcher_reachability_test.go
// because that helper is behind the nocx_local_ssh tag, and the close is not
// a tagged feature.
func workerTestDial(t *testing.T, ws *transport.WSServer) *websocket.Conn {
	t.Helper()
	u := url.URL{Scheme: "ws", Host: fmt.Sprintf("127.0.0.1:%d", ws.Port()), Path: "/session"}
	d := websocket.Dialer{Subprotocols: []string{"nocx.token." + ws.Token()}}
	conn, _, err := d.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial the stand's server: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// workerNotifications is the SINGLE reader on one renderer connection, handing
// the test every `workers.*` notification the backend broadcasts.
//
// One reader and no read deadline: a gorilla websocket that has failed a read
// stays failed and a second read on it panics, so a helper that polled the
// socket with a timeout would turn "the frame has not arrived yet" into a
// crash (the reason internal/transport's own tests read this way).
type workerNotifications struct {
	frames chan workerNotification
}

type workerNotification struct {
	method string
	params json.RawMessage
}

func readWorkerNotifications(t *testing.T, conn *websocket.Conn) *workerNotifications {
	t.Helper()
	r := &workerNotifications{frames: make(chan workerNotification, 64)}
	go func() {
		defer close(r.frames)
		for {
			typ, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if typ != websocket.TextMessage {
				continue
			}
			var frame struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(msg, &frame) != nil || frame.Method == "" {
				continue
			}
			if !strings.HasPrefix(frame.Method, "workers.") {
				continue
			}
			r.frames <- workerNotification{method: frame.Method, params: frame.Params}
		}
	}()
	return r
}

// await waits for one notification of that method, as an observable state and
// never as a duration: the frame either arrived or the package's own wait
// reports what never came.
func (r *workerNotifications) await(t *testing.T, method string) json.RawMessage {
	t.Helper()
	var got json.RawMessage
	waittest.WaitFor(t, "the "+method+" notification to arrive", func() bool {
		select {
		case f, ok := <-r.frames:
			if !ok {
				return false
			}
			if f.method == method {
				got = f.params
				return true
			}
			return false
		default:
			return false
		}
	})
	return got
}

// tabOfSession answers which tab the participant's pane was minted in, by
// walking the chain rather than by remembering an id: the session names its
// pane (AD-7) and the pane names its tab.
func (w *workerStand) tabOfSession(t *testing.T, sid string) string {
	t.Helper()
	sess, err := w.reg.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("the participant's session is not in the registry: %v", err)
	}
	snap, err := w.db.Layout().Snapshot(context.Background())
	if err != nil {
		t.Fatalf("layout snapshot: %v", err)
	}
	for _, pane := range snap.Panes {
		if pane.ID == sess.PaneID() {
			return pane.TabID
		}
	}
	t.Fatalf("the pane the participant's session opened in (%s) is in no tab", sess.PaneID())
	return ""
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// recordOver builds a second record over this stand's own seams with a chosen
// closer, so one half of a close can be made to fail while everything else is
// the product's — the shape a stand that closed over its own closer could not
// offer.
func (w *workerStand) recordOver(t *testing.T, closer workers.Closer) *workers.Registrar {
	t.Helper()
	return workers.NewRegistrar(
		w.workerStore, w.spawner, w.enrol, w.sup,
		workers.WithEnrolmentDeadline(2*time.Second),
		workers.WithCloser(closer),
	)
}

// finishParticipant ends a worker the way an ordinary one ends: its process is
// gone. What that leaves behind is the state the owner closed by hand.
func (w *workerStand) finishParticipant(t *testing.T, p workers.Participant) {
	t.Helper()
	ctx := context.Background()
	if err := w.reg.Close(session.ID(p.Liveness.SessionID)); err != nil {
		t.Fatalf("the worker's process ends: %v", err)
	}
	waittest.WaitFor(t, "the finished worker's exit to reach the record", func() bool {
		stored, err := w.workerStore.Participant(ctx, p.ID)
		return err == nil && stored.State.Terminal()
	})
}

// Criterion 1 and the wire half of criterion 2, on one scenario: after
// workers.close the tab has left the content store's window, the strip's
// positions are still dense, and the connected window is told which tab went.
func TestClosingAWorkerClosesItsTab(t *testing.T) {
	ctx := context.Background()
	stand := newWorkerStand(t)
	// Neighbours, so "the tab is gone" is a statement about a seat in a strip
	// rather than about an empty window.
	placeTabs(t, stand.db.Layout(), content.DefaultWorkspaceID, "tab-a", "tab-b")
	stand.serve(t)
	renderer := readWorkerNotifications(t, workerTestDial(t, stand.tp))

	p := stand.registerWithEnrolment(t, "read AGENTS.md and report")
	tabID := stand.tabOfSession(t, p.Liveness.SessionID)
	if ids := idsOf(t, stripOf(t, stand.db.Layout(), content.DefaultWorkspaceID)); !containsID(ids, tabID) {
		t.Fatalf("the participant's tab is not in the strip before the close: %v", ids)
	}

	if _, err := stand.record.Close(ctx, "sess-coordinator", p.ID); err != nil {
		t.Fatalf("workers.close: %v", err)
	}

	// THE STORE: the tab is out of the window, and the strip it was in is
	// still dense — a close that left a hole would draw the neighbour one
	// seat too far right on the next read.
	tabs := stripOf(t, stand.db.Layout(), content.DefaultWorkspaceID)
	if ids := idsOf(t, tabs); containsID(ids, tabID) {
		t.Fatalf("the closed worker's tab is still in the window: %v", ids)
	}
	for seat, tab := range tabs {
		if tab.Position != seat {
			t.Fatalf("tab %s sits at position %d, want %d — the strip is not dense after the close",
				tab.ID, tab.Position, seat)
		}
	}

	// AND THE WIRE: the window that is looking at the strip is told, on the
	// same broadcast workers.tabCreated already uses.
	params := renderer.await(t, "workers.tabClosed")
	var fact struct {
		TabID string `json:"tabId"`
	}
	if err := json.Unmarshal(params, &fact); err != nil {
		t.Fatalf("decode workers.tabClosed: %v\nraw: %s", err, params)
	}
	if fact.TabID != tabID {
		t.Fatalf("workers.tabClosed named tab %q, want %q", fact.TabID, tabID)
	}
}

// Criterion: a FINISHED worker — the exact case the owner hit, its process
// already gone before the close arrived — still has its tab closed, because
// the part of a close that is left to do for a participant that has already
// ended is the place it occupies.
func TestClosingAFinishedWorkerClosesItsTab(t *testing.T) {
	ctx := context.Background()
	stand := newWorkerStand(t)
	placeTabs(t, stand.db.Layout(), content.DefaultWorkspaceID, "tab-a", "tab-b")
	stand.serve(t)
	renderer := readWorkerNotifications(t, workerTestDial(t, stand.tp))

	p := stand.registerWithEnrolment(t, "claude")
	tabID := stand.tabOfSession(t, p.Liveness.SessionID)
	stand.finishParticipant(t, p)

	// THE TAB IS STILL THERE when a worker's process ends. That is deliberate
	// and not the defect: the screen a worker printed is the evidence of what
	// it did, and a tab that vanished on exit would take it away before
	// anybody read it.
	if ids := idsOf(t, stripOf(t, stand.db.Layout(), content.DefaultWorkspaceID)); !containsID(ids, tabID) {
		t.Fatalf("the tab left the window when the worker's process ended; the screen it printed goes with it: %v", ids)
	}

	if _, err := stand.record.Close(ctx, "sess-coordinator", p.ID); err != nil {
		t.Fatalf("closing a finished worker: %v", err)
	}

	if ids := idsOf(t, stripOf(t, stand.db.Layout(), content.DefaultWorkspaceID)); containsID(ids, tabID) {
		t.Fatalf("the finished worker's tab is still in the window after workers.close: %v", ids)
	}
	params := renderer.await(t, "workers.tabClosed")
	var fact struct {
		TabID string `json:"tabId"`
	}
	if err := json.Unmarshal(params, &fact); err != nil {
		t.Fatalf("decode workers.tabClosed: %v", err)
	}
	if fact.TabID != tabID {
		t.Fatalf("workers.tabClosed named tab %q, want %q", fact.TabID, tabID)
	}
}

// Criterion: closing a worker the person has already closed the tab of is not
// an error. A person's own Cmd-W is one content.DeleteTab, and a coordinator
// tidying up afterwards must not be told it failed because somebody got there
// first.
func TestClosingAWorkerWhoseTabIsAlreadyClosedIsNotAnError(t *testing.T) {
	ctx := context.Background()
	stand := newWorkerStand(t)
	placeTabs(t, stand.db.Layout(), content.DefaultWorkspaceID, "tab-a", "tab-b")

	p := stand.registerWithEnrolment(t, "claude")
	tabID := stand.tabOfSession(t, p.Liveness.SessionID)

	// The person's own close, through the SAME store write tabs.close makes.
	if err := stand.db.Layout().DeleteTab(ctx, tabID, content.Replacement{
		TabID: "tab-replacement", PaneID: "pane-replacement",
	}); err != nil {
		t.Fatalf("the person closes the tab: %v", err)
	}

	if _, err := stand.record.Close(ctx, "sess-coordinator", p.ID); err != nil {
		t.Fatalf("closing a worker whose tab was already closed: %v", err)
	}
}

// refusingTabs is a layout chain that refuses the one write the close makes,
// so the failure half of the close is reachable without a broken store.
type refusingTabs struct{ err error }

func (r *refusingTabs) DeleteTab(context.Context, string, content.Replacement) error { return r.err }

// Criterion: a close that fails to persist the tab close is REPORTED, not
// swallowed — and what is true on disk when it fails is exactly what the
// caller is told, which is why the session half runs first.
func TestAFailedTabCloseIsReportedNotSwallowed(t *testing.T) {
	ctx := context.Background()
	stand := newWorkerStand(t)
	placeTabs(t, stand.db.Layout(), content.DefaultWorkspaceID, "tab-a", "tab-b")

	p := stand.registerWithEnrolment(t, "claude")
	tabID := stand.tabOfSession(t, p.Liveness.SessionID)

	record := stand.recordOver(t, &workerCloser{
		sessions: stand.reg,
		layout:   &refusingTabs{err: errors.New("the content store refused the write")},
		tabs:     stand.tabs,
		announce: stand.tp,
		log:      stand.log,
	})

	_, err := record.Close(ctx, "sess-coordinator", p.ID)
	if err == nil {
		t.Fatal("a close whose tab write failed was reported to the coordinator as done")
	}
	if !strings.Contains(err.Error(), "tab") {
		t.Fatalf("the refusal does not say what could not be closed: %v", err)
	}

	// WHAT IS TRUE ON DISK AFTER THAT FAILURE: the participant's session is
	// gone (the tab close is ordered after it, so a failure here cannot leave
	// a live process behind a tab the person believes they closed) and the tab
	// is still in the window — which is the state the coordinator has to be
	// told about rather than have swallowed.
	if _, getErr := stand.reg.Get(session.ID(p.Liveness.SessionID)); getErr == nil {
		t.Fatal("the tab write failed and the session was not ended; the reported failure is not the whole failure")
	}
	if ids := idsOf(t, stripOf(t, stand.db.Layout(), content.DefaultWorkspaceID)); !containsID(ids, tabID) {
		t.Fatalf("the tab is out of the window, so the failure that was reported is fiction: %v", ids)
	}
}
