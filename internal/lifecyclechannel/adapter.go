// Package lifecyclechannel implements the local descriptor transport of the
// authenticated lifecycle protocol (docs/lifecycle-protocol.md; ADR-0024
// decision 2): a socketpair whose child end the shell inherits as fd 3 via
// exec.Cmd.ExtraFiles, and an adapter that pumps envelopes between that
// descriptor and the kernel.
//
// The adapter is a pipe, not a policy. It mints one lane and one Pending
// domain on the kernel, frames inbound bytes with the shared codec, delivers
// every mapped envelope to Kernel.Ingest, forwards every skipped garbage
// region to Kernel.NotifyGap, and reports loss to Kernel.TransportLost. It
// has no CurrentDomain accessor and assumes nothing about how many domains a
// transport carries — the kernel's registry is the authority (the future
// relay is a third adapter, not a protocol rewrite). The shell owns the
// event stream; the adapter never synthesizes an event.
//
// The descriptor is deliberately not private: bash's {var} redirection is
// not close-on-exec, so descendants inherit fd 3 (ADR-0024 decision 2,
// measured). That is survivable only because every frame must carry the
// epoch's capability, which the kernel verifies before consulting any state.
// A shell that execs another shell therefore needs no adapter action: the
// new image keeps speaking for the same domain (same capability, same
// epoch), and a re-hello within the epoch is a reconnect the kernel accepts
// (protocol §5).
package lifecyclechannel

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/log"
)

// ErrClosed is returned by Send once the adapter has been closed or lost.
var ErrClosed = errors.New("lifecyclechannel: adapter closed")

// writeTimeout bounds one outbound envelope write. The kernel's outbound
// sends are best-effort (the shell times out its handshake and the session
// stays conventional — the safe direction), so a shell that has stopped
// reading must never wedge the kernel's flush.
const writeTimeout = 5 * time.Second

// Kernel is the slice of the lifecycle kernel the adapter drives. The
// concrete *lifecycle.Kernel satisfies it; the seam exists so the adapter is
// testable and the composition root decides the kernel.
type Kernel interface {
	BindTransport(t lifecycle.TransportID, port lifecycle.Port) error
	RequestDomain(lane lifecycle.LaneID, parent *lifecycle.DomainID, t lifecycle.TransportID) (lifecycle.DomainHandle, error)
	Ingest(t lifecycle.TransportID, env lifecycle.Envelope) error
	NotifyGap(t lifecycle.TransportID, d lifecycle.DomainID, garbageBytes, garbageFrames int) error
	TransportLost(t lifecycle.TransportID) error
	Domain(id lifecycle.DomainID) (lifecycle.Domain, bool)
}

// LossCause names which of the adapter's three loss paths fired. lose is
// idempotent and is reached from the hello timer, the pump's end of stream,
// the pump's read error and the session's own Close, and until nocx-dvql it
// recorded none of them: a fully degraded session produced no diagnostic
// line anywhere, and the only line that did appear — endOfStream's "ended
// cleanly" — says the opposite of what happened, because it treats a domain
// lose() already marked Lost as a clean end on the strength of a report
// lose() never made.
type LossCause string

const (
	// LossHelloTimeout is the handshake bound expiring (protocol §5): the
	// transport was established and no acceptable hello arrived inside the
	// window. The session stays conventional, and this is the reason the
	// product shows for it.
	LossHelloTimeout LossCause = "hello-timeout"
	// LossEndOfStream is the shell closing its end of the descriptor.
	LossEndOfStream LossCause = "end-of-stream"
	// LossReadError is the descriptor breaking under the pump.
	LossReadError LossCause = "read-error"
	// LossClosed is the session's own disposal path — Close, from the pty
	// teardown. Not a failure: the session is going away and the product
	// has nothing to say about it.
	LossClosed LossCause = "closed"
)

// LossReporter is told which path ended one adapter's transport, keyed by the
// adapter's own lane. It exists because the kernel cannot answer this: a
// handshake that times out never establishes a domain, so the lane's
// projection never changes and no lifecycle fact is published at all — the
// publisher deliberately announces only lanes whose projection moved. The
// cause is transport knowledge (ADR-0024 decision 8: the two losses "must not
// share a code path"), and the composition root routes it to the session
// integration status the product renders. Reported before the kernel's
// TransportLost, so a consumer that also watches published facts has the
// cause in hand by the time one arrives.
type LossReporter func(lane lifecycle.LaneID, cause LossCause)

