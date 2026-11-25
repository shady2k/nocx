package transport

// THE FOUR INTERVALS the review of the commit point named (ALX-1652,
// nocx-oalo1.6.16): a withdrawal or a stop against the answer's acceptance,
// a socket's teardown against the subscriber install, a newer attach against
// the departed client's geometry, and a closer's end against the open's own
// cleanup.
//
// Every one of them is a race between two goroutines of the real server, so
// every test here is driven over the real WebSocket and held at the seam where
// the interval actually is (ws.go's openStep). None of them waits on a
// duration: each waits on a fact the other party produces — the server's
// connection count, the step the open reached, the gate a closer is standing
// in — and the generous bound only catches a hang.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/discovery"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
)

// ‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾
// The step seam's test half
// ‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾

// openSteps records the steps one open reached and holds it where a test asked.
// A step's signal is buffered so an open never waits for a test that is not
// looking yet, and a hold is a channel the test closes to let the open go on.
type openSteps struct {
	mu      sync.Mutex
	seen    []string
	signals map[string]chan struct{}
	holds   map[string]chan struct{}
}

func newOpenSteps() *openSteps {
	return &openSteps{signals: map[string]chan struct{}{}, holds: map[string]chan struct{}{}}
}

func (s *openSteps) step(name string) {
	s.mu.Lock()
	s.seen = append(s.seen, name)
	sig, ok := s.signals[name]
	if !ok {
		sig = make(chan struct{}, 4)
		s.signals[name] = sig
	}
	hold := s.holds[name]
	s.mu.Unlock()
	select {
	case sig <- struct{}{}:
	default:
	}
	if hold != nil {
		<-hold
	}
}

// has reports whether the open reached name. Safe once the open's "done" has
// been observed, which is the only way a test reads it.
func (s *openSteps) has(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, seen := range s.seen {
		if seen == name {
			return true
		}
	}
	return false
}

// holdAt stops the open at name until the returned channel is closed. It must
// be called before the open can reach the step: every caller sets its holds up
// before it writes the open request.
func (s *openSteps) holdAt(name string) chan struct{} {
	hold := make(chan struct{})
	s.mu.Lock()
	s.holds[name] = hold
	s.mu.Unlock()
	return hold
}

// wait blocks until the open reaches name. The bound is a hang guard, not a
// timing assumption: the step is produced by the test's own action.
func (s *openSteps) wait(t *testing.T, name string) {
	t.Helper()
	s.mu.Lock()
	sig, ok := s.signals[name]
	if !ok {
		sig = make(chan struct{}, 4)
		s.signals[name] = sig
	}
	s.mu.Unlock()
	select {
	case <-sig:
	case <-time.After(wantWithin):
		t.Fatalf("the open never reached %q; steps seen: %v", name, s.snapshot())
	}
}

func (s *openSteps) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// ‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾
// The two helpers the intervals need
// ‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾

// epochGateSource holds the open inside its access-epoch read — the last read
// before the answer is built — and reports when the request's own context ends
// underneath it. That is the exact moment the review's first remark is about:
// everything is registered, nothing is answered yet, and a withdrawal or a stop
// arrives.
type epochGateSource struct {
	entered  chan struct{}
	released chan struct{}
	once     sync.Once
}

func (s *epochGateSource) Intent(context.Context, string, proto.IntentParams) (proto.IntentResult, error) {
	return proto.IntentResult{}, nil
}

func (s *epochGateSource) AccessEpoch(ctx context.Context, _ string) (uint64, error) {
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()
	close(s.released)
	return 0, ctx.Err()
}

// gatedEndRegistry counts every registry-level end and holds the first one
// inside EndSession itself, before delegating. The review's fourth remark is
// about that exact place: a closer that has entered the end but not yet reached
// its transport teardown, and an open's cleanup that reads the receiver and
// decides the end is still nobody's.
type gatedEndRegistry struct {
	session.Registry
	mu      sync.Mutex
	ends    map[session.ID]int
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *gatedEndRegistry) EndSession(id session.ID) error {
	r.mu.Lock()
	r.ends[id]++
	r.mu.Unlock()
	// Only the FIRST end waits: the gate exists to hold a closer inside the
	// end while an open's cleanup runs, and a second end must pass through so
	// the test can observe it rather than deadlock on it.
	first := false
	r.once.Do(func() { first = true })
	if first && r.entered != nil {
		close(r.entered)
		<-r.release
	}
	return r.Registry.EndSession(id)
}

func (r *gatedEndRegistry) endsFor(id session.ID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ends[id]
}

