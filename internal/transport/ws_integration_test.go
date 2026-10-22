package transport

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport/outbound"
	"github.com/shady2k/nocx/internal/waittest"
)

// The session integration axis (nocx-dvql). The contract tests prove the
// shape and the handshake-timeout path off the real socket; these prove the
// transitions the product's badge and card depend on, and the paths where
// the honest answer is to say nothing at all.

// awaitIntegration waits for the session to REACH a status, skipping frames
// that report a status it already had.
//
// Skipping them is not leniency, it is the rule: a test waits on a state
// change, and a re-send of the current state is not one. The transport
// re-sends deliberately — the status is a state, replayed on reattach and
// emitted again by the open handler after its ack (AD-7) — and because
// openSession returns as soon as it reads that ack, the handler's emit can
// land at any later moment. It landed after a test had already moved the axis
// on the emulated Linux container, where the handler is slower than the test,
// and read as "the wrong status arrived" (nocx-6au4).
//
// A status that never arrives still fails, on readIntegrationWhere's bound.
func awaitIntegration(t *testing.T, conn *websocket.Conn, sid, want string) integrationChangedParams {
	t.Helper()
	_, p := readIntegrationWhere(t, conn, sid, "the session to reach "+want,
		func(p integrationChangedParams) bool { return p.Status == want })
	return p
}

// readIntegration reads the next session.integrationChanged for a session,
// whatever it says. Correct only where the very next fact is the one meant —
// see readIntegrationWhere for why that is rarely true on this axis.
func readIntegration(t *testing.T, conn *websocket.Conn, sid string) integrationChangedParams {
	t.Helper()
	_, p := readIntegrationWhere(t, conn, sid, "the next session.integrationChanged",
		func(integrationChangedParams) bool { return true })
	return p
}

// readIntegrationWhere answers with the first session.integrationChanged for
// this session that `want` accepts, RAW and decoded. Raw because the contract
// tests validate the bytes the server actually sent, and re-marshalling the
// decoded struct would prove the struct rather than the wire.
//
// The predicate is the whole point, and naming one is not optional here. This
// axis has FIVE emitters for one session — the open handler after its ack, a
// reattach's replay, the bootstrap outcome, a transport loss, the kernel's
// live domain — so how many facts a sequence puts on the wire is not the
// test's to choose. In particular a test that registers the axis after `open`
// has RETURNED is racing the handler's own emit (integrationPTYFactory below
// says why, and is the other half of this answer): on an idle machine the
// handler emits nothing and there are two frames, under load the test wins and
// there are three, with an extra `starting` sitting exactly where the outcome
// was expected. So "wait for a session.integrationChanged" cannot mean "the
// one that says how it ended", and neither can "wait for the second" — a
// predicate is the only form of the question that survives a sixth emitter.
//
// ONE ABSOLUTE DEADLINE covers the whole search rather than one per frame: a
// loop that re-armed wantWithin for each frame it skipped would have no bound
// at all, which is the defect ws_inbox_test.go's own comment records from the
// other direction.
func readIntegrationWhere(t *testing.T, conn *websocket.Conn, sid, what string,
	want func(integrationChangedParams) bool,
) (json.RawMessage, integrationChangedParams) {
	t.Helper()
	deadline := time.Now().Add(wantWithin)
	for {
		msg, err := awaitFrame(conn, deadline, isNotification("session.integrationChanged"))
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		f, _ := decodeFrame(msg)
		var p integrationChangedParams
		if derr := json.Unmarshal(f.Params, &p); derr != nil {
			t.Fatalf("decode session.integrationChanged: %v\nraw: %s", derr, f.Params)
		}
		if p.SessionID == sid && want(p) {
			return f.Params, p
		}
	}
}

// integrationConcluded accepts the fact that ENDS the honest `starting`
// interval: the one that says how the attempt came out.
//
// It names the outcome by what MAKES it the outcome — the axis has stopped
// saying "still trying" — rather than by the status the caller expects. That
// leaves the caller's own assertion on the status something to say, so a
// product that concluded with the wrong terminal word is reported as a wrong
// word rather than as a frame that never arrived.
func integrationConcluded(p integrationChangedParams) bool {
	return p.Status != IntegrationStarting
}

// integrationEnv boots a server with a lifecycle publisher, opens a session,
// registers a lane and enters the session into the axis as `starting` — the
// state every attempted integration begins in.
type integrationEnv struct {
	*lifecycleTestEnv
	pub  *lifecyclepub.Publisher
	sid  string
	lane lifecycle.LaneID
	h    lifecycle.DomainHandle
}

// integrationPTYFactory registers the session's integration axis from INSIDE
// the open, exactly where the production local factory registers it — it is
// the only thing that knows which binary it exec'd, so it is the only thing
// that may answer.
//
// It matters here for a second reason, which is why the env stopped
// registering the axis itself: the open handler emits the session's status
// once, after the ack. A test that registers the axis after `open` RETURNS is
// racing that emit — on an idle machine the handler wins and emits nothing,
// under load the test wins and the handler emits a second `starting` frame
// that the next assertion reads instead of the transition it is waiting for.
// Registering inside the open makes the count exactly one, always.
type integrationPTYFactory struct {
	stub *pty.Stub
	ws   atomic.Pointer[WSServer]
}

func (f *integrationPTYFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	if ws := f.ws.Load(); ws != nil && cfg.SessionID != "" {
		ws.RegisterIntegration(session.ID(cfg.SessionID), "/bin/bash", IntegrationStarting, ssh.ReasonNone, "")
	}
	return f.stub, nil
}

func newIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()
	return newIntegrationEnvTuned(t, nil)
}

