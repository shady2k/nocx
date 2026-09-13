//go:build nocx_local_ssh

package tunnelchan_test

// The stand these tests run against: a REAL helper daemon connection with the
// ssh service on it, a REAL coordinator client, and a REAL ssh server on the
// other side of the tunnels.
//
// Nothing here is a mock of the thing under test. The two scripted halves are
// the two that are not this repository's: the coordinator's decisions (a
// password, a host-key verdict — in production a vault and a known_hosts file)
// and the ssh SERVER (somebody else's machine). Everything between them — the
// framing, the reverse requests, the helper's pool, the channel plane, the
// forward plane, the coordinator's lease — is the shipped code path.
//
// The fixture is this package's own, for the reason every other package in
// this repository gives: ssh_tunnel_remote_test.go's forward server and
// internal/tunnel's realSeamServer are `_test.go` symbols in other packages
// (and the first was deleted with the lease it served, nocx-50w7p.8), and Go
// does not export those across packages.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/helper/tunnelchan"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
	gossh "golang.org/x/crypto/ssh"
)

const (
	testHash   = "testhash"
	wantRef    = "cred-1"
	wantPass   = "pw"
	fixtureUsr = "test"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ── the ssh server on the far side ─────────────────────────────────────

// fixture is an in-process ssh server that speaks what a tunnel needs: it
// authenticates, answers tcpip-forward / cancel-tcpip-forward global requests
// (binding a real loopback listener and delivering accepted connections to the
// client as forwarded-tcpip channels) and proxies direct-tcpip channels to
// their target. A scripted AllowTcpForwarding / PermitListen policy stands in
// for sshd's configuration.
type fixture struct {
	t          *testing.T
	hostSigner gossh.Signer
	listener   net.Listener
	addr       string

	mu           sync.Mutex
	allowForward bool
	permitListen func(host string, port int) bool
	binds        []forwardBind
	passwords    []string

	liveMu    sync.Mutex
	liveConns map[*gossh.ServerConn]struct{}
}

// forwardBind records one successful tcpip-forward bind: what was requested
// and what the server actually bound. A requested port 0 is resolved here, so
// allocatedPort is the truth the reply must have carried.
type forwardBind struct {
	requestedHost string
	requestedPort int
	allocatedPort int
}

func startFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		t:            t,
		hostSigner:   newSigner(t),
		allowForward: true,
		liveConns:    map[*gossh.ServerConn]struct{}{},
	}
	config := &gossh.ServerConfig{
		PasswordCallback: func(_ gossh.ConnMetadata, pw []byte) (*gossh.Permissions, error) {
			f.mu.Lock()
			f.passwords = append(f.passwords, string(pw))
			f.mu.Unlock()
			if string(pw) == wantPass {
				return nil, nil
			}
			return nil, fmt.Errorf("password refused")
		},
	}
	config.AddHostKey(f.hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fixture listen: %v", err)
	}
	f.listener = ln
	f.addr = ln.Addr().String()
	t.Cleanup(func() { _ = ln.Close() })
	go f.acceptLoop(config)
	return f
}

func (f *fixture) setAllowForward(allow bool) {
	f.mu.Lock()
	f.allowForward = allow
	f.mu.Unlock()
}

func (f *fixture) setPermitListen(fn func(host string, port int) bool) {
	f.mu.Lock()
	f.permitListen = fn
	f.mu.Unlock()
}

// lastBind is the most recent successful tcpip-forward bind. The allocated
// port is what the server really bound — the value the reply must have carried.
func (f *fixture) lastBind() (string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.binds) == 0 {
		return "", 0
	}
	b := f.binds[len(f.binds)-1]
	return b.requestedHost, b.allocatedPort
}

func (f *fixture) authAttempts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.passwords...)
}

func (f *fixture) acceptLoop(config *gossh.ServerConfig) {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.serveConn(conn, config)
	}
}

