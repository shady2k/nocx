package app

// After a typed ssh attempt whose child domain never helloed, and whose
// session has closed with a nonzero status, the next control requests on the
// same connection must be answered — not refused for capacity
// error ("Control plane busy", nocx-3an1s.1.2).
//
// The incident has no backend log, so this is the first DONE WHEN test: it
// drives the production composition of the typed path — the child-domain
// listener the grant builder creates, a dialer that never proves ownership,
// and a session that ends with the ssh client's own nonzero status — and then
// asks the control plane three questions that name each scope an admission
// refusal could carry: the global execution lane (app.about), the session
// domain gate (sessions.status), and the closed session's own id (close).
//
// The hello bound is stated by the test, not slept through: the listener's
// reporter delivers the loss event, which is the only clock this test reads.
// A second test asks the same questions while the hello window is still open,
// because "slow holder during the window" is the explanation that remains if
// the after-close path is clean.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
)

// exitStatus is a channel's captured wait result carrying a process code, in
// the shape session.ExitOutcome maps to cause=exited with that status.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitStatus) ExitCode() int { return int(e) }

// exitPTY is a session channel that ends when the test says the ssh client
// did, with the status it says. Nothing is invented from a duration: the
// close and the wait result are one event.
type exitPTY struct {
	done     chan struct{}
	closeOne sync.Once

	mu       sync.Mutex
	waitErr  error
	recorded bool
}

func newExitPTY() *exitPTY { return &exitPTY{done: make(chan struct{})} }

func (p *exitPTY) Read([]byte) (int, error) {
	<-p.done
	return 0, io.EOF
}

func (p *exitPTY) Write(b []byte) (int, error) { return len(b), nil }
func (p *exitPTY) Close() error {
	p.closeOne.Do(func() { close(p.done) })
	return nil
}
func (p *exitPTY) Resize(context.Context, uint16, uint16, uint16, uint16) error { return nil }
func (p *exitPTY) Done() <-chan struct{}                                        { return p.done }

func (p *exitPTY) WaitErr() (error, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr, p.recorded
}

// end records the status and closes Done: the session's exit is one event,
// exactly as the pty reports it.
func (p *exitPTY) end(err error) {
	p.mu.Lock()
	p.waitErr = err
	p.recorded = true
	p.mu.Unlock()
	_ = p.Close()
}

type onePTYFactory struct{ pt pty.Pty }

func (f *onePTYFactory) NewPTY(context.Context, pty.Config) (pty.Pty, error) { return f.pt, nil }

// blockingEndPTY is a session channel whose teardown the test holds. It is the
// production shape of a close whose helper or ssh channel is slow: the
// identity is already retired, and the process completion has not settled.
type blockingEndPTY struct {
	*exitPTY
	entered sync.Once
	enter   chan struct{}
	release chan struct{}
}

func newBlockingEndPTY() *blockingEndPTY {
	return &blockingEndPTY{exitPTY: newExitPTY(), enter: make(chan struct{}), release: make(chan struct{})}
}

func (p *blockingEndPTY) EndSession(context.Context) error {
	p.entered.Do(func() { close(p.enter) })
	<-p.release
	return p.exitPTY.Close()
}

// typedBusyResponse is one JSON-RPC answer, kept raw so the assertion can see
// the error code rather than a flattened message.
type typedBusyResponse struct {
	id    int
	raw   json.RawMessage
	code  int
	data  json.RawMessage
	error bool
}

type typedBusyExit struct {
	SessionID string `json:"sessionId"`
	Cause     string `json:"cause"`
	Status    int    `json:"status"`
}

// typedBusyWire is the one reader of the socket. Gorilla websocket permits a
// single reader, so answers and the exit notification are routed here and
// nowhere else.
type typedBusyWire struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	answers chan typedBusyResponse
	exits   chan typedBusyExit
	readErr chan error
}

func newTypedBusyWire(t *testing.T, ws *transport.WSServer) *typedBusyWire {
	t.Helper()
	d := websocket.Dialer{Subprotocols: []string{"nocx.token." + ws.Token()}}
	conn, _, err := d.Dial("ws://"+ws.Addr()+"/session", nil)
	if err != nil {
		t.Fatalf("dial the transport: %v", err)
	}
	w := &typedBusyWire{
		conn: conn, answers: make(chan typedBusyResponse, 32),
		exits: make(chan typedBusyExit, 4), readErr: make(chan error, 1),
	}
	t.Cleanup(func() { _ = conn.Close() })
	go w.read()
	return w
}

