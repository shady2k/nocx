package transport

// The server→client request broker, proven over a real socket (nocx-e2j1z).
//
// The broker (request_broker.go) is deliberately standalone: it owns the
// request lifecycle and nothing else. A transport wires it with four seams
// — a Conns snapshot and a per-connection Deliver (the two delivery seams),
// the Resolve read-loop ingress, and the ConnectionLost lifecycle signal —
// and this file stands in for that transport with a real websocket server
// whose read loop dispatches one resolution method to the broker. The
// notification travels broker → deliver seam → socket → renderer; the
// resolution travels renderer → socket → read loop → broker.Resolve. The
// future integration bead wires the same seams into the WSServer's
// broadcast, control-plane registration and connection teardown; nothing in
// these tests depends on that wiring existing yet.
//
// Two of the tests express the existing ask brokers — vault.unlock and
// connections.password — through the mechanism, proving it is capable of
// replacing both. The method names are test-local ("test.unlockRequest",
// "test.passwordRequest") because the production names are already registered
// by the brokers this bead must not rewrite; the wire shapes (requestId +
// reason down, a closed outcome enum up) are the existing ones.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// errTestNoClient is the harness transport's answer when no renderer is
// attached — the seam's counterpart to ErrNoClientConnected and
// ErrPasswordNoClientConnected, owned by the transport that wires the seam.
var errTestNoClient = errors.New("no client connected to receive the request")

// errTestUnlockCancelled and errTestPromptCancelled are the cancelled
// outcomes of the two expressed asks, standing in for ErrUnlockCancelled and
// ErrPasswordPromptCancelled.
var (
	errTestUnlockCancelled = errors.New("unlock cancelled by user")
	errTestPromptCancelled = errors.New("password prompt cancelled")
)

// testAnswer is the typed result of the password-shaped ask, mirroring
// passwordAnswerPayload.
type testAnswer struct {
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

// testUnlockKind expresses the vault-unlock ask through the mechanism:
// requestId + reason down, a closed outcome enum (unsealed/cancelled) up.
func testUnlockKind(timeout time.Duration) RequestKind {
	return RequestKind{
		NotifyMethod:  "test.unlockRequest",
		ResolveMethod: "test.unlockResolved",
		NoClientErr:   errTestNoClient,
		Timeout:       timeout,
		Validate: func(raw json.RawMessage) string {
			var p struct {
				Outcome string `json:"outcome"`
			}
			if msg := decodeParams(raw, &p); msg != "" {
				return msg
			}
			switch p.Outcome {
			case "unsealed", "cancelled":
			default:
				return "outcome must be one of unsealed, cancelled"
			}
			return ""
		},
		Resolve: func(raw json.RawMessage) (json.RawMessage, error) {
			var p struct {
				Outcome string `json:"outcome"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			switch p.Outcome {
			case "unsealed":
				return json.RawMessage(`{}`), nil
			case "cancelled":
				return nil, errTestUnlockCancelled
			default:
				return nil, fmt.Errorf("unlock resolved with unknown outcome: %q", p.Outcome)
			}
		},
	}
}

// testPasswordKind expresses the connection-password ask through the
// mechanism: requestId + connection identity down, a closed outcome enum
// (submitted/cancelled) up, and a typed result for the submitted outcome.
func testPasswordKind(timeout time.Duration) RequestKind {
	return RequestKind{
		NotifyMethod:  "test.passwordRequest",
		ResolveMethod: "test.passwordResolved",
		NoClientErr:   errTestNoClient,
		Timeout:       timeout,
		Validate: func(raw json.RawMessage) string {
			var p struct {
				Outcome string `json:"outcome"`
			}
			if msg := decodeParams(raw, &p); msg != "" {
				return msg
			}
			switch p.Outcome {
			case "submitted", "cancelled":
			default:
				return "outcome must be one of submitted, cancelled"
			}
			return ""
		},
		Resolve: func(raw json.RawMessage) (json.RawMessage, error) {
			var p struct {
				Outcome  string `json:"outcome"`
				Password string `json:"password"`
				Remember bool   `json:"remember"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			switch p.Outcome {
			case "submitted":
				return json.Marshal(testAnswer{Password: p.Password, Remember: p.Remember})
			case "cancelled":
				return nil, errTestPromptCancelled
			default:
				return nil, fmt.Errorf("password prompt resolved with unknown outcome: %q", p.Outcome)
			}
		},
	}
}

// ── the transport half: a real websocket server whose read loop is the ────
// ── broker's resolver ingress and connection-lifecycle signal            ────

// harnessConn is one server-side websocket connection: the broker's Conn
// handle, the identity the send seam reports as a recipient and the read
// loop reports as lost. Writes are serialised (gorilla allows one concurrent
// writer); notification writes come from Request goroutines via the send
// seam, response writes from the read loop.
type harnessConn struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

func (c *harnessConn) notify(method string, params json.RawMessage) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(wantWithin))
	return c.conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (c *harnessConn) writeResponse(id json.RawMessage, rpcErr RPCError) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(wantWithin))
	if rpcErr.Code == 0 {
		_ = c.conn.WriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  map[string]any{},
		})
		return
	}
	_ = c.conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    rpcErr.Code,
			"message": rpcErr.Message,
		},
	})
}

