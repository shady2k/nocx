package transport

// The two lineage PROHIBITIONS (nocx-wtv3p, design §8 items 5 and 6, D6).
// They are stated in nocx-9hu9d's comments and enforced here, because a
// prohibition with no test is a comment.
//
//  1. NO ADDRESSABILITY FOLLOWS FROM LINEAGE ALONE. Being someone's child is
//     provenance; it is never permission to observe, drive or close. ADR-0020
//     §5 carries the reason: A opens B, B is taken over, re-credentialed and
//     ssh'd into production, and B is a descendant of A forever — so ancestry
//     alone would leave A reading and driving a session whose operator and
//     context have changed.
//
//  2. A PARENT'S DEATH NEVER CLOSES ITS CHILDREN. Three of the four ways to
//     lose a parent are FAILURES — a process exit, a backend restart, a
//     dropped link — and a failure carries no information about whether the
//     work is still wanted. The fourth, an explicit human close, is an act
//     about the parent and not about anything the parent opened.
//
// HOW THESE TESTS ARE WRITTEN. Each drives the VIOLATING call over the real
// socket and asserts the refusal, rather than driving a correct call and
// asserting it works: a test that only exercises the permitted path cannot
// report that the forbidden one was quietly allowed. Where the assertion is
// negative — "these bytes never arrive" — it is paired with a positive
// control on the same run, so a test that observes nothing because nothing
// was produced fails instead of passing.
//
// WHAT THE PROHIBITION IS HELD AGAINST. The single owner of "may this caller
// address this session" is the connection's own session set (connState.Owns,
// ws.go) — filled by the open that created the session and by an explicit
// attach, and by nothing else. These tests hold that answer to the rule by
// DIFFERENCE: a session reached by a lineage edge and a session reached by
// nothing at all must be answered identically. An implementation that made
// lineage confer control would pass the first half of every test below and
// fail the differential, which is the half that cannot be satisfied by
// accident.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// ── the fake channel these tests need ─────────────────────────────────────
//
// exitFakePTY (ws_exit_test.go) is shared by every session on its server and
// records nothing that was written to it. These tests are about what reaches
// ONE session's channel while ANOTHER session's channel dies, so they need a
// channel per session that remembers its input, can produce output on demand,
// and can be killed on its own.

type lineagePTY struct {
	mu      sync.Mutex
	written []byte
	resizes []string
	waitErr error
	waitSet bool

	out       chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

func newLineagePTY() *lineagePTY {
	return &lineagePTY{out: make(chan []byte, 8), done: make(chan struct{})}
}

func (p *lineagePTY) Read(b []byte) (int, error) {
	select {
	case data := <-p.out:
		return copy(b, data), nil
	case <-p.done:
		return 0, io.EOF
	}
}

func (p *lineagePTY) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.written = append(p.written, b...)
	return len(b), nil
}

func (p *lineagePTY) Resize(_ context.Context, cols, rows, _, _ uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resizes = append(p.resizes, sizeLabel(cols, rows))
	return nil
}

func (p *lineagePTY) Close() error {
	p.closeOnce.Do(func() { close(p.done) })
	return nil
}

func (p *lineagePTY) Done() <-chan struct{} { return p.done }

// WaitErr is the optional seam the session layer reads to classify an exit.
func (p *lineagePTY) WaitErr() (error, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr, p.waitSet
}

// exit is the shell process ending: the outcome is recorded and THEN done is
// closed, the ordering the real pty and ssh watchers use.
func (p *lineagePTY) exit(err error) {
	p.mu.Lock()
	p.waitErr = err
	p.waitSet = true
	p.mu.Unlock()
	p.closeOnce.Do(func() { close(p.done) })
}

// emit produces output on this session's channel, the way a shell would.
func (p *lineagePTY) emit(s string) { p.out <- []byte(s) }

func (p *lineagePTY) sawInput(s string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Contains(string(p.written), s)
}

func (p *lineagePTY) sawResize(cols, rows uint16) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	want := sizeLabel(cols, rows)
	for _, got := range p.resizes {
		if got == want {
			return true
		}
	}
	return false
}

func sizeLabel(cols, rows uint16) string {
	return fmt.Sprintf("%dx%d", cols, rows)
}

// lineagePTYFactory hands every session its own channel and keeps them in
// open order, so a test can address "the parent's shell" and "the child's".
type lineagePTYFactory struct {
	mu   sync.Mutex
	made []*lineagePTY
}

