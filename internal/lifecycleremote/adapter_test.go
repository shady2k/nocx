package lifecycleremote

import (
	"encoding/hex"
	"errors"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/testwait"
)

// seqRand yields 1, 2, 3, … (wrapping to 1 after 255) — never a zero byte,
// so capabilities minted from it are never all-zero and every domain gets a
// distinct capability.
type seqRand struct{ b byte }

func (r *seqRand) Read(p []byte) (int, error) {
	for i := range p {
		r.b++
		p[i] = r.b
	}
	return len(p), nil
}

// newTestKernel builds the adapter's kernel seam the way the composition root
// does: the PUBLISHER wrapping the raw kernel, with an emitter that
// acknowledges every establishment synchronously — the renderer applying the
// published fact instantly (decision 9). The adapter drives the publisher;
// the raw kernel no longer satisfies the adapter seam because it returns
// outbound unsent.
func newTestKernel() *lifecyclepub.Publisher {
	k := lifecycle.New(lifecycle.Options{Rand: &seqRand{}})
	pub := lifecyclepub.New(k)
	pub.SetEmitter(ackingEmitter{pub: pub})
	return pub
}

// ackingEmitter acknowledges every published establishment fact immediately,
// as a renderer that commits the editor presentation on receipt would. The
// accept then flushes through the publisher (decision 9).
type ackingEmitter struct {
	pub *lifecyclepub.Publisher
}

func (e ackingEmitter) PublishLifecycle(f lifecyclepub.Fact) {
	if f.Generation == "" || f.Domain == "" {
		return
	}
	_ = e.pub.AcknowledgeEstablishment(
		lifecycle.LaneID(f.Lane), lifecycle.DomainID(f.Domain), f.Epoch, f.Generation)
}

// fakeTunnel is a TunnelConn whose Listen returns a real loopback listener —
// the shell side of the tests dials the adapter's allocated port over TCP,
// exactly as a shell on the remote host would. done/lostErr script the
// connection-loss contract.
type fakeTunnel struct {
	mu         sync.Mutex
	listenAddr string // the addr Listen was asked for (the bind test)
	ln         net.Listener
	done       chan struct{}
	lostErr    error
	closed     bool
}

func newFakeTunnel() *fakeTunnel {
	return &fakeTunnel{done: make(chan struct{})}
}

func (f *fakeTunnel) Listen(addr string) (net.Listener, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listenAddr = addr
	if f.ln == nil {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		f.ln = ln
	}
	return f.ln, nil
}

func (f *fakeTunnel) Done() <-chan struct{} { return f.done }

func (f *fakeTunnel) LostErr() error { return f.lostErr }

func (f *fakeTunnel) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.done)
	}
	return nil
}

// isClosed reports whether the tunnel lease was released.
func (f *fakeTunnel) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// lastListenAddr returns the addr the fake was asked to bind.
func (f *fakeTunnel) lastListenAddr() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listenAddr
}

// refusingTunnel is a TunnelConn whose Listen always refuses — the
// AllowTcpForwarding-off / PermitListen-mismatch wire signal, scripted
// without a hostile sshd.
type refusingTunnel struct{ fakeTunnel }

func (r *refusingTunnel) Listen(addr string) (net.Listener, error) {
	return nil, errors.New("ssh: tcpip-forward denied")
}

// shellConn is one candidate connection speaking the protocol, the way the
// shell's hook would: it connects to the forwarded port and drives the
// handshake and lifecycle over TCP.
type shellConn struct {
	t    *testing.T
	conn net.Conn
	dec  *lifecyclecodec.Decoder
	cfg  Config
}

// dialShell opens a candidate connection to the adapter's port and returns
// the protocol handle. It does NOT send the hello.
func dialShell(t *testing.T, a *Adapter, cfg Config) *shellConn {
	t.Helper()
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(cfg.Port))
	if err != nil {
		t.Fatalf("dial forwarded port: %v", err)
	}
	return &shellConn{t: t, conn: c, dec: lifecyclecodec.NewDecoder(c, lifecyclecodec.Config{}, nil), cfg: cfg}
}

// hello sends the authenticated hello (sequence 1) for the given domain.
func (s *shellConn) hello(domain lifecycle.DomainID, epoch uint64, capHex string, shell string) {
	s.t.Helper()
	env := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       s.cfg.Lane,
		Domain:     domain,
		Epoch:      epoch,
		Sequence:   1,
		Capability: s.capBytes(capHex),
		Event:      lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: shell}},
	}
	if _, err := lifecyclecodec.Encode(s.conn, env); err != nil {
		s.t.Fatalf("write hello: %v", err)
	}
}