// realSocketServer is the minimal transport the mechanism is proven against:
// a real websocket server whose read loop dispatches exactly one resolution
// method to the broker. On a connection's death the read loop exits and the
// server calls broker.ConnectionLost with the same handle the send seam
// reported — the lifecycle wiring the future integration bead moves into the
// WSServer's connection teardown.
type realSocketServer struct {
	broker   *Broker
	kind     RequestKind
	srv      *httptest.Server
	upgrader websocket.Upgrader
	mu       sync.Mutex
	conns    map[*harnessConn]struct{}
}

func newRealSocketServer(t *testing.T, broker *Broker, kind RequestKind) *realSocketServer {
	t.Helper()
	s := &realSocketServer{
		broker:   broker,
		kind:     kind,
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		conns:    make(map[*harnessConn]struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/session", s.handle)
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *realSocketServer) handle(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &harnessConn{conn: conn}
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		_ = conn.Close()
		// The lifecycle signal: the connection died, so every request that
		// counted it as a recipient loses a possible answerer.
		s.broker.ConnectionLost(c)
	}()
	s.readLoop(c)
}

func (s *realSocketServer) readLoop(c *harnessConn) {
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(wantWithin))
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var env struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		if env.Method != s.kind.ResolveMethod {
			c.writeResponse(env.ID, RPCError{Code: -32601, Message: "method not found"})
			continue
		}
		if rpcErr := s.broker.Resolve(env.Method, env.Params, c); rpcErr.Code != 0 {
			c.writeResponse(env.ID, rpcErr)
		} else {
			c.writeResponse(env.ID, RPCError{})
		}
	}
}

// harness binds the broker to the real socket server through the send seam.
type harness struct {
	broker *Broker
	srv    *realSocketServer
}

func newHarness(t *testing.T, kind RequestKind) *harness {
	t.Helper()
	h := &harness{}
	h.broker = NewBroker(h.conns, h.deliver)
	h.srv = newRealSocketServer(t, h.broker, kind)
	return h
}

// conns is the broker's Conns seam: a snapshot of the connected renderers.
func (h *harness) conns() []Conn {
	srv := h.srv
	srv.mu.Lock()
	defer srv.mu.Unlock()
	conns := make([]Conn, 0, len(srv.conns))
	for c := range srv.conns {
		conns = append(conns, c)
	}
	return conns
}

// deliver is the broker's Deliver seam: one request notification to one
// connection. Best-effort, like the WSServer's broadcastAsk; a failed write
// is not retried, and the connection stays a recipient until its death is
// reported through ConnectionLost.
func (h *harness) deliver(conn Conn, method string, params json.RawMessage) error {
	c, ok := conn.(*harnessConn)
	if !ok {
		return fmt.Errorf("deliver: unexpected connection %T", conn)
	}
	return c.notify(method, params)
}

// dial connects a renderer to the harness server.
func (h *harness) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(h.srv.srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url+"/session", nil)
	if err != nil {
		t.Fatalf("dial renderer: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func (h *harness) waitForRenderers(t *testing.T, n int) {
	t.Helper()
	waitFor(t, "renderer registered", wantWithin, func() bool {
		h.srv.mu.Lock()
		defer h.srv.mu.Unlock()
		return len(h.srv.conns) >= n
	})
}

// the span "resolvable from before the notification until its result, its
// timeout or the connection's death" is open. Returns the result channel and
// the minted request id.
func (h *harness) startRequest(t *testing.T, conn *websocket.Conn, kind RequestKind, params any, result any) (chan error, string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- h.broker.Request(context.Background(), kind, params, result)
	}()
	raw := readRequestNotification(t, conn, kind.NotifyMethod)
	return done, notificationRequestID(t, raw)
}

// ── renderer-side helpers ────────────────────────────────────────────────

// readRequestNotification reads the next frame from the renderer's socket
// and asserts it is the expected request notification, returning its params.
func readRequestNotification(t *testing.T, conn *websocket.Conn, method string) json.RawMessage {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read notification %s: %v", method, err)
	}
	var n struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(data, &n); err != nil {
		t.Fatalf("decode notification %s: %v", data, err)
	}
	if n.Method != method {
		t.Fatalf("expected notification %q, got %q", method, n.Method)
	}
	return n.Params
}

// notificationRequestID extracts the minted request id from a notification's
// params.
func notificationRequestID(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var p struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode notification params %s: %v", raw, err)
	}
	if p.RequestID == "" {
		t.Fatal("notification carried no requestId")
	}
	return p.RequestID
}

// brokerReply is the renderer's view of one resolution RPC's response.
type brokerReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// sendResolution sends a resolution RPC over the real socket and returns the
// server's response.
func sendResolution(t *testing.T, conn *websocket.Conn, method string, id json.RawMessage, params any) *brokerReply {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal resolution: %v", err)
	}
	if werr := conn.WriteMessage(websocket.TextMessage, payload); werr != nil {
		t.Fatalf("write resolution: %v", werr)
	}
	_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read resolution response: %v", err)
	}
	var reply brokerReply
	if err := json.Unmarshal(data, &reply); err != nil {
		t.Fatalf("decode resolution response %s: %v", data, err)
	}
	return &reply
}