// openOverTheWire sends one helper-hosted open and leaves it running. The
// answer is the caller's to read, and its arrival is the proof the request
// reached the server; nothing here waits on a duration.
func openOverTheWire(t *testing.T, conn *websocket.Conn, id int, params map[string]any) {
	t.Helper()
	raw := mustMarshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "open", "params": params})
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("write open: %v", err)
	}
}

// readAnswer reads the answer to id off the real socket and returns its result
// payload, failing on a refusal.
func readAnswer(t *testing.T, conn *websocket.Conn, id int) openResult {
	t.Helper()
	raw, err := awaitFrame(conn, time.Now().Add(wantWithin), isResponseTo(id))
	if err != nil {
		t.Fatalf("read the answer to %d: %v", id, err)
	}
	var answer struct {
		Result openResult       `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatalf("decode the answer to %d: %v\nraw: %s", id, err, raw)
	}
	if answer.Error != nil {
		t.Fatalf("the open was refused: %+v", answer.Error)
	}
	return answer.Result
}

// withdrawRequest sends the notification a renderer sends when the tab it asked
// for is gone.
func withdrawRequest(t *testing.T, conn *websocket.Conn, id int) {
	t.Helper()
	raw := mustMarshal(map[string]any{"jsonrpc": "2.0", "method": "rpc.cancel", "params": map[string]any{"id": id}})
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("write rpc.cancel: %v", err)
	}
}

// assertSessionLeftNothingWire states "nothing remains" for an open refused
// over the wire. It is assertOpenLeftNothing without the connection's own
// ownership map, which handleSession owns privately — the wire test asserts the
// server-owned halves, which are the ones a leak would show up in.
func assertSessionLeftNothingWire(t *testing.T, ws *WSServer, reg session.Registry, sched *discovery.Scheduler, sid session.ID, profileID string) {
	t.Helper()
	if sessions := reg.List(); len(sessions) != 0 {
		t.Errorf("the refused open left %d sessions", len(sessions))
	}
	if ws.getRx(sid) != nil {
		t.Errorf("the refused open left a receiver/ring")
	}
	ws.lifecycleMu.Lock()
	for lane, owner := range ws.lifecycleLanes {
		if owner == sid {
			t.Errorf("the refused open left lifecycle lane %q", lane)
		}
	}
	ws.lifecycleMu.Unlock()
	ws.integrationMu.Lock()
	_, integrated := ws.integrations[sid]
	_, bounded := ws.integrationStartupBounds[sid]
	ws.integrationMu.Unlock()
	if integrated || bounded {
		t.Errorf("the refused open left its integration entry: entry=%v bound=%v", integrated, bounded)
	}
	if profileID != "" {
		if target := sched.Status(profileID); target.Host != "" {
			t.Errorf("the refused open left a discovery target: %+v", target)
		}
	}
}

// helperOpenParams is the open these intervals run: a helper-hosted shell with
// a lifecycle lane and an integration entry, so every registration an open makes
// exists to be checked for.
func helperOpenParams(paneID string) map[string]any {
	params := map[string]any{"kind": "ssh", "profileId": "saved", "cols": 80, "rows": 24}
	if paneID != "" {
		params["paneId"] = paneID
	}
	return params
}

// ‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾
// 1. A withdrawal before the answer leaves nothing
// ‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾

// TestWithdrawnBeforeTheAnswerLeavesNothing: the renderer gives up while the
// open is still reading the epoch — everything is registered, nothing is
// answered. The withdrawal is ordered with the acceptance, so the answer is
// never written and the session, its ring, its lane, its integration entry and
// its discovery target all go.
//
// It is red on 1e3289f28: the cancelled request still reaches the live socket's
// queue, the queue answers, and the branch after the answer keeps the session.
func TestWithdrawnBeforeTheAnswerLeavesNothing(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	const sid = session.ID("0123456789abcdef0123456789abcdef")
	opener := &commitPointOpener{reg: reg, id: sid, lane: lifecycle.LaneID("withdrawn-before-answer")}
	source := &epochGateSource{entered: make(chan struct{}), released: make(chan struct{})}
	ws, sched := commitPointServer(t, ctx, reg, opener, WithPaneIntentSource(source))
	steps := newOpenSteps()
	ws.openStep = steps.step
	conn := connectWS(t, ws)

	openOverTheWire(t, conn, 1, helperOpenParams(""))
	<-source.entered

	withdrawRequest(t, conn, 1)
	// The source returns only once the request's own context has ended, so
	// this is the withdrawal reaching the handler rather than a pause.
	select {
	case <-source.released:
	case <-time.After(wantWithin):
		t.Fatal("the withdrawal never reached the open's context")
	}

	steps.wait(t, "done")
	if steps.has("committed") {
		t.Fatal("the answer was accepted for a request withdrawn before it")
	}
	assertSessionLeftNothingWire(t, ws, reg, sched, sid, "saved")
}

// ‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾
// 2. A departed socket is never the subscriber, and its geometry never wins
// ‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾

// TestDepartedSocketNeverBecomesTheSubscriber: the answer is already on the
// wire when the socket goes, so the session is the server's (AD-9) — but this
// connection must not become its subscriber after its own teardown has walked
// the sessions and found the slot empty.
//
// It is red on 1e3289f28 for the open with no pane: the socket-gone fact is
// sampled before the install and checked again after it, so a teardown that runs
// in between leaves the dead connection in the slot and its measurement
// governing the session.
func TestDepartedSocketNeverBecomesTheSubscriber(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	const sid = session.ID("0123456789abcdef0123456789abcdef")
	opener := &commitPointOpener{reg: reg, id: sid, lane: lifecycle.LaneID("departed-socket")}
	ws, _ := commitPointServer(t, ctx, reg, opener)
	steps := newOpenSteps()
	ws.openStep = steps.step
	hold := steps.holdAt("before-subscribe")
	conn := connectWS(t, ws)

	openOverTheWire(t, conn, 1, helperOpenParams(""))
	if got := readAnswer(t, conn, 1); got.SessionID != string(sid) {
		t.Fatalf("the answer named %q, the opener minted %q", got.SessionID, sid)
	}
	steps.wait(t, "before-subscribe")

	// The tab's socket goes while the open is held between the answer and the
	// subscriber install, and the teardown runs to its end.
	_ = conn.Close()
	waittest.WaitFor(t, "the departed socket's teardown to finish", func() bool {
		ws.connsMu.Lock()
		defer ws.connsMu.Unlock()
		return len(ws.conns) == 0
	})
	close(hold)
	steps.wait(t, "done")

	sessions := reg.List()
	if len(sessions) != 1 || sessions[0].ID() != sid {
		t.Fatalf("the answered open's session did not survive its socket: %d sessions", len(sessions))
	}
	rx := ws.getRx(sid)
	if rx == nil {
		t.Fatal("the answered open's ring was torn down with its socket")
	}
	if sub, _ := rx.getSubscriber(); sub != nil {
		t.Fatal("the departed socket is still the session's subscriber")
	}
	waittest.WaitFor(t, "the departed client's geometry to stop governing the session", func() bool {
		return sessions[0].EffectiveSize() == session.DefaultSize()
	})
}

// TestANewerAttachKeepsItsGeometryAcrossADepartedSocket: the departed client's
// measurement is handed back only while the slot is still empty. A window that
// attached in the meantime owns the geometry, and a late NoClient must not
// overwrite it with the default.
//
// It is red on 1e3289f28: the open reports NoClient unconditionally once it has
// decided its socket is gone, so the report lands after the new attach's own
// size and replaces a 120x40 terminal with the default. The record of the
// report itself is the test's standing assertion — the geometry the lane ends
// on is the same 120x40 either way, because a later report coalesces the
// earlier one; what must never happen is the report being made at all.
func TestANewerAttachKeepsItsGeometryAcrossADepartedSocket(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	const sid = session.ID("0123456789abcdef0123456789abcdef")
	opener := &commitPointOpener{reg: reg, id: sid, lane: lifecycle.LaneID("newer-attach")}
	ws, _ := commitPointServer(t, ctx, reg, opener)
	steps := newOpenSteps()
	ws.openStep = steps.step
	hold := steps.holdAt("before-socket-check")
	conn := connectWS(t, ws)

	openOverTheWire(t, conn, 1, helperOpenParams(""))
	readAnswer(t, conn, 1)
	steps.wait(t, "before-socket-check")

	// The request is withdrawn and the withdrawal's own answer proves it was
	// applied: the handler answers only after the request's context has ended.
	// The session is the server's now (the answer is already the wire's), so
	// the open keeps it and drops only this connection's part.
	cancel := jsonrpcCallWithID(t, conn, "rpc.cancel", map[string]any{"id": 1}, 3)
	var cancelled struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(cancel, &cancelled); err != nil || cancelled.Error != nil {
		t.Fatalf("rpc.cancel was refused: %v %+v", err, cancelled.Error)
	}

	// A new window takes the session and measures itself while the open is
	// still holding the departed client's report.
	conn2 := connectWS(t, ws)
	defer func() { _ = conn2.Close() }()
	attach := jsonrpcCallWithID(t, conn2, "attach", map[string]any{
		"sessionId": string(sid), "cols": 120, "rows": 40,
	}, 2)
	var attached struct {
		Result struct {
			Resumed bool `json:"resumed"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(attach, &attached); err != nil {
		t.Fatalf("decode the attach answer: %v", err)
	}
	if attached.Error != nil {
		t.Fatalf("attach to the answered open's session: %+v", attached.Error)
	}

	close(hold)
	steps.wait(t, "done")

	sessions := reg.List()
	if len(sessions) != 1 {
		t.Fatalf("the session did not survive: %d sessions", len(sessions))
	}
	if steps.has("no-client-reported") {
		t.Fatal("the departed client's measurement was handed back over a newer attach's")
	}
	if sub, _ := ws.getRx(sid).getSubscriber(); sub == nil {
		t.Fatal("the newer attach was not left holding the session")
	}
	waittest.WaitFor(t, "the newer attach's geometry to govern the session", func() bool {
		return sessions[0].EffectiveSize() == (session.Size{Cols: 120, Rows: 40})
	})
}

// ‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾
// 3. Stop is ordered with the answer, both ways
// ‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾

// TestStopBeforeTheAnswerLeavesNothing: Stop joins the open before it tears its
// sessions down, so an open that has not answered yet is cancelled, refused and
// dismantled by its own cleanup — never answered after the server has closed the
// session the answer would name.
//
// It is red on 1e3289f28: the open is not registered with the shutdown set, Stop
// returns without it, and the open then answers success through the still-live
// hijacked socket for a session Stop has already removed.
func TestStopBeforeTheAnswerLeavesNothing(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	const sid = session.ID("0123456789abcdef0123456789abcdef")
	opener := &commitPointOpener{reg: reg, id: sid, lane: lifecycle.LaneID("stop-before-answer")}
	source := &epochGateSource{entered: make(chan struct{}), released: make(chan struct{})}
	ws, sched := settlementServer(t, ctx, reg, opener, WithPaneIntentSource(source))
	steps := newOpenSteps()
	ws.openStep = steps.step
	var stopOnce sync.Once
	stop := func() error {
		var err error
		stopOnce.Do(func() { err = ws.Stop(ctx) })
		return err
	}
	t.Cleanup(func() { _ = stop() })
	conn := connectWS(t, ws)

	openOverTheWire(t, conn, 1, helperOpenParams(""))
	<-source.entered

	stopped := make(chan struct{})
	go func() {
		_ = stop()
		close(stopped)
	}()
	select {
	case <-source.released:
	case <-time.After(wantWithin):
		t.Fatal("Stop never reached the open")
	}
	select {
	case <-stopped:
	case <-time.After(wantWithin):
		t.Fatal("Stop never returned")
	}

	// Stop joined the open, so its end is settled by the time Stop returns.
	if steps.has("committed") {
		t.Fatal("the open answered after Stop had begun closing its session")
	}
	assertSessionLeftNothingWire(t, ws, reg, sched, sid, "saved")
}

// TestStopAfterTheAnswerReleasesTheSessionOnce: the same fence, the other way
// round. An open whose answer is already the wire's is not torn down by its own
// cleanup when Stop arrives — Stop detaches the session (AD-9/D1) and the open
// keeps what it made until the coordinator's own teardown takes it.
func TestStopAfterTheAnswerReleasesTheSessionOnce(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	const sid = session.ID("0123456789abcdef0123456789abcdef")
	opener := &commitPointOpener{reg: reg, id: sid, lane: lifecycle.LaneID("stop-after-answer")}
	ws, _ := settlementServer(t, ctx, reg, opener)
	steps := newOpenSteps()
	ws.openStep = steps.step
	var stopOnce sync.Once
	stop := func() error {
		var err error
		stopOnce.Do(func() { err = ws.Stop(ctx) })
		return err
	}
	t.Cleanup(func() { _ = stop() })
	conn := connectWS(t, ws)

	openOverTheWire(t, conn, 1, helperOpenParams(""))
	readAnswer(t, conn, 1)
	steps.wait(t, "done")
	if !steps.has("committed") {
		t.Fatal("the answer was not accepted although the wire carried it")
	}

	_ = stop()
	if steps.has("teardown") {
		t.Fatal("the answering open tore its own session down at shutdown")
	}
}

// settlementServer is commitPointServer without its Stop cleanup: the two tests
// above decide for themselves when the server stops, and a second Stop from a
// cleanup would be measuring that instead.
func settlementServer(t *testing.T, ctx context.Context, reg session.Registry, opener *commitPointOpener, extra ...WSServerOption) (*WSServer, *discovery.Scheduler) {
	t.Helper()
	_, logger := logtest.New(t)
	sched := discovery.NewScheduler(&portsFakeConnector{}, logger, discovery.WithSettleDelay(time.Hour))
	t.Cleanup(func() { _ = sched.Close() })
	opts := append([]WSServerOption{
		WithDiscoveryScheduler(sched),
		WithHelperSessionOpener(opener),
		WithProfileResolver(&fakeResolver{resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
			return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
		}}),
	}, extra...)
	ws := NewWSServer(logger, reg, opts...)
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return ws, sched
}

// ‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾
// 4. One end, one set of registrations
// ‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾

// TestACloserInsideEndSessionEndsItOnce: a pane closer has entered the registry
// end and not yet reached its transport teardown; the open's own cleanup runs in
// that gap. One end, whoever got there first — the open's cleanup must not call
// EndSession for a session a closer is already ending.
//
// It is red on 1e3289f28: the cleanup decides ownership by looking the receiver
// up, and the closer has not removed it yet, so both ends run.
func TestACloserInsideEndSessionEndsItOnce(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	counted := &gatedEndRegistry{Registry: reg, ends: make(map[session.ID]int), entered: make(chan struct{}), release: make(chan struct{})}
	const sid = session.ID("0123456789abcdef0123456789abcdef")
	source := &epochGateSource{entered: make(chan struct{}), released: make(chan struct{})}
	opener := &commitPointOpener{reg: reg, id: sid, lane: lifecycle.LaneID("closer-inside-end")}
	ws, _ := settlementServer(t, ctx, counted, opener, WithPaneIntentSource(source))
	steps := newOpenSteps()
	ws.openStep = steps.step
	hold := steps.holdAt("teardown")
	conn := connectWS(t, ws)

	// The request is withdrawn, so the open fails before its answer and reaches
	// its own cleanup while everything it made is still registered.
	openOverTheWire(t, conn, 1, helperOpenParams(helperOpenPane))
	<-source.entered
	withdrawRequest(t, conn, 1)
	select {
	case <-source.released:
	case <-time.After(wantWithin):
		t.Fatal("the withdrawal never reached the open's context")
	}
	steps.wait(t, "teardown")

	// The pane leaves: its closer claims the end and stands inside EndSession.
	closerDone := make(chan struct{})
	go func() {
		ws.closeSessionsForPanes(map[string]struct{}{helperOpenPane: {}})
		close(closerDone)
	}()
	select {
	case <-counted.entered:
	case <-time.After(wantWithin):
		t.Fatal("the closer never entered EndSession")
	}
	// Now let the open's cleanup run, and then let go of the end itself.
	close(hold)
	steps.wait(t, "done")
	close(counted.release)
	select {
	case <-closerDone:
	case <-time.After(wantWithin):
		t.Fatal("the closer never finished")
	}

	if n := counted.endsFor(sid); n != 1 {
		t.Fatalf("the session was ended %d times, want exactly once", n)
	}
	if sessions := reg.List(); len(sessions) != 0 {
		t.Fatalf("the ended session was left behind: %d", len(sessions))
	}
}