// readAccept reads the next frame and asserts it is an accept for the given
// domain.
func (s *shellConn) readAccept(domain lifecycle.DomainID) lifecycle.Envelope {
	s.t.Helper()
	env, err := s.dec.ReadFrame()
	if err != nil {
		s.t.Fatalf("read accept: %v", err)
	}
	if env.Event.Kind != lifecycle.KindAccept {
		s.t.Fatalf("want accept, got %s", env.Event.Kind)
	}
	if env.Domain != domain {
		s.t.Fatalf("accept domain = %s, want %s", env.Domain, domain)
	}
	return env
}

// close closes the candidate connection.
func (s *shellConn) close() { _ = s.conn.Close() }

// establish runs the full handshake: hello → accept.
func establish(t *testing.T, a *Adapter, cfg Config, domain lifecycle.DomainID, epoch uint64, capHex string) *shellConn {
	t.Helper()
	s := dialShell(t, a, cfg)
	s.hello(domain, epoch, capHex, "bash")
	s.readAccept(domain)
	return s
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestBindIsLiteralLoopback pins the transport's bind: the adapter asks the
// remote sshd for the literal 127.0.0.1, never a hostname — a hostname bind
// is resolved by the server and cannot be verified locally
// (internal/ssh/ssh_tunnel.go records this).
func TestBindIsLiteralLoopback(t *testing.T) {
	tunnel := newFakeTunnel()
	k := newTestKernel()
	a, _, err := New(log.NewSlogAdapter(nil), k, tunnel)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()

	addr := tunnel.lastListenAddr()
	if addr != "127.0.0.1:0" {
		t.Fatalf("bind addr = %q, want the literal 127.0.0.1:0", addr)
	}
	if strings.Contains(addr, "localhost") {
		t.Fatalf("bind addr must never be a hostname, got %q", addr)
	}
}

// TestRefusalFallsBackConventional proves a host whose sshd refuses
// forwarding (AllowTcpForwarding off, bind outside PermitListen) surfaces a
// synchronous, undifferentiated refusal: New returns an error, nothing is
// minted, and no diagnostic names the policy.
func TestRefusalFallsBackConventional(t *testing.T) {
	k := newTestKernel()
	_, _, err := New(log.NewSlogAdapter(nil), k, &refusingTunnel{})
	if err == nil {
		t.Fatal("New must fail when the remote sshd refuses the listener")
	}
	if !errors.Is(err, ErrForwardingRefused) {
		t.Fatalf("refusal must be classified ErrForwardingRefused, got %v", err)
	}
	if strings.Contains(err.Error(), "AllowTcpForwarding") || strings.Contains(err.Error(), "PermitListen") {
		t.Fatalf("refusal must not promise a diagnostic naming a policy: %v", err)
	}
	// Nothing was minted: the lane has no domain and the transport is not
	// bound, so the session stays conventional.
	if st, err := k.State("any-lane"); err == nil && st.Lifecycle != lifecycle.LifecycleNative {
		t.Fatalf("no domain may exist after a refused bind, got %+v", st)
	}
}

// TestConfigCarriesPortAndCapability pins what the caller substitutes into
// the integration script text: the allocated port (from the listener, not a
// guess) and the per-epoch capability (hex), plus the addressing names. The
// port and capability are the same mechanism the local path uses — they are
// what NOCX_LIFECYCLE_PORT and @CAP@ become.
func TestConfigCarriesPortAndCapability(t *testing.T) {
	tunnel := newFakeTunnel()
	k := newTestKernel()
	a, cfg, err := New(log.NewSlogAdapter(nil), k, tunnel)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()

	// The port is the one the listener actually allocated.
	if cfg.Port == 0 {
		t.Fatal("config port must be the server-allocated port, not 0")
	}
	// The capability is 64 lowercase hex — the shell's hello must carry it.
	if len(cfg.Capability) != 64 {
		t.Fatalf("capability = %q, want 64 hex chars", cfg.Capability)
	}
	if _, err := hex.DecodeString(cfg.Capability); err != nil {
		t.Fatalf("capability is not hex: %v", err)
	}
	if cfg.Lane == "" || cfg.Domain == "" || cfg.Epoch == 0 {
		t.Fatalf("config must carry the addressing tuple, got %+v", cfg)
	}

	// The capability actually authenticates: a hello carrying it is
	// accepted; the handshake completes over the wire.
	s := establish(t, a, cfg, cfg.Domain, cfg.Epoch, cfg.Capability)
	defer s.close()
	testwait.WaitFor(t, "domain established", func() bool {
		d, ok := k.Domain(cfg.Domain)
		return ok && d.State == lifecycle.DomainEstablished
	})
}

// TestUnauthenticatedCandidateCannotRevokeOrPreempt proves the security
// boundary of the forwarded port: any local user on the remote host can open
// the socket, so a candidate without the capability must be able to do
// nothing — not desynchronize the live domain (garbage), not steal an
// accept, not terminate the session, and a valid command afterwards still
// completes.
func TestUnauthenticatedCandidateCannotRevokeOrPreempt(t *testing.T) {
	tunnel := newFakeTunnel()
	k := newTestKernel()
	a, cfg, err := New(log.NewSlogAdapter(nil), k, tunnel)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()

	// The real shell establishes.
	sh := establish(t, a, cfg, cfg.Domain, cfg.Epoch, cfg.Capability)
	defer sh.close()
	testwait.WaitFor(t, "domain established", func() bool {
		d, ok := k.Domain(cfg.Domain)
		return ok && d.State == lifecycle.DomainEstablished
	})

	// Attacker A: garbage on the forwarded port. The adapter scans it with
	// the codec's budgets but must NOT report a gap for an unauthenticated
	// candidate — a reported gap would desynchronize the live domain.
	attacker, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(cfg.Port))
	if err != nil {
		t.Fatalf("attacker dial: %v", err)
	}
	_, _ = attacker.Write([]byte("\x00\x00\x00\xffgarbage-not-a-frame"))
	// The adapter exposes no candidate-rejected event, and the domain was
	// already established; retain this bounded window for the negative
	// assertion that garbage cannot mutate it.
	time.Sleep(100 * time.Millisecond)
	testwait.WaitFor(t, "domain still established", func() bool {
		d, ok := k.Domain(cfg.Domain)
		return ok && d.State == lifecycle.DomainEstablished
	})
	if st, _ := k.State(cfg.Lane); st.Lifecycle != lifecycle.LifecyclePromptReady {
		t.Fatalf("garbage from an unauthenticated candidate must not desync the live domain, lane = %v", st.Lifecycle)
	}
	_ = attacker.Close()

	// Attacker B: a well-formed hello with the WRONG capability. Rejected as
	// if it never arrived — no accept, no state mutation, and the rejected
	// candidate's connection is closed (it is not the shell).
	wrong := cfg.Capability
	if wrong[0] == '0' {
		wrong = "1" + wrong[1:]
	} else {
		wrong = "0" + wrong[1:]
	}
	bad := dialShell(t, a, cfg)
	bad.hello(cfg.Domain, cfg.Epoch, wrong, "bash")
	// The attacker must never receive an accept.
	_ = bad.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if env, err := bad.dec.ReadFrame(); err == nil {
		t.Fatalf("wrong-capability candidate received an outbound envelope %+v; the capability is the authenticator", env)
	}
	// The attacker's connection is closed by the adapter.
	if _, err := bad.conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("rejected candidate connection must be closed by the adapter")
	}
	testwait.WaitFor(t, "domain untouched", func() bool {
		d, ok := k.Domain(cfg.Domain)
		return ok && d.State == lifecycle.DomainEstablished
	})

	// The live domain still works end to end: a start → complete round trip.
	if _, err := k.SubmitAttempt(cfg.Domain, "echo hi", "/home/dev", "local"); err != nil {
		t.Fatalf("submit after hostile candidates: %v", err)
	}
	att, ok := k.OpenAttempt(cfg.Domain)
	if !ok {
		t.Fatal("open attempt missing")
	}
	sh.start(att.ID, "echo hi")
	sh.complete(att.ID, 0)
	testwait.WaitFor(t, "attempt completed", func() bool {
		got, ok := k.Attempt(att.ID)
		return ok && got.State == lifecycle.AttemptCompleted
	})
}