// wantResult waits for a started Request to settle and asserts it returned
// exactly want.
func wantResult(t *testing.T, done chan error, want error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("Request returned %v, want %v", err, want)
		}
	case <-time.After(wantWithin):
		t.Fatalf("Request did not settle with %v", want)
	}
}

// ── the acceptance criteria ──────────────────────────────────────────────

// TestBroker_NotifiesConnectedRendererAndReturnsTypedResult is criterion 1
// (and the password half of criterion 5): a backend caller issues a request
// and receives a typed result, with the notification and the resolution both
// crossing a real socket. The renderer receives the minted request id and
// the ask's own params, answers with a submitted outcome, and the caller
// decodes the typed answer. The interval runs from before the notification
// (the id is registered before Send) to the result.
func TestBroker_NotifiesConnectedRendererAndReturnsTypedResult(t *testing.T) {
	h := newHarness(t, testPasswordKind(0))
	conn := h.dial(t)
	h.waitForRenderers(t, 1)

	var ans testAnswer
	done := make(chan error, 1)
	go func() {
		done <- h.broker.Request(context.Background(), testPasswordKind(0), map[string]any{
			"connection": "prod",
			"user":       "dev",
			"host":       "db.internal",
			"reason":     "open needs the password",
		}, &ans)
	}()

	raw := readRequestNotification(t, conn, "test.passwordRequest")
	var notif struct {
		RequestID  string `json:"requestId"`
		Connection string `json:"connection"`
		User       string `json:"user"`
		Host       string `json:"host"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &notif); err != nil {
		t.Fatalf("decode notification params: %v", err)
	}
	if !isLowerHex(notif.RequestID, 16) {
		t.Fatalf("requestId %q is not 16 lowercase hex chars", notif.RequestID)
	}
	if notif.Connection != "prod" || notif.User != "dev" || notif.Host != "db.internal" ||
		notif.Reason != "open needs the password" {
		t.Fatalf("notification lost the ask's params: %+v", notif)
	}

	reply := sendResolution(t, conn, "test.passwordResolved", json.RawMessage(`1`), map[string]any{
		"requestId": notif.RequestID,
		"outcome":   "submitted",
		"password":  "hunter2",
		"remember":  true,
	})
	if reply.Error != nil {
		t.Fatalf("resolution refused: %s", reply.Error.Message)
	}

	wantResult(t, done, nil)
	if ans.Password != "hunter2" || !ans.Remember {
		t.Fatalf("typed result: got %+v", ans)
	}
	if n := h.broker.Pending(); n != 0 {
		t.Fatalf("%d requests still pending after the result", n)
	}
}

// TestBroker_RequestIsResolvableBeforeTheNotification pins the delivery
// order, deterministically: Request arms the pending request with its
// recipients BEFORE it delivers the notification, so a resolution issued
// from within the delivery call itself — the fastest possible renderer,
// answering inline — is accepted. A broker that armed after delivery would
// refuse it as "Unknown request id" and the request would settle with its
// timeout instead of its result. No goroutines, no scheduling: the deliver
// seam both delivers and answers.
func TestBroker_RequestIsResolvableBeforeTheNotification(t *testing.T) {
	conn := &harnessConn{} // an identity handle; nothing is written to it
	var refusals []string
	var broker *Broker
	broker = NewBroker(
		func() []Conn { return []Conn{conn} },
		func(c Conn, method string, params json.RawMessage) error {
			var p struct {
				RequestID string `json:"requestId"`
			}
			if json.Unmarshal(params, &p) != nil {
				return nil
			}
			// Answer from inside the delivery call: the request must
			// already be resolvable by this connection.
			rpcErr := broker.Resolve("test.passwordResolved", mustMarshal(map[string]any{
				"requestId": p.RequestID,
				"outcome":   "submitted",
				"password":  "inline",
				"remember":  false,
			}), c)
			if rpcErr.Code != 0 {
				refusals = append(refusals, rpcErr.Message)
			}
			return nil
		},
	)

	for range 25 {
		var ans testAnswer
		err := broker.Request(context.Background(), testPasswordKind(5*time.Second),
			map[string]any{"reason": "inline"}, &ans)
		if err != nil {
			t.Fatalf("Request: %v", err)
		}
		if ans.Password != "inline" {
			t.Fatalf("typed result: got %+v", ans)
		}
	}
	if len(refusals) > 0 {
		t.Fatalf("an inline answer was refused: %v", refusals)
	}
	if n := broker.Pending(); n != 0 {
		t.Fatalf("%d requests still pending after the inline exchanges", n)
	}
}

// TestBroker_ExpressesUnlockAsk is criterion 5's vault half: the same
// exchange shape as vault.unlockRequest/unlockResolved — requestId + reason
// down, a closed unsealed/cancelled enum up — driven through the mechanism.
// The unsealed outcome returns the empty result; the cancelled outcome maps
// to the ask's own terminal error.
func TestBroker_ExpressesUnlockAsk(t *testing.T) {
	h := newHarness(t, testUnlockKind(0))
	conn := h.dial(t)
	h.waitForRenderers(t, 1)

	// unsealed
	var res struct{}
	done := make(chan error, 1)
	go func() {
		done <- h.broker.Request(context.Background(), testUnlockKind(0),
			map[string]any{"reason": "history needs the content key"}, &res)
	}()
	raw := readRequestNotification(t, conn, "test.unlockRequest")
	var notif struct {
		RequestID string `json:"requestId"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &notif); err != nil {
		t.Fatalf("decode notification params: %v", err)
	}
	if notif.Reason != "history needs the content key" {
		t.Fatalf("reason lost: %+v", notif)
	}
	reply := sendResolution(t, conn, "test.unlockResolved", json.RawMessage(`1`), map[string]any{
		"requestId": notif.RequestID,
		"outcome":   "unsealed",
	})
	if reply.Error != nil {
		t.Fatalf("unsealed resolution refused: %s", reply.Error.Message)
	}
	wantResult(t, done, nil)

	// cancelled — the RPC is accepted (the outcome is a closed enum member)
	// and the pending request maps it to the ask's terminal error.
	done2 := make(chan error, 1)
	go func() {
		done2 <- h.broker.Request(context.Background(), testUnlockKind(0),
			map[string]any{"reason": "second ask"}, &res)
	}()
	raw2 := readRequestNotification(t, conn, "test.unlockRequest")
	rid2 := notificationRequestID(t, raw2)
	reply2 := sendResolution(t, conn, "test.unlockResolved", json.RawMessage(`2`), map[string]any{
		"requestId": rid2,
		"outcome":   "cancelled",
	})
	if reply2.Error != nil {
		t.Fatalf("cancelled resolution refused: %s", reply2.Error.Message)
	}
	wantResult(t, done2, errTestUnlockCancelled)
}

