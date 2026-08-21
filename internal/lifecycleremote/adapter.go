// Package lifecycleremote implements the remote transport of the
// authenticated lifecycle protocol (docs/lifecycle-protocol.md §1, "Remote";
// ADR-0024 decision 2 "Over SSH"): the backend asks the remote sshd for a
// loopback listener via TunnelConn.Listen (the -R strategy on the pooled
// connection AD-5 multiplexes), the shell's hook connects to that port
// (bash: exec {fd}<>/dev/tcp/127.0.0.1/<port>), and envelopes come back over
// the same SSH connection the session already uses. Nothing is installed on
// the remote host.
//
// The adapter is a pipe, not a policy — the sibling of
// internal/lifecyclechannel (which carries the local descriptor). Same
// lifecycle.Port contract, different pipe. It mints one lane and one Pending
// domain on the kernel, frames inbound bytes with the shared codec, delivers
// every mapped envelope to Kernel.Ingest, and reports loss to
// Kernel.TransportLost. It has no CurrentDomain accessor and assumes nothing
// about how many domains its transport carries: outbound routing is keyed by
// the envelope's own domain (the kernel's registry is the authority — the
// future relay is a third adapter, not a protocol rewrite).
//
// The port is not the authenticator; the capability is. Any local user on
// the remote host can open the forwarded socket, so candidate connections
// are bounded and each must prove the domain's per-epoch capability before
// it can deliver an accepted event, report a gap or receive an outbound
// envelope (protocol §4). The bind is the literal 127.0.0.1, never a
// hostname: a hostname bind is resolved by the server and cannot be verified
// locally (internal/ssh/ssh_tunnel.go records this).
//
// Refusal — AllowTcpForwarding off, or a bind outside PermitListen — is
// detectable synchronously (New returns an error and the caller spawns the
// shell without a channel) but is NOT distinguishable: the adapter promises
// no diagnostic naming a policy, only a conventional terminal with a visible
// native prompt (ADR-0024 decision 4).
//
// The launch configuration the caller must embed — port and capability —
// is described in the Config doc: only the capability is never exported;
// the port travels as the non-secret NOCX_LIFECYCLE_PORT name, exactly as
// the local path's launch block exports its non-secret names.
package lifecycleremote

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/log"
)

// ErrClosed is returned by Send once the adapter has been closed or lost.
var ErrClosed = errors.New("lifecycleremote: adapter closed")

// ErrForwardingRefused is returned by New when the remote sshd refused the
// forwarded listener. Detectable synchronously, NOT distinguishable: a
// refused bind (AllowTcpForwarding off), a bind outside PermitListen and a
// server-side failure all arrive as the same request failure, and the
// adapter promises no diagnostic naming a policy.
var ErrForwardingRefused = errors.New("lifecycleremote: remote forwarding refused")

// writeTimeout bounds one outbound envelope write. The kernel's outbound
// sends are best-effort (the shell times out its handshake and the session
// stays conventional — the safe direction), so a shell that has stopped
// reading must never wedge the kernel's flush.
const writeTimeout = 5 * time.Second

// bindAddr is the literal loopback bind the adapter requests on the remote
// sshd. Never a hostname: a hostname bind is resolved by the server and
// cannot be verified locally (internal/ssh/ssh_tunnel.go). Port 0 asks the
// server to allocate; the allocated port is read from the listener's Addr.
const bindAddr = "127.0.0.1:0"

// defaultMaxCandidates bounds how many candidate connections the adapter
// serves at once. Any local user on the remote host can open the forwarded
// socket; the bound keeps a connection flood from exhausting the adapter,
// and the capability — not the port — is what authenticates (protocol §4).
const defaultMaxCandidates = 8

// DefaultMaxCandidates is the default bound on concurrent candidate
// connections (see WithMaxCandidates). Exported so the composition root can
// name the bound it chooses explicitly — the product decision belongs there
// (same reason the local path's hello timeout is set at the composition
// root).
const DefaultMaxCandidates = defaultMaxCandidates

