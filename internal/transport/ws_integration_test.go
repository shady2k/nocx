package transport

import (
	"context"
	"encoding/json"
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
// A status that never arrives still fails, on readNotification's own bound.
func awaitIntegration(t *testing.T, conn *websocket.Conn, sid, want string) integrationChangedParams {
	t.Helper()
	for {
		got := readIntegration(t, conn, sid)
		if got.Status == want {
			return got
		}
	}
}

// readIntegration reads the next session.integrationChanged for a session.
func readIntegration(t *testing.T, conn *websocket.Conn, sid string) integrationChangedParams {
	t.Helper()
	for {
		raw := readNotification(t, conn, "session.integrationChanged", wantWithin)
		var p integrationChangedParams
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("decode session.integrationChanged: %v\nraw: %s", err, raw)
		}
		if p.SessionID == sid {
			return p
		}
	}
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
		ws.RegisterIntegration(session.ID(cfg.SessionID), "/bin/bash", IntegrationStarting, ssh.ReasonNone)
	}
	return f.stub, nil
}

func newIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	logger := log.NewSlogAdapter(nil)
	f := &integrationPTYFactory{stub: pty.NewStub(logger)}
	e := newLifecycleTestEnvWithReg(t, session.New(logger, f), WithLifecyclePublisher(pub))
	f.ws.Store(e.ws)
	pub.SetEmitter(e.ws)
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

// The product says WHERE it stopped, not that ten seconds passed (nocx-yww2).
// A session whose rcfile began executing and whose user startup never returned
// is the dominant local failure — every user with a second shell integration
// in their rc is in it — and until this it was reported as
// `handshake-timeout`, which is indistinguishable from a shell that never
// started and from one that hung.
//
// Validated against the contract on the way out, because the reason is a
// closed enum shared with the renderer: a value the schema does not know would
// reach a surface that cannot render it.
func TestIntegration_AStartupThatDidNotReturnIsNamedRatherThanTimedOut(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.NoteBootstrapStage(session.ID(e.sid), BootstrapStageStartupEntered)

	e.ws.NoteIntegrationLoss(e.lane, LossCauseHelloTimeout)
	raw := readNotification(t, e.conn, "session.integrationChanged", wantWithin)
	validateJSON(t, loadSchema(t, "session.integrationChanged.schema.json"), raw,
		"session.integrationChanged params (real socket, startup did not return)")
	var got integrationChangedParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != IntegrationConventional {
		t.Errorf("status = %q, want conventional", got.Status)
	}
	if got.Reason != string(ssh.ReasonStartupDidNotReturn) {
		t.Errorf("reason = %q, want startup-did-not-return: the bound expiring is what NOTICED, "+
			"the stage is what the user can act on", got.Reason)
	}
}

// The paired case, and the one that keeps the new answer honest: a session
// whose user startup DID return and which then failed to authenticate is our
// own bootstrap breaking, and it still reports the handshake bound. Without
// this, "startup-did-not-return" would be free to become the answer to every
// timeout, which is the same lie with a longer name.
func TestIntegration_AStartupThatReturnedStillReportsTheHandshakeBound(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.NoteBootstrapStage(session.ID(e.sid), BootstrapStageStartupEntered)
	e.ws.NoteBootstrapStage(session.ID(e.sid), BootstrapStageUserRCReturned)

	e.ws.NoteIntegrationLoss(e.lane, LossCauseHelloTimeout)
	got := readIntegration(t, e.conn, e.sid)
	if got.Status != IntegrationConventional || got.Reason != string(ssh.ReasonHandshakeTimeout) {
		t.Errorf("status/reason = %q/%q, want conventional/handshake-timeout", got.Status, got.Reason)
	}
}

// A stage is diagnostic and carries no authority whatsoever (ADR-0024
// decision 4). The descriptor it arrives on is inherited by every descendant
// of the shell, so a forged fact must be able to spoil a diagnosis and nothing
// else: it announces nothing, integrates nothing, and leaves a session in
// exactly the state it was in.
func TestIntegration_ABootstrapStageAnnouncesNothingByItself(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.NoteBootstrapStage(session.ID(e.sid), BootstrapStageStartupEntered)
	e.ws.NoteBootstrapStage(session.ID(e.sid), BootstrapStageUserRCReturned)

	// It cannot claim success: the session is still `starting`, because only
	// the kernel's own "a domain went live" moves that axis.
	e.ws.integrationMu.Lock()
	st := *e.ws.integrations[session.ID(e.sid)]
	e.ws.integrationMu.Unlock()
	if st.status != IntegrationStarting || st.reason != ssh.ReasonNone || st.everLive {
		t.Errorf("status = %+v, want an untouched `starting`: a progress fact may not integrate a session", st)
	}
	// And it announces nothing of its own. Last, because a read that times out
	// leaves this websocket unusable for the assertions after it.
	if leaked := tryReadNotification(t, e.conn, "session.integrationChanged", 300*time.Millisecond); leaked != nil {
		t.Errorf("a bootstrap stage published a status of its own: %s", leaked)
	}
}

// A session that WAS integrated and then lost its channel is `lost`, whatever
// its bootstrap stage says. The stage arm sits ahead of the cause arm, so this
// is the check that it did not also jump ahead of "it was live" — a session
// whose blocks stopped mid-command must never be told its startup did not
// return.
func TestIntegration_AStageNeverOutranksHavingBeenLive(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.NoteBootstrapStage(session.ID(e.sid), BootstrapStageStartupEntered)
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

// ── the process observation (nocx-cgzc, nocx-viil.3) ──────────────────────

// The bead's own sentence: a takeover is reported in well under a second,
// and not when the handshake bound expires. Nothing here advances a clock —
// the observation IS the trigger, which is the whole point of having a second
// detector.
func TestIntegration_ShellReplacedIsReportedWithoutWaitingForTheBound(t *testing.T) {
	e := newIntegrationEnv(t)
	e.ws.NoteShellReplaced(session.ID(e.sid), "kiro-cli-term")
	got := awaitIntegration(t, e.conn, e.sid, IntegrationConventional)
	// The reason is the STAGE, not the detector. nocx exec's the login shell
	// itself, so a second exec on that pid before the hello can only have come
	// from inside the shell's own startup — the same fact the bootstrap
	// progress descriptor reports by falling silent (nocx-yww2), reached here
	// milliseconds after the fork instead of ten seconds later. Naming the
	// bound would name what noticed rather than what happened, and since this
	// answer lands first and the first answer wins, the vaguer word would be
	// the one the user is left with.
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