// TestBroker_UnknownOrStaleRequestID_ResolvesNothing is criterion 2: a
// result carrying an unknown or stale request id is answered honestly and
// never resolves a live request. An id that was never minted is refused; a
// refused attempt leaves the live request untouched (its correct resolution
// still succeeds); an id that already resolved is refused as stale.
func TestBroker_UnknownOrStaleRequestID_ResolvesNothing(t *testing.T) {
	h := newHarness(t, testPasswordKind(0))
	conn := h.dial(t)
	h.waitForRenderers(t, 1)

	// An id that was never minted.
	reply := sendResolution(t, conn, "test.passwordResolved", json.RawMessage(`1`), map[string]any{
		"requestId": "deadbeefdeadbeef",
		"outcome":   "submitted",
		"password":  "x",
		"remember":  false,
	})
	if reply.Error == nil || reply.Error.Message != "Unknown request id" {
		t.Fatalf("never-minted id: got %+v, want refusal with \"Unknown request id\"", reply)
	}

	// A live request, and an attempt to resolve it with the WRONG id.
	var ans testAnswer
	done, rid := h.startRequest(t, conn, testPasswordKind(0), map[string]any{"reason": "live"}, &ans)
	reply = sendResolution(t, conn, "test.passwordResolved", json.RawMessage(`2`), map[string]any{
		"requestId": "deadbeefdeadbeef",
		"outcome":   "submitted",
		"password":  "wrong",
		"remember":  false,
	})
	if reply.Error == nil || reply.Error.Message != "Unknown request id" {
		t.Fatalf("wrong-id attempt: got %+v, want refusal", reply)
	}

	// The correct resolution must still succeed: the refused attempt did not
	// resolve the live request.
	reply = sendResolution(t, conn, "test.passwordResolved", json.RawMessage(`3`), map[string]any{
		"requestId": rid,
		"outcome":   "submitted",
		"password":  "hunter2",
		"remember":  true,
	})
	if reply.Error != nil {
		t.Fatalf("correct resolution refused after a stale attempt: %s", reply.Error.Message)
	}
	wantResult(t, done, nil)
	if ans.Password != "hunter2" {
		t.Fatalf("typed result: got %+v", ans)
	}

	// The same id a second time is stale: the request stopped being
	// resolvable at its result.
	reply = sendResolution(t, conn, "test.passwordResolved", json.RawMessage(`4`), map[string]any{
		"requestId": rid,
		"outcome":   "submitted",
		"password":  "again",
		"remember":  false,
	})
	if reply.Error == nil || reply.Error.Message != "Unknown request id" {
		t.Fatalf("stale id: got %+v, want refusal with \"Unknown request id\"", reply)
	}
}

// TestBroker_ConnectionLossTerminalizesInFlightRequest is criterion 3: a
// renderer that dies without answering terminalizes the in-flight request
// with a terminal reason — the caller is not left hanging on a context that
// never cancels, and no pending request leaks. The renderer's death is real:
// its socket closes, the server's read loop exits, and the lifecycle signal
// fires with the same handle the send seam reported.
func TestBroker_ConnectionLossTerminalizesInFlightRequest(t *testing.T) {
	h := newHarness(t, testPasswordKind(0))
	conn := h.dial(t)
	h.waitForRenderers(t, 1)

	var ans testAnswer
	done := make(chan error, 1)
	go func() {
		done <- h.broker.Request(context.Background(), testPasswordKind(0),
			map[string]any{"reason": "never answered"}, &ans)
	}()
	_ = readRequestNotification(t, conn, "test.passwordRequest")

	// The renderer dies without answering.
	_ = conn.Close()

	wantResult(t, done, ErrRequestDisconnected)
	if n := h.broker.Pending(); n != 0 {
		t.Fatalf("%d pending requests leaked after connection loss", n)
	}
}