// newIntegrationEnvTuned is the same env with a last look at the server BEFORE
// the session opens, which is the only moment a bound can be shortened: the
// startup bound is armed by the registration that entering the axis performs
// (ws_integration.go's armStartupBoundLocked), so a value written after the
// open would arrive after the timer it was meant to shorten.
func newIntegrationEnvTuned(t *testing.T, tune func(*WSServer)) *integrationEnv {
	t.Helper()
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	logger := log.NewSlogAdapter(nil)
	f := &integrationPTYFactory{stub: pty.NewStub(logger)}
	e := newLifecycleTestEnvWithReg(t, session.New(logger, f), WithLifecyclePublisher(pub))
	f.ws.Store(e.ws)
	pub.SetEmitter(e.ws)
	if tune != nil {
		tune(e.ws)
	}
	sid := e.openSession(t, 1)
	const lane = lifecycle.LaneID("lane-1")
	e.ws.RegisterLifecycleLane(lane, session.ID(sid))
	if err := pub.BindTransport("T", noopPort{}); err != nil {
		t.Fatal(err)
	}
	h, err := pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	if first := readIntegration(t, e.conn, sid); first.Status != IntegrationStarting {
		t.Fatalf("first status = %q, want starting", first.Status)
	}
	return &integrationEnv{lifecycleTestEnv: e, pub: pub, sid: sid, lane: lane, h: h}
}

// establish drives the handshake to a live domain, the way a shell would,
// and returns the integration status the hello produced.
//
// The status frame is read BEFORE the establishment acknowledgement, because
// that is the order the server writes them: the axis is updated from the
// published fact and emitted first, and the lifecycle.changed fact follows on
// the same socket. Reading them the other way round makes the establishment
// helper swallow the status frame.
func (e *integrationEnv) establish(t *testing.T) integrationChangedParams {
	t.Helper()
	mustLifecycleIngest(t, e.pub, "T", lifecycleEnv(e.lane, e.h, 1, lifecycleHelloEvt()))
	got := awaitIntegration(t, e.conn, e.sid, IntegrationIntegrated)
	ackEstablishmentFrom(t, e.pub, e.lane, e.h, e.conn)
	return got
}

// A live domain is the kernel's own word that this session integrated, and
// the axis follows it rather than re-deriving it.
func TestIntegration_LiveDomainReportsIntegrated(t *testing.T) {
	e := newIntegrationEnv(t)
	got := e.establish(t)
	if got.Status != IntegrationIntegrated {
		t.Errorf("status = %q, want integrated", got.Status)
	}
	if got.Reason != "" {
		t.Errorf("reason = %q, want none: an integrated session has nothing to explain", got.Reason)
	}
	if got.Shell != "/bin/bash" {
		t.Errorf("shell = %q, want the launch's own answer, unrevised", got.Shell)
	}
}

// A channel that dies AFTER the session integrated is `lost`, not
// `conventional`. The same transport loss means different things either side
// of establishment, and only the session knows which side it is on: a user
// whose blocks stopped appearing mid-session is not in the same situation as
// one whose shell never answered.
func TestIntegration_LossAfterEstablishmentIsLost(t *testing.T) {
	e := newIntegrationEnv(t)
	if got := e.establish(t); got.Status != IntegrationIntegrated {
		t.Fatalf("status = %q, want integrated before the loss", got.Status)
	}

	e.ws.NoteIntegrationLoss(e.lane, "end-of-stream")
	got := readIntegration(t, e.conn, e.sid)
	if got.Status != IntegrationLost {
		t.Errorf("status = %q, want lost", got.Status)
	}
	if got.Reason != string(ssh.ReasonChannelLost) {
		t.Errorf("reason = %q, want channel-lost", got.Reason)
	}
}

// A descriptor that ends before the shell ever proved itself is `unknown`,
// not `handshake-timeout`. The backend genuinely cannot say which, and
// claiming a bound expired when it did not is the invented confidence this
// surface exists to avoid — `unknown` is a real, visible answer.
func TestIntegration_LossBeforeEstablishmentWithoutTimeoutIsUnknown(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.NoteIntegrationLoss(e.lane, "end-of-stream")
	got := awaitIntegration(t, e.conn, e.sid, IntegrationConventional)
	if got.Reason != string(ssh.ReasonUnknown) {
		t.Errorf("reason = %q, want unknown", got.Reason)
	}
}

// The session's own disposal is not a degrade. A tab closing must not paint
// itself broken on the way out — the badge would flash on every close and
// teach the user that the badge means nothing.
func TestIntegration_SessionDisposalSaysNothing(t *testing.T) {
	e := newIntegrationEnv(t)
	if got := e.establish(t); got.Status != IntegrationIntegrated {
		t.Fatalf("status = %q, want integrated", got.Status)
	}

	e.ws.NoteIntegrationLoss(e.lane, LossCauseClosed)
	if leaked := tryReadNotification(t, e.conn, "session.integrationChanged", 300*time.Millisecond); leaked != nil {
		t.Errorf("a closing session announced a degrade: %s", leaked)
	}
}

// A session that never asked for integration is never registered, and
// therefore never speaks. Absence is how "conventional by design" is
// expressed; a badge on a raw tab the user chose is noise.
func TestIntegration_UnregisteredSessionSaysNothing(t *testing.T) {
	e := newLifecycleTestEnv(t)
	sid := e.openSession(t, 1)
	e.ws.emitIntegration(session.ID(sid))
	if leaked := tryReadNotification(t, e.conn, "session.integrationChanged", 300*time.Millisecond); leaked != nil {
		t.Errorf("a session that requested no integration announced a status: %s", leaked)
	}
}

