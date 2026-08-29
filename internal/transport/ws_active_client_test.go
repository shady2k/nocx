package transport

// The client that attached last becomes active, and the channel resizes to it
// (nocx-eidfb.2).
//
// nocx-eidfb.1 moved the size DECISION to the backend: the client measures,
// the backend decides, and a session with no client holds a named default.
// What it left undecided is whose measurement the shared channel takes when
// more than one client has held the session. This file is that answer, at the
// seam a person reaches: two windows, one pane, and the terminal ending up the
// size of the window someone is actually looking at.
//
// The reference is herdr's, not tmux's: the shared pane runtime is derived
// from the FOREGROUND client — the newcomer becomes foreground on connect and
// the runtime resizes to it (src/server/headless.rs) — while rendering stays
// each client's own. tmux would fit the shared runtime to the smallest
// attached client instead, which punishes the big window for the small one's
// existence. nocx's layout half is free, because each window is a DOM that
// lays itself out.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// activePTY is a PTY that records the size it was created at and every resize
// it is asked for. Both halves are needed: "the channel resizes to the new
// client" is a claim about a resize happening, and AD-1's "created at its
// final size, never spawned-then-resized" is a claim about one NOT happening,
// and only something counting both can report either.
type activePTY struct {
	created session.Size

	pr   *io.PipeReader
	pw   *io.PipeWriter
	done chan struct{}
	once sync.Once

	mu sync.Mutex
	// resizeErr, when set, is what Resize answers: the failure of the one
	// external call the attach-time resize makes.
	resizeErr error
	resizes   []session.Size
}

func newActivePTY(cfg pty.Config, resizeErr error) *activePTY {
	pr, pw := io.Pipe()
	return &activePTY{
		created:   session.Size{Cols: cfg.Cols, Rows: cfg.Rows, XPixel: cfg.XPixel, YPixel: cfg.YPixel},
		pr:        pr,
		pw:        pw,
		done:      make(chan struct{}),
		resizeErr: resizeErr,
	}
}

func (p *activePTY) Read(b []byte) (int, error)  { return p.pr.Read(b) }
func (p *activePTY) Write(b []byte) (int, error) { return len(b), nil }
func (p *activePTY) Done() <-chan struct{}       { return p.done }

func (p *activePTY) Close() error {
	p.once.Do(func() {
		_ = p.pw.CloseWithError(io.EOF)
		close(p.done)
	})
	return nil
}

func (p *activePTY) Resize(_ context.Context, cols, rows, xpixel, ypixel uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.resizeErr != nil {
		return p.resizeErr
	}
	p.resizes = append(p.resizes, session.Size{Cols: cols, Rows: rows, XPixel: xpixel, YPixel: ypixel})
	return nil
}

func (p *activePTY) resizeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.resizes)
}

type activeFactory struct {
	resizeErr error

	mu   sync.Mutex
	last *activePTY
}

func (f *activeFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = newActivePTY(cfg, f.resizeErr)
	return f.last, nil
}

func (f *activeFactory) pty() *activePTY {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// newActiveWS builds a server over a resize-recording PTY.
func newActiveWS(t *testing.T, resizeErr error) (*WSServer, *activeFactory) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	f := &activeFactory{resizeErr: resizeErr}
	ws := NewWSServer(logger, session.New(logger, f))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = ws.Stop(ctx)
		if p := f.pty(); p != nil {
			_ = p.Close()
		}
	})
	return ws, f
}