// TestBroker_ConnectionLossOfOneRendererLeavesRequestAnswerableByAnother is
// the recipient-scoped half of criterion 3: closing one socket terminalizes
// only the requests that lose every answerer. A request broadcast to two
// renderers survives one of them dying and is resolved by the other.
func TestBroker_ConnectionLossOfOneRendererLeavesRequestAnswerableByAnother(t *testing.T) {
	h := newHarness(t, testPasswordKind(0))
	connA := h.dial(t)
	connB := h.dial(t)
	h.waitForRenderers(t, 2)

	var ans testAnswer
	done := make(chan error, 1)
	go func() {
		done <- h.broker.Request(context.Background(), testPasswordKind(0),
			map[string]any{"reason": "two renderers"}, &ans)
	}()
	raw := readRequestNotification(t, connA, "test.passwordRequest")
	rid := notificationRequestID(t, raw)
	// B received its own copy of the same notification.
	_ = readRequestNotification(t, connB, "test.passwordRequest")

	// A dies without answering. The request must survive: B is still a
	// recipient and can resolve it.
	_ = connA.Close()
	waitFor(t, "A's loss processed", wantWithin, func() bool {
		h.srv.mu.Lock()
		defer h.srv.mu.Unlock()
		return len(h.srv.conns) == 1 && h.broker.Pending() == 1
	})

	reply := sendResolution(t, connB, "test.passwordResolved", json.RawMessage(`1`), map[string]any{
		"requestId": rid,
		"outcome":   "submitted",
		"password":  "hunter2",
		"remember":  false,
	})
	if reply.Error != nil {
		t.Fatalf("resolution by the surviving renderer refused: %s", reply.Error.Message)
	}
	wantResult(t, done, nil)
	if ans.Password != "hunter2" {
		t.Fatalf("typed result: got %+v", ans)
	}
}

// TestBroker_TimeoutTerminalizesPendingRequest is the mechanism's own
// deadline: a request whose renderer never answers settles with
// ErrRequestTimedOut on a context that never cancels, and the dropped id is
// answered honestly if it arrives late.
func TestBroker_TimeoutTerminalizesPendingRequest(t *testing.T) {
	h := newHarness(t, testPasswordKind(200*time.Millisecond))
	conn := h.dial(t)
	h.waitForRenderers(t, 1)

	var ans testAnswer
	done := make(chan error, 1)
	go func() {
		done <- h.broker.Request(context.Background(), testPasswordKind(200*time.Millisecond),
			map[string]any{"reason": "silent renderer"}, &ans)
	}()
	raw := readRequestNotification(t, conn, "test.passwordRequest")
	rid := notificationRequestID(t, raw)

	wantResult(t, done, ErrRequestTimedOut)
	if n := h.broker.Pending(); n != 0 {
		t.Fatalf("%d pending requests leaked after the timeout", n)
	}

	// The late resolution is answered honestly, and resolves nothing.
	reply := sendResolution(t, conn, "test.passwordResolved", json.RawMessage(`1`), map[string]any{
		"requestId": rid,
		"outcome":   "submitted",
		"password":  "late",
		"remember":  false,
	})
	if reply.Error == nil || reply.Error.Message != "Unknown request id" {
		t.Fatalf("late resolution: got %+v, want refusal with \"Unknown request id\"", reply)
	}
}

// TestBroker_ContextCancellationDropsPending is the caller-side half of the
// wait: a cancelled context settles the request with ctx.Err() and drops the
// id, so a late resolution cannot wake a waiter nobody is listening to.
func TestBroker_ContextCancellationDropsPending(t *testing.T) {
	h := newHarness(t, testPasswordKind(0))
	conn := h.dial(t)
	h.waitForRenderers(t, 1)

	ctx, cancel := context.WithCancel(context.Background())
	var ans testAnswer
	done := make(chan error, 1)
	go func() {
		done <- h.broker.Request(ctx, testPasswordKind(0),
			map[string]any{"reason": "cancel me"}, &ans)
	}()
	raw := readRequestNotification(t, conn, "test.passwordRequest")
	rid := notificationRequestID(t, raw)

	cancel()
	wantResult(t, done, context.Canceled)
	if n := h.broker.Pending(); n != 0 {
		t.Fatalf("%d pending requests leaked after cancellation", n)
	}

	reply := sendResolution(t, conn, "test.passwordResolved", json.RawMessage(`1`), map[string]any{
		"requestId": rid,
		"outcome":   "submitted",
		"password":  "too late",
		"remember":  false,
	})
	if reply.Error == nil || reply.Error.Message != "Unknown request id" {
		t.Fatalf("resolution after cancellation: got %+v, want refusal", reply)
	}
}

// TestBroker_NoRendererFailsTheRequest is the send seam's failure path: no
// recipient at all means the request fails with the transport's no-client
// error and nothing stays pending.
func TestBroker_NoRendererFailsTheRequest(t *testing.T) {
	h := newHarness(t, testPasswordKind(0))
	// No renderer ever dials.

	var ans testAnswer
	err := h.broker.Request(context.Background(), testPasswordKind(0),
		map[string]any{"reason": "nobody home"}, &ans)
	if !errors.Is(err, errTestNoClient) {
		t.Fatalf("Request returned %v, want %v", err, errTestNoClient)
	}
	if n := h.broker.Pending(); n != 0 {
		t.Fatalf("%d pending requests leaked after the no-client failure", n)
	}
}