func (f *fixture) serveConn(conn net.Conn, config *gossh.ServerConfig) {
	sshConn, chans, reqs, err := gossh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()
		return
	}
	f.liveMu.Lock()
	f.liveConns[sshConn] = struct{}{}
	f.liveMu.Unlock()
	defer func() {
		f.liveMu.Lock()
		delete(f.liveConns, sshConn)
		f.liveMu.Unlock()
		_ = sshConn.Close()
	}()

	// Global requests are SERVICED, not discarded: a tcpip-forward request
	// left unanswered hangs the client's Listen forever.
	go func() {
		for req := range reqs {
			switch req.Type {
			case "tcpip-forward":
				f.handleTCPIPForward(sshConn, req)
			case "cancel-tcpip-forward":
				_ = req.Reply(true, nil)
			default:
				_ = req.Reply(false, nil)
			}
		}
	}()

	for newChan := range chans {
		if newChan.ChannelType() != "direct-tcpip" {
			_ = newChan.Reject(gossh.UnknownChannelType, "unknown channel type")
			continue
		}
		f.handleDirectTCPIP(newChan, newChan.ExtraData())
	}
}

// waitLiveConns blocks until the server holds exactly want established
// connections, so a test never guesses when the helper's dial has landed.
func (f *fixture) waitLiveConns(want int) {
	f.t.Helper()
	waittest.WaitForTimeoutDetail(f.t, "the fixture's connection count", 5*time.Second, func() string {
		f.liveMu.Lock()
		defer f.liveMu.Unlock()
		return fmt.Sprintf("the fixture holds %d established connections, want %d", len(f.liveConns), want)
	}, func() bool {
		f.liveMu.Lock()
		defer f.liveMu.Unlock()
		return len(f.liveConns) == want
	})
}

// killConns closes every established server-side connection: what a network
// loss looks like to the helper — the pooled connection dies under the
// channels and the listener riding it.
//
// It refuses to kill nothing, for the reason ssh_real_test.go's own killConns
// states: every caller kills in order to observe a loss immediately after, and
// an empty set is a wait that can only end at its deadline.
func (f *fixture) killConns() {
	f.t.Helper()
	f.liveMu.Lock()
	conns := make([]*gossh.ServerConn, 0, len(f.liveConns))
	for c := range f.liveConns {
		conns = append(conns, c)
	}
	f.liveMu.Unlock()
	if len(conns) == 0 {
		f.t.Fatal("killConns: no established connection to close — the loss this test " +
			"waits for can never arrive; wait for the fixture to accept first (waitLiveConns)")
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// forwardPayloadReader decodes the forwarding payloads the protocol puts on
// the wire as string/uint32 sequences (RFC 4254 §7): gossh's Marshal writes
// lowercase field names and its Unmarshal cannot set an external package's
// unexported fields, so the payloads are parsed by hand.
type forwardPayloadReader struct{ r *bytes.Reader }

func newForwardPayloadReader(b []byte) *forwardPayloadReader {
	return &forwardPayloadReader{r: bytes.NewReader(b)}
}

func (p *forwardPayloadReader) str() (string, bool) {
	var l uint32
	if err := binary.Read(p.r, binary.BigEndian, &l); err != nil {
		return "", false
	}
	b := make([]byte, l)
	if _, err := io.ReadFull(p.r, b); err != nil {
		return "", false
	}
	return string(b), true
}

func (p *forwardPayloadReader) u32() (uint32, bool) {
	var v uint32
	if err := binary.Read(p.r, binary.BigEndian, &v); err != nil {
		return 0, false
	}
	return v, true
}

// handleTCPIPForward binds a real loopback listener for the request and
// replies with the allocated port (for a port-0 request). A refused bind — the
// policy, or the OS — replies false, which is the only refusal the client can
// see.
func (f *fixture) handleTCPIPForward(sshConn *gossh.ServerConn, req *gossh.Request) {
	p := newForwardPayloadReader(req.Payload)
	addr, ok := p.str()
	rport, okPort := p.u32()
	if !ok || !okPort {
		_ = req.Reply(false, nil)
		return
	}
	f.mu.Lock()
	allow := f.allowForward && (f.permitListen == nil || f.permitListen(addr, int(rport)))
	f.mu.Unlock()
	if !allow {
		_ = req.Reply(false, nil)
		return
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(int(rport))))
	if err != nil {
		_ = req.Reply(false, nil)
		return
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		_ = req.Reply(false, nil)
		return
	}
	f.mu.Lock()
	f.binds = append(f.binds, forwardBind{
		requestedHost: addr,
		requestedPort: int(rport),
		allocatedPort: tcpAddr.Port,
	})
	f.mu.Unlock()

	// The reply carries the allocated port only for a port-0 request
	// (RFC 4254 §7.1): the client parses it only then.
	if rport == 0 {
		_ = req.Reply(true, gossh.Marshal(struct{ Port uint32 }{uint32(tcpAddr.Port)})) //nolint:gosec // SSH protocol values fit uint32
	} else {
		_ = req.Reply(true, nil)
	}

	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			f.relayForwarded(sshConn, addr, tcpAddr.Port, c)
		}
	}()
}

