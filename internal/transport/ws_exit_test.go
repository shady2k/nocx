package transport

// The exit notification carries a cause: an authoritative shell exit (with
// its status) versus a loss (nocx-ictcq). These tests drive the real
// transport exit pipeline — handleOpen → monitorExit → the session's
// classification of a CONTROLLED captured wait outcome → the wire — and
// assert what the renderer receives. Before this change every
// one of these produced the identical {sessionId} payload, which is why a tab
// whose ssh connection dropped silently vanished: "the user typed exit" and
// "the channel died" were the same event at the wire.
//
// What these tests are, and what they are not: the exit notification has ONE
// loss-producing seam — monitorExit fires when the session's channel dies,
// and the classification input is the captured Wait outcome. "Unreachable
// host" and "expired handshake" are therefore driven here as the error
// shapes those scenarios leave in that seam, not as their originating
// events, because those events produce no exit notification at all:
//
//   - A host that is unreachable at CONNECT time fails the open RPC — there
//     is no session and no exit event (TestWSServer_OpenSSHError_ClassifiedError
//     pins that path). A host that dies MID-session is a channel loss: the
//     keepalive prober or the transport closes the connection, Wait returns a
//     non-exit error, and the classification below is the whole story.
//   - A handshake that expires (lifecycle.HelloTimeout) abandons the lifecycle
//     domain and is reported on the integration axis (session.integrationChanged
//     with reason handshake-timeout; TestHelloTimeoutAbandonsDomain /
//     TestHelloTimeoutReportsItsCause pin that seam). The session stays a
//     conventional terminal and does not exit; if it later ends without the
//     shell's own report, the exit cause is — correctly — interrupted.
//
// So the four scenarios below are the classification contract: each feeds the
// real pipeline an outcome of the class its scenario produces, and asserts
// the wire. The originating seams are covered by their own suites.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	gossh "golang.org/x/crypto/ssh"
)

// realExitStatus runs a real shell that exits with the given status and
// returns the *exec.ExitError it produced — the exact shape the local pty
// watcher records (cmd.Wait), so the wire test drives the authentic object
// rather than a hand-built stand-in. (The remote shape, *ssh.ExitError, has
// unexported fields and is produced by the ssh package's real-server tests.)
func realExitStatus(status int) error {
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(status)).Run() //nolint:gosec // test-only, fixed status
	return err
}

// exitFakePTY is a controllable pty.Pty for exit-cause tests. The test sets
// the wait outcome and closes done, mirroring exactly what the production
// watchers do — record the Wait result, then close done — so the channel-close
// ordering the session layer relies on is the one under test.
type exitFakePTY struct {
	done    chan struct{}
	waitErr error
	waitSet bool
}

func newExitFakePTY() *exitFakePTY {
	return &exitFakePTY{done: make(chan struct{})}
}

func (p *exitFakePTY) Read([]byte) (int, error)    { return 0, io.EOF }
func (p *exitFakePTY) Write(b []byte) (int, error) { return len(b), nil }
func (p *exitFakePTY) Resize(context.Context, uint16, uint16, uint16, uint16) error {
	return nil
}

func (p *exitFakePTY) Close() error {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	return nil
}
func (p *exitFakePTY) Done() <-chan struct{} { return p.done }

// recordWait sets the outcome and closes done, in that order — the same
// ordering as the pty and ssh watchers, so a reader that has observed <-done
// sees the record without further synchronisation.
func (p *exitFakePTY) recordWait(err error) {
	p.waitErr = err
	p.waitSet = true
	close(p.done)
}

// WaitErr is the optional-method seam the session layer reads to classify
// the exit (real channels capture the same shape).
func (p *exitFakePTY) WaitErr() (error, bool) { return p.waitErr, p.waitSet }

type exitFakePTYFactory struct{ fake *exitFakePTY }

func (f *exitFakePTYFactory) NewPTY(context.Context, pty.Config) (pty.Pty, error) {
	return f.fake, nil
}

// exitWire is the renderer-facing shape of the notification, decoded from
// the real socket.
type exitWire struct {
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
	Cause        string `json:"cause"`
	Status       *int   `json:"status"`
}

func newExitServer(t *testing.T, fake *exitFakePTY) *WSServer {
	t.Helper()
	reg := session.New(log.NewSlogAdapter(nil), &exitFakePTYFactory{fake: fake})
	ws := NewWSServer(log.NewSlogAdapter(nil), reg)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return ws
}