// Option configures an Adapter.
type Option func(*options)

type options struct {
	helloTimeout time.Duration
	lossReporter LossReporter
}

// WithHelloTimeout bounds the handshake: unless an authenticated hello is
// accepted within the window, the domain is abandoned (TransportLost) and
// the session stays conventional (protocol §5). Zero uses
// lifecycle.HelloTimeout. Test-only in practice; the default is the
// protocol constant.
func WithHelloTimeout(d time.Duration) Option {
	return func(o *options) { o.helloTimeout = d }
}

// WithLossReporter registers the sink for this adapter's loss cause. Nil (the
// default) reports nowhere and the loss is still logged.
func WithLossReporter(r LossReporter) Option {
	return func(o *options) { o.lossReporter = r }
}

// Adapter is one local descriptor transport. It implements lifecycle.Port
// (the outbound half the kernel sends accept and refresh_request over) and
// drives the inbound half through the kernel.
type Adapter struct {
	log        log.Logger
	kernel     Kernel
	id         lifecycle.TransportID
	lane       lifecycle.LaneID
	domain     lifecycle.DomainID
	epoch      uint64
	capability lifecycle.Capability
	recovery   lifecycle.FenceNonce // one-shot recovery fence
	conn       *os.File             // parent end of the socketpair
	dec        *lifecyclecodec.Decoder

	helloTimeout time.Duration
	report       LossReporter

	mu     sync.Mutex
	closed bool
	loss   sync.Once
	timer  *time.Timer
}

// arrives).
//
// Failure to establish the transport leaves the session conventional: New
// returns an error and the caller spawns the shell without a channel.
func New(log log.Logger, k Kernel, opts ...Option) (*Adapter, *os.File, error) {
	o := options{helloTimeout: lifecycle.HelloTimeout}
	for _, opt := range opts {
		opt(&o)
	}

	// Per-OS: Linux sets close-on-exec atomically, everything else closes the
	// same window with ForkLock (socketpair_linux.go / socketpair_other.go).
	// The constant this used to name inline exists only on Linux, which is
	// how the product came to not build on macOS at all (nocx-1w69).
	fds, err := socketpairCloexec()
	if err != nil {
		return nil, nil, fmt.Errorf("lifecycle socketpair: %w", err)
	}
	parent := os.NewFile(uintptr(fds[0]), "lifecycle-channel-parent")
	child := os.NewFile(uintptr(fds[1]), "lifecycle-channel-child")

	tptHex, err := randHex(8)
	if err != nil {
		_ = parent.Close()
		_ = child.Close()
		return nil, nil, err
	}
	laneHex, err := randHex(8)
	if err != nil {
		_ = parent.Close()
		_ = child.Close()
		return nil, nil, err
	}
	a := &Adapter{
		log:          log,
		kernel:       k,
		id:           lifecycle.TransportID("tpt-" + tptHex),
		lane:         lifecycle.LaneID("lane-" + laneHex),
		conn:         parent,
		helloTimeout: o.helloTimeout,
		report:       o.lossReporter,
	}
	a.dec = lifecyclecodec.NewDecoder(parent, lifecyclecodec.Config{}, a.reportGap)

	cleanup := func() {
		_ = parent.Close()
		_ = child.Close()
	}
	if berr := k.BindTransport(a.id, a); berr != nil {
		cleanup()
		return nil, nil, fmt.Errorf("bind lifecycle transport: %w", berr)
	}
	h, err := k.RequestDomain(a.lane, nil, a.id)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("request lifecycle domain: %w", err)
	}
	a.domain = h.Domain
	a.epoch = h.Epoch
	a.capability = h.Capability
	a.recovery = h.Recovery
	log.Info("lifecycle channel established",
		"transport", a.id, "lane", a.lane, "domain", h.Domain, "epoch", h.Epoch)

	// The timer may fire before New returns (a short hello timeout), so the
	// field is stored under the same mutex stopHelloTimer reads: the
	// callback's read is then ordered against this write and never races.
	t := time.AfterFunc(a.helloTimeout, func() { a.lose(LossHelloTimeout) })
	a.mu.Lock()
	a.timer = t
	a.mu.Unlock()
	go a.pump()
	return a, child, nil
}