// TestCandidatesBounded proves the adapter's connection bound: any local
// user can open the forwarded socket, so beyond maxCandidates the adapter
// refuses the connection outright instead of serving it.
func TestCandidatesBounded(t *testing.T) {
	tunnel := newFakeTunnel()
	k := newTestKernel()
	a, cfg, err := New(log.NewSlogAdapter(nil), k, tunnel, WithMaxCandidates(2))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()

	// Two silent candidates fill the bound.
	for range 2 {
		cc, dialErr := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(cfg.Port))
		if dialErr != nil {
			t.Fatalf("candidate dial: %v", dialErr)
		}
		defer func() { _ = cc.Close() }()
	}
	// The third is refused outright: it is closed, so reads end immediately.
	third, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(cfg.Port))
	if err != nil {
		t.Fatalf("third dial: %v", err)
	}
	defer func() { _ = third.Close() }()
	_ = third.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if n, err := third.Read(make([]byte, 1)); err == nil && n != 0 {
		t.Fatal("over-bound connection must be closed by the adapter, not served")
	}
}

// TestTunnelConnDoneRevokesDomainUnknownsAttempt proves the connection-loss
// contract: TunnelConn.Done firing ends every domain on the transport
// (protocol §12) — the domain is Lost and its open attempt becomes unknown,
// NEVER successful and NEVER assigned an exit code.
func TestTunnelConnDoneRevokesDomainUnknownsAttempt(t *testing.T) {
	tunnel := newFakeTunnel()
	k := newTestKernel()
	a, cfg, err := New(log.NewSlogAdapter(nil), k, tunnel)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()

	sh := establish(t, a, cfg, cfg.Domain, cfg.Epoch, cfg.Capability)
	defer sh.close()
	testwait.WaitFor(t, "domain established", func() bool {
		d, ok := k.Domain(cfg.Domain)
		return ok && d.State == lifecycle.DomainEstablished
	})

	// DomainEstablished is NOT the precondition SubmitAttempt needs, and the
	// wait above only reaches that. The kernel sets acceptPending in the same
	// step that takes the lane to PromptReady and clears it only when the
	// accept is DELIVERED to the shell, and requireActive refuses a domain
	// whose accept was minted but never delivered (decision 3/9,
	// ErrDomainPending). The delivery is asynchronous here — a real socket to
	// the fake shell — so on a loaded machine the submit landed inside that
	// window and the test failed instantly with "domain not past accept",
	// while an idle machine never saw it (nocx-8b47).
	//
	// acceptPending is unexported and this package cannot read it, so the wait
	// is on the operation itself, which is the honest observable: submit until
	// the kernel stops refusing. A refusal creates no attempt, so retrying
	// cannot leave a spare one behind.
	var att lifecycle.ExecutionAttempt
	testwait.WaitFor(t, "domain past accept", func() bool {
		a, err := k.SubmitAttempt(cfg.Domain, "sleep 100", "/home/dev", "local")
		if err != nil {
			return false
		}
		att = a
		return true
	})
	if got, _ := k.Attempt(att.ID); got.State != lifecycle.AttemptOpen {
		t.Fatalf("attempt must be open before loss, got %v", got.State)
	}

	// The SSH connection dies. fakeTunnel.Close is the loss signal (Done
	// closes); it is idempotent, so the adapter's own lease release on the
	// loss path is a no-op instead of a double close.
	_ = tunnel.Close()

	testwait.WaitFor(t, "domain lost", func() bool {
		d, ok := k.Domain(cfg.Domain)
		return ok && d.State == lifecycle.DomainLost
	})
	if st, _ := k.State(cfg.Lane); st.Lifecycle != lifecycle.LifecycleLost {
		t.Fatalf("lane must fall to Lost, got %v", st.Lifecycle)
	}
	got, ok := k.Attempt(att.ID)
	if !ok || got.State != lifecycle.AttemptUnknown {
		t.Fatalf("open attempt must become unknown on transport loss, got %+v", got)
	}
	if got.ExitCode != nil {
		t.Fatalf("loss must never assign an exit code, got %v", *got.ExitCode)
	}
}

