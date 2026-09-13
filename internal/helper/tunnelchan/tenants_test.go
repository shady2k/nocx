//go:build nocx_local_ssh

package tunnelchan_test

// The TENANTS: every consumer that used to take its connection from
// ssh.RealClient.TunnelConn, driven through the helper instead.
//
// The list is the landing group of nocx-50w7p.8 and it is here in one file on
// purpose — the forward model's three strategies, an API request routed
// through a connection, and the remote lifecycle channel — because the move
// was atomic: a tree with one of them still dialing from this process is the
// two-owner state AD-4 exists to prevent.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/httppolicy"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/lifecycleremote"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/tunnel"
	"github.com/shady2k/nocx/internal/waittest"
)

// The connector satisfies the forward model's own seam without importing it:
// the signatures are structural, and this assertion is where the drift would
// be caught if they stopped being.
var _ tunnel.Connector = (*connectorAssertion)(nil)

type connectorAssertion struct{}

func (connectorAssertion) TunnelConn(context.Context, string, ...ssh.ConnectOption) (ssh.TunnelConn, error) {
	return nil, nil
}

// opts are the connect options a forward is started with: the account and the
// credential REFERENCE (see stand.lease for why it is built directly).
func (s *stand) opts() []ssh.ConnectOption {
	return []ssh.ConnectOption{
		ssh.WithUser(fixtureUsr),
		ssh.ConnectOption(func(c *ssh.ConnectConfig) { c.SecretID = wantRef }),
	}
}

// startForward starts one tunnel against the fixture through the helper.
func (s *stand) startForward(t *testing.T, spec tunnel.Spec) *tunnel.Tunnel {
	t.Helper()
	tun, err := tunnel.New(spec, s.connector)
	if err != nil {
		t.Fatalf("tunnel.New: %v", err)
	}
	if err := tun.Start(context.Background(), s.fixture.addr, s.opts()...); err != nil {
		t.Fatalf("tunnel.Start: %v", err)
	}
	t.Cleanup(tun.Stop)
	return tun
}

// TestLocalForwardCarriesBytesThroughTheHelper is the -L strategy over the
// helper: a listener on THIS machine, one direct-tcpip channel per accepted
// connection, and the destination resolved on the far side's network.
func TestLocalForwardCarriesBytesThroughTheHelper(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})

	tun := s.startForward(t, tunnel.Spec{
		Direction:   tunnel.DirectionLocal,
		Bind:        tunnel.Bind{Host: "127.0.0.1", Port: 0},
		Destination: echoTarget(t),
	})
	actual := tun.Actual()
	if actual.Port == 0 {
		t.Fatal("the local forward reported port 0")
	}

	conn, err := net.Dial("tcp", net.JoinHostPort(actual.Host, strconv.Itoa(actual.Port)))
	if err != nil {
		t.Fatalf("dial the local forward: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	payload := "ping through -L over the helper"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read the echo: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("round trip = %q, want %q", buf, payload)
	}
}

// TestLocalForwardToARefusedTargetEndsOnlyThatStream pairs the success above:
// the far side refuses the destination, and the accepted connection ends
// while the forward keeps serving — spec §7.1 trap 4 at the strategy level.
func TestLocalForwardToARefusedTargetEndsOnlyThatStream(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})

	tun := s.startForward(t, tunnel.Spec{
		Direction:   tunnel.DirectionLocal,
		Bind:        tunnel.Bind{Host: "127.0.0.1", Port: 0},
		Destination: deadTarget(t),
	})
	actual := tun.Actual()

	conn, err := net.Dial("tcp", net.JoinHostPort(actual.Host, strconv.Itoa(actual.Port)))
	if err != nil {
		t.Fatalf("dial the local forward: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	// The refused destination ends this stream; nothing is relayed.
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		_ = conn.Close()
		t.Fatal("a stream to a refused destination delivered bytes")
	}
	_ = conn.Close()

	if tun.State() != tunnel.StateRunning {
		t.Fatalf("the forward stopped (%s) when ONE destination refused", tun.State())
	}
}

// TestRemoteForwardCarriesBytesThroughTheHelper is the -R strategy over the
// helper: the listener is on the FAR side (a forward op), the arriving
// connection comes back as a forwarded channel, and the destination is dialed
// locally.
func TestRemoteForwardCarriesBytesThroughTheHelper(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})

	tun := s.startForward(t, tunnel.Spec{
		Direction:   tunnel.DirectionRemote,
		Bind:        tunnel.Bind{Host: "127.0.0.1", Port: 0},
		Destination: echoTarget(t),
	})
	actual := tun.Actual()
	if _, allocated := f.lastBind(); actual.Port == 0 || actual.Port != allocated {
		t.Fatalf("reported port %d, the server allocated %d", actual.Port, allocated)
	}

	remote, err := net.Dial("tcp", net.JoinHostPort(actual.Host, strconv.Itoa(actual.Port)))
	if err != nil {
		t.Fatalf("dial the far side's listener: %v", err)
	}
	defer func() { _ = remote.Close() }()
	_ = remote.SetDeadline(time.Now().Add(10 * time.Second))

	payload := "ping through -R over the helper"
	if _, err := remote.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(remote, buf); err != nil {
		t.Fatalf("read the echo: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("round trip = %q, want %q", buf, payload)
	}
}

