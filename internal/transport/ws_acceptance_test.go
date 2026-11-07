package transport

// The epic's acceptance tests, over the real socket (nocx-sfv6.5): the wired
// executor must let the data plane run while control work blocks, must keep
// the ingress-critical resolvers immediate, must never answer a person's call
// with a capacity error (ADR-8eej3e66 stage 8), must correlate out-of-order
// responses, and must give conflicting mutations the same gate protection
// from one socket as from two.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/operation/catalog"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/waittest"
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
// prober until released.
func blockingProbeServer(t *testing.T) (*WSServer, *probeCallRecorder) {
	t.Helper()
	return blockingProbeServerWithLogger(t, log.NewSlogAdapter(nil))
}

func blockingProbeServerWithLogger(
	t *testing.T,
	logger log.Logger,
) (*WSServer, *probeCallRecorder) {
	t.Helper()
	rec := &probeCallRecorder{started: make(chan struct{}), cancelled: make(chan struct{})}
	srv := NewWSServer(logger, newRegWithStub(logger),
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

// ── While ordinary work is blocked, vault.unlockResolved still completes its
// ── pending unlock and the blocked originating RPC resumes.

func TestUnlockResolvedCompletesWhileOtherWorkIsBlocked(t *testing.T) {
	srv, rec := blockingProbeServer(t)
	ctx := context.Background()

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	startBlockedProbe(t, conn, rec)

	// An ordinary request is ADMITTED and answered while the probe holds its
	// goroutine: nothing refuses for capacity any more (ADR-8eej3e66 stage 8).
	ordinary := jsonrpcCall(t, conn, "fs.complete", map[string]any{"text": "/etc", "cwd": "/", "limit": 5})
	if code := errorCodeOf(t, ordinary); code != 0 {
		t.Fatalf("an ordinary call was answered with error %d while a probe was in flight: %s", code, ordinary)
	}

	// The ask: RequestUnlock is blocked waiting for its resolution.
	released := make(chan error, 1)
	go func() { released <- srv.RequestUnlock(ctx, "acceptance") }()
	rid := waitForPendingAsk(t, srv)

	// The resolution arrives over the same socket the read loop consumes —
	// the resolver is ingress-critical and must complete under any load.
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
		t.Fatalf("vault.unlockResolved refused while other work was blocked: %+v", okEnv.Error)
	}
	select {
	case err := <-released:
		if err != nil {
			t.Fatalf("RequestUnlock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("vault.unlockResolved did not release the waiter")
	}
}

// ── A call that does not start is recorded, and neither the record nor the
// ── answer carries request text ───────────────────────────────────────────

func TestAWaitThatEndsIsRecordedWithoutRequestText(t *testing.T) {
	var buf syncBuffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := log.NewSlogAdapter(slog.New(handler))
	srv := NewWSServer(logger, newRegWithStub(logger))
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	requestSecret := "request-secret-must-not-be-logged"
	// A handler whose admission answer is the one this stage produces: the
	// wait ended, nothing started. The reason is free text and deliberately
	// NOT on the wire or in the log.
	srv.methods["test.waitEnded"] = reg(
		control.AsyncSubmission{},
		catalog.OpFsComplete,
		params(func(json.RawMessage) string { return "" }),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) {
				rejection := control.Rejection{
					Kind:   control.RejectionWaitExceeded,
					Reason: "conflict queue full: " + requestSecret,
					Scope:  "config",
				}
				control.ReportRefusal(ctx, rejection)
				answerOperationRefusal(r, req, &capability.RefusedError{Rejection: rejection})
			}
		},
	)

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "test.waitEnded", map[string]string{"text": requestSecret})
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
		t.Fatalf("expected the expiry answer, got %s", resp)
	}
	if env.Error.Code != OperationExpiredCode {
		t.Fatalf("error code = %d, want %d", env.Error.Code, OperationExpiredCode)
	}
	// The data payload is the contract: exact key set, the declaration's own
	// reason, and nothing from the request.
	validateJSON(t, loadSchema(t, "operation.expired.schema.json"), env.Error.Data, "expiry data")
	if strings.Contains(string(env.Error.Data), requestSecret) {
		t.Fatalf("the answer echoed request text: %s", env.Error.Data)
	}

	logged := buf.String()
	for _, forbidden := range []string{requestSecret, "conflict queue full"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("the diagnostic leaked %q:\n%s", forbidden, logged)
		}
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(logged), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("bad log line %q: %v", line, err)
		}
		if record["msg"] != "control action did not start" {
			continue
		}
		if record["method"] != "test.waitEnded" ||
			record["kind"] != string(control.RejectionWaitExceeded) ||
			record["scope"] != "config" ||
			record["disposition"] != "request" {
			t.Fatalf("incomplete wait diagnostic: %v", record)
		}
		found = true
	}
	if !found {
		t.Fatalf("missing the wait diagnostic:\n%s", logged)
	}
}

