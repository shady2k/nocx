package transport

// Red-to-green transport tests for the tunnel.* RPCs (nocx-8gix). These
// drive the real handlers through the real socket; the behavioral ones run
// the real connector against the in-process SSH server, so "the forward
// works" is bytes pushed through a real direct-tcpip channel.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/tunnel"
	"github.com/shady2k/nocx/internal/waittest"
)

// tunnelRPCResult mirrors the JSON-RPC envelope for tunnel.* calls.
type tunnelRPCResult struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *vaultRPCError  `json:"error,omitempty"`
}

func tunnelCall(t *testing.T, conn *websocket.Conn, method string, params map[string]any, id int) *tunnelRPCResult {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var msg tunnelRPCResult
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.ID == id {
			return &msg
		}
	}
}

// tunnelHarness wires a WSServer with the tunnel connector and a resolver
// pointing at the in-process SSH server. connector nil means the real
// RealClient connector; a non-nil connector substitutes a fake.
type tunnelHarness struct {
	srv  *tunnelTestSSHServer
	ws   *WSServer
	stop func()
}

func newTunnelHarness(t *testing.T, connector tunnel.Connector) *tunnelHarness {
	t.Helper()
	srv := startTunnelTestSSHServer(t)
	conn := connector
	if conn == nil {
		conn = tunnelTestClient(t, srv)
	}
	resolver := &fixedProfileResolver{host: srv.addr, cfg: tunnelResolveConfig(srv)}
	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithTunnelConnector(conn),
		WithProfileResolver(resolver),
	)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return &tunnelHarness{srv: srv, ws: ws, stop: func() { _ = ws.Stop(ctx) }}
}

// openTunnel issues tunnel.open and decodes the result record.
func openTunnel(t *testing.T, conn *websocket.Conn, params map[string]any) (tunnelRecord, error) {
	t.Helper()
	resp := tunnelCall(t, conn, "tunnel.open", params, 1)
	if resp.Error != nil {
		return tunnelRecord{}, errors.New(resp.Error.Message)
	}
	var rec tunnelRecord
	if err := json.Unmarshal(resp.Result, &rec); err != nil {
		t.Fatalf("decode tunnel.open result: %v", err)
	}
	return rec, nil
}

// roundTrip pushes "ping" through a local address and expects the echo
// target's reply back — the observable proof a forward forwards.
func roundTrip(t *testing.T, addr string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, wantWithin)
	if err != nil {
		t.Fatalf("dial forwarded address %s: %v", addr, err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(wantWithin))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatalf("write through forward: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read through forward: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("round trip = %q, want %q", buf, "ping")
	}
}

// expectClosed asserts nothing accepts connections on addr anymore.
func expectClosed(t *testing.T, addr string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err == nil {
		_ = c.Close()
		t.Fatalf("address %s still accepts connections, want refused", addr)
	}
}