// TestBroker_UnmarshalableParamsFailCleanly is the marshal failure path: a
// params object the broker cannot serialize fails the request before any
// notification is sent, and the minted id is dropped.
func TestBroker_UnmarshalableParamsFailCleanly(t *testing.T) {
	h := newHarness(t, testPasswordKind(0))

	var ans testAnswer
	err := h.broker.Request(context.Background(), testPasswordKind(0),
		map[string]any{"bad": make(chan int)}, &ans)
	if err == nil || !strings.Contains(err.Error(), "marshal request params") {
		t.Fatalf("Request returned %v, want a marshal failure", err)
	}
	if n := h.broker.Pending(); n != 0 {
		t.Fatalf("%d pending requests leaked after the marshal failure", n)
	}
}

// TestBroker_ResolutionEnvelopeIsBounded is criterion 4's ingress floor: the
// size bound and the requestId's presence and bound are enforced before the
// broker looks anything up — the same per-field discipline the two existing
// resolver validators apply, plus the explicit bound.
func TestBroker_ResolutionEnvelopeIsBounded(t *testing.T) {
	h := newHarness(t, testPasswordKind(0))
	conn := h.dial(t)
	h.waitForRenderers(t, 1)

	cases := []struct {
		name   string
		params any
		want   string
	}{
		{"missing requestId", map[string]any{"outcome": "submitted"}, "requestId is required"},
		{"oversized requestId", map[string]any{
			"requestId": strings.Repeat("r", maxIDRunes+1),
			"outcome":   "submitted",
		}, "requestId exceeds the id length bound"},
		{"params not an object", "nope", "params must be a JSON object"},
		{"oversized params", map[string]any{
			"requestId": "deadbeefdeadbeef",
			"pad":       strings.Repeat("x", 2<<10),
		}, "resolution params exceed the size bound"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := sendResolution(t, conn, "test.passwordResolved", json.RawMessage(`1`), tc.params)
			if reply.Error == nil || !strings.Contains(reply.Error.Message, tc.want) {
				t.Fatalf("got %+v, want refusal containing %q", reply, tc.want)
			}
		})
	}
}

// TestBroker_GarbageOutcomeRefused_PendingSurvivesForCorrectedRetry is the
// per-field discipline's point: a resolution carrying a garbage outcome is
// refused on the ingress WITHOUT consuming the pending request, so a broken
// renderer cannot turn a garbage outcome into a silent ask failure — the
// request waits for a corrected retry (or its timeout), and here gets one.
func TestBroker_GarbageOutcomeRefused_PendingSurvivesForCorrectedRetry(t *testing.T) {
	h := newHarness(t, testPasswordKind(0))
	conn := h.dial(t)
	h.waitForRenderers(t, 1)

	var ans testAnswer
	done, rid := h.startRequest(t, conn, testPasswordKind(0), map[string]any{"reason": "retry me"}, &ans)

	reply := sendResolution(t, conn, "test.passwordResolved", json.RawMessage(`1`), map[string]any{
		"requestId": rid,
		"outcome":   "garbage",
	})
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "outcome must be one of submitted, cancelled") {
		t.Fatalf("garbage outcome: got %+v, want the closed-enum refusal", reply)
	}
	if n := h.broker.Pending(); n != 1 {
		t.Fatalf("garbage outcome consumed the pending request: %d pending", n)
	}

	reply = sendResolution(t, conn, "test.passwordResolved", json.RawMessage(`2`), map[string]any{
		"requestId": rid,
		"outcome":   "submitted",
		"password":  "hunter2",
		"remember":  true,
	})
	if reply.Error != nil {
		t.Fatalf("corrected retry refused: %s", reply.Error.Message)
	}
	wantResult(t, done, nil)
	if ans.Password != "hunter2" || !ans.Remember {
		t.Fatalf("typed result: got %+v", ans)
	}
}

// TestBroker_NonRecipientResolutionIsRefused is the recipient rule: a
// connection that never received the notification cannot resolve the
// request — it is answered the same honest way as an unknown id, and the
// request stays resolvable by the connection that did see it.
func TestBroker_NonRecipientResolutionIsRefused(t *testing.T) {
	h := newHarness(t, testPasswordKind(0))
	connA := h.dial(t)
	h.waitForRenderers(t, 1)

	var ans testAnswer
	done, rid := h.startRequest(t, connA, testPasswordKind(0), map[string]any{"reason": "A saw this"}, &ans)

	// A second renderer joins AFTER the request: it never saw the
	// notification, so it cannot answer it.
	connB := h.dial(t)
	reply := sendResolution(t, connB, "test.passwordResolved", json.RawMessage(`1`), map[string]any{
		"requestId": rid,
		"outcome":   "submitted",
		"password":  "from B",
		"remember":  false,
	})
	if reply.Error == nil || reply.Error.Message != "Unknown request id" {
		t.Fatalf("non-recipient resolution: got %+v, want refusal", reply)
	}

	reply = sendResolution(t, connA, "test.passwordResolved", json.RawMessage(`2`), map[string]any{
		"requestId": rid,
		"outcome":   "submitted",
		"password":  "hunter2",
		"remember":  false,
	})
	if reply.Error != nil {
		t.Fatalf("recipient resolution refused: %s", reply.Error.Message)
	}
	wantResult(t, done, nil)
}