// The status is a state, not an event (AD-9). A frontend that reconnects
// after the handshake expired must learn it is in a conventional terminal —
// no further transition is ever coming to tell it, so the replay is the only
// thing that can.
func TestIntegration_ReattachReplaysTheCurrentStatus(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.NoteIntegrationLoss(e.lane, LossCauseHelloTimeout)
	if got := awaitIntegration(t, e.conn, e.sid, IntegrationConventional); got.Reason != string(ssh.ReasonHandshakeTimeout) {
		t.Fatalf("reason = %q, want handshake-timeout", got.Reason)
	}

	connB := connectWS(t, e.ws)
	defer func() { _ = connB.Close() }()
	resp := jsonrpcCallWithID(t, connB, "attach", map[string]any{"sessionId": e.sid, "offset": 0}, 2)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("attach: %+v", envelope.Error)
	}
	got := awaitIntegration(t, connB, e.sid, IntegrationConventional)
	if got.Reason != string(ssh.ReasonHandshakeTimeout) {
		t.Errorf("replayed status = %+v, want conventional/handshake-timeout", got)
	}
}

// A session that WAS integrated and then lost its channel is `lost`, and the
// loss arm that answers it sits ahead of every reason a session that never
// integrated could be given. A session whose blocks stopped mid-command must
// never be told its startup did not return.
func TestIntegration_HavingBeenLiveOutranksEveryOtherLossReason(t *testing.T) {
	e := newIntegrationEnv(t)
	if got := e.establish(t); got.Status != IntegrationIntegrated {
		t.Fatalf("status = %q, want integrated before the loss", got.Status)
	}
	e.ws.NoteIntegrationLoss(e.lane, "end-of-stream")
	got := readIntegration(t, e.conn, e.sid)
	if got.Status != IntegrationLost || got.Reason != string(ssh.ReasonChannelLost) {
		t.Errorf("status/reason = %q/%q, want lost/channel-lost", got.Status, got.Reason)
	}
}

// A loss for a lane nobody registered resolves to no session and is dropped,
// rather than reaching for a session id it does not have.
func TestIntegration_LossOnAnUnknownLaneIsDropped(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.NoteIntegrationLoss(lifecycle.LaneID("lane-nobody"), LossCauseHelloTimeout)
	if leaked := tryReadNotification(t, e.conn, "session.integrationChanged", 300*time.Millisecond); leaked != nil {
		t.Errorf("an unregistered lane produced a status: %s", leaked)
	}
}

func TestIntegration_HelloTimeoutDuringOpenSurvivesLaneRegistration(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.unregisterLifecycleLanes(session.ID(e.sid))
	e.ws.integrationMu.Lock()
	delete(e.ws.integrations, session.ID(e.sid))
	e.ws.integrationMu.Unlock()
	e.ws.BeginLifecycleLane(e.lane)

	// The adapter can report a timeout before Open has returned the helper's
	// session id. The lane is not mapped, and the session is not yet on the
	// integration axis; the result must be applied when the open is adopted.
	e.ws.NoteIntegrationLoss(e.lane, LossCauseHelloTimeout)
	e.ws.RegisterLifecycleLane(e.lane, session.ID(e.sid))
	e.ws.RegisterIntegration(session.ID(e.sid), "/bin/bash", IntegrationStarting, ssh.ReasonNone, "")
	e.ws.emitIntegration(session.ID(e.sid))

	got := awaitIntegration(t, e.conn, e.sid, IntegrationConventional)
	if got.Reason != string(ssh.ReasonHandshakeTimeout) {
		t.Fatalf("startup failure reason = %q, want %q", got.Reason, ssh.ReasonHandshakeTimeout)
	}
}

func TestIntegration_LossResumingAfterTeardownDoesNotRequeueForDeadSession(t *testing.T) {
	e := newIntegrationEnv(t)
	sid := session.ID(e.sid)

	// Capture exactly the lookup result NoteIntegrationLoss holds while it is
	// descheduled. Teardown then removes both the lane and status before the
	// reporter resumes at the guarded apply step.
	e.ws.lifecycleMu.Lock()
	resolvedSID, ok := e.ws.lifecycleLanes[e.lane]
	e.ws.lifecycleMu.Unlock()
	if !ok || resolvedSID != sid {
		t.Fatalf("lane lookup = %q, %t; want %q", resolvedSID, ok, sid)
	}
	e.ws.unregisterLifecycleLanes(sid)
	e.ws.unregisterIntegration(sid)
	e.ws.applyOrQueueIntegrationLoss(e.lane, resolvedSID, LossCauseHelloTimeout)

	e.ws.lifecycleMu.Lock()
	_, orphaned := e.ws.pendingSessionLoss[sid]
	_, stillMapped := e.ws.lifecycleLanes[e.lane]
	e.ws.lifecycleMu.Unlock()
	if orphaned || stillMapped {
		t.Fatalf("teardown left stale loss state: pending=%t mapped=%t", orphaned, stillMapped)
	}
	e.ws.integrationMu.Lock()
	_, stillRegistered := e.ws.integrations[sid]
	e.ws.integrationMu.Unlock()
	if stillRegistered {
		t.Fatal("teardown left the dead session on the integration axis")
	}
}

func TestIntegration_AbandoningAnOpenDiscardsItsPendingTimeout(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.unregisterLifecycleLanes(session.ID(e.sid))
	e.ws.BeginLifecycleLane(e.lane)
	e.ws.NoteIntegrationLoss(e.lane, LossCauseHelloTimeout)
	e.ws.AbandonLifecycleLane(e.lane)

	// This lane was never adopted by a session. Even if a later fixture uses
	// the same identifier, the abandoned open's timeout must not leak into it.
	e.ws.RegisterLifecycleLane(e.lane, session.ID(e.sid))
	e.ws.RegisterIntegration(session.ID(e.sid), "/bin/bash", IntegrationStarting, ssh.ReasonNone, "")
	e.ws.emitIntegration(session.ID(e.sid))
	got := awaitIntegration(t, e.conn, e.sid, IntegrationStarting)
	if got.Reason != "" {
		t.Fatalf("abandoned open left reason %q on a later session", got.Reason)
	}
}