// ── The expiry answer is one shape for every domain ───────────────────────

func TestTheExpiryAnswer_IsOneShapeForEveryDomain(t *testing.T) {
	for _, decl := range catalog.Declarations() {
		if decl.Kind != catalog.KindMethod {
			continue
		}
		rec := &captureResponder{}
		answerOperationExpiry(rec, jsonrpcRequest{ID: json.RawMessage("1")}, decl.Expired, "")
		if rec.err == nil {
			t.Fatalf("%s: no answer was written", decl.ID)
		}
		if rec.err.Code != OperationExpiredCode {
			t.Fatalf("%s: code = %d, want %d", decl.ID, rec.err.Code, OperationExpiredCode)
		}
		raw, err := json.Marshal(rec.data)
		if err != nil {
			t.Fatalf("%s: marshal data: %v", decl.ID, err)
		}
		validateJSON(t, loadSchema(t, "operation.expired.schema.json"), raw, string(decl.ID)+" expiry data")
		var payload struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("%s: decode: %v", decl.ID, err)
		}
		if payload.Reason != string(decl.Expired) {
			t.Fatalf("%s: reason = %q, want %q", decl.ID, payload.Reason, decl.Expired)
		}
	}
}

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
	// THE GATE IS HELD BY THE RESOLVE, not by the dial. It used to be the
	// dial, and that is the defect nocx-l21ib.1 removed: an open now holds
	// [config, session] for its RESOLVE — store and vault reads — and dials
	// under neither, so a handshake can no longer refuse every other pane's
	// open and every config request for as long as it lasts. The property
	// this test is about is unchanged (a conflicting config mutation waits
	// on the gate, from one socket and from two); only the thing holding
	// the gate is now something that genuinely holds it.
	resolveStarted := make(chan struct{})
	releaseResolve := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseResolve:
		default:
			close(releaseResolve)
		}
	})
	reg.WithSSHFactory(&stubSSHFactory{
		connectFn: func(ctx context.Context, host string, _ ...ssh.ConnectOption) (ssh.Channel, error) {
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
				close(resolveStarted)
				<-releaseResolve
				return "host.example.com", &ssh.ConnectConfig{User: "test", Port: 22}, nil
			},
		}),
		// A LONG wait bound: the conflicting mutations must WAIT for the
		// resolve rather than exhaust the bound — a conflict is a queue of
		// length two, and the test drives the resolve's release directly.
		WithDomainConflictWaitTimeout(30*time.Second),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	connA := connectWS(t, srv)
	defer connA.Close() //nolint:errcheck

	// An SSH open whose RESOLVE blocks holds the [config, session] gates.
	sendControl(t, connA, "open", map[string]any{
		"kind": "ssh", "profileId": "ssh:p:1",
		"cols": 80, "rows": 24,
	}, 1)
	select {
	case <-resolveStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("resolve never started")
	}

	// resolveStarted is the gate-held observable. The queued mutations are
	// checked immediately and their responses are checked after release.
	sendControl(t, connA, "profiles.create", map[string]any{
		"id": "ssh:p:2", "name": "b", "type": "ssh",
		"options": map[string]any{"host": "b.example.com"},
	}, 2)
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
	if hasProfile(t, ps, "ssh:p:3") {
		t.Fatal("cross-socket conflicting mutation landed while the gate was held: it must wait")
	}

	// A NON-conflicting operation (content domain) genuinely overlaps the
	// blocked resolve: it completes while the config gates are held. This is
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
		t.Fatalf("non-conflicting history.query refused during the blocked resolve: %+v", histEnv.Error)
	}

	// Release the resolve: the open completes (with its dial error), the
	// gates free, and BOTH waiting mutations are serialized through and
	// succeed — a conflict waits, it is never refused.
	select {
	case <-releaseResolve:
	default:
		close(releaseResolve)
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

// ── A conflicting call WAITS and then runs; nothing is refused for capacity ──

func TestAConflictingCallWaitsAndThenRuns(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	// Held by the RESOLVE, for the reason the test above states.
	resolveStarted := make(chan struct{})
	releaseResolve := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseResolve:
		default:
			close(releaseResolve)
		}
	})
	reg.WithSSHFactory(&stubSSHFactory{
		connectFn: func(ctx context.Context, host string, _ ...ssh.ConnectOption) (ssh.Channel, error) {
			return nil, &ssh.ErrAuthFailed{User: "test", Host: host, Err: errors.New("blocked")}
		},
	})

	ps := profile.NewJSONStore(t.TempDir() + "/p.json")
	srv := NewWSServer(logger, reg,
		WithProfileRepository(ps),
		WithGroupRepository(ps),
		WithProfileResolver(&fakeResolver{
			resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
				close(resolveStarted)
				<-releaseResolve
				return "host.example.com", &ssh.ConnectConfig{User: "test", Port: 22}, nil
			},
		}),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	// The blocked resolve holds [config, session].
	sendControl(t, conn, "open", map[string]any{
		"kind": "ssh", "profileId": "ssh:p:1",
		"cols": 80, "rows": 24,
	}, 1)
	select {
	case <-resolveStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("resolve never started")
	}

	// The conflicting mutation waits for the holder. It is NOT answered with
	// a capacity error while it waits (ADR-8eej3e66 §7): its answer arrives
	// only when the gate frees.
	settled := make(chan json.RawMessage, 1)
	go func() {
		settled <- jsonrpcCallWithID(t, conn, "profiles.create", map[string]any{
			"id": "ssh:p:2", "name": "b", "type": "ssh",
			"options": map[string]any{"host": "b.example.com"},
		}, 2)
	}()
	waittest.WaitFor(t, "the conflicting write to be waiting", func() bool { return hasProfile(t, ps, "ssh:p:2") == false })
	select {
	case raw := <-settled:
		t.Fatalf("the conflicting write was answered while the holder was still running: %s", raw)
	case <-time.After(150 * time.Millisecond):
	}

	// The holder returns: the same action completes, and it did not start
	// before that.
	close(releaseResolve)
	raw := <-settled
	if code := errorCodeOf(t, raw); code != 0 {
		t.Fatalf("the conflicting write was answered with error %d after the gate freed: %s", code, raw)
	}
	if !hasProfile(t, ps, "ssh:p:2") {
		t.Fatal("the conflicting write was answered but its mutation was not applied")
	}
}