// TestTunnelConnDoneStopsServing proves the loss path is terminal for the
// adapter itself, not just for the kernel state: after TunnelConn.Done
// closes, Send refuses, the listener is closed (no new candidate can be
// served), and the kernel stays authoritative over the dead domain.
func TestTunnelConnDoneStopsServing(t *testing.T) {
	tunnel := newFakeTunnel()
	k := newTestKernel()
	a, cfg, err := New(log.NewSlogAdapter(nil), k, tunnel)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()
	sh := establish(t, a, cfg, cfg.Domain, cfg.Epoch, cfg.Capability)
	defer sh.close()
	testwait.WaitFor(t, "domain established", func() bool {
		d, ok := k.Domain(cfg.Domain)
		return ok && d.State == lifecycle.DomainEstablished
	})

	_ = tunnel.Close()
	testwait.WaitFor(t, "adapter closed after loss", func() bool {
		return errors.Is(a.Send(lifecycle.Envelope{}), ErrClosed)
	})
	// The listener is gone: a new candidate cannot connect at all. This
	// waits on the dial rather than inferring it from the refusal above:
	// lose() marks the adapter closed under the mutex and closes the
	// listener after releasing it, so Send refuses FIRST and a dial in
	// that window still connects. Asserting one state after waiting for
	// the other is how this read as a product defect on a loaded box.
	testwait.WaitFor(t, "listener closed after loss", func() bool {
		c, derr := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(cfg.Port))
		if derr == nil {
			_ = c.Close()
			return false
		}
		return true
	})
	// The kernel stays authoritative: the domain is lost, and the old
	// capability authenticates nothing further.
	testwait.WaitFor(t, "domain lost", func() bool {
		d, ok := k.Domain(cfg.Domain)
		return ok && d.State == lifecycle.DomainLost
	})
}