// ── the startup bound (nocx-1ebrs.20) ─────────────────────────────────────

// timedIntegrationAnswer is one session.integrationChanged as it ARRIVED,
// with how long after the collection began it did. The offset is the point:
// "the axis did not give up before its bound" is a statement about when, and
// the only other evidence of it would be a sleep that asserts itself.
type timedIntegrationAnswer struct {
	status string
	reason string
	shell  string
	at     time.Duration
}

// integrationAnswersWithin collects every session.integrationChanged for one
// session that arrives inside a fixed window, and stops at the window's end.
//
// ONE READER, ONE WINDOW, and it goes through the inbox (awaitFrame) so a
// frame this collection is not about is retained for the next wait rather than
// eaten: a gorilla connection whose read has timed out stays failed, so a
// window read and a subsequent wait cannot both exist on one socket
// (ws_inbox_test.go's own comment, and the reason wantWithin's history is
// three false readings).
func integrationAnswersWithin(t *testing.T, conn *websocket.Conn, sid string, window time.Duration) []timedIntegrationAnswer {
	t.Helper()
	start := time.Now()
	deadline := start.Add(window)
	var answers []timedIntegrationAnswer
	for {
		msg, err := awaitFrame(conn, deadline, func([]byte) bool { return true })
		if err != nil {
			return answers
		}
		f, _ := decodeFrame(msg)
		if f.Method != "session.integrationChanged" {
			continue
		}
		var p integrationChangedParams
		if derr := json.Unmarshal(f.Params, &p); derr != nil {
			t.Fatalf("decode session.integrationChanged: %v\nraw: %s", derr, f.Params)
		}
		if p.SessionID != sid {
			continue
		}
		answers = append(answers, timedIntegrationAnswer{
			status: p.Status, reason: p.Reason, shell: p.Shell, at: time.Since(start),
		})
	}
}

// The session integration axis has ONE bound of its own, and it is the reason
// a pane can never sit at "Checking this shell…" for ever.
//
// THE TRANSPORT BELOW IS DELIBERATELY SILENT: no adapter, no publisher, no
// loss, nothing that could answer. That is the state the defect is about — the
// pane reads `starting`, hides its terminal and drops every keystroke
// (frontend/src/integration/status.ts's isAwaitingIntegration is the only read
// and terminal-content.ts's onSessionIntent refuses on it) — and every finer
// detector in the product lives somewhere else and may be broken. Measured
// before this bound existed: the axis stayed `starting` until the session was
// destroyed, which is the pane that waits until the person quits.
func TestIntegration_StartupBoundEndsAnIntervalNothingElseWould(t *testing.T) {
	const bound = 1100 * time.Millisecond
	e := newIntegrationEnvTuned(t, func(ws *WSServer) { ws.integrationStartupBound = bound })

	answers := integrationAnswersWithin(t, e.conn, e.sid, 2*bound)

	// ONE CONCLUSION, AND ONLY ONE. `starting` may be re-sent — the status is a
	// state and the transport re-sends it — so the assertion is on the answers
	// that CONCLUDE the axis: exactly one, and it is the bound's own word,
	// `handshake-timeout`, which is also what the adapter's own expiry
	// produces because it is the same event from where the person sits.
	var concluded []timedIntegrationAnswer
	for _, a := range answers {
		if a.status != IntegrationStarting {
			concluded = append(concluded, a)
		}
	}
	if len(concluded) != 1 {
		t.Fatalf("the axis concluded %d times, want exactly once: %+v", len(concluded), answers)
	}
	got := concluded[0]
	if got.status != IntegrationConventional || got.reason != string(ssh.ReasonHandshakeTimeout) {
		t.Errorf("the axis concluded %q/%q, want conventional/%q", got.status, got.reason, ssh.ReasonHandshakeTimeout)
	}
	if got.shell != "/bin/bash" {
		t.Errorf("shell = %q, want the launch's own answer, unrevised", got.shell)
	}
	// AND NOT BEFORE ITS BOUND. The offset is measured from the start of the
	// collection, which is a few milliseconds after the registration armed the
	// timer, so the exact edge is not this test's to assert — what it asserts
	// is the property that matters: the axis waited for the bound instead of
	// giving up on the shell immediately.
	if got.at < bound/2 {
		t.Errorf("the axis concluded %s after the collection began, well inside its %s bound: it gave up on a shell that still had time",
			got.at, bound)
	}
	// The same conclusion wakes whoever is holding for the axis: a caller in
	// AwaitIntegration must not wait out its own deadline for a session the
	// transport has already answered.
	out, err := e.ws.AwaitIntegration(t.Context(), session.ID(e.sid))
	if err != nil {
		t.Fatalf("AwaitIntegration after the bound: %v", err)
	}
	if !out.Registered || out.Status != IntegrationConventional || out.Reason != ssh.ReasonHandshakeTimeout {
		t.Fatalf("AwaitIntegration = %+v, want the conventional answer the bound published", out)
	}
}

