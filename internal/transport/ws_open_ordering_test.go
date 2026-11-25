package transport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

const orderingSession = session.ID("0123456789abcdef0123456789abcdef")

func TestPaneBindingHeldAtOpenEnqueue(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	ws, _ := commitPointServer(t, ctx, reg, &commitPointOpener{reg: reg, id: orderingSession})
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	h := openHandlers{opener: ws.opener, sess: ws, log: logger}
	_, err := h.openInto(ctx, wc, newConnState(), commitPointParams(), func(result openResult) error {
		if ws.paneMu.TryLock() {
			ws.paneMu.Unlock()
			t.Error("pane retirement is not excluded at the open enqueue")
		}
		return wc.TryResult(json.RawMessage("1"), mustMarshal(result))
	})
	if err != nil {
		t.Fatal(err)
	}
	readAnswer(t, conn, 1)
}

func TestPaneRetiredBeforeOpenEnqueue(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	ws, sched := commitPointServer(t, ctx, reg, &commitPointOpener{reg: reg, id: orderingSession})
	steps := newOpenSteps()
	ws.openStep = steps.step
	hold := steps.holdAt("before-register")
	conn := connectWS(t, ws)
	openOverTheWire(t, conn, 1, helperOpenParams(helperOpenPane))
	steps.wait(t, "before-register")
	ws.closeSessionsForPanes(map[string]struct{}{helperOpenPane: {}})
	close(hold)
	steps.wait(t, "done")
	if steps.has("before-answer-enqueue") {
		t.Error("a retired pane reached its answer enqueue")
	}
	assertSessionLeftNothingWire(t, ws, reg, sched, orderingSession, "saved")
}

func TestWithdrawalAtSubscriberInstallKeepsOnlySession(t *testing.T) {
	for _, pane := range []string{"", helperOpenPane} {
		name := "no-pane"
		if pane != "" {
			name = "pane"
		}
		t.Run(name, func(t *testing.T) {
			ctx, logger := logtest.New(t)
			reg := session.New(logger, nil)
			ws, _ := commitPointServer(t, ctx, reg, &commitPointOpener{reg: reg, id: orderingSession})
			conn := connectWS(t, ws)
			wc := soleWSConn(t, ws)
			reqCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			entry := &requestCancel{cancel: cancel}
			reqCtx = context.WithValue(reqCtx, requestCancelContextKey{}, entry)
			ws.openStep = func(step string) {
				if step == "before-subscribe" {
					entry.withdraw()
				}
			}
			h := openHandlers{opener: ws.opener, sess: ws, log: logger}
			params := commitPointParams()
			params.PaneID, params.Cols, params.Rows = pane, 120, 40
			if _, err := h.openInto(reqCtx, wc, newConnState(), params, func(result openResult) error {
				return wc.TryResult(json.RawMessage("1"), mustMarshal(result))
			}); err != nil {
				t.Fatal(err)
			}
			readAnswer(t, conn, 1)
			sess, err := reg.Get(orderingSession)
			if err != nil {
				t.Fatal("the confirmed session did not survive withdrawal")
			}
			rx := ws.getRx(orderingSession)
			if rx == nil {
				t.Fatal("the confirmed session lost its receiver")
			}
			if sub, _ := rx.getSubscriber(); sub != nil {
				t.Error("the request installed a subscriber after withdrawal")
			}
			if !t.Failed() {
				waittest.WaitFor(t, "NoClient geometry after withdrawal", func() bool { return sess.EffectiveSize() == session.DefaultSize() })
			}
		})
	}
}

func TestCloserClaimRejectsLateOpenReceiver(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	counted := &gatedEndRegistry{Registry: reg, ends: make(map[session.ID]int), entered: make(chan struct{}), release: make(chan struct{})}
	ws, sched := commitPointServer(t, ctx, counted, &commitPointOpener{reg: reg, id: orderingSession})
	steps := newOpenSteps()
	ws.openStep = steps.step
	hold := steps.holdAt("before-receiver")
	conn := connectWS(t, ws)
	openOverTheWire(t, conn, 1, helperOpenParams(helperOpenPane))
	steps.wait(t, "before-receiver")
	closed := make(chan struct{})
	go func() {
		ws.closeSessionsForPanes(map[string]struct{}{helperOpenPane: {}})
		close(closed)
	}()
	select {
	case <-counted.entered:
	case <-time.After(wantWithin):
		t.Fatal("closer did not reach its registry end")
	}
	close(hold)
	steps.wait(t, "done")
	if steps.has("before-register") || ws.getRx(orderingSession) != nil {
		t.Error("open published a receiver after the closer claimed the end")
	}
	close(counted.release)
	select {
	case <-closed:
	case <-time.After(wantWithin):
		t.Fatal("closer did not finish")
	}
	assertSessionLeftNothingWire(t, ws, reg, sched, orderingSession, "saved")
	if n := counted.endsFor(orderingSession); n != 1 {
		t.Errorf("registry end calls = %d, want one", n)
	}
}

