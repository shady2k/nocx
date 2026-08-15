package transport

// The epic's acceptance tests, over the real socket (nocx-sfv6.5): the
// wired executor must let the data plane run while control work blocks, must
// refuse (never queue) saturated ordinary work with the -32004 wire error,
// must keep the ingress-critical resolvers immediate under saturation, must
// correlate out-of-order responses, and must give conflicting mutations the
// same gate protection from one socket as from two.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport/control"
)

// openLocalSession opens a local session over the wire and returns its id.
func openLocalSession(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)
	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("open unmarshal: %v\nraw: %s", err, resp)
	}
	if r.Result.SessionID == "" {
		t.Fatalf("open returned no sessionId: %s", resp)
	}
	return r.Result.SessionID
}

// writeKeystroke sends one binary data frame for the session, the way the
// renderer delivers typed input.
func writeKeystroke(t *testing.T, conn *websocket.Conn, sid string, payload []byte) {
	t.Helper()
	sidBytes, err := session.IDToBytes(session.ID(sid))
	if err != nil {
		t.Fatalf("IDToBytes: %v", err)
	}
	f := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: sidBytes, Payload: payload}
	if err := conn.WriteMessage(websocket.BinaryMessage, f.Encode()); err != nil {
		t.Fatalf("write keystroke: %v", err)
	}
}

// waitForEcho reads the session's output until needle appears — the real PTY
// echoes typed input, so the echo is the proof the keystroke reached the
// session's write path.
func waitForEcho(t *testing.T, conn *websocket.Conn, sid string, needle string) {
	t.Helper()
	reader := newWSReader(conn)
	deadline := time.Now().Add(10 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		got, _ = reader.collect(sid, 300*time.Millisecond, 1500*time.Millisecond)
		if strings.Contains(got, needle) {
			return
		}
	}
	t.Fatalf("session output never contained %q; last output: %q", needle, got)
}

// blockingProbeServer builds a server whose connections.test blocks in the
// prober until released, with the given lane capacity.
func blockingProbeServer(t *testing.T, laneCapacity int) (*WSServer, *probeCallRecorder) {
	t.Helper()
	return blockingProbeServerWithLogger(t, laneCapacity, log.NewSlogAdapter(nil))
}

func blockingProbeServerWithLogger(
	t *testing.T,
	laneCapacity int,
	logger log.Logger,
) (*WSServer, *probeCallRecorder) {
	t.Helper()
	rec := &probeCallRecorder{started: make(chan struct{}), cancelled: make(chan struct{})}
	srv := NewWSServer(logger, newRegWithStub(logger),
		WithControlLaneCapacity(laneCapacity),
		WithProber(rec),
		WithProfileResolver(probeResolver()),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })
	return srv, rec
}

// startBlockedProbe sends connections.test and waits until the prober is
// genuinely blocked mid-probe.
func startBlockedProbe(t *testing.T, conn *websocket.Conn, rec *probeCallRecorder) {
	t.Helper()
	sendControl(t, conn, "connections.test", map[string]any{"profileId": "ssh:test:1"}, 1)
	select {
	case <-rec.started:
	case <-time.After(5 * time.Second):
		t.Fatal("probe never started")
	}
}

// ── Headline: a keystroke reaches a live session while connections.test is
// ── blocked mid-probe on ANOTHER session of the same connection.