// The bound is a NET, not a verdict: a detector that describes the same
// startup may still land its more specific word after it, because the net only
// says "nothing arrived".
//
// WITHOUT THIS A SLOW STARTUP WOULD LOSE ITS DIAGNOSIS. The typed `ssh` path
// re-opens the axis as `starting` and concludes it from its own bootstrap,
// which is bounded at 60 s — a far host slower than one handshake bound would
// otherwise keep the net's vaguer word and never see the reason it actually
// produced (internal/app/typed_line.go's reportOutcome).
func TestIntegration_ASpecificAnswerCorrectsTheBoundsNet(t *testing.T) {
	cases := []struct {
		name string
		// report lands the detector's answer and returns what the axis says.
		report func(e *integrationEnv) (integrationChangedParams, bool)
		want   ssh.RefusalReason
	}{
		{
			name: "the process observation",
			report: func(e *integrationEnv) (integrationChangedParams, bool) {
				e.ws.NoteShellReplaced(session.ID(e.sid), "kiro-cli-term")
				return awaitIntegration(t, e.conn, e.sid, IntegrationConventional), true
			},
			want: ssh.ReasonStartupDidNotReturn,
		},
		{
			name: "the bootstrap's own outcome",
			report: func(e *integrationEnv) (integrationChangedParams, bool) {
				e.ws.NoteBootstrapOutcome(e.lane, ssh.ReasonPublishUnavailable)
				return awaitIntegration(t, e.conn, e.sid, IntegrationConventional), true
			},
			want: ssh.ReasonPublishUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const bound = 200 * time.Millisecond
			e := newIntegrationEnvTuned(t, func(ws *WSServer) { ws.integrationStartupBound = bound })
			// The net lands first, and the test reads it, so what follows is a
			// CORRECTION of an answer already on the wire rather than a race.
			if got := awaitIntegration(t, e.conn, e.sid, IntegrationConventional); got.Reason != string(ssh.ReasonHandshakeTimeout) {
				t.Fatalf("the bound said %q, want %q", got.Reason, ssh.ReasonHandshakeTimeout)
			}
			got, _ := tc.report(e)
			if got.Reason != string(tc.want) {
				t.Errorf("the answer after the net = %q, want %q: a detector that knows what happened outranks one bound that ran out", got.Reason, tc.want)
			}
			out, err := e.ws.AwaitIntegration(t.Context(), session.ID(e.sid))
			if err != nil || out.Reason != tc.want {
				t.Fatalf("AwaitIntegration = %+v, %v; want %q", out, err, tc.want)
			}
		})
	}
}

// And the reverse is forbidden: an answer a TRANSPORT reported is evidence,
// and the net never existed for it. This is the existing rule — a session
// already answered keeps its first answer — restated where the net could have
// loosened it.
func TestIntegration_TheNetDoesNotLoosenTheFirstAnswerRule(t *testing.T) {
	const bound = 200 * time.Millisecond
	e := newIntegrationEnvTuned(t, func(ws *WSServer) { ws.integrationStartupBound = bound })
	// A real transport verdict, before the bound can have expired.
	e.ws.NoteIntegrationLoss(e.lane, "end-of-stream")
	if got := awaitIntegration(t, e.conn, e.sid, IntegrationConventional); got.Reason != string(ssh.ReasonUnknown) {
		t.Fatalf("the loss said %q, want %q", got.Reason, ssh.ReasonUnknown)
	}
	e.ws.NoteShellReplaced(session.ID(e.sid), "kiro-cli-term")
	e.ws.NoteBootstrapOutcome(e.lane, ssh.ReasonPublishUnavailable)
	out, err := e.ws.AwaitIntegration(t.Context(), session.ID(e.sid))
	if err != nil || out.Reason != ssh.ReasonUnknown {
		t.Fatalf("a later detector replaced a transport's own verdict: %+v, %v", out, err)
	}
}

// A session that answers is not revisited when the bound it was given would
// have expired: the timer belongs to the question, and the answer cancels it.
//
// This is the pair to the test above, and it is the one that catches a bound
// which degrades a working pane one bound into every good session.
func TestIntegration_StartupBoundSaysNothingOnceTheAxisAnswered(t *testing.T) {
	const bound = 300 * time.Millisecond
	e := newIntegrationEnvTuned(t, func(ws *WSServer) { ws.integrationStartupBound = bound })
	if got := e.establish(t); got.Status != IntegrationIntegrated {
		t.Fatalf("status = %q, want integrated before the bound could expire", got.Status)
	}
	// READ FOR LONGER THAN THE BOUND ITSELF: silence after the answer is the
	// assertion, and a window shorter than the bound would prove nothing.
	for _, a := range integrationAnswersWithin(t, e.conn, e.sid, 4*bound) {
		if a.status == IntegrationConventional || a.status == IntegrationLost {
			t.Fatalf("a session that integrated was degraded %s into its bound: %+v", a.at, a)
		}
	}
	out, err := e.ws.AwaitIntegration(t.Context(), session.ID(e.sid))
	if err != nil || out.Status != IntegrationIntegrated {
		t.Fatalf("AwaitIntegration = %+v, %v; want the integrated answer, untouched", out, err)
	}
}

// ── the process observation (nocx-cgzc, nocx-viil.3) ──────────────────────

// The bead's own sentence: a takeover is reported in well under a second,
// and not when the handshake bound expires. Nothing here advances a clock —
// the observation IS the trigger, which is the whole point of having a second
// detector.
func TestIntegration_ShellReplacedIsReportedWithoutWaitingForTheBound(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.NoteShellReplaced(session.ID(e.sid), "kiro-cli-term")
	got := awaitIntegration(t, e.conn, e.sid, IntegrationConventional)
	// The reason is what HAPPENED, not what noticed. The helper exec's the
	// login shell itself, so a second exec on that pid before the hello can
	// only have come from inside the shell's own startup, and it is reached
	// here milliseconds after the fork instead of ten seconds later. Naming
	// the bound would name the detector, and since this answer lands first
	// and the first answer wins, the vaguer word would be the one the user is
	// left with.
	if got.Reason != string(ssh.ReasonStartupDidNotReturn) {
		t.Errorf("reason = %q, want startup-did-not-return: the stage, reached now rather than at the bound", got.Reason)
	}
	if got.Detail == nil || got.Detail.ObservedProcess != "kiro-cli-term" {
		t.Errorf("detail = %+v, want the observed executable's name", got.Detail)
	}
}