// Kernel is the slice of the lifecycle kernel the adapter drives. The
// concrete *lifecycle.Kernel (or the lifecyclepub.Publisher wrapping it)
// satisfies it; the seam exists so the adapter is testable and the
// composition root decides the kernel. It is the same seam
// lifecyclechannel.Kernel declares; the publisher satisfies both.
type Kernel interface {
	BindTransport(t lifecycle.TransportID, port lifecycle.Port) error
	RequestDomain(lane lifecycle.LaneID, parent *lifecycle.DomainID, t lifecycle.TransportID) (lifecycle.DomainHandle, error)
	Ingest(t lifecycle.TransportID, env lifecycle.Envelope) error
	NotifyGap(t lifecycle.TransportID, d lifecycle.DomainID, garbageBytes, garbageFrames int) error
	TransportLost(t lifecycle.TransportID) error
	Domain(id lifecycle.DomainID) (lifecycle.Domain, bool)
}

// TunnelConn is the lease surface the adapter drives. The concrete value
// comes from internal/ssh (RealClient.TunnelConn, the -R seam AD-5
// multiplexes); the interface exists so the adapter is testable without a
// live connection, the same reason lifecyclechannel declares its Kernel
// seam. Only the slice the adapter needs is declared: Listen (the remote
// listener), Done/LostErr (the connection-loss contract) and Close (lease
// release).
type TunnelConn interface {
	Listen(addr string) (net.Listener, error)
	Done() <-chan struct{}
	LostErr() error
	Close() error
}

// Config is what the caller substitutes into the integration script text —
// the same mechanism the local path uses (shellintegration.LaunchOptions):
// lane, domain and epoch are names (NOCX_LIFECYCLE_* env); the port is the
// loopback port the shell connects to (NOCX_LIFECYCLE_PORT); the capability
// is the per-epoch bearer (@CAP@ in the rcfile text). Only the capability is
// never exported to the environment: it rides the script text, and a value
// in /proc/<pid>/environ would leak the authenticator to every child. The
// port is a name, not a secret, and is not the authenticator.
type Config struct {
	Lane       lifecycle.LaneID
	Domain     lifecycle.DomainID
	Epoch      uint64
	Port       int
	Capability string // 64 lowercase hex chars
	Recovery   string // 64 lowercase hex chars; the one-shot recovery fence
}

// LossCause names which of the adapter's loss paths fired, and it is the
// carrier design's §6.2 vocabulary rather than a list of this file's `return`
// statements.
//
// §6.2 insists the events are DETECTED SEPARATELY, and the reason is that they
// mean different things to a user: a listener that went away is nocx's own
// channel disappearing, a transport that died is the SSH connection dying and
// takes the session with it, and a speaker that stopped speaking is the shell.
// Collapsing them produces one sentence — "not integrated, cannot say why" —
// for three situations with three different answers.
//
// Two of §6.2's rows are NOT produced here and are named anyway, because the
// vocabulary is the design's and not this package's: the multiplex master's
// socket file going and the master process dying belong to the typed-`ssh`
// wrapper, which owns the master. They are declared so that side reports into
// one table rather than inventing a second (AD-8).
//
// The FIRST cause wins, which is the one that actually ended the channel: lose
// is idempotent and is reached from several paths at once when a connection
// dies, and the later ones are consequences.
type LossCause string

const (
	// LossHelloTimeout is the handshake bound expiring (protocol §5): the
	// transport was established and no acceptable hello arrived inside the
	// window. §6.2's second row — after the channel exists, before
	// integration is live.
	LossHelloTimeout LossCause = "hello-timeout"
	// LossEndOfStream is the speaker closing its end.
	LossEndOfStream LossCause = "end-of-stream"
	// LossReadError is the candidate's stream breaking under the pump.
	LossReadError LossCause = "read-error"
	// LossListenerGone is the remote listener going away while the adapter
	// still wanted it: nocx's own channel to the shell is gone, and nothing
	// on the far host can reach it any more.
	LossListenerGone LossCause = "listener-gone"
	// LossTransportGone is the underlying SSH transport dying. §6.2 is
	// explicit about what follows and it is not a degrade to conventional:
	// LOSING THE UNDERLYING TRANSPORT ENDS THE SESSION. There is no prompt
	// to keep, and claiming otherwise would be an outcome we cannot deliver.
	LossTransportGone LossCause = "transport-gone"
	// LossMasterSocketGone is §6.2's first distinct event — the multiplex
	// socket file going. Produced by the owner of the master, never here.
	LossMasterSocketGone LossCause = "master-socket-gone"
	// LossMasterExited is §6.2's second — the master process dying.
	// Produced by the owner of the master, never here.
	LossMasterExited LossCause = "master-exited"
	// LossClosed is the session's own disposal path. Not a failure: the
	// session is going away and the product has nothing to say about it.
	LossClosed LossCause = "closed"
)