// TestCloseRevokesMintedDomainAndReleasesLease proves the session-end
// disposal path (decision 8, "in one local transition"): an explicit Close
// — the caller's failure to spawn the shell, or the session ending — must
// revoke the minted Pending/Established domain in the kernel, stop the
// adapter from serving, and release the tunnel lease. A domain left live
// with no adapter would hold the lane and accept nothing (protocol §5: the
// first authenticated connection claims the epoch; nothing ever would).
func TestCloseRevokesMintedDomainAndReleasesLease(t *testing.T) {
	tunnel := newFakeTunnel()
	k := newTestKernel()
	a, cfg, err := New(log.NewSlogAdapter(nil), k, tunnel)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Close without ever establishing: the domain is still Pending. The
	// disposal path must revoke it even so.
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	testwait.WaitFor(t, "minted domain revoked on close", func() bool {
		d, ok := k.Domain(cfg.Domain)
		return ok && d.State == lifecycle.DomainLost
	})
	if st, _ := k.State(cfg.Lane); st.Lifecycle != lifecycle.LifecycleLost {
		t.Fatalf("lane must fall to Lost on close, got %v", st.Lifecycle)
	}
	// The adapter is closed: Send refuses, and the lease is released.
	if err := a.Send(lifecycle.Envelope{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Send after close = %v, want ErrClosed", err)
	}
	testwait.WaitFor(t, "tunnel lease released", func() bool { return tunnel.isClosed() })
	// A new candidate cannot be served: the listener is gone.
	if c, derr := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(cfg.Port)); derr == nil {
		_ = c.Close()
		t.Fatal("listener must be closed after loss; a dial succeeded")
	}
}

// failRequestKernel wraps a real publisher (the adapter's kernel seam) but
// fails RequestDomain — the mid-New failure that leaves the transport
// already bound with no domain minted (the only kernel mutation New
// performs before the domain exists).
type failRequestKernel struct {
	*lifecyclepub.Publisher
}

func (f *failRequestKernel) RequestDomain(lane lifecycle.LaneID, parent *lifecycle.DomainID, t lifecycle.TransportID) (lifecycle.DomainHandle, error) {
	return lifecycle.DomainHandle{}, errors.New("simulated kernel failure")
}