func TestKeystrokeReachesLiveSessionWhileProbeBlocked(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	rec := &probeCallRecorder{started: make(chan struct{}), cancelled: make(chan struct{})}
	// A REAL pty: the echo is the proof the keystroke reached the session.
	srv := NewWSServer(logger, newRegWithReal(logger),
		WithProber(rec),
		WithProfileResolver(probeResolver()),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	sid := openLocalSession(t, conn)

	startBlockedProbe(t, conn, rec)

	// A keystroke for the live session must still reach its PTY and echo.
	writeKeystroke(t, conn, sid, []byte("keystroke-during-probe\n"))
	waitForEcho(t, conn, sid, "keystroke-during-probe")
}

// ── Headline: the same while dialog.openFile is outstanding.

func TestKeystrokeReachesLiveSessionWhileDialogOutstanding(t *testing.T) {
	dlg := &blockingDialog{started: make(chan struct{}), release: make(chan struct{})}
	logger := log.NewSlogAdapter(nil)
	srv := NewWSServer(logger, newRegWithReal(logger))
	srv.SetDialogService(dlg)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })
	t.Cleanup(func() { close(dlg.release) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	sid := openLocalSession(t, conn)

	// The native picker is open (blocking the dialog admission).
	sendControl(t, conn, "dialog.openFile", map[string]any{}, 1)
	select {
	case <-dlg.started:
	case <-time.After(5 * time.Second):
		t.Fatal("dialog adapter never invoked")
	}

	// The keystroke still reaches the live session.
	writeKeystroke(t, conn, sid, []byte("keystroke-during-dialog\n"))
	waitForEcho(t, conn, sid, "keystroke-during-dialog")
}

// ── While the ordinary lane is saturated, vault.unlockResolved still
// ── completes its pending unlock and the blocked originating RPC resumes.

func TestUnlockResolvedCompletesWhileLaneSaturated(t *testing.T) {
	srv, rec := blockingProbeServer(t, 1)
	ctx := context.Background()

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	startBlockedProbe(t, conn, rec)

	// An ordinary request is now refused — the lane is genuinely saturated.
	refused := jsonrpcCall(t, conn, "fs.complete", map[string]any{"text": "/etc", "cwd": "/", "limit": 5})
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(refused, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil || env.Error.Code != SaturationErrorCode {
		t.Fatalf("expected the saturation refusal, got %s", refused)
	}

	// The ask: RequestUnlock is blocked waiting for its resolution.
	released := make(chan error, 1)
	go func() { released <- srv.RequestUnlock(ctx, "acceptance") }()
	rid := waitForPendingAsk(t, srv)

	// The resolution arrives over the same socket the read loop consumes —
	// the resolver is ingress-critical and must complete under saturation.
	resp := jsonrpcCall(t, conn, "vault.unlockResolved", map[string]any{
		"requestId": rid,
		"outcome":   "unsealed",
	})
	var okEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &okEnv); err != nil {
		t.Fatalf("unmarshal resolution: %v", err)
	}
	if okEnv.Error != nil {
		t.Fatalf("vault.unlockResolved refused while the lane was saturated: %+v", okEnv.Error)
	}
	select {
	case err := <-released:
		if err != nil {
			t.Fatalf("RequestUnlock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("vault.unlockResolved did not release the waiter under lane saturation")
	}
}

func TestSaturationRefusalEmitsSafeDebugDiagnostic(t *testing.T) {
	var buf syncBuffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := log.NewSlogAdapter(slog.New(handler))
	srv, rec := blockingProbeServerWithLogger(t, 1, logger)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	startBlockedProbe(t, conn, rec)

	assertDiagnostic := func(secret, method, methodClass, scope, disposition string) {
		t.Helper()
		logged := buf.String()
		for _, forbidden := range []string{secret, "capacity exhausted"} {
			if strings.Contains(logged, forbidden) {
				t.Fatalf("saturation diagnostic leaked %q:\n%s", forbidden, logged)
			}
		}
		for _, line := range strings.Split(strings.TrimSpace(logged), "\n") {
			if line == "" {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatalf("bad log line %q: %v", line, err)
			}
			if record["msg"] != "control action refused" {
				continue
			}
			if record["method"] != method ||
				record["methodClass"] != methodClass ||
				record["scope"] != scope ||
				record["disposition"] != disposition ||
				record["retryAfterMs"] != float64(0) {
				t.Fatalf("incomplete saturation diagnostic: %v", record)
			}
			return
		}
		t.Fatalf("missing control action refusal diagnostic:\n%s", logged)
	}

	requestSecret := "request-secret-must-not-be-logged"
	resp := jsonrpcCall(t, conn, "fs.complete", map[string]any{
		"text":  requestSecret,
		"cwd":   "/",
		"limit": 5,
	})
	if !strings.Contains(string(resp), `"code":-32004`) {
		t.Fatalf("expected saturation response, got %s", resp)
	}
	assertDiagnostic(requestSecret, "fs.complete", "fs", "control", "request")

	buf.Reset()
	notificationSecret := "notification-secret-must-not-be-logged"
	frame := fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"fs.complete","params":{"text":%q,"cwd":"/","limit":5}}`,
		notificationSecret,
	)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write notification: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read saturation notification: %v", err)
	}
	assertDiagnostic(notificationSecret, "fs.complete", "fs", "control", "notification")

	buf.Reset()
	innerRequestValue := "inner-refusal-sensitive-value"
	srv.methods["test.innerRefusal"] = reg(
		control.NewBoundedSubmission(control.NewSemaphore("test-dispatch", 1)),
		"test.innerRefusal",
		// The merge: every method now declares a params validator (a method
		// without one stopped building, nocx-q27y) and writes through the
		// Responder, which is what the sealed normalizer wraps (nocx-k41yv).
		// This one accepts what the call sends — the payload IS the fixture
		// here (the diagnostic must not echo it back), so validating it away
		// would delete the thing under test.
		params(func(json.RawMessage) string { return "" }),
		func(w *wsConn, _ *connState, r Responder) handlerFunc {
			_ = w
			return func(_ context.Context, req jsonrpcRequest) {
				answerOperationRefusal(r, req, &capability.RefusedError{
					Rejection: control.Rejection{
						Reason: "inner capacity exhausted: " + innerRequestValue,
						Scope:  "config",
					},
				})
			}
		},
	)
	innerConn := connectWS(t, srv)
	defer innerConn.Close() //nolint:errcheck
	innerResp := jsonrpcCall(t, innerConn, "test.innerRefusal", map[string]string{"sensitive": innerRequestValue})
	if !strings.Contains(string(innerResp), `"code":-32004`) {
		t.Fatalf("expected inner saturation response, got %s", innerResp)
	}
	assertDiagnostic(innerRequestValue, "test.innerRefusal", "test", "config", "request")
}

// ── Ordinary saturation returns the structured retryable error immediately
// ── (over the wire, validated against the contract schema — the
// ── over-the-wire half that needed a real saturation off a real socket).

func TestSaturationError_OverTheWireConformsToContract(t *testing.T) {
	srv, rec := blockingProbeServer(t, 1)
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	startBlockedProbe(t, conn, rec)

	resp := jsonrpcCall(t, conn, "fs.complete", map[string]any{"text": "/etc", "cwd": "/", "limit": 5})
	var env struct {
		Error *struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil {
		t.Fatalf("expected a refusal, got %s", resp)
	}
	if env.Error.Code != SaturationErrorCode {
		t.Fatalf("error code = %d, want %d", env.Error.Code, SaturationErrorCode)
	}
	if env.Error.Message != SaturationMessage {
		t.Fatalf("error message = %q, want %q", env.Error.Message, SaturationMessage)
	}
	// The data payload is the contract: exact key set, required fields,
	// fixed reason vocabulary, retryable, retryAfterMs >= 0.
	validateJSON(t, loadSchema(t, "control.saturated.schema.json"), env.Error.Data, "saturation error data")
}

// ── A refused notification (no id) is answered with the rate-limited
// ── control.saturated notification, never an error, at most one per
// ── (class, scope) per interval.

func TestRefusedNotificationRateLimitedOverTheWire(t *testing.T) {
	srv, rec := blockingProbeServer(t, 1)
	// Shorten the server-side emission interval so the test does not sleep
	// a full second.
	srv.satNotify.interval = 50 * time.Millisecond

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	startBlockedProbe(t, conn, rec)

	// Three refused NOTIFICATIONS (no id) of the same class+scope: the first
	// emits control.saturated, the next two within the interval stay silent.
	for range 3 {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(
			`{"jsonrpc":"2.0","method":"fs.complete","params":{"text":"/etc","cwd":"/","limit":5}}`,
		)); err != nil {
			t.Fatalf("write notification: %v", err)
		}
	}

	// Collect text frames until quiet; count control.saturated notifications.
	saturated := 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, data, err := conn.ReadMessage()
		if err != nil {
			break // quiet (timeout) — the burst has settled
		}
		if strings.Contains(string(data), `"method":"control.saturated"`) {
			saturated++
		}
	}
	if saturated != 1 {
		t.Fatalf("control.saturated notifications = %d, want exactly 1 (rate-limited)", saturated)
	}
}

// ── Responses may complete out of request order and still correlate
// ── correctly: the slow request's id and the fast request's id each ride
// ── their own response.

func TestResponsesCorrelateOutOfOrder(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	release := make(chan struct{})
	started := make(chan struct{})
	prober := &fakeProber{probeFn: func(ctx context.Context, host string, cfg *ssh.ConnectConfig) error {
		close(started)
		<-release
		return nil
	}}
	srv := NewWSServer(logger, newRegWithStub(logger),
		WithProber(prober),
		WithProfileResolver(probeResolver()),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	// Request 1: a probe that blocks until released — its response cannot
	// arrive before the release.
	sendControl(t, conn, "connections.test", map[string]any{"profileId": "ssh:test:1"}, 1)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("probe never started")
	}

	// Request 2: a fast ordinary method, sent AFTER the slow one.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(
		`{"jsonrpc":"2.0","id":2,"method":"fs.complete","params":{"text":"/etc","cwd":"/","limit":5}}`,
	)); err != nil {
		t.Fatalf("write fs.complete: %v", err)
	}

	// The fast response arrives while the slow request is still pending,
	// and it carries the fast request's id — out-of-order completion with
	// correct correlation. Read TEXT frames with a deadline (the wsReader
	// forwards binary frames only).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading responses: %v", err)
		}
		if strings.Contains(string(data), `"id":2`) {
			if !strings.Contains(string(data), `"result"`) {
				t.Fatalf("fast response is not a result: %s", data)
			}
			break
		}
	}

	// Release the probe: the slow response then arrives with ITS own id.
	close(release)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading slow response: %v", err)
		}
		if strings.Contains(string(data), `"id":1`) {
			if !strings.Contains(string(data), `"result"`) {
				t.Fatalf("slow response is not a result: %s", data)
			}
			return
		}
	}
	t.Fatal("the slow response never arrived after the release")
}

// ── Conflicting mutations WAIT (serialized), from one socket and from two,
// ── while a non-conflicting operation still overlaps. The socket-level
// ── helpers:
// readResponseFor reads until the response with the given id arrives,
// skipping notifications and other ids, or fails after d. It must be the
// FIRST read on the connection (gorilla memoizes read errors, so a
// deadline-poisoned connection can never be read again — the waiting
// assertions below use the profile store, not socket reads, for that
// reason).
func readResponseFor(t *testing.T, conn *websocket.Conn, id int, d time.Duration) json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		_ = conn.SetReadDeadline(deadline)
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("response for id %d never arrived: %v", id, err)
		}
		var env struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(data, &env) != nil || len(env.ID) == 0 || string(env.ID) == "null" {
			continue // notification
		}
		var got int
		if json.Unmarshal(env.ID, &got) == nil && got == id {
			return data
		}
	}
}

// hasProfile reads the profile store directly and reports whether the id is
// present. The store is the ground truth of whether a mutation ran: a
// mutation WAITING on a held gate has not written it, so the check is
// deterministic and does not touch the socket (which must stay readable for
// the response assertions).
func hasProfile(t *testing.T, ps *profile.JSONStore, id string) bool {
	t.Helper()
	profiles, err := ps.LoadProfiles()
	if err != nil {
		t.Fatalf("load profiles: %v", err)
	}
	for _, p := range profiles {
		if p.ID == id {
			return true
		}
	}
	return false
}

// ── Conflicting mutations WAIT (serialized), from one socket and from two,
// ── while a non-conflicting operation still overlaps.

func TestConflictingMutationsSameProtectionOneAndTwoSockets(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	dialStarted := make(chan struct{})
	releaseDial := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseDial:
		default:
			close(releaseDial)
		}
	})
	reg.WithSSHFactory(&stubSSHFactory{
		connectFn: func(ctx context.Context, host string, _ ...ssh.ConnectOption) (ssh.Channel, error) {
			close(dialStarted)
			<-releaseDial
			return nil, &ssh.ErrAuthFailed{User: "test", Host: host, Err: errors.New("blocked then refused")}
		},
	})

	ps := profile.NewJSONStore(t.TempDir() + "/p.json")
	srv := NewWSServer(logger, reg,
		WithProfileRepository(ps),
		WithGroupRepository(ps),
		WithContentDB(&fakeHistoryDB{}),
		WithProfileResolver(&fakeResolver{
			resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
				return "host.example.com", &ssh.ConnectConfig{User: "test", Port: 22}, nil
			},
		}),
		// A LONG wait bound: the conflicting mutations must WAIT for the
		// dial rather than exhaust the bound — a conflict is a queue of
		// length two, and the test drives the dial's release directly.
		WithDomainConflictWaitTimeout(30*time.Second),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	connA := connectWS(t, srv)
	defer connA.Close() //nolint:errcheck

	// An SSH open whose DIAL blocks holds the [config, session] gates for
	// the whole dial (the conservative grain).
	sendControl(t, connA, "open", map[string]any{
		"kind": "ssh", "profileId": "ssh:p:1",
		"cols": 80, "rows": 24,
	}, 1)
	select {
	case <-dialStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("dial never started")
	}

	// A conflicting config mutation from the SAME socket WAITS: with the
	// dial holding the gates, it cannot run until the dial releases, and
	// with the waiting gates it is not refused. Give the task a moment to
	// reach the gate, then prove it has not landed.
	sendControl(t, connA, "profiles.create", map[string]any{
		"id": "ssh:p:2", "name": "b", "type": "ssh",
		"options": map[string]any{"host": "b.example.com"},
	}, 2)
	time.Sleep(150 * time.Millisecond)
	if hasProfile(t, ps, "ssh:p:2") {
		t.Fatal("same-socket conflicting mutation landed while the gate was held: it must wait")
	}

	// The same mutation from a SECOND socket gets the same treatment: the
	// gate protection is per-domain, not per-connection.
	connB := connectWS(t, srv)
	defer connB.Close() //nolint:errcheck
	sendControl(t, connB, "profiles.create", map[string]any{
		"id": "ssh:p:3", "name": "c", "type": "ssh",
		"options": map[string]any{"host": "c.example.com"},
	}, 3)
	time.Sleep(150 * time.Millisecond)
	if hasProfile(t, ps, "ssh:p:3") {
		t.Fatal("cross-socket conflicting mutation landed while the gate was held: it must wait")
	}

	// A NON-conflicting operation (content domain) genuinely overlaps the
	// blocked dial: it completes while the config gates are held. This is
	// the FIRST read on connB — the socket is still clean.
	hist := jsonrpcCall(t, connB, "history.query", map[string]any{
		"scope": "everywhere", "limit": 5,
	})
	var histEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(hist, &histEnv); err != nil {
		t.Fatalf("unmarshal history: %v", err)
	}
	if histEnv.Error != nil {
		t.Fatalf("non-conflicting history.query refused during the blocked dial: %+v", histEnv.Error)
	}

	// Release the dial: the open completes (with its dial error), the
	// gates free, and BOTH waiting mutations are serialized through and
	// succeed — a conflict waits, it is never refused.
	select {
	case <-releaseDial:
	default:
		close(releaseDial)
	}

	for _, tc := range []struct {
		conn *websocket.Conn
		id   int
	}{
		{connA, 2},
		{connB, 3},
	} {
		resp := readResponseFor(t, tc.conn, tc.id, 10*time.Second)
		var env struct {
			Error *jsonrpcErrorObj `json:"error"`
		}
		if err := json.Unmarshal(resp, &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Error != nil {
			t.Fatalf("conflicting mutation refused after the gate freed: %s", resp)
		}
	}

	// Both mutations actually landed (serialized, not dropped).
	if !hasProfile(t, ps, "ssh:p:2") || !hasProfile(t, ps, "ssh:p:3") {
		t.Fatal("serialized mutations missing from the profile store")
	}
}

// ── Conflicting mutations WAIT (serialized), from one socket and from two,
// ── while a non-conflicting operation still overlaps.

// ── A single sequential client issuing conflicting requests back to back,
// ── as fast as the responses arrive, is never told the control plane is
// ── busy. The deterministic window proof lives in the control package
// ── (TestResponseEnqueuedBeforeReleaseIsNotRefused drives the release
// ── ordering directly); this is the socket-level regression loop.

func TestSequentialBackToBackConflictingRequestsNeverSaturated(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	ps := profile.NewJSONStore(t.TempDir() + "/p.json")
	srv := NewWSServer(logger, newRegWithStub(logger),
		WithProfileRepository(ps),
		WithGroupRepository(ps),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	// create (config) then list (config), back to back, as fast as the
	// responses arrive. The create's response is enqueued before its gate
	// is released, so without the waiting gates the very next request can
	// land in that window and be refused.
	for i := range 25 {
		id := i*2 + 1
		createResp := jsonrpcCallWithID(t, conn, "profiles.create", map[string]any{
			"id": fmt.Sprintf("ssh:seq:%d", i), "name": "seq", "type": "ssh",
			"options": map[string]any{"host": "seq.example.com"},
		}, id)
		var env struct {
			Error *jsonrpcErrorObj `json:"error"`
		}
		if err := json.Unmarshal(createResp, &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Error != nil {
			t.Fatalf("iteration %d: profiles.create refused: %s", i, createResp)
		}
		listResp := jsonrpcCallWithID(t, conn, "profiles.list", nil, id+1)
		if strings.Contains(string(listResp), `"error"`) {
			t.Fatalf("iteration %d: profiles.list refused right after create: %s", i, listResp)
		}
	}
}

// ── The bounded wait is bounded: a conflict that outlives the wait bound
// ── is refused with the saturation error, and only then.

func TestBoundedConflictWaitExhaustsToRefusal(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	dialStarted := make(chan struct{})
	releaseDial := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseDial:
		default:
			close(releaseDial)
		}
	})
	reg.WithSSHFactory(&stubSSHFactory{
		connectFn: func(ctx context.Context, host string, _ ...ssh.ConnectOption) (ssh.Channel, error) {
			close(dialStarted)
			<-releaseDial
			return nil, &ssh.ErrAuthFailed{User: "test", Host: host, Err: errors.New("blocked")}
		},
	})

	ps := profile.NewJSONStore(t.TempDir() + "/p.json")
	srv := NewWSServer(logger, reg,
		WithProfileRepository(ps),
		WithGroupRepository(ps),
		WithProfileResolver(&fakeResolver{
			resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
				return "host.example.com", &ssh.ConnectConfig{User: "test", Port: 22}, nil
			},
		}),
		// A SHORT wait bound: the conflicting request exhausts it and is
		// refused with the saturation error — the refusal is the bound
		// exhausting, not the instant refusal of the old code.
		WithDomainConflictWaitTimeout(150*time.Millisecond),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	// The blocked dial holds the [config, session] gates.
	sendControl(t, conn, "open", map[string]any{
		"kind": "ssh", "profileId": "ssh:p:1",
		"cols": 80, "rows": 24,
	}, 1)
	select {
	case <-dialStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("dial never started")
	}

	// The conflicting mutation waits the bound out, then is refused.
	start := time.Now()
	refused := jsonrpcCallWithID(t, conn, "profiles.create", map[string]any{
		"id": "ssh:p:2", "name": "b", "type": "ssh",
		"options": map[string]any{"host": "b.example.com"},
	}, 2)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(refused, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil || env.Error.Code != SaturationErrorCode {
		t.Fatalf("conflicting request must be refused after the wait bound, got %s", refused)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("refusal came after %v; the wait bound was not honored", elapsed)
	}

	select {
	case <-releaseDial:
	default:
		close(releaseDial)
	}
}

// ── Ordinary execution saturation (the lane) still refuses instantly,
// ── including for domain-gated methods: the refusal is answered by the
// ── handler with the same -32004, never a wait.

func TestLaneSaturationRefusesDomainMethodInstantly(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	rec := &probeCallRecorder{started: make(chan struct{}), cancelled: make(chan struct{})}
	ps := profile.NewJSONStore(t.TempDir() + "/p.json")
	srv := NewWSServer(logger, newRegWithStub(logger),
		WithControlLaneCapacity(1),
		WithProber(rec),
		WithProfileResolver(probeResolver()),
		WithProfileRepository(ps),
		WithGroupRepository(ps),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	// A blocked probe holds the only lane permit.
	sendControl(t, conn, "connections.test", map[string]any{"profileId": "ssh:test:1"}, 1)
	select {
	case <-rec.started:
	case <-time.After(5 * time.Second):
		t.Fatal("probe never started")
	}

	// A domain-gated request finds the lane full and is refused instantly
	// with the saturation error — the lane never waits, even under the new
	// waiting gates (the conflict gates are free; the lane is the bound).
	start := time.Now()
	refused := jsonrpcCallWithID(t, conn, "profiles.create", map[string]any{
		"id": "ssh:p:1", "name": "a", "type": "ssh",
		"options": map[string]any{"host": "a.example.com"},
	}, 2)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(refused, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil || env.Error.Code != SaturationErrorCode {
		t.Fatalf("lane-saturated domain request must be refused, got %s", refused)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("lane refusal took %v; saturation must refuse immediately", elapsed)
	}
}

// ── A mutation awaited, then a read: the read observes the mutation. They
// ── are deliberately NOT sent together — that would canonise a FIFO the
// ── transport does not promise.

func TestMutationAwaitedThenReadObservesIt(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	ps := profile.NewJSONStore(t.TempDir() + "/p.json")
	srv := NewWSServer(logger, newRegWithStub(logger),
		WithProfileRepository(ps),
		WithGroupRepository(ps),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	// Await the mutation.
	createResp := jsonrpcCall(t, conn, "profiles.create", map[string]any{
		"id": "ssh:await:1", "name": "awaited", "type": "ssh",
		"options": map[string]any{"host": "await.example.com"},
	})
	var createEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(createResp, &createEnv); err != nil {
		t.Fatalf("unmarshal create: %v", err)
	}
	if createEnv.Error != nil {
		t.Fatalf("profiles.create: %+v", createEnv.Error)
	}

	// THEN the read: it must observe the mutation.
	listResp := jsonrpcCall(t, conn, "profiles.list", nil)
	if !strings.Contains(string(listResp), "ssh:await:1") {
		t.Fatalf("profiles.list did not observe the awaited create: %s", listResp)
	}
}
