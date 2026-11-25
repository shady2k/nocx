package transport

// THE COMMIT POINT (nocx-oalo1.6.16): the open answer accepted by the socket's
// outbound queue. Before it an open that loses its request or its socket is
// torn down entire; after it the session is the server's (AD-9) and only this
// connection's part may go away. These tests hold the boundary still — the ack
// itself blocks them — rather than racing it, so each one states one fact about
// one side of the point.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/discovery"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
)

type openCommitOutcome struct {
	result openResult
	err    error
}

// TestOpenAnsweredThenSocketGoneKeepsTheSession is the red one: a person opens
// a tab, the renderer has its sessionId, and the socket drops in the next
// instant. The session is the server's from the moment the answer was queued,
// so the registry keeps it, the ring stays open for the next attach, output
// written while nobody held it arrives there, and the shell's exit is still
// observed.
func TestOpenAnsweredThenSocketGoneKeepsTheSession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		paneID string
	}{
		{name: "no pane", paneID: ""},
		{name: "pane", paneID: helperOpenPane},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, logger := logtest.New(t)
			factory := &activeFactory{}
			reg := session.New(logger, factory)
			ws := NewWSServer(logger, reg)
			if err := ws.Start(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ws.Stop(ctx) })

			conn := connectWS(t, ws)
			wc := soleWSConn(t, ws)
			state := newConnState()
			params := openParams{PaneID: tc.paneID, Cols: 100, Rows: 30}

			openCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			enqueued := make(chan struct{})
			release := make(chan struct{})
			out := make(chan openCommitOutcome, 1)
			h := openHandlers{opener: ws.opener, sess: ws, log: logger}
			go func() {
				result, err := h.openInto(openCtx, wc, state, params, func(result openResult) error {
					err := wc.TryResult(json.RawMessage("1"), mustMarshal(result))
					close(enqueued)
					<-release
					return err
				})
				out <- openCommitOutcome{result: result, err: err}
			}()

			// The answer is on the REAL socket: reading it is both the proof
			// that the queue accepted it and where the session id comes from.
			<-enqueued
			if err := conn.SetReadDeadline(time.Now().Add(wantWithin)); err != nil {
				t.Fatal(err)
			}
			_, raw, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read the open answer: %v", err)
			}
			var answer struct {
				Result openResult       `json:"result"`
				Error  *jsonrpcErrorObj `json:"error"`
			}
			if err := json.Unmarshal(raw, &answer); err != nil {
				t.Fatalf("decode the open answer: %v\nraw: %s", err, raw)
			}
			if answer.Error != nil {
				t.Fatalf("the answer was refused: %+v", answer.Error)
			}
			sid := session.ID(answer.Result.SessionID)
			if sid == "" {
				t.Fatal("the open answer carried no sessionId")
			}

			// The socket goes and the request is withdrawn while the answer is
			// still held at the barrier.
			_ = conn.Close()
			cancel()
			close(release)

			got := <-out
			if got.err != nil {
				t.Fatalf("the answered open failed after its socket went: %v", got.err)
			}
			if got.result.SessionID != string(sid) {
				t.Fatalf("open reported %s, the socket carried %s", got.result.SessionID, sid)
			}
			waittest.WaitFor(t, "the server to observe the disconnect", func() bool {
				ws.connsMu.Lock()
				defer ws.connsMu.Unlock()
				return len(ws.conns) == 0
			})

			sessions := reg.List()
			if len(sessions) != 1 || sessions[0].ID() != sid {
				t.Fatalf("the answered open's session did not survive its socket: %d sessions", len(sessions))
			}
			sess := sessions[0]
			rx := ws.getRx(sid)
			if rx == nil {
				t.Fatal("the answered open's ring was torn down with its socket")
			}
			// A departed socket is never left as the subscriber, and its
			// measurement stops governing a session it no longer holds.
			if sub, _ := rx.getSubscriber(); sub != nil {
				t.Fatal("the departed socket is still the session's subscriber")
			}
			waittest.WaitFor(t, "the departed client's geometry to stop governing the session", func() bool {
				return sess.EffectiveSize() == session.DefaultSize()
			})

			// Output produced while no client held the session stays in the
			// ring for the next attach.
			pty := factory.pty()
			if pty == nil {
				t.Fatal("the open created no channel")
			}
			const detached = "written while no client held it"
			go func() { _, _ = pty.pw.Write([]byte(detached + "\n")) }()
			waittest.WaitFor(t, "the ring to hold the output written while detached", func() bool {
				return rx.ring.writtenLocked() > 0
			})

			conn2 := connectWS(t, ws)
			defer func() { _ = conn2.Close() }()
			attach := jsonrpcCallWithID(t, conn2, "attach", map[string]any{
				"sessionId": string(sid), "cols": 120, "rows": 40,
			}, 2)
			var attached struct {
				Result struct {
					Resumed bool   `json:"resumed"`
					Reset   bool   `json:"reset"`
					From    uint64 `json:"from"`
				} `json:"result"`
				Error *jsonrpcErrorObj `json:"error"`
			}
			if err := json.Unmarshal(attach, &attached); err != nil {
				t.Fatalf("decode the attach answer: %v", err)
			}
			if attached.Error != nil {
				t.Fatalf("attach to the answered open's session: %+v", attached.Error)
			}
			if _, err := awaitFrame(conn2, time.Now().Add(wantWithin), func(msg []byte) bool {
				return len(msg) > FrameHeaderSize && msg[1] == MsgTypeData &&
					strings.Contains(string(msg[FrameHeaderSize:]), detached)
			}); err != nil {
				t.Fatalf("the output written while nobody was attached never arrived: %v", err)
			}

			// The exit monitor was installed by the open, so the shell's own
			// exit is still observed after the client that opened it is gone.
			_ = pty.Close()
			waittest.WaitFor(t, "the shell's exit to be observed", func() bool {
				return len(reg.List()) == 0
			})
		})
	}
}

