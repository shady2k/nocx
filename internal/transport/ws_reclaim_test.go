package transport

// Reclaiming a live pane from a client that has never seen it (nocx-oevq4,
// the nocx-server design D5, D7 and D8).
//
// The tests here are written from the acceptance criteria rather than from the
// implementation, and each one names the criterion it is. What they exercise
// is the seam a person reaches: a window that has just started, holding no
// memory of anything, asks what is alive, takes one back, and reads the output
// that was produced while it was not there.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// reclaimPane is the pane the session under test is the pipe of. A UUIDv7,
// because the renderer mints it and validateOpenRaw refuses anything else.
const reclaimPane = "0198f2b0-0000-7000-8000-0000000000a1"

// aForeignInstance is a well-shaped instance id that is not this backend's:
// the shape of a record kept across a coordinator restart.
const aForeignInstance = "ffffffffffffffffffffffffffffffff"

// newReclaimWS builds a server whose sessions carry real output, because a
// test about reclaiming a stream cannot use a PTY that answers EOF.
func newReclaimWS(t *testing.T) (*WSServer, *feedablePTY) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	term := newFeedablePTY()
	reg := session.New(logger, &feedableFactory{p: term})
	ws := NewWSServer(logger, reg)
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx); _ = term.Close() })
	return ws, term
}