// TestTunnelOpen_ReportsActualPortAndRoundTripsBytes is the behavioural
// proof (brief §Prove it): open a forward through the REAL transport against
// the in-process SSH server, push bytes through it, and stop it. Port 0 must
// come back as the OS-allocated port, never 0.
func TestTunnelOpen_ReportsActualPortAndRoundTripsBytes(t *testing.T) {
	h := newTunnelHarness(t, nil)
	defer h.stop()
	target := startEchoTarget(t)

	conn := connectWS(t, h.ws)
	// The connection is the tab: the forward's lifetime is tied to it, so
	// it must outlive the round trip below. Keeping it referenced here also
	// stops the GC finalizing it mid-test and closing the socket under the
	// forward (tab-scoped teardown under concurrent load).
	defer func() { _ = conn.Close() }()
	rec, err := openTunnel(t, conn, map[string]any{
		"profileId":   "ssh:p1:1",
		"port":        0, // allocate
		"destination": target,
	})
	if err != nil {
		t.Fatalf("tunnel.open: %v", err)
	}
	if rec.State != string(tunnel.StateRunning) {
		t.Fatalf("state = %q, want %q", rec.State, tunnel.StateRunning)
	}
	if rec.RequestedBind.Port != 0 {
		t.Fatalf("requested port = %d, want 0 (the caller asked to allocate)", rec.RequestedBind.Port)
	}
	if rec.ActualBind.Port == 0 {
		t.Fatal("actual port = 0, want the OS-allocated port (reporting the request would be a lie)")
	}
	if rec.ActualBind.Host == "" {
		t.Fatal("actual host is empty")
	}
	if rec.StopReason != nil || rec.Error != nil {
		t.Fatalf("running tunnel carries stopReason=%v error=%v, want null/null", rec.StopReason, rec.Error)
	}

	// Bytes through the forward: local listener → SSH direct-tcpip → echo.
	roundTrip(t, net.JoinHostPort(rec.ActualBind.Host, strconv.Itoa(rec.ActualBind.Port)))

	// Stop it; the record comes back stopped and the port no longer accepts.
	stopResp := tunnelCall(t, connectWS(t, h.ws), "tunnel.stop", map[string]any{"id": rec.ID}, 2)
	if stopResp.Error != nil {
		t.Fatalf("tunnel.stop: %+v", stopResp.Error)
	}
	var stopped tunnelRecord
	if err := json.Unmarshal(stopResp.Result, &stopped); err != nil {
		t.Fatalf("decode tunnel.stop result: %v", err)
	}
	if stopped.State != string(tunnel.StateStopped) {
		t.Fatalf("state after stop = %q, want %q", stopped.State, tunnel.StateStopped)
	}
	if stopped.StopReason == nil || *stopped.StopReason != string(tunnel.StopReasonUser) {
		t.Fatalf("stopReason after user stop = %v, want %q", stopped.StopReason, tunnel.StopReasonUser)
	}
	expectClosed(t, net.JoinHostPort(stopped.ActualBind.Host, strconv.Itoa(stopped.ActualBind.Port)))
}

// TestTunnelOpen_BusyLocalPortFailsSynchronously pins the synchronous
// bind-error contract (spec §7.1): EADDRINUSE surfaces as the open's error,
// and the session and every other forward are unaffected.
func TestTunnelOpen_BusyLocalPortFailsSynchronously(t *testing.T) {
	h := newTunnelHarness(t, nil)
	defer h.stop()
	target := startEchoTarget(t)
	busy := busyPort(t)

	conn := connectWS(t, h.ws)
	// The connection is the tab; keep it referenced (and closed at the end)
	// so the second forward below survives its round trip (see the teardown
	// test for the GC-finalizer mechanism this guards against).
	defer func() { _ = conn.Close() }()

	resp := tunnelCall(t, conn, "tunnel.open", map[string]any{
		"profileId":   "ssh:p1:1",
		"port":        busy,
		"destination": target,
	}, 1)
	if resp.Error == nil {
		t.Fatal("tunnel.open on a busy port: expected an error, got a result")
	}
	if !strings.Contains(resp.Error.Message, "address already in use") &&
		!strings.Contains(resp.Error.Message, "in use") {
		t.Fatalf("error %q does not name the busy port", resp.Error.Message)
	}

	// The same profile can still open a forward on an allocated port.
	rec, err := openTunnel(t, conn, map[string]any{
		"profileId":   "ssh:p1:1",
		"port":        0,
		"destination": target,
	})
	if err != nil {
		t.Fatalf("second tunnel.open after busy-port failure: %v", err)
	}
	roundTrip(t, net.JoinHostPort(rec.ActualBind.Host, strconv.Itoa(rec.ActualBind.Port)))
}

// TestTunnelOpen_ConnectorRefuses proves the acquire-failure path: a
// connector that refuses must surface as a clean RPC error, never a half
// forward.
func TestTunnelOpen_ConnectorRefuses(t *testing.T) {
	h := newTunnelHarness(t, refusingTunnelConnector{})
	defer h.stop()
	target := startEchoTarget(t)

	resp := tunnelCall(t, connectWS(t, h.ws), "tunnel.open", map[string]any{
		"profileId":   "ssh:p1:1",
		"destination": target,
	}, 1)
	if resp.Error == nil {
		t.Fatal("tunnel.open with a refusing connector: expected an error, got a result")
	}
	if !strings.Contains(resp.Error.Message, "refused") {
		t.Fatalf("error %q does not name the connector failure", resp.Error.Message)
	}
}