// TestOpenRefusedByTheOutboundQueueLeavesNothing: the queue refusing the answer
// is the same failure as never reaching it. A refused answer must leave no
// session, receiver, lifecycle lane, integration entry or discovery target.
func TestOpenRefusedByTheOutboundQueueLeavesNothing(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	const sid = session.ID("0123456789abcdef0123456789abcdef")
	opener := &commitPointOpener{reg: reg, id: sid, lane: lifecycle.LaneID("refused-answer")}
	ws, sched := commitPointServer(t, ctx, reg, opener)
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	_ = conn.Close()
	waittest.WaitFor(t, "the closed socket to be unregistered", func() bool {
		ws.connsMu.Lock()
		defer ws.connsMu.Unlock()
		return len(ws.conns) == 0
	})

	state := newConnState()
	h := openHandlers{opener: ws.opener, sess: ws, log: logger}
	answered := false
	_, err := h.openInto(ctx, wc, state, commitPointParams(), func(result openResult) error {
		answered = true
		return wc.TryResult(json.RawMessage("1"), mustMarshal(result))
	})
	if err == nil {
		t.Fatal("the open reported success although the queue refused its answer")
	}
	if !answered {
		t.Fatal("the open never attempted its answer")
	}
	assertOpenLeftNothing(t, ws, reg, sched, sid, state)
	if started, aborted := opener.lifecycleState(); started || !aborted {
		t.Fatalf("refused answer lifecycle: started=%v aborted=%v", started, aborted)
	}
}

// TestOpenCancelledBeforeTheAnswerLeavesNothing: a request withdrawn while the
// session is still being dialed answered nobody, so the session, its lane and
// the lifecycle delivery it reserved all go.
func TestOpenCancelledBeforeTheAnswerLeavesNothing(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	const sid = session.ID("0123456789abcdef0123456789abcdef")
	opener := &commitPointOpener{
		reg: reg, id: sid, lane: lifecycle.LaneID("withdrawn-before-answer"),
		entered: make(chan struct{}), resume: make(chan struct{}),
	}
	ws, sched := commitPointServer(t, ctx, reg, opener)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	wc := soleWSConn(t, ws)

	openCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	state := newConnState()
	h := openHandlers{opener: ws.opener, sess: ws, log: logger}
	out := make(chan openCommitOutcome, 1)
	answered := false
	go func() {
		result, err := h.openInto(openCtx, wc, state, commitPointParams(), func(openResult) error {
			answered = true
			return nil
		})
		out <- openCommitOutcome{result: result, err: err}
	}()
	<-opener.entered
	cancel()
	close(opener.resume)

	got := <-out
	if got.err == nil {
		t.Fatal("a withdrawn open reported success")
	}
	if answered {
		t.Fatal("a withdrawn open answered the request")
	}
	assertOpenLeftNothing(t, ws, reg, sched, sid, state)
	if started, aborted := opener.lifecycleState(); started || !aborted {
		t.Fatalf("withdrawn open lifecycle: started=%v aborted=%v", started, aborted)
	}
}

// TestPaneClosedAfterTheAnswerEndsTheSessionOnce: the pane leaving after the
// answer is not a lost socket. It ends the session, and it does so once —
// whoever claims the teardown first, the pane's closer or the open's own
// cleanup, and never both.
func TestPaneClosedAfterTheAnswerEndsTheSessionOnce(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	counted := &countingEndRegistry{Registry: reg, ends: make(map[session.ID]int)}
	const sid = session.ID("0123456789abcdef0123456789abcdef")
	opener := &commitPointOpener{reg: reg, id: sid, lane: lifecycle.LaneID("pane-closed-after-answer")}
	ws, sched := commitPointServer(t, ctx, counted, opener)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	wc := soleWSConn(t, ws)

	openCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	steps := newOpenSteps()
	ws.openStep = steps.step
	release := steps.holdAt("committed")
	state := newConnState()
	out := make(chan openCommitOutcome, 1)
	h := openHandlers{opener: ws.opener, sess: ws, log: logger}
	go func() {
		result, err := h.openInto(openCtx, wc, state, commitPointParams(), func(result openResult) error {
			return wc.TryResult(json.RawMessage("1"), mustMarshal(result))
		})
		out <- openCommitOutcome{result: result, err: err}
	}()
	steps.wait(t, "committed")
	if err := conn.SetReadDeadline(time.Now().Add(wantWithin)); err != nil {
		t.Fatal(err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read the open answer: %v", err)
	}
	var answer struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatalf("decode the open answer: %v", err)
	}
	if answer.Error != nil {
		t.Fatalf("the answer was refused: %+v", answer.Error)
	}

	// The pane leaves at the post-answer barrier, so the open
	// meets the loss after the commit point rather than before it.
	ws.closeSessionsForPanes(map[string]struct{}{helperOpenPane: {}})
	close(release)

	got := <-out
	if got.err == nil {
		t.Fatal("an open whose pane left after its answer reported success")
	}
	assertOpenLeftNothing(t, ws, reg, sched, sid, state)
	if n := counted.endsFor(sid); n != 1 {
		t.Fatalf("the session ended %d times, want exactly once", n)
	}
	if started, _ := opener.lifecycleState(); started {
		t.Fatal("a pane that left after the answer started lifecycle delivery")
	}
}