func (w *typedBusyWire) read() {
	for {
		kind, raw, err := w.conn.ReadMessage()
		if err != nil {
			select {
			case w.readErr <- err:
			default:
			}
			return
		}
		if kind != websocket.TextMessage {
			continue // binary frames are the session's output; none is produced here
		}
		var env struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int             `json:"code"`
				Message string          `json:"message"`
				Data    json.RawMessage `json:"data"`
			} `json:"error"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		if env.ID != nil {
			resp := typedBusyResponse{id: *env.ID, raw: raw}
			if env.Error != nil {
				resp.error = true
				resp.code = env.Error.Code
				resp.data = env.Error.Data
			}
			w.answers <- resp
			continue
		}
		if env.Method == "exit" {
			var exit typedBusyExit
			if json.Unmarshal(env.Params, &exit) == nil {
				w.exits <- exit
			}
		}
	}
}

func (w *typedBusyWire) send(id int, method string, params any) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	req := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if id != 0 {
		req["id"] = id
	}
	if err := w.conn.WriteJSON(req); err != nil {
		panic(fmt.Sprintf("send %s: %v", method, err))
	}
}

func (w *typedBusyWire) answer(t *testing.T, id int) typedBusyResponse {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case resp := <-w.answers:
			if resp.id == id {
				return resp
			}
		case err := <-w.readErr:
			t.Fatalf("socket closed while waiting for answer %d: %v", id, err)
		case <-deadline:
			t.Fatalf("no answer to request %d", id)
		}
	}
}

func (w *typedBusyWire) awaitExit(t *testing.T, sid string) typedBusyExit {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case exit := <-w.exits:
			if exit.SessionID == sid {
				return exit
			}
		case err := <-w.readErr:
			t.Fatalf("socket closed while waiting for the exit notification: %v", err)
		case <-deadline:
			t.Fatal("the session never reported its exit")
		}
	}
}

// capacityRefusalCode is the -32004 the control plane used to answer a
// refusal with. ADR-8eej3e66 stage 8 removed it; these tests assert it never
// comes back, which is why the number is spelled here and nowhere in the
// product.
const capacityRefusalCode = -32004

// assertAnsweredNotBusy is the bead's assertion: an answer arrived, and it is
// not the removed capacity refusal. Any other error passes — the claim is
// about admission, not about the answer's own subject.
func assertAnsweredNotBusy(t *testing.T, resp typedBusyResponse, what string) {
	t.Helper()
	if !resp.error || resp.code != capacityRefusalCode {
		return
	}
	var data struct {
		Scope string `json:"scope"`
	}
	_ = json.Unmarshal(resp.data, &data)
	t.Fatalf("%s was refused for capacity (scope=%q): %s", what, data.Scope, resp.raw)
}

// typedBusyStack is the production typed-path composition plus a real
// transport socket, with no sshd and no helper: what is under test is the
// child-domain listener and the admission plane, not a remote host.
type typedBusyStack struct {
	logger   log.Logger
	reg      *session.Reg
	ws       *transport.WSServer
	wire     *typedBusyWire
	pt       pty.Pty
	sid      string
	lane     lifecycle.LaneID
	loss     chan lifecyclechannel.LossCause
	stopRun  context.CancelFunc
	grant    lifecyclepub.GrantBootstrap
	pub      *lifecyclepub.Publisher
	parentLn *lifecyclechannel.Listener
}

func startTypedBusyStack(t *testing.T, helloTimeout time.Duration) *typedBusyStack {
	t.Helper()
	return startTypedBusyStackWith(t, helloTimeout, newExitPTY())
}

// startTypedBusyStackWith is the same stack with the session channel whose
// teardown the test can hold. There is no execution-lane capacity to name any
// more: ADR-8eej3e66 stage 8 removed the lane, so the only holder a test can
// create is the session's own teardown.
func startTypedBusyStackWith(t *testing.T, helloTimeout time.Duration, pt pty.Pty) *typedBusyStack {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := session.New(logger, &onePTYFactory{pt: pt})
	ws := transport.NewWSServer(logger, reg)
	ctx, cancel := context.WithCancel(context.Background())
	if err := ws.Start(ctx); err != nil {
		cancel()
		t.Fatalf("transport start: %v", err)
	}
	t.Cleanup(func() {
		_ = ws.Stop(context.Background())
		cancel()
	})
	wire := newTypedBusyWire(t, ws)
	wire.send(1, "open", map[string]any{"cols": 80, "rows": 24})
	openAnswer := wire.answer(t, 1)
	var opened struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(openAnswer.raw, &opened); err != nil || opened.Result.SessionID == "" {
		t.Fatalf("open returned no session id: %v (%s)", err, openAnswer.raw)
	}

	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	parentLn, err := lifecyclechannel.NewListener(logger, pub)
	if err != nil {
		t.Fatalf("parent listener: %v", err)
	}
	t.Cleanup(func() { _ = parentLn.Close() })
	lane := lifecycle.LaneID("lane-typed-busy-after-close")
	parent, err := pub.RequestDomain(lane, nil, parentLn.TransportID())
	if err != nil {
		t.Fatalf("mint parent domain: %v", err)
	}
	// The child may only be minted under a LIVE parent. A parent is live once
	// its own hello is accepted on the listener transport, so the test says
	// hello exactly as the parent shell's own lifecycle channel does.
	parentConn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(parentLn.Port())))
	if err != nil {
		t.Fatalf("dial parent listener: %v", err)
	}
	t.Cleanup(func() { _ = parentConn.Close() })
	if _, sendErr := lifecyclecodec.Encode(parentConn, lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       lane,
		Domain:     parent.Domain,
		Epoch:      parent.Epoch,
		Sequence:   1,
		Capability: parent.Capability,
		Event:      lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "typed-busy-test"}},
	}); sendErr != nil {
		t.Fatalf("send parent hello: %v", sendErr)
	}
	parentDec := lifecyclecodec.NewDecoder(parentConn, lifecyclecodec.Config{}, nil)
	accept, err := parentDec.ReadFrame()
	if err != nil || accept.Event.Kind != lifecycle.KindAccept {
		t.Fatalf("parent accept = %v (kind %v), err %v", accept, accept.Event.Kind, err)
	}

	sessions := newSessionRegistry()
	sessions.register(lane, opened.Result.SessionID)
	transports := newTransportRegistry()
	transports.register(parentLn.TransportID(), transportKind{local: true})

	sockRoot, err := os.MkdirTemp(filepath.Dir(ssh.DefaultControlRoot()), "nx")
	if err != nil {
		t.Fatalf("socket root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockRoot) })

	loss := make(chan lifecyclechannel.LossCause, 4)
	runCtx, stopRun := context.WithCancel(context.Background())
	t.Cleanup(stopRun)
	typed := &typedRunner{
		log: logger,
		wrapper: ssh.NewTypedWrapper(logger,
			ssh.NewSSHConfigResolver(logger, os.DevNull, ""), sockRoot),
		// Ownership is never proven: there is no control socket in this test.
		dial: func(string) (TypedMaster, error) {
			return nil, errors.New("no control socket in this test")
		},
		publish:      &countingPublisher{},
		sessions:     sessionWindows{reg: reg},
		probes:       defaultMasterProbes,
		helloTimeout: helloTimeout,
		lossReporter: func(l lifecycle.LaneID, ls lifecyclechannel.Loss) {
			if l == lane {
				loss <- ls.Cause
			}
		},
		deliveryContext: func() context.Context { return runCtx },
	}
	req := lifecyclepub.GrantRequest{
		Lane: lane, Parent: parent.Domain, Host: "example.test", User: "tester", Port: 22,
	}
	grant, err := buildSSHChildBootstrap(logger, pub, sessions, nil, req, transportKind{local: true}, typed)
	if err != nil || grant.Bootstrap == "" {
		t.Fatalf("the typed child bootstrap was not composed: %v", err)
	}
	return &typedBusyStack{
		logger: logger, reg: reg, ws: ws, wire: wire, pt: pt,
		sid: opened.Result.SessionID, lane: lane, loss: loss, stopRun: stopRun,
		grant: grant, pub: pub, parentLn: parentLn,
	}
}

// The three scopes an admission refusal can name, asked in the same order on
// the same connection: the global execution lane, the session domain gate,
// and a request naming the closed session's own id.
func askEveryAdmissionScope(t *testing.T, wire *typedBusyWire, sid string, firstID int) {
	t.Helper()
	askEveryAdmissionScopeWithClosed(t, wire, sid, firstID,
		"close", map[string]any{"sessionId": sid})
}

// askEveryAdmissionScopeWithClosed is the same sweep with the closed-id
// request chosen by the caller. The reason it is a parameter: `close` on one
// session id is ordered behind that id's previous close by the session-ops
// FIFO, which is an ordering promise and not an admission. A test that holds
// one close in flight must probe the closed id with a request that does not
// stand in that same FIFO (`session.signal`).
func askEveryAdmissionScopeWithClosed(
	t *testing.T, wire *typedBusyWire, sid string, firstID int, closedMethod string, closedParams any,
) {
	t.Helper()
	cases := []struct {
		name   string
		method string
		params any
	}{
		{"global scope (no admission at all)", "app.about", map[string]any{}},
		{"session scope (the session domain gate)", "sessions.status", map[string]any{"profileIds": []string{}}},
		{"the closed session's id", closedMethod, closedParams},
	}
	for i, tc := range cases {
		id := firstID + i
		wire.send(id, tc.method, tc.params)
		assertAnsweredNotBusy(t, wire.answer(t, id), tc.name)
	}
}

// TestTypedSSHHelloTimeoutThenCloseLeavesEveryAdmissionScopeAnswering is the
// bead's first DONE WHEN: the child domain's listener reports hello-timeout,
// ownership was never proven, the session ends with the ssh client's nonzero
// status, and the next control requests are answered.
func TestTypedSSHHelloTimeoutThenCloseLeavesEveryAdmissionScopeAnswering(t *testing.T) {
	s := startTypedBusyStack(t, 25*time.Millisecond)

	select {
	case cause := <-s.loss:
		if cause != lifecyclechannel.LossHelloTimeout {
			t.Fatalf("the child listener reported %q, want hello-timeout", cause)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the child domain's listener never reported hello-timeout")
	}

	// The ssh client's own end, with the status a failed handshake leaves.
	sessionPTY, ok := s.pt.(*exitPTY)
	if !ok {
		t.Fatalf("the stack channel is %T, not the exit pty this test stated", s.pt)
	}
	sessionPTY.end(exitStatus(1))
	exit := s.wire.awaitExit(t, s.sid)
	if exit.Cause != "exited" || exit.Status != 1 {
		t.Fatalf("session exit = cause %q status %d, want exited/1", exit.Cause, exit.Status)
	}

	// The next control requests on the SAME connection.
	askEveryAdmissionScope(t, s.wire, s.sid, 2)
}

// TestTypedSSHHelloWindowHoldsNoAdmissionOfItsOwn asks the same questions
// while the listener is still waiting for the hello. If the after-close path
// is clean, this is the other explanation the incident leaves: a holder that
// is slow during the window. The test states which scopes are answered and
// which are refused, and refuses to invent a cause.
func TestTypedSSHHelloWindowHoldsNoAdmissionOfItsOwn(t *testing.T) {
	// Long enough that the window is still open when the probes are sent;
	// the test ends it by cancelling the delivery, never by sleeping.
	s := startTypedBusyStack(t, time.Hour)

	askEveryAdmissionScope(t, s.wire, s.sid, 2)
}

// TestTypedSSHCloseInFlightLeavesTheControlPlaneAnswering is the candidate
// holder the diagnosis named, driven through the same typed stack. A close
// retires the session's identity FIRST; this test holds the teardown that
// follows, so the session is already gone from the registry while its
// completion is still in flight. The next control requests on the same
// connection must be answered.
//
// The teardown is held, and the control plane must go on answering beside it:
// there is no lane for it to exhaust, and no capacity answer to give.
func TestTypedSSHCloseInFlightLeavesTheControlPlaneAnswering(t *testing.T) {
	pt := newBlockingEndPTY()
	s := startTypedBusyStackWith(t, 25*time.Millisecond, pt)

	select {
	case cause := <-s.loss:
		if cause != lifecyclechannel.LossHelloTimeout {
			t.Fatalf("the child listener reported %q, want hello-timeout", cause)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the child domain's listener never reported hello-timeout")
	}

	// The person's tab closes the failed session (the renderer does this on
	// the nonzero exit). The handler retires the identity, then reaches the
	// teardown, which this test holds.
	s.wire.send(2, "close", map[string]any{"sessionId": s.sid})
	select {
	case <-pt.enter:
	case <-time.After(20 * time.Second):
		t.Fatal("the close never reached its teardown")
	}
	if _, err := s.reg.Get(session.ID(s.sid)); err == nil {
		t.Fatal("the close has not retired the session before holding its teardown")
	}

	// Retirement is the close. Everything from here is teardown, and it must
	// not occupy the control plane.
	askEveryAdmissionScopeWithClosed(t, s.wire, s.sid, 3,
		"session.signal", map[string]any{"sessionId": s.sid, "signal": "interrupt"})

	close(pt.release)
	assertAnsweredNotBusy(t, s.wire.answer(t, 2), "the close's own answer")
}
