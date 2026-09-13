package tunnel_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/tunnel"
)

// ---------------------------------------------------------------------------
// Fake connector: models the pooled-connection semantics the strategies
// depend on — one shared connection per host, a lease per forward, refcounted
// lifetime, and a transport-loss path that closes every lease's Done and
// releases its reference.
// ---------------------------------------------------------------------------

type fakeConn struct {
	mu       sync.Mutex
	refs     int
	leases   []*fakeLease
	dialFn   func(addr string) (net.Conn, error)
	listenFn func(addr string) (net.Listener, error)
}

func (fc *fakeConn) acquire() *fakeLease {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fl := &fakeLease{conn: fc, done: make(chan struct{}), closed: make(chan struct{})}
	fc.leases = append(fc.leases, fl)
	fc.refs++
	return fl
}

func (fc *fakeConn) refCount() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.refs
}

func (fc *fakeConn) release() {
	fc.mu.Lock()
	fc.refs--
	fc.mu.Unlock()
}

// lose simulates transport death: every lease's Done closes and its
// reference drops, mirroring the real per-lease loss watchers.
func (fc *fakeConn) lose() {
	fc.mu.Lock()
	leases := fc.leases
	fc.leases = nil
	fc.mu.Unlock()
	for _, fl := range leases {
		fl.lost()
	}
}

type fakeLease struct {
	conn *fakeConn

	done       chan struct{} // closed on transport loss
	closed     chan struct{} // closed on Close
	closeOnce  sync.Once
	lostOnce   sync.Once
	releaseRef sync.Once
	lostErr    error
}

var _ ssh.TunnelConn = (*fakeLease)(nil)

func (fl *fakeLease) Dial(addr string) (net.Conn, error) {
	select {
	case <-fl.done:
		return nil, errors.New("ssh: tunnel connection lost")
	case <-fl.closed:
		return nil, errors.New("ssh: tunnel connection closed")
	default:
	}
	return fl.conn.dialFn(addr)
}

// Listen models the remote side accepting tcpip-forward: a real loopback
// listener stands in for the server's bind, and a test scripts the server's
// policy (AllowTcpForwarding / PermitListen) via setListen. Same lost/closed
// guards as the real lease: a spent lease never asks for a new listener.
func (fl *fakeLease) Listen(addr string) (net.Listener, error) {
	select {
	case <-fl.done:
		return nil, errors.New("ssh: tunnel connection lost")
	case <-fl.closed:
		return nil, errors.New("ssh: tunnel connection closed")
	default:
	}
	return fl.conn.listenFn(addr)
}

func (fl *fakeLease) Done() <-chan struct{} { return fl.done }

func (fl *fakeLease) LostErr() error {
	select {
	case <-fl.done:
		return fl.lostErr
	default:
		return nil
	}
}

func (fl *fakeLease) Close() error {
	fl.closeOnce.Do(func() {
		close(fl.closed)
		fl.releaseRef.Do(func() { fl.conn.release() })
	})
	return nil
}

func (fl *fakeLease) lost() {
	fl.lostOnce.Do(func() {
		fl.lostErr = errors.New("simulated connection reset by peer")
		close(fl.done)
		fl.releaseRef.Do(func() { fl.conn.release() })
	})
}

type fakeConnector struct {
	mu       sync.Mutex
	conns    map[string]*fakeConn
	calls    int
	dialFn   func(addr string) (net.Conn, error)
	listenFn func(addr string) (net.Listener, error)
}

var _ tunnel.Connector = (*fakeConnector)(nil)

func newFakeConnector() *fakeConnector {
	return &fakeConnector{
		conns:  make(map[string]*fakeConn),
		dialFn: func(addr string) (net.Conn, error) { return net.Dial("tcp", addr) },
		// The fake's Listen models the remote side accepting tcpip-forward:
		// a real loopback listener stands in for the server's bind. Tests
		// script refusals via setListen.
		listenFn: func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) },
	}
}

func (fc *fakeConnector) TunnelConn(_ context.Context, host string, _ ...ssh.ConnectOption) (ssh.TunnelConn, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.calls++
	c := fc.conns[host]
	if c == nil {
		c = &fakeConn{dialFn: fc.dialFn, listenFn: fc.listenFn}
		fc.conns[host] = c
	}
	return c.acquire(), nil
}

func (fc *fakeConnector) conn(host string) *fakeConn {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.conns[host]
}

// setDial scripts the "remote target" behaviour for every connection.
func (fc *fakeConnector) setDial(fn func(addr string) (net.Conn, error)) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.dialFn = fn
	for _, c := range fc.conns {
		c.dialFn = fn
	}
}