// TestRemoteForwardRefusedByTheServersPolicy pairs the success above: the
// server will not forward, the START fails with the policy-worded reason, and
// the record lands stopped/error rather than running.
func TestRemoteForwardRefusedByTheServersPolicy(t *testing.T) {
	f := startFixture(t)
	f.setAllowForward(false)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})

	tun, err := tunnel.New(tunnel.Spec{
		Direction:   tunnel.DirectionRemote,
		Bind:        tunnel.Bind{Host: "127.0.0.1", Port: 0},
		Destination: "127.0.0.1:1",
	}, s.connector)
	if err != nil {
		t.Fatalf("tunnel.New: %v", err)
	}
	err = tun.Start(context.Background(), f.addr, s.opts()...)
	if err == nil {
		tun.Stop()
		t.Fatal("Start: expected a refusal, got a running forward")
	}
	for _, want := range []string{"refused by server", "AllowTcpForwarding", "PermitListen"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Start error %q does not name %q", err, want)
		}
	}
	if tun.State() != tunnel.StateStopped || tun.StopReason() != tunnel.StopReasonError {
		t.Fatalf("after a refused start: state %q reason %q, want stopped/error", tun.State(), tun.StopReason())
	}
}

// TestRemoteForwardBindCaveatSurvivesTheMove pins the disclosure the transport
// cannot improve on: a requested non-loopback bind is reported as the server
// bound it, with the caveat that the address was never verified — and a
// loopback request is clean.
func TestRemoteForwardBindCaveatSurvivesTheMove(t *testing.T) {
	t.Run("non-loopback-request-caveated", func(t *testing.T) {
		f := startFixture(t)
		s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
		tun := s.startForward(t, tunnel.Spec{
			Direction:   tunnel.DirectionRemote,
			Bind:        tunnel.Bind{Host: "0.0.0.0", Port: 0},
			Destination: "127.0.0.1:1",
		})
		caveat := tun.Caveat()
		for _, want := range []string{"not verified", "may only work on the server"} {
			if !strings.Contains(caveat, want) {
				t.Fatalf("Caveat %q does not say %q", caveat, want)
			}
		}
	})

	t.Run("loopback-request-clean", func(t *testing.T) {
		f := startFixture(t)
		s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
		tun := s.startForward(t, tunnel.Spec{
			Direction:   tunnel.DirectionRemote,
			Bind:        tunnel.Bind{Host: "127.0.0.1", Port: 0},
			Destination: "127.0.0.1:1",
		})
		if caveat := tun.Caveat(); caveat != "" {
			t.Fatalf("Caveat = %q for a loopback bind, want empty", caveat)
		}
	})
}

// TestDynamicForwardCarriesSocksThroughTheHelper is the -D strategy over the
// helper: a SOCKS5 client on this machine, one direct-tcpip channel per
// CONNECT, and the name resolved by the FAR side — which is what -D is for.
func TestDynamicForwardCarriesSocksThroughTheHelper(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})

	tun := s.startForward(t, tunnel.Spec{
		Direction: tunnel.DirectionDynamic,
		Bind:      tunnel.Bind{Host: "127.0.0.1", Port: 0},
	})
	actual := tun.Actual()
	if actual.Port == 0 {
		t.Fatal("the dynamic proxy reported port 0")
	}
	proxyAddr := net.JoinHostPort(actual.Host, strconv.Itoa(actual.Port))

	// CONNECT by IP.
	target := echoTarget(t)
	conn, rep := socks5Connect(t, proxyAddr, target)
	if rep != 0x00 {
		_ = conn.Close()
		t.Fatalf("CONNECT reply = 0x%02x, want 0x00", rep)
	}
	socksRoundTrip(t, conn, "ping over a helper-carried direct-tcpip channel")
	_ = conn.Close()

	// CONNECT by domain: forwarded verbatim, resolved by the far side.
	_, portStr, _ := net.SplitHostPort(target)
	conn2, rep2 := socks5Connect(t, proxyAddr, net.JoinHostPort("localhost", portStr))
	if rep2 != 0x00 {
		_ = conn2.Close()
		t.Fatalf("domain CONNECT reply = 0x%02x, want 0x00", rep2)
	}
	socksRoundTrip(t, conn2, "ping through far-end name resolution")
	_ = conn2.Close()

	// A refused target answers 0x05 on its own stream, and the proxy is still
	// serving afterwards.
	dead, rep3 := socks5Connect(t, proxyAddr, deadTarget(t))
	if rep3 != 0x05 {
		_ = dead.Close()
		t.Fatalf("refused CONNECT reply = 0x%02x, want 0x05 (connection refused)", rep3)
	}
	_ = dead.Close()

	conn4, rep4 := socks5Connect(t, proxyAddr, target)
	if rep4 != 0x00 {
		_ = conn4.Close()
		t.Fatalf("CONNECT after a refused stream reply = 0x%02x, want 0x00", rep4)
	}
	socksRoundTrip(t, conn4, "still serving after a refused CONNECT")
	_ = conn4.Close()
}