// TestNewCleanupOnKernelFailureLeavesNoKernelState proves the partial-
// failure invariant (AGENTS.md rule 3): when New fails after BindTransport
// succeeded (RequestDomain refused), no domain is minted, no lane state is
// created, the listener is closed and the tunnel lease is released — a live
// kernel entry with no adapter must not be left behind. The kernel's bound
// transport entry is documented as never unbound (BindTransport binds once);
// the random transport id is unreachable by any later caller.
func TestNewCleanupOnKernelFailureLeavesNoKernelState(t *testing.T) {
	tunnel := newFakeTunnel()
	k := &failRequestKernel{Publisher: newTestKernel()}
	a, cfg, err := New(log.NewSlogAdapter(nil), k, tunnel)
	if a != nil || cfg != (Config{}) {
		t.Fatalf("failed New must return nil adapter and zero config, got %v %+v", a, cfg)
	}
	if err == nil {
		t.Fatal("failed RequestDomain must surface as a New error")
	}

	// No domain and no lane state exist in the kernel: RequestDomain is the
	// only path that mints either, and it failed. Any lane lookup errors.
	if _, serr := k.State("any-lane"); !errors.Is(serr, lifecycle.ErrUnknownLane) {
		t.Fatalf("a failed New must not create lane state, got %v", serr)
	}
	// The listener and the lease are released.
	if !tunnel.isClosed() {
		t.Fatal("failed New must release the tunnel lease")
	}
}

// TestAdapterSeamCannotResume is the structural half of protocol §12's "two
// losses, two code paths": the Kernel seam the adapter drives is the whole
// reachable surface of the kernel from the SSH-loss path, and it must not
// contain ReplayLane — the reconnect-resume entry point. A transport
// adapter can lose a domain; it literally cannot resume one. The resume
// path (publisher.ReplayLane, reached only from the attach handler) and the
// loss path (kernel.TransportLost, reached from the adapters) share no
// method, so a reconnect that resumes is structurally unreachable from the
// SSH-loss path.
func TestAdapterSeamCannotResume(t *testing.T) {
	seam := reflect.TypeOf((*Kernel)(nil)).Elem()
	found := map[string]bool{}
	for i := range seam.NumMethod() {
		found[seam.Method(i).Name] = true
	}
	if found["ReplayLane"] {
		t.Fatal("the adapter Kernel seam must not expose ReplayLane: a reconnect that resumes must be unreachable from the SSH-loss path")
	}
	if !found["TransportLost"] {
		t.Fatal("the adapter Kernel seam must expose TransportLost: loss is the adapter's one job")
	}
	// And the publisher's resume entry point must not be the loss entry:
	// the publisher forwards TransportLost to the kernel and ReplayLane is
	// a separate method; the loss method never calls it.
	pubTyp := reflect.TypeOf((*lifecyclepub.Publisher)(nil))
	if _, ok := pubTyp.MethodByName("ReplayLane"); !ok {
		t.Fatal("publisher must expose ReplayLane for the attach handler")
	}
}

// TestOneLaneSeveralDomainsNoCurrentDomain proves the adapter is a pipe, not
// a policy: it registers one lane, its transport can carry several domains
// (the kernel's registry is the authority), and it exposes no CurrentDomain
// accessor — the future relay is a third adapter, not a protocol rewrite.
func TestOneLaneSeveralDomainsNoCurrentDomain(t *testing.T) {
	tunnel := newFakeTunnel()
	k := newTestKernel()
	a, cfg, err := New(log.NewSlogAdapter(nil), k, tunnel)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()

	// One lane: the kernel registered exactly the adapter's lane.
	if _, stateErr := k.State(cfg.Lane); stateErr != nil {
		t.Fatalf("adapter lane must be registered: %v", stateErr)
	}

	// Several domains on one transport: suspend the root, mint a child on
	// the same transport and lane, and establish it over the wire — the
	// child's hello rides the same candidate channel, its accept routes to
	// the right claimant.
	root := establish(t, a, cfg, cfg.Domain, cfg.Epoch, cfg.Capability)
	defer root.close()
	testwait.WaitFor(t, "root established", func() bool {
		d, ok := k.Domain(cfg.Domain)
		return ok && d.State == lifecycle.DomainEstablished
	})

	root.suspend()
	// Wait for the suspension to be APPLIED, not merely written. The
	// kernel refuses a child whose parent is still live (ErrParentActive,
	// kernel.go), and the suspend travels on the root's connection while
	// the child's hello arrives on its own — two adapter goroutines, no
	// ordering between them. On a many-core machine the suspend won every
	// time; on a CI runner with few cores the child's hello got there
	// first and was rejected, so the test read EOF instead of an accept
	// (nocx-x8ol). Synchronise on the observable state, not on luck.
	testwait.WaitFor(t, "root suspended", func() bool {
		d, ok := k.Domain(cfg.Domain)
		return ok && d.State == lifecycle.DomainSuspended
	})
	child, err := k.RequestDomain(cfg.Lane, &cfg.Domain, a.id)
	if err != nil {
		t.Fatalf("RequestDomain child on the same transport: %v", err)
	}
	childCap := hex.EncodeToString(child.Capability[:])
	childShell := establish(t, a, cfg, child.Domain, child.Epoch, childCap)
	defer childShell.close()
	testwait.WaitFor(t, "child established", func() bool {
		d, ok := k.Domain(child.Domain)
		return ok && d.State == lifecycle.DomainEstablished
	})
	st, _ := k.State(cfg.Lane)
	if len(st.Stack) != 2 {
		t.Fatalf("one transport must carry two domains on one lane, stack = %v", st.Stack)
	}

	// No CurrentDomain accessor anywhere on the adapter.
	typ := reflect.TypeOf(a)
	if m, ok := typ.MethodByName("CurrentDomain"); ok {
		t.Fatalf("adapter must expose no CurrentDomain(), found %v", m)
	}
}