// openInPaneTapped opens a session that is the pipe of a pane over a tapped
// connection, and hands back the whole identity the ack carried — which is
// what a claim later has to name.
func openInPaneTapped(t *testing.T, ws *WSServer, conn *websocket.Conn, tap *socketTap, paneID string, id int) openResult {
	t.Helper()
	raw := tapCall(t, conn, tap, id, "open", map[string]any{
		"cols": 80, "rows": 24, "paneId": paneID,
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
	if env.Result.SessionID == "" {
		t.Fatal("open returned an empty sessionId")
	}
	awaitSubscriber(t, ws, session.ID(env.Result.SessionID))
	return env.Result
}

// liveSessions calls sessions.live and returns the decoded result together
// with the raw JSON, so a caller can both read it and hold it against the
// contract.
func liveSessions(t *testing.T, conn *websocket.Conn, tap *socketTap, id int) (sessionsLiveResult, json.RawMessage) {
	t.Helper()
	raw := tapCall(t, conn, tap, id, "sessions.live", map[string]any{})
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("sessions.live: %v\nraw: %s", err, raw)
	}
	if env.Error != nil {
		t.Fatalf("sessions.live: %+v", env.Error)
	}
	var got sessionsLiveResult
	if err := json.Unmarshal(env.Result, &got); err != nil {
		t.Fatalf("sessions.live: decode result: %v", err)
	}
	return got, env.Result
}

// entryFor picks the listed session out of the answer, failing when it is
// absent — "the list did not contain it" is the interesting failure and it
// must not arrive as a nil dereference.
func entryFor(t *testing.T, live sessionsLiveResult, sid string) liveSessionResult {
	t.Helper()
	for _, s := range live.Sessions {
		if s.SessionID == sid {
			return s
		}
	}
	t.Fatalf("sessions.live did not list %s; it listed %+v", sid, live.Sessions)
	return liveSessionResult{}
}

// awaitDetached blocks until nothing is attached to the session — the state
// a closed window leaves behind. An observable state, never a duration.
func awaitDetached(t *testing.T, ws *WSServer, sid session.ID) {
	t.Helper()
	waittest.WaitForTimeout(t, "the subscriber slot to empty", wantWithin, func() bool {
		rx := ws.getRx(sid)
		if rx == nil {
			return false
		}
		wconn, _ := rx.getSubscriber()
		return wconn == nil
	})
}

// reclaimErr is a refusal with its data kept RAW. The package's
// jsonrpcErrorObj decodes data into `any`, which loses the discriminator this
// file is about — the refusals here are told apart by data.reason, so the
// field has to survive the decode.
type reclaimErr struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// attachCall sends one attach and returns the whole envelope, so a test can
// read either half: the claim outcome or the refusal.
func attachCall(t *testing.T, conn *websocket.Conn, tap *socketTap, id int, params map[string]any) (json.RawMessage, *reclaimErr) {
	t.Helper()
	raw := tapCall(t, conn, tap, id, "attach", params)
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *reclaimErr     `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("attach: %v\nraw: %s", err, raw)
	}
	return env.Result, env.Error
}

// refusalReason reads the machine-readable half of a refusal — the discriminator
// the vault errors established (ws_vault.go): a caller must never have to read
// prose to tell two refusals apart.
func refusalReason(t *testing.T, e *reclaimErr) string {
	t.Helper()
	if e == nil {
		t.Fatal("expected a refusal, got a result")
	}
	if e.Data == nil {
		t.Fatalf("refusal carried no data to discriminate on: %+v", e)
	}
	var d struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("refusal data: %v (raw %s)", err, e.Data)
	}
	return d.Reason
}

// ── acceptance 1 ───────────────────────────────────────────────────────────

// THE WHOLE FEATURE, at the seam a person reaches. A client with an empty
// session map — a window that has just started — asks what is alive, is told
// which pane each session belongs to and where its replay starts, takes one
// back, and receives the output produced BEFORE it attached.
//
// The second connection is genuinely fresh: it has no state from the first,
// which is the thing that is broken today. frontend/src/ipc.ts reattaches only
// what is in its own Map, so a new window reattaches nothing at all.
func TestReclaim_AFreshClientListsTheLiveSessionsAndTakesOneBack(t *testing.T) {
	ws, term := newReclaimWS(t)

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	opened := openInPaneTapped(t, ws, connA, tapA, reclaimPane, 1)
	sid := opened.SessionID

	// Produced while the first window was watching, and still in the ring:
	// nothing acks in this test, so the ring keeps everything.
	term.emit(t, "before-the-window-closed\n")
	tapDataFor(t, tapA, sid, "before-the-window-closed", wantWithin)

	// The window closes. The session does not — that is AD-9, and it is the
	// premise of the whole design.
	_ = connA.Close()
	awaitDetached(t, ws, session.ID(sid))

	term.emit(t, "while-nothing-was-attached\n")

	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)

	live, _ := liveSessions(t, connB, tapB, 2)
	entry := entryFor(t, live, sid)
	if entry.PaneID == nil || *entry.PaneID != reclaimPane {
		t.Fatalf("paneId = %v, want the pane the session was opened in (%s)", entry.PaneID, reclaimPane)
	}
	if entry.InstanceID != opened.InstanceID || entry.SessionEpoch != opened.SessionEpoch {
		t.Fatalf("identity = (%s, %d), want the pair the open ack carried (%s, %d)",
			entry.InstanceID, entry.SessionEpoch, opened.InstanceID, opened.SessionEpoch)
	}
	if entry.Attached {
		t.Error("attached = true, but the only client that ever held this session has gone")
	}

	result, rpcErr := attachCall(t, connB, tapB, 3, map[string]any{
		"sessionId":    sid,
		"instanceId":   entry.InstanceID,
		"sessionEpoch": entry.SessionEpoch,
		"offset":       entry.ReplayFrom,
	})
	if rpcErr != nil {
		t.Fatalf("the claim was refused: %+v", rpcErr)
	}
	var claim attachResult
	if err := json.Unmarshal(result, &claim); err != nil {
		t.Fatalf("attach result: %v", err)
	}
	if !claim.Resumed || claim.Reset {
		t.Fatalf("attach at the offset sessions.live named = %+v, want a resume with no gap", claim)
	}

	// The acceptance: the bytes produced while nothing was attached arrive,
	// and so do the ones from before the first window closed. A reclaim that
	// only delivered what came after it would be a new session with an old id.
	got := tapDataFor(t, tapB, sid, "while-nothing-was-attached", wantWithin)
	if !strings.Contains(got, "before-the-window-closed") {
		t.Errorf("the reclaimed stream did not carry the output produced before the client attached; got %q", got)
	}
}

// ── acceptance 2 ───────────────────────────────────────────────────────────

// A claim naming another backend instance is refused, and the refusal is
// distinguishable from "no such session" WITHOUT reading prose. The two are
// different facts: one says the binding is from a coordinator that is gone
// (every session it named died with it), the other says this coordinator does
// not hold that session. A client that cannot tell them apart cannot tell a
// restart from a mistake.
//
// The instance is judged FIRST and without a lookup, which is the rule
// internal/session/lineage.go already applies to the parent edge: it is the
// component whose failure means the claim could never be true here, rather
// than merely is not true now. So a stale instance is refused as a stale
// instance even when the session id it names is unknown too.
func TestReclaim_AClaimFromAnotherInstanceIsRefusedDistinguishably(t *testing.T) {
	ws, _ := newReclaimWS(t)

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	opened := openInPaneTapped(t, ws, connA, tapA, reclaimPane, 1)

	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)

	_, staleErr := attachCall(t, connB, tapB, 2, map[string]any{
		"sessionId":    opened.SessionID,
		"instanceId":   aForeignInstance,
		"sessionEpoch": opened.SessionEpoch,
		"offset":       0,
	})
	staleReason := refusalReason(t, staleErr)
	if staleReason != reasonForeignInstance {
		t.Errorf("a claim from another instance was refused with reason %q, want %q", staleReason, reasonForeignInstance)
	}

	_, unknownErr := attachCall(t, connB, tapB, 3, map[string]any{
		"sessionId":    "00000000000000000000000000000000",
		"instanceId":   opened.InstanceID,
		"sessionEpoch": 1,
		"offset":       0,
	})
	unknownReason := refusalReason(t, unknownErr)
	if unknownReason != reasonUnknownSession {
		t.Errorf("a claim on a session nobody holds was refused with reason %q, want %q", unknownReason, reasonUnknownSession)
	}

	if staleReason == unknownReason {
		t.Fatal("a stale instance and an unknown session are the same refusal; a client cannot tell a coordinator restart from a mistake")
	}

	// The same id, a different incarnation of the same instance. It is a third
	// fact and it gets a third word: the session exists, and it is not the one
	// the claim was written against.
	_, epochErr := attachCall(t, connB, tapB, 4, map[string]any{
		"sessionId":    opened.SessionID,
		"instanceId":   opened.InstanceID,
		"sessionEpoch": opened.SessionEpoch + 1,
		"offset":       0,
	})
	if got := refusalReason(t, epochErr); got != reasonForeignIncarnation {
		t.Errorf("a claim on another incarnation was refused with reason %q, want %q", got, reasonForeignIncarnation)
	}
}

// A claim that names an instance which IS this one, on a session that exists,
// is admitted. The paired success for the refusals above: without it, a
// handler that refused every claim would pass all three.
func TestReclaim_AClaimNamingThisInstanceIsAdmitted(t *testing.T) {
	ws, _ := newReclaimWS(t)

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	opened := openInPaneTapped(t, ws, connA, tapA, reclaimPane, 1)

	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)

	result, rpcErr := attachCall(t, connB, tapB, 2, map[string]any{
		"sessionId":    opened.SessionID,
		"instanceId":   opened.InstanceID,
		"sessionEpoch": opened.SessionEpoch,
		"offset":       0,
	})
	if rpcErr != nil {
		t.Fatalf("a claim naming this instance was refused: %+v", rpcErr)
	}
	var claim attachResult
	if err := json.Unmarshal(result, &claim); err != nil {
		t.Fatalf("attach result: %v", err)
	}
	if !claim.Resumed {
		t.Fatalf("attach = %+v, want a resume", claim)
	}
}

// ── acceptance 3: the invariant, as an interval ────────────────────────────

// THE ASSOCIATION EXISTS FROM BEFORE THE SESSION IS ANNOUNCED TO ANY CLIENT
// UNTIL THE SESSION ENDS. Both ends are asserted, and the test fails if either
// moves.
//
// The opening end is the one a moment-shaped test cannot state. The
// announcement is the open ack — the first instant any client can learn the
// session id — so the assertion is made THERE, from inside the write of that
// very response: the responder below asks the registry what it holds at the
// moment the ack is handed to the socket. Move the association's establishment
// after the ack and this fails; the ack would carry an id nothing could yet be
// claimed by.
//
// The closing end is the session's own end. After the close, the association
// is gone from the list — the pane outlives it, and it is the pane that will
// carry the next session.
func TestReclaim_ThePaneAssociationHoldsFromBeforeTheAnnouncementUntilTheSessionEnds(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	term := newFeedablePTY()
	reg := session.New(logger, &feedableFactory{p: term})
	ws := NewWSServer(logger, reg)
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx); _ = term.Close() })

	conn := connectWS(t, ws)
	tap := newSocketTap(conn)

	// The opening end. The registry is asked at the instant the ack exists —
	// the ack is decoded here, before the client has had a chance to read it,
	// and the question put to the registry is the one a claim would ask.
	opened := openInPaneTapped(t, ws, conn, tap, reclaimPane, 1)
	boundAtAnnouncement := false
	for _, s := range reg.List() {
		if string(s.ID()) == opened.SessionID && s.PaneID() == reclaimPane &&
			string(s.Identity().InstanceID) == opened.InstanceID {
			boundAtAnnouncement = true
		}
	}
	if !boundAtAnnouncement {
		t.Fatal("the pane association did not exist when the session was announced: the ack named a session no claim could resolve")
	}

	// It is also visible over the wire from that moment: the interval is a
	// property of the association, not of the goroutine that asked first.
	live, _ := liveSessions(t, conn, tap, 2)
	entry := entryFor(t, live, opened.SessionID)
	if entry.PaneID == nil || *entry.PaneID != reclaimPane {
		t.Fatalf("paneId = %v at announcement, want %s", entry.PaneID, reclaimPane)
	}

	// The closing end.
	raw := tapCall(t, conn, tap, 4, "close", map[string]any{"sessionId": opened.SessionID})
	var closeEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &closeEnv); err != nil || closeEnv.Error != nil {
		t.Fatalf("close: %v %+v", err, closeEnv.Error)
	}

	after, _ := liveSessions(t, conn, tap, 5)
	for _, s := range after.Sessions {
		if s.SessionID == opened.SessionID {
			t.Fatal("the association outlived the session it binds; a pane would be claimable into a shell that has gone")
		}
	}
}

// The other closing end named in the invariant: the coordinator's instanceId
// changes. A registry is a backend instance, so a second one is a second
// instance — and the association the first one held cannot be claimed through
// it, whatever the id says.
func TestReclaim_AnAssociationDoesNotSurviveTheInstanceItWasMintedBy(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	first := session.New(logger, &feedableFactory{p: newFeedablePTY()})
	second := session.New(logger, &feedableFactory{p: newFeedablePTY()})

	if first.InstanceID() == second.InstanceID() {
		t.Fatal("two registries minted one instance id; every claim would resolve against the wrong backend")
	}

	ws := NewWSServer(logger, second)
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	conn := connectWS(t, ws)
	tap := newSocketTap(conn)
	_, rpcErr := attachCall(t, conn, tap, 1, map[string]any{
		"sessionId":    "0123456789abcdef0123456789abcdef",
		"instanceId":   string(first.InstanceID()),
		"sessionEpoch": 1,
		"offset":       0,
	})
	if got := refusalReason(t, rpcErr); got != reasonForeignInstance {
		t.Errorf("a claim carried across a coordinator restart was refused with %q, want %q", got, reasonForeignInstance)
	}
}

// ── acceptance 4: one client owns it, and the loser is told ────────────────

// A second client attaching TAKES the session, and the client that lost it is
// told (D8). Silent replacement is what the transport did before: the loser
// went on rendering a stream it no longer owned and offering a keyboard whose
// bytes the backend would refuse — a surface advertising what it can no longer
// deliver.
func TestReclaim_ASecondClientTakesTheSessionAndTheLoserIsTold(t *testing.T) {
	ws, term := newReclaimWS(t)

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	opened := openInPaneTapped(t, ws, connA, tapA, reclaimPane, 1)
	sid := opened.SessionID

	term.emit(t, "mine\n")
	tapDataFor(t, tapA, sid, "mine", wantWithin)

	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)
	if _, rpcErr := attachCall(t, connB, tapB, 2, map[string]any{
		"sessionId":    sid,
		"instanceId":   opened.InstanceID,
		"sessionEpoch": opened.SessionEpoch,
		"offset":       0,
	}); rpcErr != nil {
		t.Fatalf("the second client's claim was refused: %+v", rpcErr)
	}

	// The loser is TOLD, by the whole identity: a notification naming a bare
	// id could be about a previous incarnation, and the renderer refuses one
	// whose pair is not its own.
	params := tapNotify(t, tapA, "session.displaced", wantWithin)
	var told sessionDisplacedParams
	if err := json.Unmarshal(params, &told); err != nil {
		t.Fatalf("session.displaced params: %v", err)
	}
	if told.SessionID != sid || told.InstanceID != opened.InstanceID || told.SessionEpoch != opened.SessionEpoch {
		t.Errorf("session.displaced = %+v, want the identity of the session that was taken (%s, %s, %d)",
			told, sid, opened.InstanceID, opened.SessionEpoch)
	}

	// And the take is real, not merely announced: the new client receives the
	// session's output.
	term.emit(t, "yours-now\n")
	tapDataFor(t, tapB, sid, "yours-now", wantWithin)

	// The displaced connection stops being a party to the session at all: its
	// input is refused, which is what makes "you lost it" true rather than
	// decorative.
	rawResize := tapCall(t, connA, tapA, 3, "resize", map[string]any{
		"sessionId": sid, "cols": 100, "rows": 40,
	})
	var resizeEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(rawResize, &resizeEnv); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if resizeEnv.Error == nil {
		t.Error("the displaced client could still resize the session it no longer holds")
	}
}

// The failure path of the notification's one external call: the write to the
// displaced client. A connection that has gone cannot be told anything, and
// the claim must still succeed — the new owner's attach may not be held
// hostage by the old owner's socket.
func TestReclaim_ADisplacedClientThatHasGoneDoesNotFailTheClaim(t *testing.T) {
	ws, term := newReclaimWS(t)

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	opened := openInPaneTapped(t, ws, connA, tapA, reclaimPane, 1)
	sid := opened.SessionID
	_ = connA.Close()
	awaitDetached(t, ws, session.ID(sid))

	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)
	result, rpcErr := attachCall(t, connB, tapB, 2, map[string]any{
		"sessionId":    sid,
		"instanceId":   opened.InstanceID,
		"sessionEpoch": opened.SessionEpoch,
		"offset":       0,
	})
	if rpcErr != nil {
		t.Fatalf("a claim on a session whose previous client is gone was refused: %+v", rpcErr)
	}
	var claim attachResult
	if err := json.Unmarshal(result, &claim); err != nil {
		t.Fatalf("attach result: %v", err)
	}
	if !claim.Resumed {
		t.Fatalf("attach = %+v, want a resume", claim)
	}
	term.emit(t, "after-the-gone-client\n")
	tapDataFor(t, tapB, sid, "after-the-gone-client", wantWithin)
}

// ── the failure paths of sessions.live ─────────────────────────────────────

// A backend holding nothing answers an EMPTY LIST, not null and not an error.
// The renderer maps over it on the first frame it draws, and `providers`
// marshalling as null rather than [] is the defect this directory's first run
// found (contracts/README.md).
func TestReclaim_SessionsLiveOnABackendHoldingNothing(t *testing.T) {
	ws, _ := newReclaimWS(t)
	conn := connectWS(t, ws)
	tap := newSocketTap(conn)

	live, raw := liveSessions(t, conn, tap, 1)
	if live.Sessions == nil {
		t.Error("sessions is null on a backend with no sessions; the renderer maps over it")
	}
	if len(live.Sessions) != 0 {
		t.Errorf("sessions = %+v, want none", live.Sessions)
	}
	if !strings.Contains(string(raw), `"sessions":[]`) {
		t.Errorf("the empty answer marshalled as %s, want an empty array", raw)
	}
}

// A session that is the pipe of NO pane is listed with a null paneId rather
// than dropped or given an empty string. It is a legitimate state — every open
// before the renderer minted panes — and "no pane" is a different fact from
// "the pane with no id".
func TestReclaim_ASessionWithNoPaneIsListedWithNoBinding(t *testing.T) {
	ws, _ := newReclaimWS(t)
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	tap := newSocketTap(conn)
	live, raw := liveSessions(t, conn, tap, 2)
	entry := entryFor(t, live, sid)
	if entry.PaneID != nil {
		t.Errorf("paneId = %q for a session attached to no pane, want null", *entry.PaneID)
	}
	if !strings.Contains(string(raw), `"paneId":null`) {
		t.Errorf("the unbound session marshalled as %s, want an explicit null", raw)
	}
}

// attached reports what is true now: the session the calling client is holding
// is listed as attached. The paired success for the detached case asserted in
// the reclaim test above.
func TestReclaim_AHeldSessionIsListedAsAttached(t *testing.T) {
	ws, _ := newReclaimWS(t)
	conn := connectWS(t, ws)
	tap := newSocketTap(conn)
	opened := openInPaneTapped(t, ws, conn, tap, reclaimPane, 1)

	live, _ := liveSessions(t, conn, tap, 2)
	if entry := entryFor(t, live, opened.SessionID); !entry.Attached {
		t.Error("attached = false while the caller itself is holding the session")
	}
}

// ── the wire (AGENTS.md rule 5) ────────────────────────────────────────────

func TestSessionsLive_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "sessions.live.schema.json")
	pane := reclaimPane

	cases := map[string]sessionsLiveResult{
		"a backend holding nothing": {Sessions: []liveSessionResult{}},
		"a session bound to a pane": {Sessions: []liveSessionResult{{
			SessionID:    "0123456789abcdef0123456789abcdef",
			InstanceID:   "fedcba9876543210fedcba9876543210",
			SessionEpoch: 1,
			PaneID:       &pane,
			ReplayFrom:   4096,
			Attached:     true,
		}}},
		"a session bound to no pane": {Sessions: []liveSessionResult{{
			SessionID:    "0123456789abcdef0123456789abcdef",
			InstanceID:   "fedcba9876543210fedcba9876543210",
			SessionEpoch: 7,
			PaneID:       nil,
			ReplayFrom:   0,
			Attached:     false,
		}}},
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "sessions.live result ("+name+")")
		})
	}
}

// The one that matters: the REAL result off the REAL socket. A test that
// validates a payload it built itself proves the struct is well-formed, never
// that the server sends it.
func TestSessionsLive_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "sessions.live.schema.json")
	ws, _ := newReclaimWS(t)
	conn := connectWS(t, ws)
	tap := newSocketTap(conn)

	// Both shapes over the same socket: the empty answer and the populated
	// one. The empty answer is where a nil slice would show up.
	_, empty := liveSessions(t, conn, tap, 1)
	validateJSON(t, schema, empty, "sessions.live result (nothing held)")

	openInPaneTapped(t, ws, conn, tap, reclaimPane, 2)
	_, populated := liveSessions(t, conn, tap, 3)
	validateJSON(t, schema, populated, "sessions.live result (a live session)")
}

func TestAttach_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "attach.schema.json")
	cases := map[string]attachResult{
		"a resume with no gap": {Resumed: true, From: 1234},
		"a reset":              {Reset: true, From: 5678},
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "attach result ("+name+")")
		})
	}
}

func TestAttach_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "attach.schema.json")
	ws, term := newReclaimWS(t)

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	opened := openInPaneTapped(t, ws, connA, tapA, reclaimPane, 1)
	term.emit(t, "some output\n")
	tapDataFor(t, tapA, opened.SessionID, "some output", wantWithin)
	_ = connA.Close()
	awaitDetached(t, ws, session.ID(opened.SessionID))

	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)
	result, rpcErr := attachCall(t, connB, tapB, 2, map[string]any{
		"sessionId":    opened.SessionID,
		"instanceId":   opened.InstanceID,
		"sessionEpoch": opened.SessionEpoch,
		"offset":       0,
	})
	if rpcErr != nil {
		t.Fatalf("attach: %+v", rpcErr)
	}
	validateJSON(t, schema, result, "attach result")

	// And the reset half of the shape, off the same socket: an offset the ring
	// has moved past. Both branches are sent by the handler, so both are held
	// against the contract.
	rx := ws.getRx(session.ID(opened.SessionID))
	if rx == nil {
		t.Fatal("the session has no ring")
	}
	if err := rx.ring.ack(rx.ring.writtenLocked()); err != nil {
		t.Fatalf("ack: %v", err)
	}
	term.emit(t, "past the trim\n")
	tapDataFor(t, tapB, opened.SessionID, "past the trim", wantWithin)
	_ = connB.Close()
	awaitDetached(t, ws, session.ID(opened.SessionID))

	connC := connectWS(t, ws)
	tapC := newSocketTap(connC)
	resetResult, resetErr := attachCall(t, connC, tapC, 3, map[string]any{
		"sessionId":    opened.SessionID,
		"instanceId":   opened.InstanceID,
		"sessionEpoch": opened.SessionEpoch,
		"offset":       0,
	})
	if resetErr != nil {
		t.Fatalf("attach below the ring base: %+v", resetErr)
	}
	validateJSON(t, schema, resetResult, "attach result (reset)")
	var claim attachResult
	if err := json.Unmarshal(resetResult, &claim); err != nil {
		t.Fatalf("attach result: %v", err)
	}
	if !claim.Reset || claim.Resumed {
		t.Fatalf("attach below the ring base = %+v, want a reset", claim)
	}
}

func TestSessionDisplaced_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.displaced.schema.json")
	raw, err := json.Marshal(sessionDisplacedParams{
		SessionID:    "0123456789abcdef0123456789abcdef",
		InstanceID:   "fedcba9876543210fedcba9876543210",
		SessionEpoch: 3,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "session.displaced params")
}

func TestSessionDisplaced_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.displaced.schema.json")
	ws, _ := newReclaimWS(t)

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	opened := openInPaneTapped(t, ws, connA, tapA, reclaimPane, 1)

	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)
	if _, rpcErr := attachCall(t, connB, tapB, 2, map[string]any{
		"sessionId":    opened.SessionID,
		"instanceId":   opened.InstanceID,
		"sessionEpoch": opened.SessionEpoch,
		"offset":       0,
	}); rpcErr != nil {
		t.Fatalf("attach: %+v", rpcErr)
	}

	params := tapNotify(t, tapA, "session.displaced", wantWithin)
	validateJSON(t, schema, params, "session.displaced params")
}

// The reconnect this method has always served still works, and still works
// WITHOUT an identity: the same client coming back on a new socket names the
// session it never let go of. The claim's identity is what a client that did
// not open the session carries; requiring it of every attach would refuse a
// caller whose only fault is naming less than it had to.
func TestReclaim_AnAttachWithNoIdentityStillReconnects(t *testing.T) {
	ws, term := newReclaimWS(t)

	connA := connectWS(t, ws)
	tapA := newSocketTap(connA)
	opened := openInPaneTapped(t, ws, connA, tapA, reclaimPane, 1)
	_ = connA.Close()
	awaitDetached(t, ws, session.ID(opened.SessionID))

	connB := connectWS(t, ws)
	tapB := newSocketTap(connB)
	result, rpcErr := attachCall(t, connB, tapB, 2, map[string]any{
		"sessionId": opened.SessionID,
		"offset":    0,
	})
	if rpcErr != nil {
		t.Fatalf("the reconnect was refused: %+v", rpcErr)
	}
	var claim attachResult
	if err := json.Unmarshal(result, &claim); err != nil {
		t.Fatalf("attach result: %v", err)
	}
	if !claim.Resumed {
		t.Fatalf("attach = %+v, want a resume", claim)
	}
	term.emit(t, "still-here\n")
	tapDataFor(t, tapB, opened.SessionID, "still-here", wantWithin)
}

// A malformed identity is refused before it reaches the registry, like every
// other id on this plane: the shape is checked by the validator and the
// refusal is -32602 with no reason, because a caller that sent nonsense is not
// being told a fact about a session.
func TestReclaim_AMalformedIdentityIsRefusedByShape(t *testing.T) {
	ws, _ := newReclaimWS(t)
	conn := connectWS(t, ws)
	tap := newSocketTap(conn)
	opened := openInPaneTapped(t, ws, conn, tap, reclaimPane, 1)

	for name, params := range map[string]map[string]any{
		"an instance id that is not 32 hex": {
			"sessionId": opened.SessionID, "instanceId": "nonsense", "offset": 0,
		},
		"an epoch below the first": {
			"sessionId": opened.SessionID, "instanceId": opened.InstanceID,
			"sessionEpoch": 0, "offset": 0,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, rpcErr := attachCall(t, conn, tap, 2, params)
			if rpcErr == nil {
				t.Fatal("admitted")
			}
			if rpcErr.Code != -32602 {
				t.Errorf("code = %d, want -32602", rpcErr.Code)
			}
		})
	}
}

// sessions.live never blocks on a session whose ring has gone: the answer is
// the registry's, and a session between its registry entry and its ring is
// reported with what is known rather than omitted. The list is what a fresh
// client reasons from, and a silently shorter list is the worst answer.
func TestReclaim_ASessionWithNoRingIsStillListed(t *testing.T) {
	ws, _ := newReclaimWS(t)
	conn := connectWS(t, ws)
	tap := newSocketTap(conn)
	opened := openInPaneTapped(t, ws, conn, tap, reclaimPane, 1)

	// Take the ring away — the state a session is in between the registry
	// insert and the ring's creation, and after a teardown that has removed
	// the ring but not yet the session.
	ws.removeRx(session.ID(opened.SessionID))

	live, _ := liveSessions(t, conn, tap, 2)
	entry := entryFor(t, live, opened.SessionID)
	if entry.ReplayFrom != 0 {
		t.Errorf("replayFrom = %d for a session with no ring, want 0", entry.ReplayFrom)
	}
	if entry.Attached {
		t.Error("attached = true for a session with no ring")
	}
}

// The claim is refused when the session's ring has gone, rather than answering
// a resume on a stream nothing will ever write to. Same rule as the unknown
// session, and the paired failure of the ring lookup the claim makes.
func TestReclaim_AClaimOnASessionWithNoRingIsRefused(t *testing.T) {
	ws, _ := newReclaimWS(t)
	conn := connectWS(t, ws)
	tap := newSocketTap(conn)
	opened := openInPaneTapped(t, ws, conn, tap, reclaimPane, 1)
	ws.removeRx(session.ID(opened.SessionID))

	other := connectWS(t, ws)
	otherTap := newSocketTap(other)
	_, rpcErr := attachCall(t, other, otherTap, 2, map[string]any{
		"sessionId":    opened.SessionID,
		"instanceId":   opened.InstanceID,
		"sessionEpoch": opened.SessionEpoch,
		"offset":       0,
	})
	if rpcErr == nil {
		t.Fatal("a claim on a session with no ring was admitted")
	}
	if got := refusalReason(t, rpcErr); got != reasonUnknownSession {
		t.Errorf("reason = %q, want %q", got, reasonUnknownSession)
	}
}

// A deliberate belt-and-braces read of the timing the reclaim depends on: the
// ring's oldest offset is what sessions.live reports, and it moves only when
// the client acks. Without this, replayFrom could be "whatever the ring's end
// is" and the reclaim test would still pass while delivering nothing.
func TestReclaim_ReplayFromIsTheOldestByteTheRingStillHolds(t *testing.T) {
	ws, term := newReclaimWS(t)
	conn := connectWS(t, ws)
	tap := newSocketTap(conn)
	opened := openInPaneTapped(t, ws, conn, tap, reclaimPane, 1)

	term.emit(t, "first\n")
	tapDataFor(t, tap, opened.SessionID, "first", wantWithin)

	live, _ := liveSessions(t, conn, tap, 2)
	if got := entryFor(t, live, opened.SessionID).ReplayFrom; got != 0 {
		t.Fatalf("replayFrom = %d before any ack, want 0 — the ring still holds everything", got)
	}

	rx := ws.getRx(session.ID(opened.SessionID))
	if rx == nil {
		t.Fatal("no ring")
	}
	written := rx.ring.writtenLocked()
	if err := rx.ring.ack(written); err != nil {
		t.Fatalf("ack: %v", err)
	}
	// The ack trims the ring synchronously, so the next answer is the trimmed
	// one: no wait, and nothing here depends on a duration.
	after, _ := liveSessions(t, conn, tap, 3)
	if got := entryFor(t, after, opened.SessionID).ReplayFrom; got != written {
		t.Fatalf("replayFrom = %d after acking %d bytes, want %d — the ring no longer holds anything before it", got, written, written)
	}
}