// LossReporter is told which path ended one adapter's transport, keyed by the
// adapter's own lane.
//
// It exists because the kernel cannot answer this, and on the REMOTE path the
// consequence was worse than on the local one: a remote session whose shell
// never spoke established no domain, so the lane's projection never moved, so
// the publisher announced nothing — and with no reporter here either, the
// session's integration axis stayed at `starting` for the life of the tab.
// §7 forbids exactly that ("`starting` can never be permanent"), and nothing
// enforced it on this path until the reporter existed.
type LossReporter func(lane lifecycle.LaneID, cause LossCause)

// Option configures an Adapter.
type Option func(*options)

type options struct {
	helloTimeout  time.Duration
	maxCandidates int
	lossReporter  LossReporter
}

// WithLossReporter registers the sink for this adapter's loss cause. Nil (the
// default) reports nowhere and the loss is still logged.
func WithLossReporter(r LossReporter) Option {
	return func(o *options) { o.lossReporter = r }
}

// WithHelloTimeout bounds the handshake: unless an authenticated hello is
// accepted within the window, the domain is abandoned (TransportLost) and
// the session stays conventional (protocol §5). Zero uses
// lifecycle.HelloTimeout. Test-only in practice; the default is the
// protocol constant.
func WithHelloTimeout(d time.Duration) Option {
	return func(o *options) { o.helloTimeout = d }
}

// WithMaxCandidates bounds the number of candidate connections served
// concurrently. Beyond the bound, new connections are closed immediately.
// Defaults to defaultMaxCandidates.
func WithMaxCandidates(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.maxCandidates = n
		}
	}
}

// Adapter is one remote forwarded-port transport. It implements
// lifecycle.Port (the outbound half the kernel sends accept and
// refresh_request over) and drives the inbound half through the kernel.
type Adapter struct {
	log        log.Logger
	kernel     Kernel
	id         lifecycle.TransportID
	lane       lifecycle.LaneID
	domain     lifecycle.DomainID
	epoch      uint64
	capability lifecycle.Capability
	recovery   lifecycle.FenceNonce
	tc         TunnelConn
	ln         net.Listener
	port       int

	helloTimeout  time.Duration
	maxCandidates int
	report        LossReporter

	// slots bounds concurrent candidate connections (the accept loop parks
	// one token per served connection).
	slots chan struct{}

	// helloMu serializes hello ingestion: only one candidate may be inside
	// the claim → Ingest → settle window at a time. The kernel answers an
	// accepted hello with a synchronous accept (flushed inside Ingest), so
	// this serialization is what guarantees the accept routes to the
	// connection whose hello was accepted — a concurrent hostile claim
	// cannot steal it. Non-hello frames do not take helloMu.
	helloMu sync.Mutex

	mu sync.Mutex
	// claim maps a domain to the connection currently attempting its
	// handshake (set before Ingest, settled after). Send(accept) routes
	// here; Send(refresh_request) never does — an unauthenticated claimant
	// must not receive an outbound envelope it cannot answer.
	claim map[lifecycle.DomainID]net.Conn
	// speakers maps a domain to the connection that owns it: the one whose
	// authenticated hello the kernel accepted. Send routes outbound here.
	speakers map[lifecycle.DomainID]net.Conn
	// conns tracks every live candidate connection so lose() can close them
	// all at once.
	conns map[net.Conn]struct{}

	closed bool
	loss   sync.Once
	timer  *time.Timer
}