// TestSilentCandidateClosedByHandshakeDeadline proves the handshake bound
// (protocol §5: "Connection count and handshake time are bounded at the
// transport adapter"): a candidate that connects but never proves the
// capability is closed once the hello window elapses — it cannot hold a
// connection slot forever, and its silence never touches the kernel.
func TestSilentCandidateClosedByHandshakeDeadline(t *testing.T) {
	tunnel := newFakeTunnel()
	k := newTestKernel()
	a, cfg, err := New(log.NewSlogAdapter(nil), k, tunnel, WithHelloTimeout(200*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = a.Close() }()

	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(cfg.Port))
	if err != nil {
		t.Fatalf("silent candidate dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	// The candidate sends nothing; the adapter must close it once the
	// handshake deadline passes.
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := c.Read(make([]byte, 1)); err == nil && n != 0 {
		t.Fatal("silent candidate must be closed by the adapter, not served")
	}
	// The domain never established: no hello ever arrived, so the hello
	// timer abandoned it (never Established, never active). A silent
	// candidate cannot mint authority by sitting on the socket.
	d, ok := k.Domain(cfg.Domain)
	if !ok || d.State == lifecycle.DomainEstablished {
		t.Fatalf("silent candidate must leave the domain unestablished, got %+v", d)
	}
}

// shell lifecycle helpers (the wire, from the shell's side).

func (s *shellConn) start(attempt lifecycle.AttemptID, command string) {
	s.t.Helper()
	env := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       s.cfg.Lane,
		Domain:     s.cfg.Domain,
		Epoch:      s.cfg.Epoch,
		Sequence:   2,
		Capability: s.capBytes(s.cfg.Capability),
		Event:      lifecycle.Event{Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &attempt, Command: command}},
	}
	if _, err := lifecyclecodec.Encode(s.conn, env); err != nil {
		s.t.Fatalf("write start: %v", err)
	}
}

func (s *shellConn) complete(attempt lifecycle.AttemptID, code int) {
	s.t.Helper()
	fence := lifecycle.FenceNonce{}
	for i := range fence {
		fence[i] = 0xAB
	}
	env := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       s.cfg.Lane,
		Domain:     s.cfg.Domain,
		Epoch:      s.cfg.Epoch,
		Sequence:   3,
		Capability: s.capBytes(s.cfg.Capability),
		Event: lifecycle.Event{Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{
			AttemptID: &attempt, ExitCode: &code, Fence: fence,
		}},
	}
	if _, err := lifecyclecodec.Encode(s.conn, env); err != nil {
		s.t.Fatalf("write complete: %v", err)
	}
}

func (s *shellConn) suspend() {
	s.t.Helper()
	env := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       s.cfg.Lane,
		Domain:     s.cfg.Domain,
		Epoch:      s.cfg.Epoch,
		Sequence:   2,
		Capability: s.capBytes(s.cfg.Capability),
		Event:      lifecycle.Event{Kind: lifecycle.KindDomainSuspended, DomainSuspended: &lifecycle.DomainSuspendedEvent{}},
	}
	if _, err := lifecyclecodec.Encode(s.conn, env); err != nil {
		s.t.Fatalf("write suspend: %v", err)
	}
}

func (s *shellConn) capBytes(capHex string) lifecycle.Capability {
	s.t.Helper()
	b, err := hex.DecodeString(capHex)
	if err != nil {
		s.t.Fatalf("capability hex: %v", err)
	}
	var c lifecycle.Capability
	copy(c[:], b)
	return c
}