// leaseLeaser is the composition root's apiRouteLeaser in the one respect this
// test needs: it answers a profile id with the lease that profile resolved to.
type leaseLeaser struct {
	lease ssh.TunnelConn
	err   error
}

func (l leaseLeaser) LeaseForProfile(context.Context, string) (ssh.TunnelConn, error) {
	return l.lease, l.err
}

// TestAnAPIRequestRoutedThroughTheHelperIsCarriedByAChannel drives the API
// feature's own route: the route table the sender uses, the policy transport
// the send goes through, and a REAL http request whose connection is a
// direct-tcpip channel this machine's helper opened.
func TestAnAPIRequestRoutedThroughTheHelperIsCarriedByAChannel(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})

	// The service the request is aimed at. It stands in for the far side's
	// admin port, which is the case the feature exists for (a loopback name
	// through a connection means the far side's loopback).
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "carried by a helper channel")
	}))
	defer service.Close()

	routes := apisend.NewRoutes(leaseLeaser{lease: s.lease(t)})
	routeID, err := apisend.RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "profile-1"})
	if err != nil {
		t.Fatalf("RouteIDFor: %v", err)
	}
	route, err := routes(context.Background(), routeID)
	if err != nil {
		t.Fatalf("routes: %v", err)
	}
	client := &http.Client{Transport: httppolicy.NewTransport(httppolicy.Params{
		Component: "tunnelchan-test",
		Route:     route,
	})}
	client.Timeout = 10 * time.Second

	resp, err := client.Get("http://localhost:" + portOfURL(t, service.URL) + "/admin")
	if err != nil {
		t.Fatalf("GET through the connection route: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the body: %v", err)
	}
	if string(body) != "carried by a helper channel" {
		t.Fatalf("body = %q, want the far side's answer", body)
	}
}

// TestARoutedSendRefusesRatherThanDialingLocally pairs the success above. A
// route whose lease cannot be taken must FAIL the request — never fall back to
// a local dialer, which would put the request on this machine's own network
// around the bastion the environment named.
//
// No stand is built here and that is the point: the failure happens before any
// helper is involved, and the service below would answer this request if
// anything on the path quietly dialled locally.
func TestARoutedSendRefusesRatherThanDialingLocally(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "this answer must never arrive")
	}))
	defer service.Close()

	routes := apisend.NewRoutes(leaseLeaser{err: errors.New("the connection is not available")})
	routeID, err := apisend.RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "profile-1"})
	if err != nil {
		t.Fatalf("RouteIDFor: %v", err)
	}
	route, err := routes(context.Background(), routeID)
	if err != nil {
		t.Fatalf("routes: %v", err)
	}
	client := &http.Client{Transport: httppolicy.NewTransport(httppolicy.Params{
		Component: "tunnelchan-test",
		Route:     route,
	})}
	client.Timeout = 10 * time.Second

	resp, err := client.Get("http://localhost:" + portOfURL(t, service.URL) + "/admin")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a request with no lease succeeded; it must refuse rather than dial locally")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Fatalf("error = %v, want the leaser's own refusal in it", err)
	}
}

func portOfURL(t *testing.T, raw string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(raw, "http://"))
	if err != nil {
		t.Fatalf("split %q: %v", raw, err)
	}
	return port
}

// notifyingEmitter is the renderer stand-in the publisher requires: this
// package's subject is the transport, and the facts a real renderer would
// commit are internal/lifecyclepub's own tests.
type notifyingEmitter struct{}

func (notifyingEmitter) PublishLifecycle(lifecyclepub.Fact) {}

// lossRecorder records the losses the lifecycle adapter reports.
type lossRecorder struct {
	mu     sync.Mutex
	causes []lifecycleremote.LossCause
}

func (r *lossRecorder) report(_ lifecycle.LaneID, cause lifecycleremote.LossCause) {
	r.mu.Lock()
	r.causes = append(r.causes, cause)
	r.mu.Unlock()
}