// TestACloserThatFinishedBeforeTheOpenRegisteredLeavesNothing: the closer ends
// the session and finishes its teardown while the open is still registering. The
// open then adds its lane, its discovery target and its integration entry —
// after the teardown that would have removed them — and must take them back
// itself, because no later owner ever looks at that session again.
//
// It is red on 1e3289f28: the cleanup reads a receiver that is already gone and
// returns, leaving the lane, the integration entry and the discovery target
// behind for the life of the process.
func TestACloserThatFinishedBeforeTheOpenRegisteredLeavesNothing(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	const sid = session.ID("0123456789abcdef0123456789abcdef")
	opener := &commitPointOpener{reg: reg, id: sid, lane: lifecycle.LaneID("closer-before-register")}
	ws, sched := settlementServer(t, ctx, reg, opener)
	steps := newOpenSteps()
	ws.openStep = steps.step
	hold := steps.holdAt("before-register")
	conn := connectWS(t, ws)

	openOverTheWire(t, conn, 1, helperOpenParams(helperOpenPane))
	steps.wait(t, "before-register")

	// The pane leaves and its closer finishes completely before the open
	// registers anything.
	ws.closeSessionsForPanes(map[string]struct{}{helperOpenPane: {}})
	close(hold)

	steps.wait(t, "done")
	assertSessionLeftNothingWire(t, ws, reg, sched, sid, "saved")
}