// relayForwarded opens a forwarded-tcpip channel to the client for one
// accepted connection and copies bytes both ways — the exact -R data path
// OpenSSH provides.
func (f *fixture) relayForwarded(sshConn *gossh.ServerConn, requestedHost string, allocatedPort int, c net.Conn) {
	defer func() { _ = c.Close() }()
	originHost, originPortStr, err := net.SplitHostPort(c.RemoteAddr().String())
	if err != nil {
		return
	}
	originPort, err := strconv.Atoi(originPortStr)
	if err != nil {
		return
	}
	payload := gossh.Marshal(struct {
		Addr       string
		Port       uint32
		OriginAddr string
		OriginPort uint32
	}{
		Addr:       requestedHost,
		Port:       uint32(allocatedPort), //nolint:gosec // SSH protocol values fit uint32
		OriginAddr: originHost,
		OriginPort: uint32(originPort), //nolint:gosec // SSH protocol values fit uint32
	})
	ch, reqs, err := sshConn.OpenChannel("forwarded-tcpip", payload)
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()
	go gossh.DiscardRequests(reqs)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(c, ch); done <- struct{}{} }()
	go func() { _, _ = io.Copy(ch, c); done <- struct{}{} }()
	<-done
}

// handleDirectTCPIP proxies a direct-tcpip channel to its target. The dial
// happens BEFORE the channel is accepted, so a refused target rejects the open
// itself — which is what the coordinator's dial sees as a refusal rather than
// as an EOF.
func (f *fixture) handleDirectTCPIP(newChan gossh.NewChannel, extraData []byte) {
	p := newForwardPayloadReader(extraData)
	raddr, ok := p.str()
	rport, okPort := p.u32()
	if _, okOrigin := p.str(); !ok || !okPort || !okOrigin {
		_ = newChan.Reject(gossh.ConnectionFailed, "connect failed: malformed direct-tcpip payload")
		return
	}
	targetConn, err := net.DialTimeout("tcp", net.JoinHostPort(raddr, strconv.Itoa(int(rport))), 5*time.Second)
	if err != nil {
		_ = newChan.Reject(gossh.ConnectionFailed, "connect failed: "+err.Error())
		return
	}
	defer func() { _ = targetConn.Close() }()
	ch, reqs, err := newChan.Accept()
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()
	go gossh.DiscardRequests(reqs)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(targetConn, ch); done <- struct{}{} }()
	go func() { _, _ = io.Copy(ch, targetConn); done <- struct{}{} }()
	<-done
}

// ── the coordinator on this side ───────────────────────────────────────

// coordinator is this side of the reverse channel: the four ops a helper may
// ask a coordinator, scripted. It stands in for internal/app's handlers (the
// vault, known_hosts) exactly in shape: same service, same ops, same result
// types, same refusal codes.
type coordinator struct {
	sealed bool
	// verdict is what the coordinator answers a host-key question with. The
	// fixture's host key is nobody's record, so `trusted` is what a test that
	// does not care about the accept flow answers — it is the coordinator's
	// decision either way, which is the whole point of the reverse ops.
	verdict proto.HostKeyVerdict
}