// New establishes the remote transport: ask the remote sshd for a loopback
// listener (the literal 127.0.0.1), bind a transport and mint the domain on
// the kernel, and start serving candidates. The returned Config carries
// what the caller must substitute into the integration script text — port
// and capability included (see the Config doc for which of the two is
// exportable).
//
// Failure to establish the transport leaves the session conventional: New
// returns an error and the caller spawns the shell without a channel. A
// refused bind is ErrForwardingRefused, detectable synchronously and NOT
// distinguishable — no diagnostic names a policy.
func New(log log.Logger, k Kernel, tc TunnelConn, opts ...Option) (*Adapter, Config, error) {
	o := options{helloTimeout: lifecycle.HelloTimeout, maxCandidates: defaultMaxCandidates}
	for _, opt := range opts {
		opt(&o)
	}

	ln, err := tc.Listen(bindAddr)
	if err != nil {
		return nil, Config{}, fmt.Errorf("%w: %v", ErrForwardingRefused, err)
	}
	port := portOf(ln.Addr())

	tptHex, err := randHex(8)
	if err != nil {
		_ = ln.Close()
		return nil, Config{}, err
	}
	laneHex, err := randHex(8)
	if err != nil {
		_ = ln.Close()
		return nil, Config{}, err
	}
	a := &Adapter{
		log:           log,
		kernel:        k,
		id:            lifecycle.TransportID("tpt-" + tptHex),
		lane:          lifecycle.LaneID("lane-" + laneHex),
		tc:            tc,
		ln:            ln,
		port:          port,
		helloTimeout:  o.helloTimeout,
		maxCandidates: o.maxCandidates,
		report:        o.lossReporter,
		slots:         make(chan struct{}, o.maxCandidates),
		claim:         map[lifecycle.DomainID]net.Conn{},
		speakers:      map[lifecycle.DomainID]net.Conn{},
		conns:         map[net.Conn]struct{}{},
	}

	cleanup := func() {
		_ = ln.Close()
		_ = tc.Close()
	}
	if berr := k.BindTransport(a.id, a); berr != nil {
		cleanup()
		return nil, Config{}, fmt.Errorf("bind lifecycle transport: %w", berr)
	}
	h, err := k.RequestDomain(a.lane, nil, a.id)
	if err != nil {
		cleanup()
		return nil, Config{}, fmt.Errorf("request lifecycle domain: %w", err)
	}
	a.domain = h.Domain
	a.epoch = h.Epoch
	a.capability = h.Capability
	a.recovery = h.Recovery
	log.Info("lifecycle remote channel established",
		"transport", a.id, "lane", a.lane, "domain", h.Domain, "epoch", h.Epoch, "port", port)

	// The timer may fire before New returns (a short hello timeout), so the
	// field is stored under the same mutex stopHelloTimer reads: the
	// callback's read is then ordered against this write and never races.
	t := time.AfterFunc(a.helloTimeout, func() { a.lose(LossHelloTimeout) })
	a.mu.Lock()
	a.timer = t
	a.mu.Unlock()

	go a.acceptLoop()
	go a.watchLoss()
	return a, Config{
		Lane:       a.lane,
		Domain:     a.domain,
		Epoch:      a.epoch,
		Port:       port,
		Capability: hex.EncodeToString(a.capability[:]),
		Recovery:   hex.EncodeToString(a.recovery[:]),
	}, nil
}

// portOf extracts the allocated port from a remote listener's Addr. The
// tunnel doc promises the listener reports the address the server actually
// allocated (a requested port 0 is resolved by the server), so a
// non-TCP addr is a programming error, not a runtime possibility.
func portOf(addr net.Addr) int {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp == nil {
		panic(fmt.Sprintf("lifecycleremote: remote listener addr is not *net.TCPAddr: %T %v", addr, addr))
	}
	return tcp.Port
}

// Send implements lifecycle.Port: it routes one outbound envelope (accept,
// refresh_request, domain_grant — the three kinds the kernel sends) to the
// connection that owns the addressed domain. accept routes to the handshake
// claimant (its hello was accepted); refresh_request routes to the
// authenticated speaker; a domain_grant is addressed to the PARENT and
// routes to its speaker. Failures are best-effort: the kernel ignores them
// and the shell times out its handshake in the safe direction.
// TransportID returns the adapter's own transport id, for the composition
// root's transport-kind registry (the grant builder needs to know whether a
// parent's domains ride the inherited descriptor or a forwarded port).
func (a *Adapter) TransportID() lifecycle.TransportID {
	return a.id
}