func TestRefusedOpenReleasesCloseMarker(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	ws, sched := commitPointServer(t, ctx, reg, &commitPointOpener{reg: reg, id: orderingSession})
	connectWS(t, ws)
	h := openHandlers{opener: ws.opener, sess: ws, log: logger}
	state := newConnState()
	_, err := h.openInto(ctx, soleWSConn(t, ws), state, commitPointParams(), func(openResult) error { return context.Canceled })
	if err == nil {
		t.Fatal("open succeeded despite enqueue refusal")
	}
	assertOpenLeftNothing(t, ws, reg, sched, orderingSession, state)
	ws.ringsMu.Lock()
	_, retained := ws.closing[orderingSession]
	ws.ringsMu.Unlock()
	if retained {
		t.Fatal("a refused open left a close marker with no exit monitor")
	}
}

func TestStopFenceHeldAtOpenEnqueue(t *testing.T) {
	for _, reconnect := range []bool{false, true} {
		name := "rpc"
		if reconnect {
			name = "reconnect"
		}
		t.Run(name, func(t *testing.T) {
			ctx, logger := logtest.New(t)
			reg := session.New(logger, nil)
			ws, _ := commitPointServer(t, ctx, reg, &commitPointOpener{reg: reg, id: orderingSession})
			conn := connectWS(t, ws)
			wc := soleWSConn(t, ws)
			checked := make(chan bool, 1)
			steps := newOpenSteps()
			ws.openStep = func(step string) {
				if step == "before-answer-enqueue" {
					acquired := ws.ringsMu.TryLock()
					if acquired {
						ws.ringsMu.Unlock()
					}
					checked <- !acquired
				}
				steps.step(step)
			}
			if reconnect {
				ws.bindPane(helperOpenPane, wc, newConnState(), commitPointParams())
				ws.paneRecordOf(helperOpenPane).bindingGeneration = 1
				if err := ws.ReconnectPane(ctx, helperOpenPane, 1); err != nil {
					t.Fatal(err)
				}
			} else {
				openOverTheWire(t, conn, 1, helperOpenParams(""))
				readAnswer(t, conn, 1)
				steps.wait(t, "done")
			}
			select {
			case fenced := <-checked:
				if !fenced {
					t.Fatal("Stop can mark the server stopped inside the answer interval")
				}
			default:
				t.Fatal("open never reached enqueue")
			}
		})
	}
}

func TestStopAfterDrainEndsUnconfirmedAdoptedOpen(t *testing.T) {
	for _, reconnect := range []bool{false, true} {
		name := "rpc"
		if reconnect {
			name = "reconnect"
		}
		t.Run(name, func(t *testing.T) {
			ctx, logger := logtest.New(t)
			reg := session.New(logger, nil)
			counted := &countingEndRegistry{Registry: reg, ends: make(map[session.ID]int)}
			ws, sched := settlementServer(t, ctx, counted, &commitPointOpener{reg: reg, id: orderingSession})
			steps := newOpenSteps()
			ws.openStep = steps.step
			hold := steps.holdAt("before-register")
			conn := connectWS(t, ws)
			wc := soleWSConn(t, ws)
			if reconnect {
				ws.bindPane(helperOpenPane, wc, newConnState(), commitPointParams())
				ws.paneRecordOf(helperOpenPane).bindingGeneration = 1
				go func() { _ = ws.ReconnectPane(ctx, helperOpenPane, 1) }()
			} else {
				openOverTheWire(t, conn, 1, helperOpenParams(""))
			}
			steps.wait(t, "before-register")
			// The adopted open cannot return until we release it, so Stop must
			// deal with its session without relying on the open's deferred cleanup.
			stopErr := ws.Stop(ctx)
			endsAtStop := counted.endsFor(orderingSession)
			close(hold)
			steps.wait(t, "done")
			if stopErr != nil {
				t.Fatal(stopErr)
			}
			if endsAtStop != 1 {
				t.Errorf("Stop made %d registry ends for the unconfirmed shell, want one", endsAtStop)
			}
			if n := counted.endsFor(orderingSession); n != 1 {
				t.Errorf("registry end calls after open returned = %d, want one", n)
			}
			if steps.has("committed") {
				t.Error("open committed after Stop")
			}
			assertSessionLeftNothingWire(t, ws, reg, sched, orderingSession, "saved")
		})
	}
}