func openExitSession(t *testing.T, ws *WSServer) (sid string, conn *websocket.Conn) {
	t.Helper()
	conn = connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{"cols": 80, "rows": 24}, 1)
	var env struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("open: unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("open: %+v", env.Error)
	}
	// The open RESULT is not the moment this session can be observed.
	// handleOpen answers first and installs the connection as the session's
	// subscriber only afterwards, deliberately (AD-7), and every
	// session-scoped notification these callers provoke — exit, and
	// session.liveness — resolves its destination at emit time from that
	// slot. Provoking one before the install drops it at Debug and the
	// reader then waits out its whole window, which is what
	// TestLiveness_UnknownReachesTheRendererOverTheWire did under load.
	//
	// awaitSubscriber is the package's existing answer to this (ws_test.go);
	// openSessionOnConn has waited on it since nocx-2h08 and this opener is
	// simply the one that never did. It is NOT the app package's
	// notification-shaped wait: a session opened here comes off a fake pty
	// factory, so nothing registers it on the integration axis and no
	// session.integrationChanged is ever emitted to wait for. The slot
	// itself is read instead, through the same accessor the emit path uses,
	// and polling it touches no socket — so the read-deadline hazard that
	// rules internal/waittest out over there does not arise here.
	awaitSubscriber(t, ws, session.ID(env.Result.SessionID))
	return env.Result.SessionID, conn
}

func awaitExit(t *testing.T, conn *websocket.Conn) exitWire {
	t.Helper()
	raw := readNotification(t, conn, "exit", wantWithin)
	var got exitWire
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("exit params: unmarshal: %v", err)
	}
	return got
}

// A shell that exits on its own is an authoritative terminal event: the tab
// closes as it always did, and the exit status rides the wire — the value a
// loss can never carry.
func TestExitNotification_CleanShellExitCarriesStatus(t *testing.T) {
	fake := newExitFakePTY()
	ws := newExitServer(t, fake)
	sid, conn := openExitSession(t, ws)
	fake.recordWait(realExitStatus(42))
	got := awaitExit(t, conn)

	if got.SessionID != sid {
		t.Errorf("sessionId = %q, want %q", got.SessionID, sid)
	}
	if got.Cause != string(session.ExitExited) {
		t.Errorf("cause = %q, want %q", got.Cause, session.ExitExited)
	}
	if got.Status == nil || *got.Status != 42 {
		t.Errorf("status = %v, want 42", got.Status)
	}
}

// A status-0 shell exit is still an exit: nil from Wait means the process
// reported success, which is the same authoritative event as a nonzero one.
func TestExitNotification_CleanShellExitZeroStatus(t *testing.T) {
	fake := newExitFakePTY()
	ws := newExitServer(t, fake)
	_, conn := openExitSession(t, ws)

	fake.recordWait(nil)
	got := awaitExit(t, conn)

	if got.Cause != string(session.ExitExited) {
		t.Errorf("cause = %q, want %q", got.Cause, session.ExitExited)
	}
	if got.Status == nil || *got.Status != 0 {
		t.Errorf("status = %v, want 0", got.Status)
	}
}

// The channel is gone mid-session: a loss. The tab must be marked, never
// destroyed, and no status may ride the wire — a loss is not an exit. The
// error is the REAL type gossh returns when the remote side closed the
// channel without ever sending an exit status — what a dropped connection
// leaves in session.Wait — not a hand-written string.
func TestExitNotification_ChannelLossCarriesNoStatus(t *testing.T) {
	fake := newExitFakePTY()
	ws := newExitServer(t, fake)
	_, conn := openExitSession(t, ws)

	fake.recordWait(&gossh.ExitMissingError{})
	got := awaitExit(t, conn)

	if got.Cause != string(session.ExitInterrupted) {
		t.Errorf("cause = %q, want %q", got.Cause, session.ExitInterrupted)
	}
	if got.Status != nil {
		t.Errorf("status = %v, want absent for a loss", *got.Status)
	}
}

// The host was never reachable from the shell's point of view: the same
// class of error a dial or a keepalive death leaves in the channel's Wait —
// here a REAL refused-connect *net.OpError (from a just-released ephemeral
// port, so no hardcoded address), not a hand-written string. Still a loss:
// the backend cannot assert how the session ended, so it does not invent an
// exit. (A host unreachable at CONNECT time never reaches this seam at all —
// it fails the open RPC with no session — see the file header.)
func TestExitNotification_UnreachableHostIsALoss(t *testing.T) {
	fake := newExitFakePTY()
	ws := newExitServer(t, fake)
	_, conn := openExitSession(t, ws)

	// A deterministic refused-connect: bind an ephemeral port, release it,
	// then dial it — the connect is refused because nothing listens there.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	_, dialErr := net.Dial("tcp", addr)
	if dialErr == nil {
		t.Fatal("dial to a released port unexpectedly succeeded")
	}
	fake.recordWait(fmt.Errorf("ssh connect: %w", dialErr))
	got := awaitExit(t, conn)

	if got.Cause != string(session.ExitInterrupted) {
		t.Errorf("cause = %q, want %q", got.Cause, session.ExitInterrupted)
	}
	if got.Status != nil {
		t.Errorf("status = %v, want absent for a loss", *got.Status)
	}
}