func (a *Adapter) Send(env lifecycle.Envelope) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return ErrClosed
	}
	var target net.Conn
	if env.Event.Kind == lifecycle.KindAccept {
		// The accept answers the hello the kernel accepted: it must go to
		// the claimant — the connection whose hello is in flight — even
		// when the domain already has a speaker (a reconnect within the
		// epoch: the fresh accept belongs to the NEW connection, and the
		// kernel supersedes the old speaker). The accept is NOT flushed
		// synchronously with Ingest anymore (decision 9: it is gated on
		// the renderer's acknowledgement), so the claim stays until this
		// send settles it.
		target = a.claim[env.Domain]
	}
	if target == nil {
		target = a.speakers[env.Domain]
	}
	a.mu.Unlock()
	if target == nil {
		// accept with no claimant or refresh_request with no speaker: the
		// envelope has nowhere to go. Best-effort drop — the shell times
		// out in the safe direction.
		a.log.Debug("lifecycle outbound dropped: no speaker",
			"domain", env.Domain, "kind", env.Event.Kind)
		return nil
	}
	_ = target.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := lifecyclecodec.Encode(target, env); err != nil {
		a.log.Debug("lifecycle outbound send failed", "kind", env.Event.Kind, "error", err)
		return err
	}
	if env.Event.Kind == lifecycle.KindAccept {
		// The accept reached its claimant: settle the handshake — the
		// connection becomes the speaker, the claim closes, the
		// connection's handshake bound clears, and the adapter's hello
		// bound stops. All of it only once the accept is actually
		// flushed (decision 9).
		a.mu.Lock()
		if a.claim[env.Domain] == target {
			a.speakers[env.Domain] = target
			delete(a.claim, env.Domain)
		}
		a.mu.Unlock()
		_ = target.SetReadDeadline(time.Time{})
		a.stopHelloTimer()
	}
	return nil
}

// Close tears the transport down: the domain ends (TransportLost), the hello
// timer stops, the listener and every candidate connection close, and the
// tunnel lease is released. It is the session-end disposal path, and it is also
// the HARD INVALIDATION of design §5.3's validity interval — a bootstrap
// refusal or timeout closes the handle, the domain ends, and a frame of that
// epoch is rejected from then on.
//
// Teardown leaves nothing running, and every step of it is bounded: the accept
// loop ends when the listener closes, each candidate goroutine ends when its
// connection closes, the hello timer is stopped rather than left to fire into
// a dead adapter, and the lease is released. It is idempotent under concurrent
// callers, which it has to be — the refusal path, the session's own disposal
// and the connection dying all reach it, sometimes at once.
func (a *Adapter) Close() error {
	a.lose(LossClosed)
	return nil
}

// LoseForTest drives one loss path from a test, so §6.2's rows are asserted
// against the adapter's own vocabulary rather than against a copy of it.
func (a *Adapter) LoseForTest(cause LossCause) { a.lose(cause) }

// lose is the single loss path, executed once: notify the kernel, mark the
// adapter closed, stop accepting, close every live connection and release
// the tunnel lease. Idempotent under concurrent callers (pump EOF, hello
// timeout, Done watcher, explicit Close).
func (a *Adapter) lose(cause LossCause) {
	a.loss.Do(func() {
		a.stopHelloTimer()
		// Reported BEFORE the kernel's TransportLost, so a consumer that
		// also watches published facts has the cause in hand by the time
		// one arrives.
		if a.report != nil {
			a.report(a.lane, cause)
		}
		a.log.Info("lifecycle remote transport lost", "transport", a.id, "lane", a.lane, "cause", cause)
		if err := a.kernel.TransportLost(a.id); err != nil {
			a.log.Warn("lifecycle transport lost notification failed", "error", err)
		}
		a.mu.Lock()
		a.closed = true
		conns := make([]net.Conn, 0, len(a.conns))
		for c := range a.conns {
			conns = append(conns, c)
		}
		a.conns = map[net.Conn]struct{}{}
		a.mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
		_ = a.ln.Close()
		_ = a.tc.Close()
	})
}

func (a *Adapter) stopHelloTimer() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
}