// commitPointParams is the open these tests commit: a helper-hosted shell for a
// pane, so the session carries a lifecycle lane and an integration entry.
func commitPointParams() openParams {
	return openParams{PaneID: helperOpenPane, Kind: "ssh", ProfileID: "saved", Cols: 80, Rows: 24}
}

func commitPointServer(t *testing.T, ctx context.Context, reg session.Registry, opener *commitPointOpener, extra ...WSServerOption) (*WSServer, *discovery.Scheduler) {
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
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return ws, sched
}

// assertOpenLeftNothing states the whole of "nothing remains" for an open that
// never reached its commit point.
func assertOpenLeftNothing(t *testing.T, ws *WSServer, reg session.Registry, sched *discovery.Scheduler, sid session.ID, state *connState) {
	t.Helper()
	if sessions := reg.List(); len(sessions) != 0 {
		t.Errorf("the open left %d sessions", len(sessions))
	}
	state.mu.Lock()
	_, owned := state.sessions[sid]
	state.mu.Unlock()
	if owned {
		t.Error("the open left the session in the connection's ownership")
	}
	if ws.getRx(sid) != nil {
		t.Error("the open left a receiver/ring")
	}
	ws.lifecycleMu.Lock()
	for lane, owner := range ws.lifecycleLanes {
		if owner == sid {
			t.Errorf("the open left lifecycle lane %q", lane)
		}
	}
	ws.lifecycleMu.Unlock()
	ws.integrationMu.Lock()
	_, integrated := ws.integrations[sid]
	_, bounded := ws.integrationStartupBounds[sid]
	ws.integrationMu.Unlock()
	if integrated || bounded {
		t.Errorf("the open left its integration entry: entry=%v bound=%v", integrated, bounded)
	}
	if target := sched.Status("saved"); target.Host != "" {
		t.Errorf("the open left a discovery target: %+v", target)
	}
}

// commitPointOpener adopts one helper-hosted session and reports the whole
// shape an open commits, so a test can watch all of it go when the answer is
// not accepted. entered and resume, when set, hold the dial still: the session
// is adopted from a background context, because an external spawn can complete
// after the request that asked for it is gone.
type commitPointOpener struct {
	reg     *session.Reg
	id      session.ID
	lane    lifecycle.LaneID
	entered chan struct{}
	resume  chan struct{}

	mu      sync.Mutex
	started bool
	aborted bool
}

func (o *commitPointOpener) OpenHosted(ctx context.Context, cfg session.Config, _ string) (HostedSessionOpen, bool, error) {
	if ctx.Value(paneReconnectOpenKey{}) != nil {
		BindPaneConnection(ctx, 2)
	}
	if o.entered != nil {
		close(o.entered)
	}
	if o.resume != nil {
		<-o.resume
	}
	sess, err := o.reg.Adopt(context.Background(), cfg, o.id, newExitEndableChannel())
	if err != nil {
		return HostedSessionOpen{}, true, err
	}
	return HostedSessionOpen{
		Session:           sess,
		Host:              cfg.Host,
		Account:           "alice",
		Generation:        "commit-point",
		LifecycleLane:     o.lane,
		IntegrationStatus: IntegrationStarting,
		IntegrationShell:  "bash",
		StartLifecycle: func() {
			o.mu.Lock()
			o.started = true
			o.mu.Unlock()
		},
		AbortLifecycle: func() {
			o.mu.Lock()
			o.aborted = true
			o.mu.Unlock()
		},
	}, true, nil
}

func (o *commitPointOpener) lifecycleState() (started, aborted bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.started, o.aborted
}

// countingEndRegistry counts the registry-level end of each session, which is
// where "the session ends once" is observable: the registry's own second call
// for an id it no longer holds never reaches the session.
type countingEndRegistry struct {
	session.Registry
	mu   sync.Mutex
	ends map[session.ID]int
}

func (r *countingEndRegistry) EndSession(id session.ID) error {
	r.mu.Lock()
	r.ends[id]++
	r.mu.Unlock()
	return r.Registry.EndSession(id)
}

func (r *countingEndRegistry) endsFor(id session.ID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ends[id]
}