// Lane returns the adapter's own lane — the addressing tuple it minted and
// bound to the kernel. The session/app wiring uses it to register the lane
// against a session id so published facts route to the right subscriber.
// It is the adapter's own identity, not a current-domain singleton: the
// transport may carry several domains, and this is the one this adapter
// established.
func (a *Adapter) Lane() lifecycle.LaneID {
	return a.lane
}

// Launch carries the addressing tuple the shell's bootstrap must embed: the
// non-secret names (lane, domain, epoch, fd) travel as environment, and the
// capability plus the one-shot recovery fence ride the rcfile TEXT — never
// the environment (ADR-0024 decision 2; protocol §4).
type Launch struct {
	Lane       lifecycle.LaneID
	Domain     lifecycle.DomainID
	Epoch      uint64
	Capability string // 64 lowercase hex chars
	Recovery   string // 64 lowercase hex chars; the one-shot recovery fence
}

// Launch returns the adapter's own addressing tuple, for the session/app
// wiring to build the shell's bootstrap (the local tier's rcfile). It is
// the adapter's own identity, not a current-domain singleton: the transport
// may carry several domains, and this is the one this adapter established.
func (a *Adapter) Launch() Launch {
	return Launch{
		Lane:       a.lane,
		Domain:     a.domain,
		Epoch:      a.epoch,
		Capability: hex.EncodeToString(a.capability[:]),
		Recovery:   hex.EncodeToString(a.recovery[:]),
	}
}

// TransportID returns the adapter's own transport id, for the composition
// root's transport-kind registry (the grant builder needs to know whether a
// parent's domains ride the inherited descriptor or a forwarded port).
func (a *Adapter) TransportID() lifecycle.TransportID {
	return a.id
}

// Send implements lifecycle.Port: it frames one outbound envelope (accept,
// refresh_request, domain_grant — the three kinds the kernel sends) onto
// the descriptor. Failures are best-effort: the kernel ignores them and the
// shell times out its handshake in the safe direction. The grant is
// addressed to the parent, which reads it exactly like an accept.
func (a *Adapter) Send(env lifecycle.Envelope) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return ErrClosed
	}
	_ = a.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err := lifecyclecodec.Encode(a.conn, env)
	a.mu.Unlock()
	if err != nil {
		a.log.Debug("lifecycle outbound send failed", "kind", env.Event.Kind, "error", err)
		return err
	}
	// The accept reached the shell: the handshake is complete, and ONLY
	// now does the hello bound stop (decision 9). The accept is gated on
	// the renderer's acknowledgement, so a publication/ack failure leaves
	// the timer running and the domain times out — it must never sit
	// Established forever.
	if env.Event.Kind == lifecycle.KindAccept {
		a.stopHelloTimer()
	}
	return nil
}

// Close tears the transport down: the domain ends (TransportLost), the hello
// timer stops, and the pump stops. It is the session-end disposal path.
func (a *Adapter) Close() error {
	a.lose(LossClosed)
	return nil
}