// TestBroker_UndecodableResultIsAnError is the result-decode failure path:
// a resolution the ask accepted but whose payload does not fit the caller's
// typed result is an error, not a silent zero value.
func TestBroker_UndecodableResultIsAnError(t *testing.T) {
	kind := testPasswordKind(0)
	kind.Resolve = func(raw json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"not-an-object"`), nil
	}
	h := newHarness(t, kind)
	conn := h.dial(t)
	h.waitForRenderers(t, 1)

	var ans testAnswer
	done := make(chan error, 1)
	go func() {
		done <- h.broker.Request(context.Background(), kind,
			map[string]any{"reason": "undecodable"}, &ans)
	}()
	raw := readRequestNotification(t, conn, "test.passwordRequest")
	rid := notificationRequestID(t, raw)

	reply := sendResolution(t, conn, "test.passwordResolved", json.RawMessage(`1`), map[string]any{
		"requestId": rid,
		"outcome":   "submitted",
	})
	if reply.Error != nil {
		t.Fatalf("resolution refused: %s", reply.Error.Message)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "decode request result") {
			t.Fatalf("Request returned %v, want a decode failure", err)
		}
	case <-time.After(wantWithin):
		t.Fatal("Request did not settle with the decode failure")
	}
}

// ── review findings (nocx-e2j1z wave 2) ──────────────────────────────────

// TestBroker_PreCancelledContextPerformsNoDelivery is finding 1's first end:
// a context cancelled BEFORE the call performs no delivery at all — the
// renderer is never asked to execute an effect for a caller that is already
// terminal. The second end (a context cancelled after delivery still
// terminalizes the pending request) is asserted by
// TestBroker_ContextCancellationDropsPending.
func TestBroker_PreCancelledContextPerformsNoDelivery(t *testing.T) {
	conn := &harnessConn{}
	deliveries := 0
	broker := NewBroker(
		func() []Conn { return []Conn{conn} },
		func(Conn, string, json.RawMessage) error {
			deliveries++
			return nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ans testAnswer
	err := broker.Request(ctx, testPasswordKind(0), map[string]any{"reason": "already gone"}, &ans)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Request returned %v, want context.Canceled", err)
	}
	if deliveries != 0 {
		t.Fatalf("%d deliveries performed for a pre-cancelled caller", deliveries)
	}
	if n := broker.Pending(); n != 0 {
		t.Fatalf("%d pending requests leaked", n)
	}
}

// TestBroker_DeathBetweenSnapshotAndArmTerminalizes is finding 2: a
// connection that dies between the recipient snapshot and the arm must not
// leave the request waiting on a dead connection forever (Timeout: 0,
// context that never cancels). The death is recorded on the un-armed
// pending request and arm skips the dead recipient, terminalizing the
// request. Deterministic: the conns seam reports the death — exactly what
// happens when the transport's teardown fires in that window — before
// returning the snapshot.
func TestBroker_DeathBetweenSnapshotAndArmTerminalizes(t *testing.T) {
	conn := &harnessConn{}
	deliveries := 0
	var broker *Broker
	broker = NewBroker(
		func() []Conn {
			// The teardown lands while the request is registered but not
			// yet armed.
			broker.ConnectionLost(conn)
			return []Conn{conn}
		},
		func(Conn, string, json.RawMessage) error {
			deliveries++
			return nil
		},
	)

	var ans testAnswer
	done := make(chan error, 1)
	go func() {
		done <- broker.Request(context.Background(), testPasswordKind(0),
			map[string]any{"reason": "died in the window"}, &ans)
	}()
	wantResult(t, done, ErrRequestDisconnected)
	if n := broker.Pending(); n != 0 {
		t.Fatalf("%d pending requests leaked after the lost death", n)
	}
}

// TestBroker_AllDeliveriesFailTerminalizes is finding 3's first end: a
// renderer is attached but every delivery fails (a full outbound queue), and
// with Timeout: 0 nothing else could ever terminalize the request — so the
// all-fail state settles it with ErrRequestUndelivered.
func TestBroker_AllDeliveriesFailTerminalizes(t *testing.T) {
	conn := &harnessConn{}
	broker := NewBroker(
		func() []Conn { return []Conn{conn} },
		func(Conn, string, json.RawMessage) error {
			return errors.New("outbound queue full")
		},
	)

	var ans testAnswer
	err := broker.Request(context.Background(), testPasswordKind(0),
		map[string]any{"reason": "undeliverable"}, &ans)
	if !errors.Is(err, ErrRequestUndelivered) {
		t.Fatalf("Request returned %v, want ErrRequestUndelivered", err)
	}
	if n := broker.Pending(); n != 0 {
		t.Fatalf("%d pending requests leaked after the undelivered failure", n)
	}
}

// TestBroker_FailedDeliveryIsNotARecipient is finding 3's second end: a
// delivery failure removes that connection from the recipients — it cannot
// answer what it never received — while a recipient whose delivery landed
// still resolves the request.
func TestBroker_FailedDeliveryIsNotARecipient(t *testing.T) {
	c1, c2 := &harnessConn{}, &harnessConn{}
	broker := NewBroker(
		func() []Conn { return []Conn{c1, c2} },
		func(c Conn, method string, params json.RawMessage) error {
			if c == c1 {
				return errors.New("c1 queue full")
			}
			return nil
		},
	)

	var ans testAnswer
	done := make(chan error, 1)
	go func() {
		done <- broker.Request(context.Background(), testPasswordKind(0),
			map[string]any{"reason": "c1 lost, c2 live"}, &ans)
	}()

	// Wait for the request to be registered (the deliver seams are no-ops,
	// so the id never reaches the test through a socket), then resolve it
	// as c2 — the recipient whose delivery landed.
	waitFor(t, "pending request registered", wantWithin, func() bool {
		return broker.Pending() == 1
	})
	broker.mu.Lock()
	rid := ""
	for k := range broker.pending {
		rid = k
	}
	broker.mu.Unlock()
	if rid == "" {
		t.Fatal("no pending request to resolve")
	}

	rpcErr := broker.Resolve("test.passwordResolved", mustMarshal(map[string]any{
		"requestId": rid,
		"outcome":   "submitted",
		"password":  "hunter2",
		"remember":  false,
	}), c2)
	if rpcErr.Code != 0 {
		t.Fatalf("resolution by the delivered recipient refused: %s", rpcErr.Message)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Request: %v", err)
		}
	case <-time.After(wantWithin):
		t.Fatal("Request did not settle after the surviving recipient resolved")
	}
	if ans.Password != "hunter2" {
		t.Fatalf("typed result: got %+v", ans)
	}
	if n := broker.Pending(); n != 0 {
		t.Fatalf("%d pending requests leaked", n)
	}
}

// TestBroker_WrongResolveMethodIsRefused is finding 4: a resolution is bound
// to the RPC method its kind declared. Taking the request's id and submitting
// it through a different *Resolved method is refused honestly and never
// consumes the request — the correct method still resolves it.
func TestBroker_WrongResolveMethodIsRefused(t *testing.T) {
	conn := &harnessConn{}
	broker := NewBroker(
		func() []Conn { return []Conn{conn} },
		func(Conn, string, json.RawMessage) error { return nil },
	)

	var ans testAnswer
	done := make(chan error, 1)
	go func() {
		done <- broker.Request(context.Background(), testPasswordKind(0),
			map[string]any{"reason": "wrong method"}, &ans)
	}()

	// The deliver seam is a no-op, so read the pending id the way the
	// notification would have carried it — directly from the broker.
	waitFor(t, "pending request registered", wantWithin, func() bool {
		return broker.Pending() == 1
	})
	broker.mu.Lock()
	rid := ""
	for k := range broker.pending {
		rid = k
	}
	broker.mu.Unlock()
	if rid == "" {
		t.Fatal("no pending request to resolve")
	}

	// The LIVE request id submitted through a method no kind declared is
	// refused — the method binding, not a stale id, is what refuses it —
	// and the refusal does not consume the request.
	reply := broker.Resolve("test.passwordResolved.other", mustMarshal(map[string]any{
		"requestId": rid,
		"outcome":   "submitted",
		"password":  "wrong method",
		"remember":  false,
	}), conn)
	if reply.Code == 0 {
		t.Fatal("resolution through an undeclared method was accepted")
	}
	if n := broker.Pending(); n != 1 {
		t.Fatalf("wrong-method refusal consumed the pending request: %d pending", n)
	}

	// The same live id through the kind's own method still resolves it.
	reply = broker.Resolve("test.passwordResolved", mustMarshal(map[string]any{
		"requestId": rid,
		"outcome":   "submitted",
		"password":  "hunter2",
		"remember":  false,
	}), conn)
	if reply.Code != 0 {
		t.Fatalf("resolution through the correct method refused: %s", reply.Message)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Request: %v", err)
		}
	case <-time.After(wantWithin):
		t.Fatal("Request did not settle")
	}
	if ans.Password != "hunter2" {
		t.Fatalf("typed result: got %+v", ans)
	}
}

// TestBroker_NonComparableConnRefused is finding 5: a transport that hands
// the broker a non-comparable connection handle gets a request error at the
// seam — never a panic inside the recipients map — and no delivery happens.
func TestBroker_NonComparableConnRefused(t *testing.T) {
	deliveries := 0
	broker := NewBroker(
		func() []Conn { return []Conn{[]byte("renderer-1")} },
		func(Conn, string, json.RawMessage) error {
			deliveries++
			return nil
		},
	)

	var ans testAnswer
	err := broker.Request(context.Background(), testPasswordKind(0),
		map[string]any{"reason": "unhashable handle"}, &ans)
	if err == nil || !strings.Contains(err.Error(), "not comparable") {
		t.Fatalf("Request returned %v, want the not-comparable refusal", err)
	}
	if deliveries != 0 {
		t.Fatalf("%d deliveries performed with a non-comparable recipient", deliveries)
	}
	if n := broker.Pending(); n != 0 {
		t.Fatalf("%d pending requests leaked", n)
	}
}