// setListen scripts the "remote listener" behaviour for every connection —
// the fake stand-in for the server's AllowTcpForwarding / PermitListen
// policy. The default binds a real loopback listener; a test can script a
// refusal to model the server refusing tcpip-forward.
func (fc *fakeConnector) setListen(fn func(addr string) (net.Listener, error)) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.listenFn = fn
	for _, c := range fc.conns {
		c.listenFn = fn
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// echoTarget listens on a loopback port and echoes every accepted connection
// back, standing in for the remote destination of a forward.
func echoTarget(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo target listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String()
}

func startTunnel(t *testing.T, spec tunnel.Spec, fc *fakeConnector) *tunnel.Tunnel {
	t.Helper()
	tun, err := tunnel.New(spec, fc)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tun
}

// roundTrip writes payload through the tunnel's local listener and reads the
// echo back, proving accept → direct-tcpip → copy end to end.
func roundTrip(t *testing.T, tun *tunnel.Tunnel, payload string) {
	t.Helper()
	addr := net.JoinHostPort(tun.Actual().Host, itoa(tun.Actual().Port))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial local listener: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write through tunnel: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read through tunnel: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("round trip = %q, want %q", buf, payload)
	}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func waitDone(t *testing.T, tun *tunnel.Tunnel) {
	t.Helper()
	select {
	case <-tun.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("tunnel Done did not close within 5s")
	}
}

// ---------------------------------------------------------------------------
// model validation
// ---------------------------------------------------------------------------

func TestNew_Validation(t *testing.T) {
	fc := newFakeConnector()

	tests := []struct {
		name string
		spec tunnel.Spec
		want string
	}{
		{"empty direction", tunnel.Spec{}, "direction is required"},
		{"unknown direction", tunnel.Spec{Direction: tunnel.Direction("sideways")}, "unknown direction"},
		{"local without destination", tunnel.Spec{Direction: tunnel.DirectionLocal}, "destination is required"},
		{"local with bare destination", tunnel.Spec{Direction: tunnel.DirectionLocal, Destination: "localhost"}, "invalid local destination"},
		{"remote without destination", tunnel.Spec{Direction: tunnel.DirectionRemote}, "destination is required"},
		{"dynamic with destination", tunnel.Spec{Direction: tunnel.DirectionDynamic, Destination: "127.0.0.1:80"}, "dynamic destination must be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tunnel.New(tt.spec, fc)
			if err == nil {
				t.Fatal("New: expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New error = %q, want substring %q", err, tt.want)
			}
		})
	}

	if _, err := tunnel.New(tunnel.Spec{Direction: tunnel.DirectionLocal, Destination: "127.0.0.1:1"}, nil); err == nil {
		t.Fatal("New with nil connector: expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// traps 1-3: bind before reporting, no pre-check, port 0 reports the actual
// ---------------------------------------------------------------------------

// TestLocal_EADDRINUSE_Synchronous proves trap 1: a busy local port fails
// synchronously from Start with the OS error — never a later goroutine
// discovery — and the acquired connection lease is released (no leaked pooled
// reference).
func TestLocal_EADDRINUSE_Synchronous(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("busy listen: %v", err)
	}
	defer func() { _ = busy.Close() }()
	tcpAddr, ok := busy.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("busy listener address is not TCP: %T", busy.Addr())
	}
	busyPort := tcpAddr.Port

	fc := newFakeConnector()
	tun := startTunnel(t, tunnel.Spec{
		Direction:   tunnel.DirectionLocal,
		Destination: echoTarget(t),
		Bind:        tunnel.Bind{Port: busyPort},
	}, fc)

	err = tun.Start(context.Background(), "example.com")
	if err == nil {
		t.Fatal("Start on busy port: expected error, got nil")
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("Start error = %v, want EADDRINUSE", err)
	}
	if got := tun.State(); got != tunnel.StateStopped {
		t.Fatalf("state = %q, want stopped", got)
	}
	if got := tun.StopReason(); got != tunnel.StopReasonError {
		t.Fatalf("stop reason = %q, want error", got)
	}
	waitDone(t, tun)

	// The lease was acquired once and released on the bind failure — the
	// pool reference must not leak.
	if got := fc.calls; got != 1 {
		t.Fatalf("connector calls = %d, want 1", got)
	}
	if got := fc.conn("example.com").refCount(); got != 0 {
		t.Fatalf("refcount after failed start = %d, want 0 (lease released)", got)
	}

	// A stopped tunnel cannot restart (spec §7.3: no silent rebind).
	if err := tun.Start(context.Background(), "example.com"); err == nil {
		t.Fatal("Start after stop: expected error, got nil")
	}
}

// TestLocal_PortZeroReportsActualPort proves traps 2 and 3: no pre-checking
// (the listen is the check), and port 0 binds an ephemeral port that is
// reported as the ACTUAL port — never left as 0. Default bind is loopback.
func TestLocal_PortZeroReportsActualPort(t *testing.T) {
	fc := newFakeConnector()
	tun := startTunnel(t, tunnel.Spec{
		Direction:   tunnel.DirectionLocal,
		Destination: echoTarget(t),
		Bind:        tunnel.Bind{Port: 0},
	}, fc)

	if err := tun.Start(context.Background(), "example.com"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := tun.State(); got != tunnel.StateRunning {
		t.Fatalf("state = %q, want running", got)
	}
	actual := tun.Actual()
	if actual.Port == 0 {
		t.Fatal("actual port = 0, want an OS-assigned port")
	}
	if actual.Host != tunnel.DefaultLocalHost {
		t.Fatalf("actual host = %q, want default %q", actual.Host, tunnel.DefaultLocalHost)
	}

	roundTrip(t, tun, "hello through the tunnel")

	tun.Stop()
	if got := tun.State(); got != tunnel.StateStopped {
		t.Fatalf("state after stop = %q, want stopped", got)
	}
	if got := tun.StopReason(); got != tunnel.StopReasonUser {
		t.Fatalf("stop reason = %q, want user", got)
	}
	waitDone(t, tun)

	// The listener is really closed after a user stop.
	if _, err := net.DialTimeout("tcp", net.JoinHostPort(actual.Host, itoa(actual.Port)), 500*time.Millisecond); err == nil {
		t.Fatal("listener still accepting after stop")
	}
}

// ---------------------------------------------------------------------------
// trap 4: one stream failing must not kill the listener
// ---------------------------------------------------------------------------

// TestLocal_RefusedRemoteLeavesListenerAlive proves the remote target
// refusing a connection affects that stream only: the listener survives and
// the next connection forwards normally.
func TestLocal_RefusedRemoteLeavesListenerAlive(t *testing.T) {
	fc := newFakeConnector()
	refusals := 0
	fc.setDial(func(addr string) (net.Conn, error) {
		if refusals < 1 {
			refusals++
			return nil, errors.New("connection refused")
		}
		return net.Dial("tcp", addr)
	})

	tun := startTunnel(t, tunnel.Spec{
		Direction:   tunnel.DirectionLocal,
		Destination: echoTarget(t),
		Bind:        tunnel.Bind{Port: 0},
	}, fc)
	if err := tun.Start(context.Background(), "example.com"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// First connection: the remote target refuses; the stream dies, but the
	// listener must keep serving.
	addr := net.JoinHostPort(tun.Actual().Host, itoa(tun.Actual().Port))
	first, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial local listener: %v", err)
	}
	_, _ = first.Write([]byte("doomed"))
	firstBuf := make([]byte, 1)
	if _, rerr := first.Read(firstBuf); rerr == nil {
		t.Fatal("refused stream: expected the stream to fail, got data")
	}
	_ = first.Close()

	// The listener survived: the next connection round-trips.
	second, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial local listener after refused stream: %v", err)
	}
	defer func() { _ = second.Close() }()
	if _, err := second.Write([]byte("ping")); err != nil {
		t.Fatalf("write after refused stream: %v", err)
	}
	secondBuf := make([]byte, 4)
	if _, err := io.ReadFull(second, secondBuf); err != nil {
		t.Fatalf("read after refused stream: %v", err)
	}
	if string(secondBuf) != "ping" {
		t.Fatalf("second round trip = %q, want %q", secondBuf, "ping")
	}

	if got := tun.State(); got != tunnel.StateRunning {
		t.Fatalf("state = %q, want running (refused stream must not kill the forward)", got)
	}
	tun.Stop()
}

// ---------------------------------------------------------------------------
// trap 5 + §7.3: own pooled handle, tab teardown isolates
// ---------------------------------------------------------------------------

// TestLocal_TwoTunnelsShareConnection_OneStopDoesNotKillTheOther is the
// ownership invariant at the model level: two forwards on one shared
// connection each hold their OWN lease. Stopping one releases only its lease;
// the connection stays up for the other, which keeps forwarding.
func TestLocal_TwoTunnelsShareConnection_OneStopDoesNotKillTheOther(t *testing.T) {
	fc := newFakeConnector()
	target := echoTarget(t)

	spec := tunnel.Spec{
		Direction:   tunnel.DirectionLocal,
		Destination: target,
		Bind:        tunnel.Bind{Port: 0},
	}
	tunA := startTunnel(t, spec, fc)
	tunB := startTunnel(t, spec, fc)

	if err := tunA.Start(context.Background(), "example.com"); err != nil {
		t.Fatalf("Start A: %v", err)
	}
	if err := tunB.Start(context.Background(), "example.com"); err != nil {
		t.Fatalf("Start B: %v", err)
	}
	if got := fc.conn("example.com").refCount(); got != 2 {
		t.Fatalf("refcount with two tunnels = %d, want 2", got)
	}

	// Tab A closes (simulated by stopping tunnel A): only A's lease is
	// released. The shared connection stays up and B keeps forwarding.
	tunA.Stop()
	if got := fc.conn("example.com").refCount(); got != 1 {
		t.Fatalf("refcount after A stop = %d, want 1 (B still holds the connection)", got)
	}
	roundTrip(t, tunB, "B still forwards after A stopped")

	// B's stop releases the last reference.
	tunB.Stop()
	if got := fc.conn("example.com").refCount(); got != 0 {
		t.Fatalf("refcount after B stop = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// §7.3: connection loss
// ---------------------------------------------------------------------------

// TestLocal_ConnectionLoss_StoppedConnectionLost proves transport death moves
// the tunnel to stopped: connection lost — it never silently rebinds and
// never claims to be running. The listener is really closed, and a restart
// attempt is refused.
func TestLocal_ConnectionLoss_StoppedConnectionLost(t *testing.T) {
	fc := newFakeConnector()
	tun := startTunnel(t, tunnel.Spec{
		Direction:   tunnel.DirectionLocal,
		Destination: echoTarget(t),
		Bind:        tunnel.Bind{Port: 0},
	}, fc)
	if err := tun.Start(context.Background(), "example.com"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	actual := tun.Actual()

	fc.conn("example.com").lose()

	waitDone(t, tun)
	if got := tun.State(); got != tunnel.StateStopped {
		t.Fatalf("state = %q, want stopped", got)
	}
	if got := tun.StopReason(); got != tunnel.StopReasonConnectionLost {
		t.Fatalf("stop reason = %q, want %q", got, tunnel.StopReasonConnectionLost)
	}
	if tun.Err() == nil {
		t.Fatal("Err = nil after connection loss, want the transport error")
	}

	// The listener is closed — the tunnel is not silently rebinding.
	if _, err := net.DialTimeout("tcp", net.JoinHostPort(actual.Host, itoa(actual.Port)), 500*time.Millisecond); err == nil {
		t.Fatal("listener still accepting after connection loss")
	}

	// No restart (restoration belongs to nocx-9le.7).
	if err := tun.Start(context.Background(), "example.com"); err == nil {
		t.Fatal("Start after connection loss: expected error, got nil")
	}

	// The lease released its reference on loss.
	if got := fc.conn("example.com").refCount(); got != 0 {
		t.Fatalf("refcount after loss = %d, want 0", got)
	}
}

// TestLocal_ConnectionLoss_OtherTunnelSurvives? — a connection loss is
// transport-wide: every forward on the dead connection stops. That is
// covered at the ssh layer (killConns); here the fake models it by closing
// every lease. Assert both tunnels stop when the shared connection dies.
func TestLocal_ConnectionLoss_StopsEveryTunnelOnTheConnection(t *testing.T) {
	fc := newFakeConnector()
	spec := tunnel.Spec{
		Direction:   tunnel.DirectionLocal,
		Destination: echoTarget(t),
		Bind:        tunnel.Bind{Port: 0},
	}
	tunA := startTunnel(t, spec, fc)
	tunB := startTunnel(t, spec, fc)
	if err := tunA.Start(context.Background(), "example.com"); err != nil {
		t.Fatalf("Start A: %v", err)
	}
	if err := tunB.Start(context.Background(), "example.com"); err != nil {
		t.Fatalf("Start B: %v", err)
	}

	fc.conn("example.com").lose()

	waitDone(t, tunA)
	waitDone(t, tunB)
	if got := tunA.StopReason(); got != tunnel.StopReasonConnectionLost {
		t.Fatalf("A stop reason = %q, want connection lost", got)
	}
	if got := tunB.StopReason(); got != tunnel.StopReasonConnectionLost {
		t.Fatalf("B stop reason = %q, want connection lost", got)
	}
}

// TestStopBeforeStartIsSafe: Stop on a never-started tunnel must not panic
// and must leave the record stopped.
func TestStopBeforeStartIsSafe(t *testing.T) {
	fc := newFakeConnector()
	tun := startTunnel(t, tunnel.Spec{
		Direction:   tunnel.DirectionLocal,
		Destination: echoTarget(t),
	}, fc)
	tun.Stop()
	if got := tun.State(); got != tunnel.StateStopped {
		t.Fatalf("state = %q, want stopped", got)
	}
	waitDone(t, tun)
}

// ---------------------------------------------------------------------------
// remote (-R): the listener lives on the server, the destination dials
// locally
// ---------------------------------------------------------------------------

// TestRemote_BytesCross_RemoteSideDial proves the -R happy path through the
// strategy: a connection arriving at the remote listener — here a real
// loopback listener the fake lease holds, dialed from the test — is proxied
// to the destination and bytes echo back.
func TestRemote_BytesCross_RemoteSideDial(t *testing.T) {
	fc := newFakeConnector()
	tun := startTunnel(t, tunnel.Spec{
		Direction:   tunnel.DirectionRemote,
		Destination: echoTarget(t),
		Bind:        tunnel.Bind{Port: 0},
	}, fc)
	if err := tun.Start(context.Background(), "example.com"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := tun.State(); got != tunnel.StateRunning {
		t.Fatalf("state = %q, want running", got)
	}
	roundTrip(t, tun, "hello through the remote forward")
	tun.Stop()
	waitDone(t, tun)
}

// TestRemote_DestinationDialedLocally proves -R resolves the destination on
// the CLIENT's network (OpenSSH -R semantics): the lease's dial path — which
// -L uses, and which here is scripted to fail — is never consulted. The
// forward works anyway, over a plain local dial.
func TestRemote_DestinationDialedLocally(t *testing.T) {
	fc := newFakeConnector()
	fc.setDial(func(addr string) (net.Conn, error) {
		return nil, errors.New("lease dial must not be used by -R")
	})
	tun := startTunnel(t, tunnel.Spec{
		Direction:   tunnel.DirectionRemote,
		Destination: echoTarget(t),
		Bind:        tunnel.Bind{Port: 0},
	}, fc)
	if err := tun.Start(context.Background(), "example.com"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	roundTrip(t, tun, "dialed locally, not through the lease")
	tun.Stop()
	waitDone(t, tun)
}

// TestRemote_RefusedListenSurfacesPolicyReason proves a server-side refusal
// — AllowTcpForwarding off, or the bind outside PermitListen — surfaces as a
// policy reason the user can act on, not a bare failure. The two refusals
// are indistinguishable on the wire, so the reason names both. The acquired
// lease is released on the failed start.
func TestRemote_RefusedListenSurfacesPolicyReason(t *testing.T) {
	fc := newFakeConnector()
	fc.setListen(func(addr string) (net.Listener, error) {
		return nil, errors.New("ssh: tcpip-forward request denied by peer")
	})
	tun := startTunnel(t, tunnel.Spec{
		Direction:   tunnel.DirectionRemote,
		Destination: echoTarget(t),
		Bind:        tunnel.Bind{Port: 0},
	}, fc)
	err := tun.Start(context.Background(), "example.com")
	if err == nil {
		t.Fatal("Start: expected the server's refusal, got nil")
	}
	for _, want := range []string{"AllowTcpForwarding", "PermitListen"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Start error = %q, want policy context naming %q", err, want)
		}
	}
	if got := tun.State(); got != tunnel.StateStopped {
		t.Fatalf("state = %q, want stopped", got)
	}
	if got := tun.StopReason(); got != tunnel.StopReasonError {
		t.Fatalf("stop reason = %q, want error", got)
	}
	waitDone(t, tun)
	if got := fc.conn("example.com").refCount(); got != 0 {
		t.Fatalf("refcount after refused listen = %d, want 0 (lease released)", got)
	}
}

// TestRemote_PortZeroReportsAllocatedPort proves a requested port 0 is
// resolved by the far end and reported as the ACTUAL port — never left as 0.
func TestRemote_PortZeroReportsAllocatedPort(t *testing.T) {
	fc := newFakeConnector()
	tun := startTunnel(t, tunnel.Spec{
		Direction:   tunnel.DirectionRemote,
		Destination: echoTarget(t),
		Bind:        tunnel.Bind{Port: 0},
	}, fc)
	if err := tun.Start(context.Background(), "example.com"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	actual := tun.Actual()
	if actual.Port == 0 {
		t.Fatal("actual port = 0, want the server-allocated port")
	}
	if actual.Host == "" {
		t.Fatal("actual host is empty")
	}
	tun.Stop()
	waitDone(t, tun)
}

// TestRemote_ConnectionLoss_StoppedConnectionLost proves transport death
// moves a remote forward to stopped: connection lost — exactly like -L — and
// never silently rebinds.
func TestRemote_ConnectionLoss_StoppedConnectionLost(t *testing.T) {
	fc := newFakeConnector()
	tun := startTunnel(t, tunnel.Spec{
		Direction:   tunnel.DirectionRemote,
		Destination: echoTarget(t),
		Bind:        tunnel.Bind{Port: 0},
	}, fc)
	if err := tun.Start(context.Background(), "example.com"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	actual := tun.Actual()

	fc.conn("example.com").lose()

	waitDone(t, tun)
	if got := tun.State(); got != tunnel.StateStopped {
		t.Fatalf("state = %q, want stopped", got)
	}
	if got := tun.StopReason(); got != tunnel.StopReasonConnectionLost {
		t.Fatalf("stop reason = %q, want %q", got, tunnel.StopReasonConnectionLost)
	}
	if tun.Err() == nil {
		t.Fatal("Err = nil after connection loss, want the transport error")
	}
	// The remote listener is really closed — the forward is not running.
	if _, err := net.DialTimeout("tcp", net.JoinHostPort(actual.Host, itoa(actual.Port)), 500*time.Millisecond); err == nil {
		t.Fatal("listener still accepting after connection loss")
	}
	if got := fc.conn("example.com").refCount(); got != 0 {
		t.Fatalf("refcount after loss = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// dynamic (-D): a local SOCKS5 server
// ---------------------------------------------------------------------------

// socksGreet performs the SOCKS5 greeting with the given methods and returns
// the server's two-byte method-selection reply.
func socksGreet(t *testing.T, c net.Conn, methods ...byte) []byte {
	t.Helper()
	msg := append([]byte{0x05, byte(len(methods))}, methods...)
	if _, err := c.Write(msg); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	rep := make([]byte, 2)
	if _, err := io.ReadFull(c, rep); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	return rep
}

// socksRequest builds a SOCKS5 request: CMD, ATYP chosen from the host (IPv4,
// IPv6 or domain), DST.ADDR and DST.PORT.
func socksRequest(cmd byte, host string, port int) []byte {
	req := []byte{0x05, cmd, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req = append(req, 0x01)
			req = append(req, ip4...)
		} else {
			req = append(req, 0x04)
			req = append(req, ip.To16()...)
		}
	} else {
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	return append(req, byte(port>>8), byte(port))
}

// readSocksReply reads the full 10-byte SOCKS5 reply envelope.
func readSocksReply(t *testing.T, c net.Conn) []byte {
	t.Helper()
	rep := make([]byte, 10)
	if _, err := io.ReadFull(c, rep); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	return rep
}

// dialDynamic opens a connection to the dynamic proxy's local listener.
func dialDynamic(t *testing.T, tun *tunnel.Tunnel) net.Conn {
	t.Helper()
	addr := net.JoinHostPort(tun.Actual().Host, itoa(tun.Actual().Port))
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial dynamic listener: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// startDynamic starts a dynamic forward with default bind port 0.
func startDynamic(t *testing.T, fc *fakeConnector) *tunnel.Tunnel {
	t.Helper()
	tun := startTunnel(t, tunnel.Spec{
		Direction: tunnel.DirectionDynamic,
		Bind:      tunnel.Bind{Port: 0},
	}, fc)
	if err := tun.Start(context.Background(), "example.com"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return tun
}

// TestDynamic_SOCKS5HandshakeReachesTarget is the -D happy path: a real
// SOCKS5 client handshake — greeting, no-auth selection, CONNECT over IPv4 —
// through the proxy's local listener, and bytes echo back from the target.
func TestDynamic_SOCKS5HandshakeReachesTarget(t *testing.T) {
	fc := newFakeConnector()
	tun := startDynamic(t, fc)

	target := echoTarget(t)
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatalf("split echo target %q: %v", target, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("echo target port %q: %v", portStr, err)
	}

	c := dialDynamic(t, tun)
	greet := socksGreet(t, c, 0x00)
	if greet[0] != 0x05 || greet[1] != 0x00 {
		t.Fatalf("method reply = %v, want [5 0]", greet)
	}
	if _, err := c.Write(socksRequest(0x01, host, port)); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	rep := readSocksReply(t, c)
	if rep[0] != 0x05 || rep[1] != 0x00 {
		t.Fatalf("CONNECT reply = %v, want success (5 0)", rep)
	}

	payload := "ping through the socks proxy"
	if _, err := c.Write([]byte(payload)); err != nil {
		t.Fatalf("write through proxy: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read through proxy: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("round trip = %q, want %q", buf, payload)
	}
}

// TestDynamic_UnsupportedAuthMethodGets0xFF proves a client that offers no
// acceptable method gets the 0xFF method reply — exactly two bytes, not a
// dropped socket — and the proxy keeps serving clients that do offer no-auth.
func TestDynamic_UnsupportedAuthMethodGets0xFF(t *testing.T) {
	fc := newFakeConnector()
	tun := startDynamic(t, fc)

	c := dialDynamic(t, tun)
	rep := socksGreet(t, c, 0x02) // only username/password offered
	if len(rep) != 2 || rep[0] != 0x05 || rep[1] != 0xFF {
		t.Fatalf("method reply = %v, want [5 255]", rep)
	}

	// The proxy still serves a client that offers no-auth.
	c2 := dialDynamic(t, tun)
	rep2 := socksGreet(t, c2, 0x00)
	if rep2[1] != 0x00 {
		t.Fatalf("second greeting reply = %v, want method 0", rep2)
	}
}

// TestDynamic_BINDAndUDPAssociateGet0x07 proves BIND and UDP ASSOCIATE are
// answered with command-not-supported (0x07), not a dropped socket.
func TestDynamic_BINDAndUDPAssociateGet0x07(t *testing.T) {
	fc := newFakeConnector()
	tun := startDynamic(t, fc)

	for _, cmd := range []byte{0x02, 0x03} {
		c := dialDynamic(t, tun)
		greet := socksGreet(t, c, 0x00)
		if greet[1] != 0x00 {
			t.Fatalf("cmd 0x%02x: method reply = %v, want 0", cmd, greet)
		}
		if _, err := c.Write(socksRequest(cmd, "127.0.0.1", 80)); err != nil {
			t.Fatalf("write request: %v", err)
		}
		rep := readSocksReply(t, c)
		if rep[1] != 0x07 {
			t.Fatalf("cmd 0x%02x reply code = %d, want 7 (command not supported)", cmd, rep[1])
		}
	}
}

// TestDynamic_RefusedCONNECTLeavesProxyAlive proves one refused CONNECT
// replies 0x05 and closes only that stream: the proxy keeps serving, and the
// next CONNECT round-trips (spec §7.2, the same rule as trap 4).
func TestDynamic_RefusedCONNECTLeavesProxyAlive(t *testing.T) {
	fc := newFakeConnector()
	refusals := 0
	fc.setDial(func(addr string) (net.Conn, error) {
		if refusals < 1 {
			refusals++
			return nil, errors.New("connection refused")
		}
		return net.Dial("tcp", addr)
	})
	tun := startDynamic(t, fc)

	target := echoTarget(t)
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatalf("split echo target: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("echo target port: %v", err)
	}

	first := dialDynamic(t, tun)
	socksGreet(t, first, 0x00)
	if _, err := first.Write(socksRequest(0x01, host, port)); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	rep := readSocksReply(t, first)
	if rep[1] != 0x05 {
		t.Fatalf("refused CONNECT reply code = %d, want 5 (connection refused)", rep[1])
	}

	second := dialDynamic(t, tun)
	socksGreet(t, second, 0x00)
	if _, err := second.Write(socksRequest(0x01, host, port)); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	rep2 := readSocksReply(t, second)
	if rep2[1] != 0x00 {
		t.Fatalf("second CONNECT reply code = %d, want 0 (success)", rep2[1])
	}
	if _, err := second.Write([]byte("still alive")); err != nil {
		t.Fatalf("write after refused CONNECT: %v", err)
	}
	buf := make([]byte, 11)
	if _, err := io.ReadFull(second, buf); err != nil {
		t.Fatalf("read after refused CONNECT: %v", err)
	}
	if string(buf) != "still alive" {
		t.Fatalf("round trip = %q, want %q", buf, "still alive")
	}
}

// TestDynamic_DomainNameForwardedToFarEnd proves the domain-name address form
// is forwarded VERBATIM to the dial path — name resolution happens at the far
// end of the SSH connection, which is the point of -D — and that a failed
// dial maps to the generic 0x01 reply.
func TestDynamic_DomainNameForwardedToFarEnd(t *testing.T) {
	fc := newFakeConnector()
	var dialedMu sync.Mutex
	var dialed string
	fc.setDial(func(addr string) (net.Conn, error) {
		dialedMu.Lock()
		dialed = addr
		dialedMu.Unlock()
		return nil, errors.New("boom")
	})
	tun := startDynamic(t, fc)

	c := dialDynamic(t, tun)
	socksGreet(t, c, 0x00)
	if _, err := c.Write(socksRequest(0x01, "example.invalid", 8080)); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	rep := readSocksReply(t, c)
	if rep[1] != 0x01 {
		t.Fatalf("dial-failure reply code = %d, want 1 (general failure)", rep[1])
	}
	dialedMu.Lock()
	got := dialed
	dialedMu.Unlock()
	if got != "example.invalid:8080" {
		t.Fatalf("dialed %q, want %q — the domain must be forwarded verbatim, not resolved locally", got, "example.invalid:8080")
	}
}

// TestDynamic_EADDRINUSE_Synchronous proves a busy local port fails
// synchronously from Start — the SOCKS bind is a local bind, so the -L trap
// order applies — and the acquired lease is released.
func TestDynamic_EADDRINUSE_Synchronous(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("busy listen: %v", err)
	}
	defer func() { _ = busy.Close() }()
	tcpAddr, ok := busy.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("busy listener address is not TCP: %T", busy.Addr())
	}

	fc := newFakeConnector()
	tun := startTunnel(t, tunnel.Spec{
		Direction: tunnel.DirectionDynamic,
		Bind:      tunnel.Bind{Port: tcpAddr.Port},
	}, fc)
	err = tun.Start(context.Background(), "example.com")
	if err == nil {
		t.Fatal("Start on busy port: expected error, got nil")
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("Start error = %v, want EADDRINUSE", err)
	}
	if got := fc.conn("example.com").refCount(); got != 0 {
		t.Fatalf("refcount after failed start = %d, want 0 (lease released)", got)
	}
	waitDone(t, tun)
}

// TestDynamic_ConnectionLoss_StoppedConnectionLost proves transport death
// moves a dynamic forward to stopped: connection lost, never silently
// rebinding.
func TestDynamic_ConnectionLoss_StoppedConnectionLost(t *testing.T) {
	fc := newFakeConnector()
	tun := startDynamic(t, fc)

	fc.conn("example.com").lose()

	waitDone(t, tun)
	if got := tun.State(); got != tunnel.StateStopped {
		t.Fatalf("state = %q, want stopped", got)
	}
	if got := tun.StopReason(); got != tunnel.StopReasonConnectionLost {
		t.Fatalf("stop reason = %q, want %q", got, tunnel.StopReasonConnectionLost)
	}
	if tun.Err() == nil {
		t.Fatal("Err = nil after connection loss, want the transport error")
	}
	if got := fc.conn("example.com").refCount(); got != 0 {
		t.Fatalf("refcount after loss = %d, want 0", got)
	}
}

// TestRemote_TwoTunnelsShareConnection_OneStopDoesNotKillTheOther is the -R
// ownership invariant: two remote forwards on one shared connection each hold
// their OWN lease; stopping one releases only its lease and the other keeps
// forwarding.
func TestRemote_TwoTunnelsShareConnection_OneStopDoesNotKillTheOther(t *testing.T) {
	fc := newFakeConnector()
	target := echoTarget(t)

	spec := tunnel.Spec{
		Direction:   tunnel.DirectionRemote,
		Destination: target,
		Bind:        tunnel.Bind{Port: 0},
	}
	tunA := startTunnel(t, spec, fc)
	tunB := startTunnel(t, spec, fc)
	if err := tunA.Start(context.Background(), "example.com"); err != nil {
		t.Fatalf("Start A: %v", err)
	}
	if err := tunB.Start(context.Background(), "example.com"); err != nil {
		t.Fatalf("Start B: %v", err)
	}
	if got := fc.conn("example.com").refCount(); got != 2 {
		t.Fatalf("refcount with two tunnels = %d, want 2", got)
	}

	tunA.Stop()
	if got := fc.conn("example.com").refCount(); got != 1 {
		t.Fatalf("refcount after A stop = %d, want 1 (B still holds the connection)", got)
	}
	roundTrip(t, tunB, "B still forwards after A stopped")
	tunB.Stop()
}