// answersAfter collects every session.integrationChanged for a session that
// arrives in a short window and returns the DISTINCT answers in them.
//
// The tests below cannot assert that nothing arrives at all, and the reason is
// the harness rather than the code: openSession returns as soon as the ack is
// read, while the open handler goes on to emit the session's CURRENT status
// after that ack (AD-7), on its own goroutine. So a re-send of a status a test
// has already read can land at any moment, and one did — on the emulated
// Linux container, where the handler is slower than the test.
//
// That re-send is not a defect and must not be asserted away: the status is a
// STATE, replayed on reattach for exactly this reason, and re-sending a state
// says nothing new. What a test may assert — and what the user experiences —
// is that nothing says anything DIFFERENT.
func answersAfter(t *testing.T, conn *websocket.Conn, sid string) []string {
	t.Helper()
	seen := map[string]bool{}
	var answers []string
	for {
		raw := tryReadNotification(t, conn, "session.integrationChanged", 300*time.Millisecond)
		if raw == nil {
			return answers
		}
		var p integrationChangedParams
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("decode session.integrationChanged: %v\nraw: %s", err, raw)
		}
		if p.SessionID != sid {
			continue
		}
		answer := p.Status + "/" + p.Reason
		if p.Detail != nil {
			answer += "/" + p.Detail.ObservedProcess
		}
		if !seen[answer] {
			seen[answer] = true
			answers = append(answers, answer)
		}
	}
}

// One takeover, one answer. The bound still expires ten seconds later and
// still reports its own loss; the axis has already said what that loss would
// say, so the product must not change what it tells the user — a card that
// restates itself differently is a card people learn to ignore.
func TestIntegration_TheBoundExpiringAfterAnObservationSaysNothingNew(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.NoteShellReplaced(session.ID(e.sid), "kiro-cli-term")
	awaitIntegration(t, e.conn, e.sid, IntegrationConventional)
	e.ws.NoteIntegrationLoss(e.lane, LossCauseHelloTimeout)
	want := IntegrationConventional + "/" + string(ssh.ReasonHandshakeTimeout) + "/kiro-cli-term"
	for _, answer := range answersAfter(t, e.conn, e.sid) {
		if answer != want {
			t.Errorf("the bound changed the answer to %q, want %q", answer, want)
		}
	}
}

// A loss that arrives after an observation must not DOWNGRADE it either. The
// wrapper closing the inherited descriptor is an end-of-stream, which alone
// means `unknown`; here the backend can say more than that and has already
// said it, and the first answer wins.
func TestIntegration_ALaterLossDoesNotOverwriteTheObservedAnswer(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.NoteShellReplaced(session.ID(e.sid), "kiro-cli-term")
	if got := awaitIntegration(t, e.conn, e.sid, IntegrationConventional); got.Reason != string(ssh.ReasonStartupDidNotReturn) {
		t.Fatalf("reason = %q, want startup-did-not-return", got.Reason)
	}
	e.ws.NoteIntegrationLoss(e.lane, "end-of-stream")
	want := IntegrationConventional + "/" + string(ssh.ReasonStartupDidNotReturn) + "/kiro-cli-term"
	for _, answer := range answersAfter(t, e.conn, e.sid) {
		if answer != want {
			t.Errorf("a later loss changed the answer to %q, want %q", answer, want)
		}
	}
}

// An integrated session's shell may legitimately replace its own image, and
// the product must not tear a working session down for it. The observation
// answers only the pre-handshake window; a channel that WAS live and then
// ends is the adapter's loss path, with its own cause and its own reason.
func TestIntegration_AnObservationAfterEstablishmentChangesNothing(t *testing.T) {
	e := newIntegrationEnv(t)
	if got := e.establish(t); got.Status != IntegrationIntegrated {
		t.Fatalf("status = %q, want integrated", got.Status)
	}
	e.ws.NoteShellReplaced(session.ID(e.sid), "kiro-cli-term")
	for _, answer := range answersAfter(t, e.conn, e.sid) {
		if answer != IntegrationIntegrated+"/" {
			t.Errorf("an integrated session was changed to %q by a process observation", answer)
		}
	}
}

// The contract requires a name inside detail, and a guess nobody can name is
// not worth showing: an observation without one is dropped rather than
// flipping a tab to conventional on evidence the user cannot act on.
func TestIntegration_AnUnnamedObservationIsDropped(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.NoteShellReplaced(session.ID(e.sid), "")
	for _, answer := range answersAfter(t, e.conn, e.sid) {
		if answer != IntegrationStarting+"/" {
			t.Errorf("an observation with no name moved the session to %q", answer)
		}
	}
}

// A session nobody registered has no axis to move — the same rule the loss
// path already obeys, so an observation about a raw tab stays silent. Absence
// IS assertable here: an unregistered session emits nothing on any path, so
// there is no re-send to race with.
func TestIntegration_AnObservationForAnUnregisteredSessionIsDropped(t *testing.T) {
	e := newLifecycleTestEnv(t)
	sid := e.openSession(t, 1)
	e.ws.NoteShellReplaced(session.ID(sid), "kiro-cli-term")
	if leaked := tryReadNotification(t, e.conn, "session.integrationChanged", 300*time.Millisecond); leaked != nil {
		t.Errorf("an unregistered session announced a status: %s", leaked)
	}
}