// watchLoss reports transport loss: TunnelConn.Done closes when the
// underlying SSH connection shuts down. Every domain bound to the transport
// is lost (protocol §12) — open attempts become unknown, never successful —
// and the adapter stops serving. The lease releases its own pooled reference
// on loss; lose()'s tc.Close is a no-op after it.
func (a *Adapter) watchLoss() {
	<-a.tc.Done()
	a.log.Info("lifecycle remote underlying transport gone", "transport", a.id, "error", a.tc.LostErr())
	a.lose(LossTransportGone)
}

// acceptLoop serves candidate connections from the remote listener until the
// adapter closes. Each candidate is bounded: at most maxCandidates are
// served concurrently, and an over-bound connection is refused outright —
// any local user on the remote host can open this socket, and the capability
// is the authenticator, so the bound is the only thing a flood can consume.
func (a *Adapter) acceptLoop() {
	for {
		c, err := a.ln.Accept()
		if err != nil {
			// Two different events reach here and §6.2 requires them
			// apart: the listener closing because WE closed it (lose has
			// already run and owns the cause), and the listener going away
			// while the adapter still wanted it — nocx's own channel to
			// the shell disappearing. lose is idempotent and first-cause-
			// wins, so reporting here cannot overwrite a real cause.
			a.lose(LossListenerGone)
			return
		}
		select {
		case a.slots <- struct{}{}:
		default:
			a.log.Debug("lifecycle remote candidate over bound; refusing", "max", a.maxCandidates)
			_ = c.Close()
			continue
		}
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			<-a.slots
			_ = c.Close()
			return
		}
		a.conns[c] = struct{}{}
		a.mu.Unlock()
		go a.serveCandidate(c)
	}
}

// serveCandidate pumps one candidate connection into the kernel until the
// stream ends or the candidate is rejected. It is the remote analog of the
// local adapter's pump: read frames, deliver authenticated envelopes to the
// kernel, report the handshake and the end-of-stream policy.
//
// An unauthenticated candidate can neither revoke nor preempt a live domain:
// its gap sink reports nothing (garbage is scanned with the codec's budgets
// and discarded), its hello is validated by the kernel before any state is
// touched, and outbound envelopes never route to it except the accept for
// its own accepted hello.
func (a *Adapter) serveCandidate(c net.Conn) {
	defer func() {
		<-a.slots
		a.mu.Lock()
		delete(a.conns, c)
		// A claimant that ended without its accept (handshake bound, close)
		// must not leave a stale claim behind: a later Send(accept) would
		// route to a dead connection and never settle.
		for d, claimant := range a.claim {
			if claimant == c {
				delete(a.claim, d)
			}
		}
		a.mu.Unlock()
		_ = c.Close()
	}()

	// Handshake bound: a candidate that cannot prove the capability within
	// the window is closed. The bound is cleared once the hello is accepted.
	_ = c.SetReadDeadline(time.Now().Add(a.helloTimeout))

	dec := lifecyclecodec.NewDecoder(c, lifecyclecodec.Config{}, a.gapSink(c))
	for {
		env, err := dec.ReadFrame()
		if err == nil {
			if env.Event.Kind == lifecycle.KindHello {
				// Serialize hello ingestion: the claim must be unambiguous
				// while Ingest runs. The accept is NOT flushed inside
				// Ingest anymore (decision 9: it is gated on the renderer's
				// acknowledgement), so helloMu covers only the claim →
				// Ingest window — never a wait on the renderer — and the
				// claim settles in Send when the accept is actually
				// flushed. The handshake bound (read deadline) stays until
				// then, so an unacknowledged candidate cannot claim forever.
				a.helloMu.Lock()
				a.mu.Lock()
				a.claim[env.Domain] = c
				a.mu.Unlock()
				ierr := a.kernel.Ingest(a.id, env)
				a.mu.Lock()
				accepted := ierr == nil
				if !accepted && a.claim[env.Domain] == c {
					// Rejected candidate: unauthenticated, can neither
					// mutate nor preempt. Drop the claim and close it.
					delete(a.claim, env.Domain)
				}
				a.mu.Unlock()
				a.helloMu.Unlock()
				if accepted {
					continue // the accept comes through Send once acknowledged
				}
				a.log.Debug("lifecycle hello rejected",
					"domain", env.Domain, "error", ierr)
				return
			}
			if ierr := a.kernel.Ingest(a.id, env); ierr != nil {
				// Quarantine (a Desynchronized domain), a rejected event,
				// an illegal kind: the kernel mutates nothing and this
				// adapter records nothing but the fact.
				a.log.Debug("lifecycle envelope rejected",
					"domain", env.Domain, "kind", env.Event.Kind, "error", ierr)
				continue
			}
			continue
		}
		switch {
		case errors.Is(err, io.EOF):
			// The candidate closed its end. A clean exit sends
			// domain_closed first; the kernel's read model is the authority
			// on whether the domain ended.
			a.endOfStream(c)
			return
		case errors.Is(err, lifecyclecodec.ErrScanBudgetExhausted):
			// The kernel revoked the domain (the final gap report crossed a
			// budget) — or an unauthenticated candidate exhausted the scan
			// budgets on its own stream. Drain so the sender never blocks on
			// a full buffer; the end-of-stream policy applies when it closes.
			_, _ = io.Copy(io.Discard, c)
			a.endOfStream(c)
			return
		default:
			// A read error (including the handshake deadline): the stream
			// broke. An unauthenticated candidate simply goes away; a
			// speaker that died is end-of-stream.
			if a.isSpeaker(c) {
				a.endOfStream(c)
			}
			return
		}
	}
}