func (f *lineagePTYFactory) NewPTY(context.Context, pty.Config) (pty.Pty, error) {
	p := newLineagePTY()
	f.mu.Lock()
	f.made = append(f.made, p)
	f.mu.Unlock()
	return p, nil
}

func (f *lineagePTYFactory) nth(t *testing.T, i int) *lineagePTY {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.made) {
		t.Fatalf("no channel was opened for session %d (only %d exist)", i, len(f.made))
	}
	return f.made[i]
}

func newLineageServer(t *testing.T, f *lineagePTYFactory) *WSServer {
	t.Helper()
	reg := session.New(log.NewSlogAdapter(nil), f)
	ws := NewWSServer(log.NewSlogAdapter(nil), reg)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return ws
}

// openLineageSession opens a session over conn, optionally claiming an
// opener, and waits until the open's tail has installed the subscriber — the
// state every later notification's emit-time lookup reads.
func openLineageSession(t *testing.T, ws *WSServer, conn *websocket.Conn, parent *openParentWire, id int) openWire {
	t.Helper()
	params := map[string]any{"cols": 80, "rows": 24}
	if parent != nil {
		params["parent"] = map[string]any{
			"sessionId":    parent.SessionID,
			"instanceId":   parent.InstanceID,
			"sessionEpoch": parent.SessionEpoch,
		}
	}
	raw, rpcErr := callOpen(t, conn, params, id)
	if rpcErr != nil {
		t.Fatalf("open: %+v", rpcErr)
	}
	got := decodeOpen(t, raw)
	awaitSubscriber(t, ws, session.ID(got.SessionID))
	return got
}

// refOf is the wire form of a session's full identity — what a child claims
// as its opener.
func wireRefOf(s openWire) *openParentWire {
	return &openParentWire{SessionID: s.SessionID, InstanceID: s.InstanceID, SessionEpoch: s.SessionEpoch}
}

// callExpectingRefusal drives a control method and returns the JSON-RPC error
// it was answered with, failing if the call SUCCEEDED — which is the shape of
// the violation under test.
func callExpectingRefusal(t *testing.T, conn *websocket.Conn, method string, params map[string]any, id int, what string) jsonrpcErrorObj {
	t.Helper()
	resp := jsonrpcCallWithID(t, conn, method, params, id)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("%s: unmarshal: %v", what, err)
	}
	if env.Error == nil {
		t.Fatalf("%s SUCCEEDED (result %s): lineage is not permission to act on another session", what, env.Result)
	}
	return *env.Error
}

// sendInput writes one data frame for sid, then fences it: the read loop
// handles a binary frame INLINE, so a response to a request written after it
// proves the frame has already been routed (or dropped).
func sendInput(t *testing.T, conn *websocket.Conn, sid, payload string, fenceSid string, fenceID int) {
	t.Helper()
	sidBytes, err := session.IDToBytes(session.ID(sid))
	if err != nil {
		t.Fatalf("IDToBytes: %v", err)
	}
	f := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: sidBytes, Payload: []byte(payload)}
	if err := conn.WriteMessage(websocket.BinaryMessage, f.Encode()); err != nil {
		t.Fatalf("write data frame: %v", err)
	}
	jsonrpcCallWithID(t, conn, "resize", map[string]any{"sessionId": fenceSid, "cols": 80, "rows": 24}, fenceID)
}

// collectPerSession reads every frame that arrives during budget and returns
// the payload bytes per session id. Used where the assertion is that nothing
// arrives for one session while something does for another: a per-session
// collector is what makes the negative half real, since a collector that
// filtered to one id would silently discard the frames the test is looking
// for.
func collectPerSession(r *wsReader, budget time.Duration) map[string]string {
	got := make(map[string]string)
	deadline := time.After(budget)
	for {
		select {
		case <-deadline:
			return got
		case f, ok := <-r.frames:
			if !ok {
				return got
			}
			got[string(session.IDFromBytes(f.SessionID))] += string(f.Payload)
		}
	}
}

// ── prohibition 1: lineage confers nothing ────────────────────────────────