// The observation survives a reconnect like every other part of the status
// (AD-9): it is a state, and no further transition is coming to re-deliver
// it.
func TestIntegration_ReattachReplaysTheObservation(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.NoteShellReplaced(session.ID(e.sid), "kiro-cli-term")
	if got := awaitIntegration(t, e.conn, e.sid, IntegrationConventional); got.Detail == nil {
		t.Fatal("no detail on the first fact")
	}

	connB := connectWS(t, e.ws)
	defer func() { _ = connB.Close() }()
	resp := jsonrpcCallWithID(t, connB, "attach", map[string]any{"sessionId": e.sid, "offset": 0}, 2)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("attach: %+v", envelope.Error)
	}
	got := awaitIntegration(t, connB, e.sid, IntegrationConventional)
	if got.Detail == nil || got.Detail.ObservedProcess != "kiro-cli-term" {
		t.Errorf("replayed detail = %+v, want the observation", got.Detail)
	}
}

// And the paired half of nocx-viil.3: a session where nothing unusual was
// observed sends no detail at all. The details chain must not grow a line
// that says nothing — an empty guess would read as a finding.
func TestIntegration_NothingObservedSendsNoDetail(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.NoteIntegrationLoss(e.lane, LossCauseHelloTimeout)
	got := awaitIntegration(t, e.conn, e.sid, IntegrationConventional)
	if got.Reason != string(ssh.ReasonHandshakeTimeout) {
		t.Fatalf("reason = %q, want handshake-timeout", got.Reason)
	}
	if got.Detail != nil {
		t.Errorf("detail = %+v, want none when the backend observed nothing", got.Detail)
	}
}

// ── §6.2's loss intervals, and the three events kept apart ────────────────