// refusingTunnelConnector is the rule-3 failure stand-in: the SSH connection
// cannot be acquired for a forward.
type refusingTunnelConnector struct{}

func (refusingTunnelConnector) TunnelConn(_ context.Context, _ string, _ ...ssh.ConnectOption) (ssh.TunnelConn, error) {
	return nil, errors.New("connection refused by connector")
}

// TestTunnelStop_UnknownID proves a stop for an id that does not exist is an
// error, not a silent success.
func TestTunnelStop_UnknownID(t *testing.T) {
	h := newTunnelHarness(t, nil)
	defer h.stop()

	resp := tunnelCall(t, connectWS(t, h.ws), "tunnel.stop", map[string]any{"id": "no-such-tunnel"}, 1)
	if resp.Error == nil {
		t.Fatal("tunnel.stop with an unknown id: expected an error, got a result")
	}
}

// TestTunnelPaneTeardown_DoesNotStopOtherPanesForward is the lifetime
// invariant of spec §7.3 at the transport seam: two tabs open forwards on
// the SAME shared connection; one tab disconnects; its forward stops, the
// other tab's forward keeps forwarding. Technically the tunnels each hold
// their own pooled reference; this test proves the RPC layer never couples
// them.
func TestTunnelPaneTeardown_DoesNotStopOtherPanesForward(t *testing.T) {
	h := newTunnelHarness(t, nil)
	defer h.stop()
	target := startEchoTarget(t)

	connA := connectWS(t, h.ws)
	connB := connectWS(t, h.ws)
	// connB must outlive this test. It is not referenced again after the
	// opens below, so without an explicit owner the GC can finalize it
	// mid-test and close its socket (Go's netFD finalizer); the server
	// then (correctly) tears down B's forward as a disconnected tab and
	// the final roundTrip fails. The defer keeps the connection alive
	// until the test returns (tab-scoped teardown under concurrent load).
	defer func() { _ = connB.Close() }()

	recA, err := openTunnel(t, connA, map[string]any{
		"profileId":   "ssh:p1:1",
		"port":        0,
		"destination": target,
	})
	if err != nil {
		t.Fatalf("tab A tunnel.open: %v", err)
	}
	recB, err := openTunnel(t, connB, map[string]any{
		"profileId":   "ssh:p1:1",
		"port":        0,
		"destination": target,
	})
	if err != nil {
		t.Fatalf("tab B tunnel.open: %v", err)
	}
	addrA := net.JoinHostPort(recA.ActualBind.Host, strconv.Itoa(recA.ActualBind.Port))
	addrB := net.JoinHostPort(recB.ActualBind.Host, strconv.Itoa(recB.ActualBind.Port))
	roundTrip(t, addrA)
	roundTrip(t, addrB)

	// Tab A disconnects: its forward must stop, B's must survive.
	_ = connA.Close()

	// Teardown is asynchronous (the read loop notices the close); wait until
	// A's port refuses connections, the observable of the forward stopping.
	waittest.WaitForTimeout(t, "tab A's forward to stop accepting connections", wantWithin, func() bool {
		c, dErr := net.DialTimeout("tcp", addrA, 200*time.Millisecond)
		if dErr == nil {
			_ = c.Close()
			return false
		}
		return true
	})

	// B's forward is untouched: bytes still round-trip.
	roundTrip(t, addrB)
}

// TestTunnelOpen_NotWired proves the method is a clean -32601-style error
// when no connector is wired — the transport does not construct clients
// itself.
func TestTunnelOpen_NotWired(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	resp := tunnelCall(t, connectWS(t, ws), "tunnel.open", map[string]any{
		"profileId":   "ssh:p1:1",
		"destination": "example.com:80",
	}, 1)
	if resp.Error == nil {
		t.Fatal("tunnel.open with no connector wired: expected an error, got a result")
	}
}