// The whole of prohibition 1 in one run: a connection holding a session and
// NOTHING but a lineage edge to another tries, in turn, to close it, to
// resize it, to type into it, and to read its output. Every attempt is the
// violating call; every one is refused at the backend, and the child goes on
// living, answering its own connection, unresized and unread.
func TestLineage_AParentMayNotCloseDriveOrObserveItsChild(t *testing.T) {
	f := &lineagePTYFactory{}
	ws := newLineageServer(t, f)

	parentConn := connectWS(t, ws)
	t.Cleanup(func() { _ = parentConn.Close() })
	childConn := connectWS(t, ws)
	t.Cleanup(func() { _ = childConn.Close() })

	parent := openLineageSession(t, ws, parentConn, nil, 1)
	child := openLineageSession(t, ws, childConn, wireRefOf(parent), 1)
	// Without an admitted edge the rest of this test proves nothing: it would
	// be asserting that a stranger cannot drive a stranger.
	if child.Parent == nil || child.Parent.SessionID != parent.SessionID {
		t.Fatalf("the child was not admitted as %s's child (parent = %+v); the prohibition below would be vacuous",
			parent.SessionID, child.Parent)
	}
	parentPTY, childPTY := f.nth(t, 0), f.nth(t, 1)

	// (a) CLOSE. The parent asks the backend to end the session it opened.
	rpcErr := callExpectingRefusal(t, parentConn, "close",
		map[string]any{"sessionId": child.SessionID}, 2, "the parent closing its child")
	if rpcErr.Code != -32602 {
		t.Errorf("close refusal code = %d, want -32602", rpcErr.Code)
	}
	if _, err := ws.registry.Get(session.ID(child.SessionID)); err != nil {
		t.Fatalf("the child is gone from the registry after its parent asked for it to be closed: %v", err)
	}

	// (b) DRIVE. A resize is the smallest thing that changes another
	// session's world, and the channel is asked whether it ever arrived —
	// a refusal on the wire that still reached the shell would be worse
	// than no refusal at all.
	rpcErr = callExpectingRefusal(t, parentConn, "resize",
		map[string]any{"sessionId": child.SessionID, "cols": 200, "rows": 50}, 3, "the parent resizing its child")
	if rpcErr.Code != -32602 {
		t.Errorf("resize refusal code = %d, want -32602", rpcErr.Code)
	}
	if childPTY.sawResize(200, 50) {
		t.Error("the resize reached the child's channel: the wire refused it and the backend applied it anyway")
	}

	// (c) INPUT. The data plane is the one place a caller can act on a
	// session without a method name, so the prohibition has to hold there
	// too — and the positive control on the child's own connection is what
	// makes "the poison never arrived" a fact rather than an empty channel.
	const poison = "rm -rf /\n"
	const legit = "echo mine\n"
	sendInput(t, parentConn, child.SessionID, poison, parent.SessionID, 4)
	sendInput(t, childConn, child.SessionID, legit, child.SessionID, 2)
	waittest.WaitForTimeout(t, "the child's own input to reach its channel", wantWithin, func() bool {
		return childPTY.sawInput(legit)
	})
	if childPTY.sawInput(poison) {
		t.Error("input addressed to the child from its PARENT's connection reached the child's channel")
	}

	// (d) OBSERVE. Both shells speak; the child's connection hears the
	// child, and the parent's connection hears only itself.
	parentReader, childReader := newWSReader(parentConn), newWSReader(childConn)
	const childSays = "child-secret-output\n"
	const parentSays = "parent-own-output\n"
	childPTY.emit(childSays)
	parentPTY.emit(parentSays)

	onChild := collectPerSession(childReader, 2*time.Second)
	if !strings.Contains(onChild[child.SessionID], childSays) {
		t.Fatalf("the child's own connection never received the child's output (%q): the negative assertion below would be vacuous", onChild[child.SessionID])
	}
	onParent := collectPerSession(parentReader, 2*time.Second)
	if !strings.Contains(onParent[parent.SessionID], parentSays) {
		t.Fatalf("the parent's connection never received its OWN output (%q): this run cannot tell silence from a broken pump", onParent[parent.SessionID])
	}
	if got := onParent[child.SessionID]; got != "" {
		t.Errorf("the parent's connection received %d bytes of its child's output (%q): lineage is not a subscription", len(got), got)
	}
}