func (r *lossRecorder) seen() []lifecycleremote.LossCause {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]lifecycleremote.LossCause(nil), r.causes...)
}

// TestTheRemoteLifecycleChannelRidesAHelperForward is the ADR-0024 transport
// over the helper: the loopback listener is a `forward` op, the shell's
// connection back arrives as a forwarded channel, and the handshake completes
// end to end — the shell dials the port the adapter reported, sends its
// authenticated hello, and reads the accept.
func TestTheRemoteLifecycleChannelRidesAHelperForward(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
	lease := s.lease(t)

	kernel := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	kernel.SetEmitter(notifyingEmitter{})
	losses := &lossRecorder{}

	adapter, cfg, err := lifecycleremote.New(log.NewSlogAdapter(nil), kernel, lease,
		lifecycleremote.WithHelloTimeout(10*time.Second),
		lifecycleremote.WithLossReporter(losses.report),
	)
	if err != nil {
		t.Fatalf("lifecycleremote.New: %v", err)
	}
	defer func() { _ = adapter.Close() }()

	// The far side's listener is the fixture's, at the port the SERVER
	// allocated — which is the port the shell is told to connect to.
	if _, allocated := f.lastBind(); cfg.Port == 0 || cfg.Port != allocated {
		t.Fatalf("the launch config says port %d, the server allocated %d", cfg.Port, allocated)
	}

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)))
	if err != nil {
		t.Fatalf("dial the forwarded port (what the shell's hook does): %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	var capability lifecycle.Capability
	raw, err := hex.DecodeString(cfg.Capability)
	if err != nil {
		t.Fatalf("decode the capability: %v", err)
	}
	copy(capability[:], raw)
	hello := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       cfg.Lane,
		Domain:     cfg.Domain,
		Epoch:      cfg.Epoch,
		Sequence:   1,
		Capability: capability,
		Event:      lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}},
	}
	if _, encodeErr := lifecyclecodec.Encode(conn, hello); encodeErr != nil {
		t.Fatalf("send the hello: %v", encodeErr)
	}

	dec := lifecyclecodec.NewDecoder(conn, lifecyclecodec.Config{}, nil)
	accept, err := dec.ReadFrame()
	if err != nil {
		t.Fatalf("read the accept: %v", err)
	}
	if accept.Event.Kind != lifecycle.KindAccept {
		t.Fatalf("frame = %s, want accept", accept.Event.Kind)
	}
	if accept.Domain != cfg.Domain {
		t.Fatalf("accept domain = %s, want %s", accept.Domain, cfg.Domain)
	}
}

// TestARefusedLifecycleForwardLeavesTheSessionConventional pairs the success
// above: the server will not forward, so the adapter refuses by name and the
// caller spawns the shell without a channel (ADR-0024's safe direction).
func TestARefusedLifecycleForwardLeavesTheSessionConventional(t *testing.T) {
	f := startFixture(t)
	f.setAllowForward(false)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
	lease := s.lease(t)

	kernel := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	kernel.SetEmitter(notifyingEmitter{})

	_, _, err := lifecycleremote.New(log.NewSlogAdapter(nil), kernel, lease)
	if err == nil {
		t.Fatal("New: expected a refusal, got an established channel")
	}
	if !errors.Is(err, lifecycleremote.ErrForwardingRefused) {
		t.Fatalf("err = %v, want ErrForwardingRefused", err)
	}
}

// TestALifecycleChannelLostUnderTheAdapterIsReported is the failure path a
// person meets when the far side's connection dies with an integrated session
// running: the listener goes, the adapter reports WHY (loss-listener-gone and
// not a silent hang), and the shell's next read ends.
func TestALifecycleChannelLostUnderTheAdapterIsReported(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
	lease := s.lease(t)

	kernel := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	kernel.SetEmitter(notifyingEmitter{})
	losses := &lossRecorder{}

	adapter, cfg, err := lifecycleremote.New(log.NewSlogAdapter(nil), kernel, lease,
		lifecycleremote.WithHelloTimeout(10*time.Second),
		lifecycleremote.WithLossReporter(losses.report),
	)
	if err != nil {
		t.Fatalf("lifecycleremote.New: %v", err)
	}
	defer func() { _ = adapter.Close() }()
	f.waitLiveConns(1)

	f.killConns()

	waittest.WaitForTimeoutDetail(t, "the adapter to report the loss", 10*time.Second,
		func() string { return "the adapter reported nothing after its listener's connection died" },
		func() bool { return len(losses.seen()) > 0 })

	// And the lease itself is over, so nothing in this process believes the
	// channel is still there.
	select {
	case <-lease.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the lease's Done is still open after the listener was lost")
	}
	_ = cfg
}