// isSpeaker reports whether c currently owns a domain on this transport.
func (a *Adapter) isSpeaker(c net.Conn) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, owner := range a.speakers {
		if owner == c {
			return true
		}
	}
	return false
}

// domainOf returns the domain c owns, or "" when it owns none.
func (a *Adapter) domainOf(c net.Conn) lifecycle.DomainID {
	a.mu.Lock()
	defer a.mu.Unlock()
	for d, owner := range a.speakers {
		if owner == c {
			return d
		}
	}
	return ""
}

// gapSink is the codec's gap sink for one candidate connection. A gap is
// reported to the kernel ONLY when this connection is the authenticated
// speaker of a domain: garbage from an unauthenticated candidate must never
// desynchronize a live domain — that would be a preemption, and the budgets
// it would charge belong to nobody. The codec's own scan budgets still bound
// how much garbage an unauthenticated candidate can make the adapter scan.
func (a *Adapter) gapSink(c net.Conn) lifecyclecodec.GapSink {
	return func(bytes, frames int) {
		d := a.domainOf(c)
		if d == "" {
			return // unauthenticated candidate garbage: discarded, not reported
		}
		if err := a.kernel.NotifyGap(a.id, d, bytes, frames); err != nil {
			a.log.Debug("lifecycle gap notification rejected",
				"domain", d, "bytes", bytes, "frames", frames, "error", err)
		}
	}
}

// endOfStream applies the end-of-stream policy for one connection: a domain
// whose speaker closed cleanly (domain_closed, or a superseded reconnect)
// ends cleanly; a domain still live whose speaker vanished without saying
// goodbye lost its voice, so the kernel marks it Lost and its open attempts
// unknown — never successful (protocol §12).
func (a *Adapter) endOfStream(c net.Conn) {
	d := a.domainOf(c)
	if d == "" {
		// A candidate or a superseded connection ended: nothing to do — the
		// domain's fate belongs to its current speaker.
		return
	}
	dom, ok := a.kernel.Domain(d)
	if ok {
		switch dom.State {
		case lifecycle.DomainClosed, lifecycle.DomainLost:
			a.log.Info("lifecycle transport ended cleanly", "domain", d)
			return
		}
	}
	a.log.Info("lifecycle transport ended with a live domain; marking lost", "domain", d)
	a.lose(LossEndOfStream)
}

// randHex mints this adapter's transport and lane IDENTIFIERS. Neither is an
// authenticator, and a zero value lets nobody in — but every adapter would
// then carry the SAME transport id and the SAME lane id, and the kernel tells
// one transport's domains from another's by exactly that value
// (ErrWrongTransport is an equality on it). Two remote sessions would share a
// lane. So a failed read is an error here too, refused at construction where
// the caller already has an error to return (nocx-s16k8).
var randReader io.Reader = rand.Reader

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(randReader, b); err != nil {
		return "", fmt.Errorf("lifecycleremote: the randomness source failed; no identifier was minted: %w", err)
	}
	return hex.EncodeToString(b), nil
}