func (c *coordinator) registry() *helperclient.ReverseRegistry {
	r := helperclient.NewReverseRegistry()
	r.Register(proto.ServiceSSH, proto.OpSecret, func(_ context.Context, _ json.RawMessage) (any, error) {
		if c.sealed {
			return nil, &proto.Refusal{Code: proto.ErrCodeVaultSealed, Message: "the vault is sealed"}
		}
		return proto.SecretResult{Secret: []byte(wantPass)}, nil
	})
	r.Register(proto.ServiceSSH, proto.OpSign, func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, &proto.Refusal{Code: proto.ErrCodeInternal, Message: "this coordinator has no key material"}
	})
	r.Register(proto.ServiceSSH, proto.OpVerifyHostKey, func(_ context.Context, raw json.RawMessage) (any, error) {
		var p proto.VerifyHostKeyParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		fingerprint := ""
		if key, err := gossh.ParsePublicKey(p.Key); err == nil {
			fingerprint = gossh.FingerprintSHA256(key)
		}
		return proto.VerifyHostKeyResult{Verdict: c.verdict, Fingerprint: fingerprint}, nil
	})
	r.Register(proto.ServiceSSH, proto.OpTrustHostKey, func(_ context.Context, _ json.RawMessage) (any, error) {
		return proto.TrustHostKeyResult{Fingerprint: "SHA256:recorded"}, nil
	})
	return r
}

// ── the stand ──────────────────────────────────────────────────────────

// stand is one helper connection: a host with the ssh service on it, a client
// with a scripted coordinator behind it, and the connector under test pointed
// at both.
type stand struct {
	t         *testing.T
	fixture   *fixture
	connector *tunnelchan.Connector
	client    *helperclient.Client
	host      *host.Host

	// helperEnd is this side's end of the socket pair the host serves: closing
	// it is what a daemon that dies looks like to the client (its input reaches
	// EOF), which is the loss the coordinator-side watcher reports.
	helperEnd net.Conn

	served    chan struct{}
	cancel    context.CancelFunc
	closeOnce sync.Once
}

func newStand(t *testing.T, f *fixture, coord *coordinator) *stand {
	t.Helper()
	helperEnd, coordEnd := net.Pipe()
	s := &stand{
		t:         t,
		fixture:   f,
		helperEnd: helperEnd,
		served:    make(chan struct{}),
	}
	realClient, err := ssh.NewReal(log.NewSlogAdapter(nil))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	t.Cleanup(func() { _ = realClient.Close() })

	svc := sshsvc.New(realClient, discardLogger())
	s.host = host.New(helperEnd, helperEnd, testHash, "instance-1", discardLogger())
	s.host.Register(svc)

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go func() {
		defer close(s.served)
		_ = s.host.Serve(ctx)
	}()

	c, err := helperclient.Dial(ctx, helperclient.Config{
		Exec:        helperclient.NewSocketConn(coordEnd),
		ExpectHash:  testHash,
		SentinelTTL: 5 * time.Second,
		Reverse:     coord.registry(),
		Log:         discardLogger(),
	})
	if err != nil {
		t.Fatalf("helper client Dial: %v", err)
	}
	s.client = c
	s.connector = &tunnelchan.Connector{
		Local:   func(context.Context) (*helperclient.Client, error) { return c, nil },
		Resolve: realClient,
		Log:     discardLogger(),
	}
	t.Cleanup(s.stop)
	return s
}

// killHelperTransport ends the helper's side of the connection: the daemon is
// gone, and every stream and listener the coordinator holds on it is over.
func (s *stand) killHelperTransport() {
	_ = s.helperEnd.Close()
}

func (s *stand) stop() {
	s.closeOnce.Do(func() {
		_ = s.client.Close()
		s.cancel()
		select {
		case <-s.served:
		case <-time.After(5 * time.Second):
		}
	})
}