// The differential, and the assertion that cannot be satisfied by accident: a
// session reached by a lineage edge and a session reached by nothing at all
// are answered IDENTICALLY. An implementation that read the edge to decide
// what a caller may do would pass "a stranger is refused" and fail here,
// because the child would be answered differently from the stranger.
func TestLineage_IsNotAnInputToTheAddressabilityAnswer(t *testing.T) {
	f := &lineagePTYFactory{}
	ws := newLineageServer(t, f)

	parentConn := connectWS(t, ws)
	t.Cleanup(func() { _ = parentConn.Close() })
	otherConn := connectWS(t, ws)
	t.Cleanup(func() { _ = otherConn.Close() })

	parent := openLineageSession(t, ws, parentConn, nil, 1)
	child := openLineageSession(t, ws, otherConn, wireRefOf(parent), 1)
	stranger := openLineageSession(t, ws, otherConn, nil, 2)
	if child.Parent == nil {
		t.Fatal("the child carries no admitted edge; the comparison below is between two strangers")
	}
	if stranger.Parent != nil {
		t.Fatalf("the control session claims an opener (%+v); it is meant to be reachable by nothing", stranger.Parent)
	}

	onChild := callExpectingRefusal(t, parentConn, "close",
		map[string]any{"sessionId": child.SessionID}, 2, "the parent closing its child")
	onStranger := callExpectingRefusal(t, parentConn, "close",
		map[string]any{"sessionId": stranger.SessionID}, 3, "the parent closing a stranger")
	if onChild.Code != onStranger.Code || onChild.Message != onStranger.Message {
		t.Errorf("close answered a child (%d %q) differently from a stranger (%d %q): the edge is being read to decide what a caller may do",
			onChild.Code, onChild.Message, onStranger.Code, onStranger.Message)
	}

	onChild = callExpectingRefusal(t, parentConn, "resize",
		map[string]any{"sessionId": child.SessionID, "cols": 200, "rows": 50}, 4, "the parent resizing its child")
	onStranger = callExpectingRefusal(t, parentConn, "resize",
		map[string]any{"sessionId": stranger.SessionID, "cols": 200, "rows": 50}, 5, "the parent resizing a stranger")
	if onChild.Code != onStranger.Code || onChild.Message != onStranger.Message {
		t.Errorf("resize answered a child (%d %q) differently from a stranger (%d %q)",
			onChild.Code, onChild.Message, onStranger.Code, onStranger.Message)
	}

	// And the edge does not run the other way either. A child holds the
	// only copy of the edge naming its parent, and it buys exactly as much.
	up := callExpectingRefusal(t, otherConn, "close",
		map[string]any{"sessionId": parent.SessionID}, 6, "the child closing its parent")
	if up.Code != onStranger.Code {
		t.Errorf("close answered a parent from its child (%d) differently from a stranger (%d)", up.Code, onStranger.Code)
	}

	for _, id := range []string{parent.SessionID, child.SessionID, stranger.SessionID} {
		if _, err := ws.registry.Get(session.ID(id)); err != nil {
			t.Errorf("session %s is gone after a run of refused calls: %v", id, err)
		}
	}
}

// ── prohibition 2: a parent's death never closes its children ─────────────

// The first failure: the parent's shell exits. The child and the grandchild
// keep running, keep their edges, and keep taking input — and neither is told
// it has exited, because it has not.
func TestLineage_ParentProcessExitLeavesItsDescendantsAlive(t *testing.T) {
	f := &lineagePTYFactory{}
	ws := newLineageServer(t, f)

	parentConn := connectWS(t, ws)
	t.Cleanup(func() { _ = parentConn.Close() })
	childConn := connectWS(t, ws)
	t.Cleanup(func() { _ = childConn.Close() })

	parent := openLineageSession(t, ws, parentConn, nil, 1)
	child := openLineageSession(t, ws, childConn, wireRefOf(parent), 1)
	grandchild := openLineageSession(t, ws, childConn, wireRefOf(child), 2)
	childPTY, grandPTY := f.nth(t, 1), f.nth(t, 2)

	// The real shape a local shell's exit leaves in the watcher, not a
	// hand-built stand-in (realExitStatus, ws_exit_test.go).
	f.nth(t, 0).exit(realExitStatus(3))
	waittest.WaitForTimeout(t, "the parent's exit to be processed", wantWithin, func() bool {
		_, err := ws.registry.Get(session.ID(parent.SessionID))
		return err != nil
	})

	assertStillLive(t, ws, "the child", child)
	assertStillLive(t, ws, "the grandchild", grandchild)
	assertEdgeIntact(t, ws, child.SessionID, parent.SessionID)
	assertEdgeIntact(t, ws, grandchild.SessionID, child.SessionID)

	// Alive is not the same as usable: the descendants must still take
	// input, on the connection that holds them.
	sendInput(t, childConn, child.SessionID, "still here\n", child.SessionID, 3)
	sendInput(t, childConn, grandchild.SessionID, "so am i\n", grandchild.SessionID, 4)
	waittest.WaitForTimeout(t, "the child's input after its parent exited", wantWithin, func() bool {
		return childPTY.sawInput("still here")
	})
	waittest.WaitForTimeout(t, "the grandchild's input after its grandparent exited", wantWithin, func() bool {
		return grandPTY.sawInput("so am i")
	})
}