// lose is the single loss path, executed once: say which caller fired,
// report the cause, notify the kernel, mark the adapter closed, and close
// the descriptor so the pump unblocks. Idempotent under concurrent callers
// (pump EOF, pump read error, hello timeout, explicit Close) — the FIRST
// cause wins, which is the one that actually ended the channel.
//
// The log line is not decoration. All three failure paths converge here, and
// while this function was silent a degraded session left no trace anywhere:
// twenty-two seconds of one on the owner's machine produced zero diagnostic
// lines, in the product or the log (nocx-dvql). The cause is what
// distinguishes "the shell never answered" from "the shell went away", and
// those need different fixes.
func (a *Adapter) lose(cause LossCause) {
	a.loss.Do(func() {
		a.log.Info("lifecycle channel lost",
			"cause", string(cause), "transport", a.id, "lane", a.lane, "domain", a.domain)
		// Before TransportLost: that call publishes the kernel's own facts
		// synchronously, and a consumer watching both must not see the fact
		// before the cause that explains it.
		if a.report != nil {
			a.report(a.lane, cause)
		}
		a.stopHelloTimer()
		if err := a.kernel.TransportLost(a.id); err != nil {
			a.log.Warn("lifecycle transport lost notification failed", "error", err)
		}
		a.mu.Lock()
		a.closed = true
		a.mu.Unlock()
		_ = a.conn.Close()
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

// reportGap is the codec's gap sink: every skipped garbage region reaches
// the kernel so the desync budgets are enforced in one place (protocol §6).
// NotifyGap rejects regions for domains that are not live (e.g. garbage
// before the handshake); those are expected and ignored.
func (a *Adapter) reportGap(bytes, frames int) {
	if err := a.kernel.NotifyGap(a.id, a.domain, bytes, frames); err != nil {
		a.log.Debug("lifecycle gap notification rejected",
			"domain", a.domain, "bytes", bytes, "frames", frames, "error", err)
	}
}

// pump moves inbound envelopes and loss into the kernel until the stream
// ends. It is the sole reader of the descriptor.
func (a *Adapter) pump() {
	defer func() { _ = a.conn.Close() }()
	for {
		env, err := a.dec.ReadFrame()
		if err == nil {
			if ierr := a.kernel.Ingest(a.id, env); ierr != nil {
				// Quarantine (a Desynchronized domain), a rejected
				// candidate, an illegal event: the kernel mutates nothing
				// and this adapter records nothing but the fact.
				a.log.Debug("lifecycle envelope rejected",
					"domain", env.Domain, "kind", env.Event.Kind, "error", ierr)
				continue
			}
			// The hello bound is NOT stopped here: the accept is gated on
			// the renderer's acknowledgement (decision 9) and may be
			// flushed later, so the timer keeps bounding the whole
			// handshake and stops only in Send, when the accept actually
			// goes out.
			continue
		}
		switch {
		case errors.Is(err, io.EOF):
			// The shell closed its end. A clean exit sends domain_closed
			// first (stream ordering guarantees it precedes EOF); the
			// kernel's read model is the authority on whether the domain
			// ended.
			a.endOfStream()
			return
		case errors.Is(err, lifecyclecodec.ErrScanBudgetExhausted):
			// The kernel revoked the domain (the final gap report crossed
			// a budget). Drain the socket so the shell never blocks on a
			// full buffer; the end-of-stream policy applies when it closes.
			_, _ = io.Copy(io.Discard, a.conn)
			a.endOfStream()
			return
		default:
			// A read error: the transport broke.
			a.log.Warn("lifecycle transport read error", "error", err)
			a.lose(LossReadError)
			return
		}
	}
}

// endOfStream applies the end-of-stream policy: a domain the shell already
// closed (domain_closed, or a revoked one) ends cleanly; a domain that is
// still live lost its speaker without saying goodbye, so the kernel marks it
// Lost and its open attempts unknown (protocol §12).
func (a *Adapter) endOfStream() {
	d, ok := a.kernel.Domain(a.domain)
	if ok {
		switch d.State {
		case lifecycle.DomainClosed, lifecycle.DomainLost:
			// Clean only in the sense that the loss is already accounted
			// for: DomainClosed is the shell saying goodbye, and DomainLost
			// means lose() already ran and already named its cause. That
			// second half used to be a lie by omission — lose() said
			// nothing, so this was the only line and it read as a clean
			// exit for a channel that had just been lost.
			a.log.Info("lifecycle transport ended with the domain already accounted for",
				"domain", a.domain, "state", d.State)
			a.mu.Lock()
			a.closed = true
			a.mu.Unlock()
			return
		}
	}
	a.log.Info("lifecycle transport ended with a live domain; marking lost", "domain", a.domain)
	a.lose(LossEndOfStream)
}

var randReader io.Reader = rand.Reader

// randHex mints this adapter's transport and lane IDENTIFIERS. Neither is an
// authenticator, so a zero value lets nobody in — but the kernel tells one
// transport's domains from another's by exactly this value (the binding check
// is an equality; a mismatch is ErrWrongTransport). With every adapter
// carrying the same pair, two local sessions share a lane and each
// authenticates against the other's domains. The identical function in
// internal/lifecycleremote had the identical hole; both are refusals now
// (nocx-s16k8).
func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(randReader, b); err != nil {
		return "", fmt.Errorf("lifecyclechannel: the randomness source failed; no identifier was minted: %w", err)
	}
	return hex.EncodeToString(b), nil
}