// A WAIT ENDS WITH THE OPERATION THAT SET IT (nocx-3an1s.1.1).
//
// The bead's criterion in one run, over the wire: one operation holds the
// config domain, a conflicting request waits, and the moment that holder
// returns the waiting request completes. No state outlives the operation that
// held the resource, and nothing is answered "busy" in between.
func TestAWaitEndsWhenTheHoldingOperationReleases(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	resolveStarted := make(chan struct{})
	releaseResolve := make(chan struct{})
	var released sync.Once
	release := func() { released.Do(func() { close(releaseResolve) }) }
	t.Cleanup(release)
	reg.WithSSHFactory(&stubSSHFactory{
		connectFn: func(ctx context.Context, host string, _ ...ssh.ConnectOption) (ssh.Channel, error) {
			return nil, &ssh.ErrAuthFailed{User: "test", Host: host, Err: errors.New("blocked")}
		},
	})
	ps := profile.NewJSONStore(t.TempDir() + "/p.json")
	srv := NewWSServer(logger, reg,
		WithProfileRepository(ps),
		WithGroupRepository(ps),
		WithProfileResolver(&fakeResolver{
			resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
				close(resolveStarted)
				<-releaseResolve
				return "host.example.com", &ssh.ConnectConfig{User: "test", Port: 22}, nil
			},
		}),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	// The holder: an open whose resolve blocks under [config, session].
	sendControl(t, conn, "open", map[string]any{
		"kind": "ssh", "profileId": "ssh:p:1",
		"cols": 80, "rows": 24,
	}, 1)
	select {
	case <-resolveStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("resolve never started")
	}

	create := func(id int) json.RawMessage {
		return jsonrpcCallWithID(t, conn, "profiles.create", map[string]any{
			"id": "ssh:p:2", "name": "b", "type": "ssh",
			"options": map[string]any{"host": "b.example.com"},
		}, id)
	}
	// While the holder is in its domain, the conflicting write waits — and is
	// not answered at all until the gate frees.
	settled := make(chan json.RawMessage, 1)
	go func() { settled <- create(2) }()
	select {
	case raw := <-settled:
		t.Fatalf("the conflicting write settled while the resolve held the domain: %s", raw)
	case <-time.After(150 * time.Millisecond):
	}
	release()
	raw := <-settled
	if code := errorCodeOf(t, raw); code != 0 {
		t.Fatalf("the write was answered with error %d after the holder returned: %s", code, raw)
	}
}