// Assertion 24: each loss interval of §6.2 produces its outcome, and the three
// loss events are distinguished from one another.
//
// The intervals are the design's three rows, and which one applies is decided
// by WHERE the session was when the channel went, not by which timer noticed:
// before integration was live the session degrades with a named reason; after
// it was live the answer is `channel-lost` whichever path saw it end.
func TestIntegration_SixTwoLossRowsEachProduceTheirOutcome(t *testing.T) {
	cases := []struct {
		name       string
		cause      string
		everLive   bool
		wantStatus string
		wantReason ssh.RefusalReason
	}{
		// Row 2 — after the channel existed, before integration was live.
		// Three different events, and answers a user can tell apart.
		{
			"the shell never proved itself", LossCauseHelloTimeout, false,
			IntegrationConventional, ssh.ReasonHandshakeTimeout,
		},
		{
			"nocx's own listener went away", LossCauseListenerGone, false,
			IntegrationConventional, ssh.ReasonChannelUnavailable,
		},
		{
			"the multiplex socket file went", LossCauseMasterSocketGone, false,
			IntegrationConventional, ssh.ReasonChannelUnavailable,
		},
		{
			"the multiplex master exited", LossCauseMasterExited, false,
			IntegrationConventional, ssh.ReasonChannelUnavailable,
		},
		{
			"the underlying SSH transport died", LossCauseTransportGone, false,
			IntegrationConventional, ssh.ReasonBootstrapInterrupted,
		},
		// Row 3 — after integration was live. Which path noticed does not
		// change the answer the user needs.
		{
			"the socket went after integration", LossCauseMasterSocketGone, true,
			IntegrationLost, ssh.ReasonChannelLost,
		},
		{
			"the master exited after integration", LossCauseMasterExited, true,
			IntegrationLost, ssh.ReasonChannelLost,
		},
		{
			"the transport died after integration", LossCauseTransportGone, true,
			IntegrationLost, ssh.ReasonChannelLost,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newIntegrationEnv(t)
			if tc.everLive {
				e.establish(t)
			}
			e.ws.NoteIntegrationLoss(e.lane, tc.cause)
			got := awaitIntegration(t, e.conn, e.sid, tc.wantStatus)
			if got.Reason != string(tc.wantReason) {
				t.Errorf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

// The distinguishability, stated as the property rather than read off the
// table above: before integration is live, the events §6.2 names do not all
// collapse into one answer. Two of them share a reason deliberately — the
// socket going and the master dying are the same thing happening to nocx's own
// channel — and the others do not, which is what makes "distinguished" a claim
// rather than a coincidence.
func TestIntegration_TheUnderlyingTransportIsNotTheChannelGoing(t *testing.T) {
	reason := func(cause string) ssh.RefusalReason {
		e := newIntegrationEnv(t)
		e.ws.NoteIntegrationLoss(e.lane, cause)
		got := awaitIntegration(t, e.conn, e.sid, IntegrationConventional)
		return ssh.RefusalReason(got.Reason)
	}
	channelGone := reason(LossCauseListenerGone)
	transportGone := reason(LossCauseTransportGone)
	shellSilent := reason(LossCauseHelloTimeout)
	if channelGone == transportGone {
		t.Errorf("nocx's channel going and the SSH transport dying both report %q; "+
			"§6.2 detects them separately and they need different answers", channelGone)
	}
	if channelGone == shellSilent {
		t.Errorf("nocx's channel going and the shell falling silent both report %q", channelGone)
	}
	if transportGone == shellSilent {
		t.Errorf("the SSH transport dying and the shell falling silent both report %q", transportGone)
	}
	for _, r := range []ssh.RefusalReason{channelGone, transportGone, shellSilent} {
		if r == ssh.ReasonUnknown {
			t.Errorf("a §6.2 event reports %q; the backend knows which event it was", r)
		}
	}
}

// A leg halted because the store refused a lifecycle frame on every attempt
// (ADR-0077) is a loss the pane shows, not a line only in the log (AGENTS.md:
// a soft degrade is visible in the product). After establishment it reads as
// any other lost channel; before it, as nocx's own channel being unavailable
// — the shell is fine, what failed is this coordinator's side.
func TestIntegration_AStoreRefusedLegIsAVisibleLoss(t *testing.T) {
	e := newIntegrationEnv(t)
	if got := e.establish(t); got.Status != IntegrationIntegrated {
		t.Fatalf("status = %q, want integrated before the loss", got.Status)
	}
	e.ws.NoteIntegrationLoss(e.lane, LossCauseStoreRefused)
	got := readIntegration(t, e.conn, e.sid)
	if got.Status != IntegrationLost || got.Reason != string(ssh.ReasonChannelLost) {
		t.Errorf("status = %q reason = %q, want lost / channel-lost", got.Status, got.Reason)
	}

	before := newIntegrationEnv(t)
	before.ws.NoteIntegrationLoss(before.lane, LossCauseStoreRefused)
	got = awaitIntegration(t, before.conn, before.sid, IntegrationConventional)
	if got.Reason != string(ssh.ReasonChannelUnavailable) {
		t.Errorf("before establishment: reason = %q, want channel-unavailable", got.Reason)
	}
}

// The LAST status a renderer hears must be the axis's current one. The open
// handler re-sends the current status after its ack while the lifecycle
// goroutine can conclude the axis at the same moment; a re-send that read
// `starting` before the conclusion and wrote after it left the pane on
// "Checking this shell…" for good, because nothing after `integrated` ever
// speaks again (nocx-6h5au: CI showed the open's ack, the bridge start and
// the session's prompt_ready inside one millisecond). Re-sending a state is
// harmless; re-sending a stale one over a newer one is not.
func TestIntegration_AConcurrentResendNeverOutlivesTheConclusion(t *testing.T) {
	for trial := 0; trial < 150; trial++ {
		e := newIntegrationEnv(t)
		sid := session.ID(e.sid)
		start := make(chan struct{})
		var ready, done sync.WaitGroup
		ready.Add(2)
		done.Add(2)
		// The open handler's one re-send and the conclusion, released
		// together: the defect needs the re-send's snapshot to precede the
		// conclusion and its write to follow it.
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			e.ws.emitIntegration(sid)
		}()
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			e.ws.noteIntegrationLive(sid)
		}()
		ready.Wait()
		close(start)
		done.Wait()

		last := ""
		for {
			raw := tryReadNotification(t, e.conn, "session.integrationChanged", 100*time.Millisecond)
			if raw == nil {
				break
			}
			var p integrationChangedParams
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatalf("decode session.integrationChanged: %v\nraw: %s", err, raw)
			}
			if p.SessionID == e.sid {
				last = p.Status
			}
		}
		if last != IntegrationIntegrated {
			t.Fatalf("trial %d: last status the renderer heard = %q, want %q: a stale re-send outlived the conclusion",
				trial, last, IntegrationIntegrated)
		}
	}
}

// A full outbound queue drops a refreshable notification, and the integration
// axis has no later notification once it reaches integrated — so a drop at
// that moment would be the renderer's last word for good (nocx-zg3k3.14.13).
// What prevents that is a chain, asserted here end to end: the drop puts the
// stall notice on the connection's reserved slot, the renderer answers that
// notice by reconnecting (dispatcher.ts, nocx-mrtvy), and the reconnect's
// attach replays the CURRENT status. The final state reaches the renderer on
// the next connection, never on none.
func TestIntegration_AFinalStatusDroppedOnAFullQueueArrivesOnTheReconnect(t *testing.T) {
	e := newIntegrationEnv(t)

	// The attached connection stops draining: the pump is held mid-write and
	// the refreshable queue behind it is filled to its depth.
	sock := &blockingSocket{released: make(chan struct{}), writeStarted: make(chan struct{})}
	stalledOut := outbound.New(sock, outbound.Config{})
	t.Cleanup(stalledOut.Close)
	t.Cleanup(func() {
		select {
		case <-sock.released:
		default:
			close(sock.released)
		}
	})
	filler := notificationPayload("test.filler", json.RawMessage(`{}`))
	if err := stalledOut.TryEnqueue(outbound.TextMessage, filler); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	select {
	case <-sock.writeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the pump never reached the stopped consumer")
	}
	for i := 0; i < outbound.DefaultQueueDepth; i++ {
		if err := stalledOut.TryEnqueue(outbound.TextMessage, filler); err != nil {
			t.Fatalf("filling the queue, frame %d: %v", i, err)
		}
	}
	rx := e.ws.getRx(session.ID(e.sid))
	_, state := rx.getSubscriber()
	rx.setSubscriber(&wsConn{out: stalledOut, log: e.ws.log}, state)

	// The shell integrates while nothing can be written: the final status is
	// dropped, and the connection says so.
	mustLifecycleIngest(t, e.pub, "T", lifecycleEnv(e.lane, e.h, 1, lifecycleHelloEvt()))
	waittest.WaitForTimeout(t, "the integrated status to overflow the stopped queue", 5*time.Second,
		stalledOut.Stalled)
	close(sock.released)
	waittest.WaitForTimeout(t, "the stall notice on the wire", 5*time.Second, func() bool {
		sock.mu.Lock()
		defer sock.mu.Unlock()
		for _, f := range sock.frames {
			if _, ok := notificationParams(f.Data, outbound.StallNoticeMethod); ok {
				return true
			}
		}
		return false
	})
	sock.mu.Lock()
	for _, f := range sock.frames {
		if params, ok := notificationParams(f.Data, "session.integrationChanged"); ok {
			t.Fatalf("the stopped connection received the status after all (%s); this test no longer exercises the drop", params)
		}
	}
	sock.mu.Unlock()

	// The renderer reconnects on that notice, and the attach replays the
	// status it missed.
	connB := connectWS(t, e.ws)
	defer func() { _ = connB.Close() }()
	resp := jsonrpcCallWithID(t, connB, "attach", map[string]any{"sessionId": e.sid, "offset": 0}, 2)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("attach: %+v", envelope.Error)
	}
	awaitIntegration(t, connB, e.sid, IntegrationIntegrated)
}