// A session whose end is not the shell's own report is a loss — the
// classification contract, driven here with a deadline error as the
// representative of a timeout-bound teardown. This is CLASSIFICATION, not a
// path drive: the handshake-expiry seam itself (lifecycle.HelloTimeout) is
// an integration event, not an exit (see the file header).
func TestExitNotification_NonExitTeardownIsALoss(t *testing.T) {
	fake := newExitFakePTY()
	ws := newExitServer(t, fake)
	_, conn := openExitSession(t, ws)

	fake.recordWait(context.DeadlineExceeded)
	got := awaitExit(t, conn)

	if got.Cause != string(session.ExitInterrupted) {
		t.Errorf("cause = %q, want %q", got.Cause, session.ExitInterrupted)
	}
	if got.Status != nil {
		t.Errorf("status = %v, want absent for a loss", *got.Status)
	}
}

// A teardown that never let the process report — the explicit-close race,
// where Close closed done before the watcher recorded — must read as a loss,
// never as a fabricated exit.
func TestExitNotification_UnrecordedOutcomeIsALoss(t *testing.T) {
	fake := newExitFakePTY()
	ws := newExitServer(t, fake)
	_, conn := openExitSession(t, ws)

	// Close done without any record: monitorExit wakes and asks the session
	// how it ended, and the session must say "do not know", not "exited 0".
	_ = fake.Close()
	got := awaitExit(t, conn)

	if got.Cause != string(session.ExitInterrupted) {
		t.Errorf("cause = %q, want %q", got.Cause, session.ExitInterrupted)
	}
	if got.Status != nil {
		t.Errorf("status = %v, want absent for an unrecorded teardown", *got.Status)
	}
}

// The real exit off the real socket satisfies its contract — the assertion
// that would catch a field the DTO could marshal but the server never sends.
func TestExit_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "exit.schema.json")
	fake := newExitFakePTY()
	ws := newExitServer(t, fake)
	_, conn := openExitSession(t, ws)
	fake.recordWait(realExitStatus(7))
	raw := readNotification(t, conn, "exit", wantWithin)
	validateJSON(t, schema, raw, "exit params (real socket)")

	// And the loss half: a second session whose channel dies.
	fake2 := newExitFakePTY()
	ws2 := newExitServer(t, fake2)
	_, conn2 := openExitSession(t, ws2)
	fake2.recordWait(errors.New("ssh: connection lost"))
	raw2 := readNotification(t, conn2, "exit", wantWithin)
	validateJSON(t, schema, raw2, "exit params, loss (real socket)")
}

// The identity crosses the wire intact (nocx-3oupk): the exit carries the
// same instance + epoch the session record holds, so a renderer that stored
// the open ack's pair can refuse a late exit from a previous incarnation.
// The schema validation in the test above proves presence; this asserts the
// VALUES are the session's own, not fabricated at emit time.
func TestExit_IdentityCrossesTheWire(t *testing.T) {
	fake := newExitFakePTY()
	ws := newExitServer(t, fake)
	sid, conn := openExitSession(t, ws)

	sess, err := ws.registry.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	want := sess.Identity()

	fake.recordWait(realExitStatus(0))
	got := awaitExit(t, conn)
	if got.SessionID != sid {
		t.Errorf("sessionId = %q, want %q", got.SessionID, sid)
	}
	if got.InstanceID != string(want.InstanceID) {
		t.Errorf("instanceId = %q, want %q", got.InstanceID, want.InstanceID)
	}
	if got.SessionEpoch != want.Epoch {
		t.Errorf("sessionEpoch = %d, want %d", got.SessionEpoch, want.Epoch)
	}
}

// The open ack carries the identity too, so the renderer has somewhere to
// learn it from (AD-7: the backend mints, the renderer stores). Same value
// the exit and every observation will later carry.
func TestOpen_IdentityCrossesTheWire(t *testing.T) {
	fake := newExitFakePTY()
	ws := newExitServer(t, fake)

	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{"cols": 80, "rows": 24}, 1)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("open: unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("open: %+v", envelope.Error)
	}
	var env struct {
		Result openResult
	}
	if err := json.Unmarshal(envelope.Result, &env.Result); err != nil {
		t.Fatalf("open result: unmarshal: %v", err)
	}
	sess, err := ws.registry.Get(session.ID(env.Result.SessionID))
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	want := sess.Identity()
	if env.Result.InstanceID != string(want.InstanceID) {
		t.Errorf("instanceId = %q, want %q", env.Result.InstanceID, want.InstanceID)
	}
	if env.Result.SessionEpoch != want.Epoch {
		t.Errorf("sessionEpoch = %d, want %d", env.Result.SessionEpoch, want.Epoch)
	}
	// The REAL result off the real socket satisfies the contract — the
	// rule-5 check, on the bytes the server sent, not a re-marshal.
	validateJSON(t, loadSchema(t, "open.schema.json"), envelope.Result, "open result (real socket)")
}