// ── A DIAL HOLDS NO DOMAIN GATE (nocx-l21ib.1) ────────────────────────────
//
// The inverse of the two tests above, and the reason they had to change: an
// ssh handshake waits on the network and sometimes on a person — the password
// prompt is answered by a human — and while it did so it held [config,
// session]. Every other pane's open, and every config, vault, git and files
// request, waited one second on those gates and was then refused, which the
// product showed as "The terminal is busy — that action was refused" and, for
// a pane, as a tab that died with "Terminal failed to start: Control plane
// busy". A restored workspace came back with one live pane and the rest dead.
//
// So this is written as the user's sentence: while one connection is dialing,
// the rest of the application still answers.

func TestDialInFlightRefusesNothing(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)

	// The FIRST dial blocks; any later one answers at once. A second open
	// must be able to reach its own dial — that is the half of the claim the
	// session gate used to make impossible.
	var dials atomic.Int32
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
			if dials.Add(1) == 1 {
				close(dialStarted)
				<-releaseDial
			}
			return nil, &ssh.ErrAuthFailed{User: "test", Host: host, Err: errors.New("no auth in this test")}
		},
	})

	ps := profile.NewJSONStore(t.TempDir() + "/p.json")
	srv := NewWSServer(logger, reg,
		// The ssh panes this test opens are this machine's helper's
		// (nocx-50w7p.5). Its subject is that a second open reaches its own
		// dial while the first is in flight — downstream of the pane existing,
		// and about the DIAL LANE rather than how the dial was reached.
		sshHelperOpt(reg),
		WithProfileRepository(ps),
		WithGroupRepository(ps),
		WithProfileResolver(&fakeResolver{
			resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
				return "host.example.com", &ssh.ConnectConfig{User: "test", Port: 22}, nil
			},
		}),
		// The PRODUCTION wait bound. A test that widened it could not fail:
		// the refusal this is about is the bound expiring, so the bound is
		// part of what is being asserted.
		WithDomainConflictWaitTimeout(DefaultDomainConflictWaitTimeout),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	sendControl(t, conn, "open", map[string]any{
		"kind": "ssh", "profileId": "ssh:p:1",
		"cols": 80, "rows": 24,
	}, 1)
	select {
	case <-dialStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("dial never started")
	}

	// A config mutation lands while the dial is in flight. Before the split
	// this waited out the bound and came back -32004.
	created := jsonrpcCallWithID(t, conn, "profiles.create", map[string]any{
		"id": "ssh:p:2", "name": "b", "type": "ssh",
		"options": map[string]any{"host": "b.example.com"},
	}, 2)
	assertNotRefused(t, created, "profiles.create during an in-flight dial")

	// And so does a SECOND open — the session half of the same claim. It
	// fails on its own auth, which is this stub's business and not the
	// point; what matters is that it was admitted and reached its dial.
	second := jsonrpcCallWithID(t, conn, "open", map[string]any{
		"kind": "ssh", "profileId": "ssh:p:2",
		"cols": 80, "rows": 24,
	}, 3)
	assertNotRefused(t, second, "a second open during an in-flight dial")
	if n := dials.Load(); n < 2 {
		t.Fatalf("the second open never reached a dial (%d dials); it was refused or queued behind the first", n)
	}

	close(releaseDial)
	readResponseFor(t, conn, 1, 10*time.Second)
}

// assertNotRefused fails when the response is the removed capacity refusal,
// -32004. Any OTHER error passes: these calls are made against stubs that fail
// for their own reasons, and the claim is about admission, not about the
// answer.
func assertNotRefused(t *testing.T, resp []byte, what string) {
	t.Helper()
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal %s: %v", what, err)
	}
	if env.Error != nil && env.Error.Code == -32004 {
		t.Fatalf("%s was answered with the removed capacity refusal: %s", what, resp)
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