// lease acquires one lease against the fixture through the helper.
//
// The destination is built with a RAW ConnectOption rather than a helper
// constructor, and that is exact rather than a shortcut: ResolveTarget reads
// two facts — the account and the credential REFERENCE — and the coordinator's
// richer options (a credential storer, an authorization binding) are exercised
// by internal/ssh's own tests against a vault that this package has no reason
// to construct. The password itself never travels in this direction; the
// helper asks for it (proto.OpSecret), which is the whole point of the
// reference being opaque.
func (s *stand) lease(t *testing.T) ssh.TunnelConn {
	t.Helper()
	l, err := s.connector.TunnelConn(context.Background(), s.fixture.addr,
		ssh.WithUser(fixtureUsr),
		ssh.ConnectOption(func(c *ssh.ConnectConfig) { c.SecretID = wantRef }),
	)
	if err != nil {
		t.Fatalf("TunnelConn: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// ── small helpers ─────────────────────────────────────────────────────

func newSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer
}

// echoTarget listens on a loopback port and echoes every accepted connection,
// standing in for the "remote destination" of a forward.
func echoTarget(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo target listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
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

// deadTarget is an address on which nothing listens (the listener is closed
// before returning) — the refused-CONNECT target.
func deadTarget(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dead target listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// socks5Connect dials proxyAddr, performs the no-auth SOCKS5 greeting and a
// CONNECT to target, and returns the relayed connection plus the reply code.
// The address type follows the host: an IP is sent as ATYP IPv4, anything else
// as ATYP domain — mirroring what real SOCKS5 clients do.
func socks5Connect(t *testing.T, proxyAddr, target string) (net.Conn, byte) {
	t.Helper()
	conn, dialErr := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if dialErr != nil {
		t.Fatalf("dial proxy: %v", dialErr)
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("socks greeting write: %v", err)
	}
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil {
		t.Fatalf("socks method reply: %v", err)
	}
	if method[0] != 0x05 || method[1] != 0x00 {
		_ = conn.Close()
		t.Fatalf("socks method reply = %x %x, want 05 00", method[0], method[1])
	}

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("target %q: %v", target, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("target port %q: %v", portStr, err)
	}
	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host).To4(); ip != nil {
		req = append(req, 0x01)
		req = append(req, ip...)
	} else {
		req = append(req, 0x03, byte(len(host))) //nolint:gosec // a SOCKS domain name is at most 255 bytes
		req = append(req, host...)
	}
	req = append(req, byte(port>>8), byte(port)) //nolint:gosec // low byte of a 16-bit port
	if _, err := conn.Write(req); err != nil {
		_ = conn.Close()
		t.Fatalf("socks connect write: %v", err)
	}
	var rep [10]byte
	if _, err := io.ReadFull(conn, rep[:]); err != nil {
		_ = conn.Close()
		t.Fatalf("socks connect reply: %v", err)
	}
	if rep[0] != 0x05 {
		_ = conn.Close()
		t.Fatalf("socks reply VER = %d, want 5", rep[0])
	}
	return conn, rep[1]
}

// socksRoundTrip writes payload through a relayed SOCKS connection and reads
// the echo back.
func socksRoundTrip(t *testing.T, conn net.Conn, payload string) {
	t.Helper()
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write through socks: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read through socks: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("socks round trip = %q, want %q", buf, payload)
	}
}

// acceptWithin bounds an Accept.
//
// The transport answers a deadline request with "not supported" (channelConn's
// own note: the pool lease it replaced answered the same way), so a test that
// waits on a connection that will never arrive would hang until the package
// timeout instead of failing. Nothing is proven by a hang: the mutation probe
// this helper was written for made five tests time out together, and a timeout
// names no assertion.
func acceptWithin(t *testing.T, ln net.Listener, d time.Duration) (net.Conn, error) {
	t.Helper()
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		ch <- result{c: c, err: err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-time.After(d):
		return nil, fmt.Errorf("no connection arrived within %s", d)
	}
}

// relay copies bytes both ways between two connections until either ends.
func relay(t *testing.T, a, b net.Conn) {
	t.Helper()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	<-done
}