// The second failure: the link carrying the parent drops. Nothing here is a
// decision — a dropped socket says nothing about whether the work is wanted —
// so nothing ends, including the parent itself (AD-9: a session outlives
// every WebSocket).
func TestLineage_DroppedLinkLeavesTheParentsDescendantsAlive(t *testing.T) {
	f := &lineagePTYFactory{}
	ws := newLineageServer(t, f)

	parentConn := connectWS(t, ws)
	childConn := connectWS(t, ws)
	t.Cleanup(func() { _ = childConn.Close() })

	parent := openLineageSession(t, ws, parentConn, nil, 1)
	child := openLineageSession(t, ws, childConn, wireRefOf(parent), 1)
	childPTY := f.nth(t, 1)

	_ = parentConn.Close()
	waittest.WaitForTimeout(t, "the server to notice the dropped link", wantWithin, func() bool {
		ws.connsMu.Lock()
		defer ws.connsMu.Unlock()
		return len(ws.conns) == 1
	})

	assertStillLive(t, ws, "the child", child)
	assertEdgeIntact(t, ws, child.SessionID, parent.SessionID)
	if _, err := ws.registry.Get(session.ID(parent.SessionID)); err != nil {
		t.Errorf("the parent itself is gone after its link dropped: %v — a lost socket is not a close (AD-9)", err)
	}

	sendInput(t, childConn, child.SessionID, "unaffected\n", child.SessionID, 2)
	waittest.WaitForTimeout(t, "the child's input after its parent's link dropped", wantWithin, func() bool {
		return childPTY.sawInput("unaffected")
	})
}

// The fourth way, and the only one that is a decision: a human closes the
// parent's tab. Even then the backend closes exactly what it was asked to
// close. Whether the descendants should go too is a question for the person,
// which is why the renderer ASKS (lineage.ts / PaneManager.closePane) — and it
// can only ask because the backend never decided on their behalf.
func TestLineage_ExplicitCloseOfAParentLeavesItsDescendantsAlive(t *testing.T) {
	f := &lineagePTYFactory{}
	ws := newLineageServer(t, f)

	parentConn := connectWS(t, ws)
	t.Cleanup(func() { _ = parentConn.Close() })
	childConn := connectWS(t, ws)
	t.Cleanup(func() { _ = childConn.Close() })

	parent := openLineageSession(t, ws, parentConn, nil, 1)
	child := openLineageSession(t, ws, childConn, wireRefOf(parent), 1)
	grandchild := openLineageSession(t, ws, childConn, wireRefOf(child), 2)

	resp := jsonrpcCallWithID(t, parentConn, "close", map[string]any{"sessionId": parent.SessionID}, 2)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("close: unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("the holder could not close its own session: %+v", env.Error)
	}
	waittest.WaitForTimeout(t, "the parent's close", wantWithin, func() bool {
		_, err := ws.registry.Get(session.ID(parent.SessionID))
		return err != nil
	})

	assertStillLive(t, ws, "the child", child)
	assertStillLive(t, ws, "the grandchild", grandchild)
	assertEdgeIntact(t, ws, child.SessionID, parent.SessionID)
	assertEdgeIntact(t, ws, grandchild.SessionID, child.SessionID)
}

// assertStillLive: the registry holds the session and its own record does not
// say it has ended. Both halves matter — a session that is still in the map
// while its liveness reads terminal has been killed in the only way anything
// downstream would notice.
func assertStillLive(t *testing.T, ws *WSServer, what string, s openWire) {
	t.Helper()
	sess, err := ws.registry.Get(session.ID(s.SessionID))
	if err != nil {
		t.Fatalf("%s (%s) is gone from the registry: %v", what, s.SessionID, err)
	}
	if state := sess.Liveness(); state.Liveness.Terminal() {
		t.Errorf("%s reads as %q: it was never asked to end", what, state.Liveness)
	}
}

// assertEdgeIntact: the provenance record still names the same opener. The
// edge outlives its subject — a parent that dies leaves it exactly as it was
// — so a run that "kept the child alive" while clearing its edge would have
// answered the wrong question.
func assertEdgeIntact(t *testing.T, ws *WSServer, sid, wantParent string) {
	t.Helper()
	sess, err := ws.registry.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("session %s is gone: %v", sid, err)
	}
	edge, has := sess.Parent()
	if !has {
		t.Fatalf("session %s lost its parent edge when its parent died: provenance is not liveness", sid)
	}
	if string(edge.ID) != wantParent {
		t.Errorf("session %s now names %s as its opener, want %s", sid, edge.ID, wantParent)
	}
}