// openAtSize opens a session over a tapped connection at an explicit geometry
// and hands back the whole identity the ack carried, which is what a later
// claim has to name.
func openAtSize(t *testing.T, ws *WSServer, conn *websocket.Conn, tap *socketTap, id int, size session.Size) openResult {
	t.Helper()
	raw := tapCall(t, conn, tap, id, "open", map[string]any{
		"cols": size.Cols, "rows": size.Rows, "paneId": reclaimPane,
	})
	var env struct {
		Result openResult       `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("open: %v\nraw: %s", err, raw)
	}
	if env.Error != nil {
		t.Fatalf("open: %+v", env.Error)
	}
	awaitSubscriber(t, ws, session.ID(env.Result.SessionID))
	return env.Result
}

// effectiveSizeOf is the backend's own conclusion about a session, read off
// the registry — never off a field the transport kept, and never off what a
// client asked for. It is set only after the channel accepted the size, so
// reading it is reading what the terminal is actually running at.
func effectiveSizeOf(t *testing.T, ws *WSServer, sid string) session.Size {
	t.Helper()
	sess, err := ws.registry.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("registry.Get(%s): %v", sid, err)
	}
	return sess.EffectiveSize()
}

// awaitEffectiveSize waits for an observable state — the size the session
// reports — never for a duration. The resize is applied on the session's own
// lane, off the read loop, so the attach response can and does precede it.
func awaitEffectiveSize(t *testing.T, ws *WSServer, sid string, want session.Size) {
	t.Helper()
	waittest.WaitForTimeoutDetail(t, "the session to run at the foreground client's size", wantWithin,
		func() string { return fmt.Sprintf("the session is running at %+v", effectiveSizeOf(t, ws, sid)) },
		func() bool { return effectiveSizeOf(t, ws, sid) == want })
}

// attachAt claims a session, reporting the claiming client's own geometry.
func attachAt(t *testing.T, conn *websocket.Conn, tap *socketTap, id int, opened openResult, offset int, size session.Size) attachResult {
	t.Helper()
	params := map[string]any{
		"sessionId":    opened.SessionID,
		"instanceId":   opened.InstanceID,
		"sessionEpoch": opened.SessionEpoch,
		"offset":       offset,
	}
	if size.Valid() {
		params["cols"] = size.Cols
		params["rows"] = size.Rows
		params["xpixel"] = size.XPixel
		params["ypixel"] = size.YPixel
	}
	raw, rpcErr := attachCall(t, conn, tap, id, params)
	if rpcErr != nil {
		t.Fatalf("the claim was refused: %+v", rpcErr)
	}
	var got attachResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("attach result: %v\nraw: %s", err, raw)
	}
	return got
}

// ── acceptance 1: the client that attached last becomes active ─────────────

// THE WHOLE FEATURE. A second window takes a live pane and the shared channel
// resizes to THAT window's geometry — not to the departing window's, and not
// to the smaller of the two. Read off the session, which records a size only
// after the channel accepted it, so this is the terminal's real grid rather
// than a number the transport kept.
func TestActiveClient_TheClientThatAttachedLastOwnsTheChannelSize(t *testing.T) {
	ws, _ := newActiveWS(t, nil)

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	first := session.Size{Cols: 100, Rows: 30}
	opened := openAtSize(t, ws, connA, tapA, 1, first)

	if got := effectiveSizeOf(t, ws, opened.SessionID); got != first {
		t.Fatalf("EffectiveSize = %+v before the second client, want the opening client's %+v", got, first)
	}

	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)
	second := session.Size{Cols: 132, Rows: 43, XPixel: 1320, YPixel: 860}
	attachAt(t, connB, tapB, 2, opened, 0, second)

	awaitEffectiveSize(t, ws, opened.SessionID, second)
}

// ── acceptance 2: the loser is still told ──────────────────────────────────

// The displacement notification must keep working on THIS path — an attach
// that carries a geometry — and not merely on the one the previous task
// tested. Asserted here rather than assumed, because the take and the resize
// now happen in the same handler and an ordering mistake between them would
// be invisible to a test that only watches the size.
func TestActiveClient_TheDisplacedClientIsStillToldItLostTheSession(t *testing.T) {
	ws, _ := newActiveWS(t, nil)

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	opened := openAtSize(t, ws, connA, tapA, 1, session.Size{Cols: 100, Rows: 30})

	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)
	second := session.Size{Cols: 132, Rows: 43}
	attachAt(t, connB, tapB, 2, opened, 0, second)

	params := tapNotify(t, tapA, "session.displaced", wantWithin)
	var told sessionDisplacedParams
	if err := json.Unmarshal(params, &told); err != nil {
		t.Fatalf("session.displaced params: %v", err)
	}
	if told.SessionID != opened.SessionID || told.InstanceID != opened.InstanceID ||
		told.SessionEpoch != opened.SessionEpoch {
		t.Errorf("session.displaced = %+v, want the identity of the session that was taken (%s, %s, %d)",
			told, opened.SessionID, opened.InstanceID, opened.SessionEpoch)
	}

	// And the take is real in both halves: the loser lost it, and the winner's
	// geometry is what the channel runs at.
	awaitEffectiveSize(t, ws, opened.SessionID, second)
}

// ── acceptance 3: the interval, both ends named ────────────────────────────

// A session runs at the foreground client's geometry FROM the moment that
// client's attach is admitted and its report reaches the channel, UNTIL either
// another client's attach replaces it or the last client detaches. From that
// detach until the next client reports, it runs at the named default.
//
// The opening size is deliberately not 80x24: with the default as the opening
// size, "returned to the default" and "kept the first client's size" would be
// the same observation, and the test could not tell them apart.
func TestActiveClient_TheForegroundSizeHoldsUntilTheLastClientDetaches(t *testing.T) {
	ws, _ := newActiveWS(t, nil)

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	first := session.Size{Cols: 100, Rows: 30}
	opened := openAtSize(t, ws, connA, tapA, 1, first)
	sid := opened.SessionID

	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)
	second := session.Size{Cols: 132, Rows: 43}
	attachAt(t, connB, tapB, 2, opened, 0, second)

	// THE INTERVAL IS OPEN: the newcomer's geometry, and it stays that way
	// while the newcomer holds the session.
	awaitEffectiveSize(t, ws, sid, second)

	// THE CLOSING EVENT: the last client goes away. connA already lost the
	// session at the displacement, so connB's departure empties the slot.
	_ = connB.Close()
	awaitDetached(t, ws, session.ID(sid))

	// The session does not keep a departed client's size — it returns to the
	// named default, which is the size a session with nobody looking at it
	// runs at.
	awaitEffectiveSize(t, ws, sid, session.DefaultSize())
}

// ── a client that has not measured itself is not the absence of a client ───

// An attach carrying no geometry is a client that has not measured itself
// yet — a fresh window reclaiming a pane it has never rendered. It is NOT the
// no-client state, and answering both with the default would put a live
// window's terminal on 80x24 for no reason. The size stands until somebody
// reports one.
func TestActiveClient_AnAttachThatMeasuredNothingLeavesTheSizeAlone(t *testing.T) {
	ws, _ := newActiveWS(t, nil)

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	first := session.Size{Cols: 100, Rows: 30}
	opened := openAtSize(t, ws, connA, tapA, 1, first)

	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)
	attachAt(t, connB, tapB, 2, opened, 0, session.NoClient())

	// The take happened — the loser is told — and the size did not move.
	tapNotify(t, tapA, "session.displaced", wantWithin)
	if got := effectiveSizeOf(t, ws, opened.SessionID); got != first {
		t.Fatalf("EffectiveSize = %+v after an attach that reported nothing, want the unchanged %+v", got, first)
	}
}

// ── AD-1: created at its final size, never spawned-then-resized ────────────

// The claim this task must not break, asserted at the transport seam rather
// than assumed from the session package's own test: an open produces NO
// resize on the channel, whatever the client reported. The attach-time resize
// added here happens to a session that already exists; it can never become
// the second half of a spawn-then-resize.
func TestActiveClient_OpenStillCreatesTheChannelAtItsFinalSize(t *testing.T) {
	ws, f := newActiveWS(t, nil)

	conn := connectWS(t, ws)
	tap := newSocketTap(conn)
	reported := session.Size{Cols: 100, Rows: 30}
	openAtSize(t, ws, conn, tap, 1, reported)

	p := f.pty()
	if p == nil {
		t.Fatal("no PTY was created")
	}
	if p.created != reported {
		t.Errorf("channel created at %+v, want the reported %+v", p.created, reported)
	}
	if n := p.resizeCount(); n != 0 {
		t.Errorf("the channel was resized %d times during open; AD-1 says it is created at its final size", n)
	}
}

// ── the failure path of the attach-time resize ─────────────────────────────

// The one external call the take makes is the channel resize, and it can
// fail — a dead SSH channel answers every window-change with an error. The
// claim must still succeed: the newcomer holds the session either way, and a
// terminal that could not be resized is a terminal at the wrong size, not a
// session somebody else still owns. The session goes on reporting the size it
// is actually running at.
//
// The success half of this pair is
// TestActiveClient_TheClientThatAttachedLastOwnsTheChannelSize, which is the
// same call on a channel that accepts it.
func TestActiveClient_TheChannelRefusesTheResize_TheClaimStillSucceeds(t *testing.T) {
	ws, _ := newActiveWS(t, errors.New("resize unsupported"))

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	first := session.Size{Cols: 100, Rows: 30}
	opened := openAtSize(t, ws, connA, tapA, 1, first)

	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)
	claim := attachAt(t, connB, tapB, 2, opened, 0, session.Size{Cols: 132, Rows: 43})
	if !claim.Resumed {
		t.Fatalf("attach = %+v, want a resume", claim)
	}

	// The take is real: the loser is told, and the new client holds it.
	tapNotify(t, tapA, "session.displaced", wantWithin)

	// And the session reports the grid the channel is still running at, never
	// the one it refused.
	if got := effectiveSizeOf(t, ws, opened.SessionID); got != first {
		t.Fatalf("EffectiveSize = %+v after a refused resize, want the unchanged %+v", got, first)
	}
}
