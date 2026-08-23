// Package lifecyclepub is the publication boundary of the authenticated
// lifecycle protocol (ADR-0024 decision 7; docs/lifecycle-protocol.md §3,
// "two outbound paths, one boundary").
//
// Authentication terminates in the backend: the kernel (internal/lifecycle)
// validates version, epoch, capability, sequence and transition, and the
// adapters (internal/lifecyclechannel) own the transports. What crosses the
// control plane is neither frames nor secrets — it is this package's Fact,
// a schema-checked projection of the kernel's read model
// (contracts/lifecycle.changed.schema.json), carrying at least lane, domain,
// epoch, the lifecycle state, the active attempt if any, and an attempt's
// completion when one completes. No capability and no raw frame ever leaves
// the backend; the wire test in internal/transport asserts that against the
// actual serialized payload.
//
// The Publisher wraps the kernel and implements the same Kernel-shaped
// interface the adapters consume, so the composition root injects the
// publisher where it would have injected the kernel and every mutation an
// adapter drives is also projected into a fact. Facts are emitted only when
// the lane's projection changes (a reconnect hello that changes nothing is
// not a notification), and only after the mutation succeeded — a rejected
// frame mutates nothing and publishes nothing, except that a desync-budget
// revocation that happens while rejecting a quarantined frame is itself a
// state change and is published.
//
// The publisher is deliberately free of transport and WebSocket knowledge:
// it hands the fact to an Emitter (the WSServer at the composition root),
// which routes it to the lane's session's current subscriber.
package lifecyclepub

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
)

// Fact is the published lifecycle fact: the params of the lifecycle.changed
// JSON-RPC notification (contracts/lifecycle.changed.schema.json). It is what
// the kernel concluded — never the capability, never a raw frame, never the
// channel's sequence counter. A lane in native or lost has no live domain, so
// Domain and Epoch are absent there.
type Fact struct {
	Lane      string   `json:"lane"`
	Lifecycle string   `json:"lifecycle"`
	Domain    string   `json:"domain,omitempty"`
	Epoch     uint64   `json:"epoch,omitempty"`
	Attempt   *Attempt `json:"attempt,omitempty"`
	// Destination is where the domain IS, present exactly when the fact
	// names a domain minted for an ssh child (nocx-ax79). It answers "which
	// machine will run the next command", which the renderer could not
	// otherwise ask: a child domain had no authenticated host source, so a
	// cwd of /home/pi on a far host was indistinguishable from the same path
	// locally. The values are the ones domain_request carried and nothing
	// more (ADR-0025); they are descriptive, never authority — the domain id
	// and epoch remain the only authority the renderer is given, and the
	// capability and raw frames still never cross (decision 7).
	Destination *Destination `json:"destination,omitempty"`
	// Generation is the backend-minted establishment generation of the
	// domain (decision 9): minted fresh for every accept-producing hello,
	// present exactly when the fact names a domain. The renderer returns it
	// in lifecycle.establishAck after committing the editor presentation;
	// the backend flushes the pending accept only for the exact generation.
	Generation string `json:"generation,omitempty"`
	// Recovery is present exactly when this lost fact opens a restoration
	// episode (ADR-0024 decision 8): the one-shot fence the shell will
	// write to the pty at its next prompt boundary, and the generation the
	// renderer echoes back in the recovery ack. Both are the same minted
	// nonce — one value, two uses. Absent on every other lifecycle, and
	// stripped by the transport when the session is dead (no restoration
	// claim over a dead connection).
	Recovery *Recovery `json:"recovery,omitempty"`
}

// Destination is where an ssh child domain runs: the destination the
// parent's domain_request named, echoed to the renderer so a nested session
// can say which machine it is on. A local domain has none.
type Destination struct {
	Host string `json:"host"`
	User string `json:"user,omitempty"`
	Port int    `json:"port,omitempty"`
}

// Recovery is the restoration-acknowledgement contract of a lost fact. The
// fence is what the renderer matches in the render stream; the generation is
// what it returns in lifecycle.recoverAck. A hostile program cannot forge
// the fence (it never saw the pre-provisioned nonce), and the worst a forged
// one could do is force a safe transition to native mode — an availability
// loss the ADR already accepts.
type Recovery struct {
	Fence      string `json:"fence"`
	Generation string `json:"generation"`
}

// Attempt is the projection of one ExecutionAttempt. Completion fields
// (ExitCode, CompletedAt, Fence) are present exactly when State is completed:
// the kernel sets an exit status exactly once, only from an authenticated
// completion.
type Attempt struct {
	ID          string     `json:"id"`
	State       string     `json:"state"`
	Command     string     `json:"command,omitempty"`
	Origin      string     `json:"origin,omitempty"`
	StartedAt   time.Time  `json:"startedAt,omitempty"`
	ExitCode    *int       `json:"exitCode,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Fence       string     `json:"fence,omitempty"`
}

// Wire names of the lifecycle axis and attempt states. The renderer keys its
// two-axis state machine on these exact strings (ADR-0024 decision 6).
const (
	LifecycleNative         = "native"
	LifecyclePromptReady    = "prompt_ready"
	LifecycleRunning        = "running"
	LifecycleDesynchronized = "desynchronized"
	LifecycleLost           = "lost"

	AttemptOpen      = "open"
	AttemptCompleted = "completed"
	AttemptUnknown   = "unknown"

	OriginApp   = "app"
	OriginShell = "shell"
)

// derive projects the kernel's read model for one lane into a Fact. ok is
// false when the lane does not exist — nothing to publish. The read model is
// the kernel's snapshot after the mutation that triggered the call; the
// derive runs in the caller's goroutine immediately after the mutation, and
// per lane there is exactly one pump goroutine driving it today (the
// lifecyclechannel adapter), so the projection cannot be overtaken by the
// next transition before it is read.
func (p *Publisher) derive(lane lifecycle.LaneID) (Fact, bool) {
	st, err := p.kernel.State(lane)
	if err != nil {
		return Fact{}, false
	}
	f := Fact{
		Lane:      string(st.Lane),
		Lifecycle: lifecycleString(st.Lifecycle),
	}
	if st.Domain != "" {
		f.Domain = string(st.Domain)
		if d, ok := p.kernel.Domain(st.Domain); ok {
			f.Epoch = d.Epoch
		}
		p.mu.Lock()
		if dst, ok := p.dest[st.Domain]; ok {
			d := dst
			f.Destination = &d
		}
		p.mu.Unlock()
	}
	if st.Attempt != "" {
		if att, ok := p.kernel.Attempt(st.Attempt); ok {
			f.Attempt = attemptFact(att)
		}
	}
	// A lost lane opens a restoration episode when it has a recovery nonce
	// (the lane mirrors its most recent domain's). The renderer needs the
	// expected fence to match the shell's restoration, and the generation to
	// acknowledge it. The transport decides whether the episode is real —
	// it strips the promise over a dead session (decision 8: no restoration
	// claim when the shell is unreachable).
	if f.Lifecycle == LifecycleLost && st.RecoveryNonce != (lifecycle.FenceNonce{}) {
		nonce := hex.EncodeToString(st.RecoveryNonce[:])
		f.Recovery = &Recovery{Fence: nonce, Generation: nonce}
	}
	return f, true
}

func attemptFact(att lifecycle.ExecutionAttempt) *Attempt {
	a := &Attempt{
		ID:        string(att.ID),
		State:     attemptStateString(att.State),
		Command:   att.Command,
		Origin:    originString(att.Origin),
		StartedAt: att.StartedAt,
	}
	if att.ExitCode != nil {
		a.ExitCode = att.ExitCode
	}
	if att.CompletedAt != nil {
		a.CompletedAt = att.CompletedAt
	}
	if att.Fence != (lifecycle.FenceNonce{}) {
		a.Fence = hex.EncodeToString(att.Fence[:])
	}
	return a
}

func lifecycleString(s lifecycle.LifecycleState) string {
	switch s {
	case lifecycle.LifecycleNative:
		return LifecycleNative
	case lifecycle.LifecyclePromptReady:
		return LifecyclePromptReady
	case lifecycle.LifecycleRunning:
		return LifecycleRunning
	case lifecycle.LifecycleDesynchronized:
		return LifecycleDesynchronized
	case lifecycle.LifecycleLost:
		return LifecycleLost
	default:
		return ""
	}
}

func attemptStateString(s lifecycle.AttemptState) string {
	switch s {
	case lifecycle.AttemptOpen:
		return AttemptOpen
	case lifecycle.AttemptCompleted:
		return AttemptCompleted
	case lifecycle.AttemptUnknown:
		return AttemptUnknown
	default:
		return ""
	}
}

func originString(o lifecycle.AttemptOrigin) string {
	switch o {
	case lifecycle.OriginApp:
		return OriginApp
	case lifecycle.OriginShell:
		return OriginShell
	default:
		return ""
	}
}

// Kernel is the slice of the lifecycle kernel the publisher forwards to. The
// concrete *lifecycle.Kernel satisfies it; the seam exists so the publisher
// is testable and the composition root decides the kernel. It is a superset
// of the lifecyclechannel.Kernel interface (which is what the adapters
// consume), so *Publisher can be injected where an adapter expects its
// kernel.
type Kernel interface {
	BindTransport(t lifecycle.TransportID, port lifecycle.Port) error
	RequestDomain(lane lifecycle.LaneID, parent *lifecycle.DomainID, t lifecycle.TransportID) (lifecycle.DomainHandle, error)
	Ingest(t lifecycle.TransportID, env lifecycle.Envelope) ([]lifecycle.Outbound, error)
	NotifyGap(t lifecycle.TransportID, d lifecycle.DomainID, garbageBytes, garbageFrames int) ([]lifecycle.Outbound, error)
	Deliver(out lifecycle.Outbound) error
	EstablishmentTimeout(domain lifecycle.DomainID) error
	TransportLost(t lifecycle.TransportID) error
	RecoverLane(lane lifecycle.LaneID) error
	SubmitAttempt(domain lifecycle.DomainID, command, cwd, host string) (lifecycle.ExecutionAttempt, error)
	AbandonAttempt(id lifecycle.AttemptID) error
	State(lane lifecycle.LaneID) (lifecycle.LaneSnapshot, error)
	Domain(id lifecycle.DomainID) (lifecycle.Domain, bool)
	Attempt(id lifecycle.AttemptID) (lifecycle.ExecutionAttempt, bool)
	OpenAttempt(domain lifecycle.DomainID) (lifecycle.ExecutionAttempt, bool)
}

// Option configures a Publisher.
type Option func(*options)

type options struct {
	establishTimeout time.Duration
	grantBuilder     GrantBuilder
}

// WithEstablishmentTimeout bounds how long a minted accept may wait for the
// renderer's acknowledgement before the domain is rolled back (decision 9).
// Zero uses lifecycle.HelloTimeout, which mirrors the shell's own bounded
// handshake wait (protocol §5): the backend never outwaits the shell.
func WithEstablishmentTimeout(d time.Duration) Option {
	return func(o *options) { o.establishTimeout = d }
}

// GrantBuilder composes the bootstrap for a child domain requested over the
// authenticated channel (protocol doc §9): it mints the child (via
// kernel.RequestDomain — the kernel stays the sole minter), picks the
// child's transport, and returns the opaque, already-substituted bootstrap
// the parent executes. Wired at the composition root, where the rcfile
// builders and the ssh launcher live; nil (tests, or a server without the
// wiring) delivers the request echo as the empty-bootstrap refusal and the
// parent runs its command conventionally — the honest fallback.
type GrantBuilder func(req GrantRequest) (GrantBootstrap, error)

// GrantRequest is the context a domain_request carries, echoed by the
// kernel into the grant outbound: the parent's lane and domain (the child
// is minted under them) and the nested environment the parent is entering.
type GrantRequest struct {
	Lane      lifecycle.LaneID
	Parent    lifecycle.DomainID
	RequestID lifecycle.RequestID
	Env       string
	Host      string
	User      string
	Port      int
	// Opts are the ssh options the user typed, in order, with their
	// arguments. The composer rebuilds the command line and these are the
	// rest of what it is made of; without them `ssh -i key -J bastion host`
	// was executed as a bare `ssh host` (nocx-c6z0).
	Opts []string
}

// GrantBootstrap is the builder's answer: the child's identity and the
// opaque launch text the parent executes. An empty Bootstrap is the
// refusal — the parent runs its command conventionally, never suspended
// under a child that cannot exist.
type GrantBootstrap struct {
	Domain    lifecycle.DomainID
	Epoch     uint64
	Bootstrap string
}

// WithGrantBuilder wires the child-domain bootstrap builder behind the
// domain_grant outbound (protocol doc §9). Every domain_request the kernel
// validates is answered through it; without it, requests are answered with
// the empty-bootstrap refusal.
func WithGrantBuilder(b GrantBuilder) Option {
	return func(o *options) { o.grantBuilder = b }
}

// Establishment sentinel errors, returned by AcknowledgeEstablishment.
var (
	ErrNoPendingEstablishment  = errors.New("lifecyclepub: no establishment is pending acknowledgement")
	ErrEstablishmentGeneration = errors.New("lifecyclepub: acknowledgement generation does not match the pending establishment")
)

// estKey addresses one establishment episode: a domain on a lane in an
// epoch. Pending accepts are keyed by domain/epoch — never by lane or
// adapter alone, because one transport carries several domains (nested
// ssh/sudo/su), and a parent's acknowledgement can never authorize a child
// (decision 9).
type estKey struct {
	lane   lifecycle.LaneID
	domain lifecycle.DomainID
	epoch  uint64
}

// pendingAccept is one minted accept awaiting the renderer's
// acknowledgement: the exact outbound envelope to flush, the generation the
// acknowledgement must name, and the establishment bound.
type pendingAccept struct {
	gen   string
	out   lifecycle.Outbound
	timer *time.Timer
}

// Emitter is where published facts go: the WSServer at the composition root,
// which routes them to the lane's session's current subscriber. The emitter
// is bound post-construction (SetEmitter) because it is the transport, which
// is built after the kernel; facts cannot exist before a session spawns a
// shell, which is long after both exist, so the unbound window is empty in
// practice.
type Emitter interface {
	PublishLifecycle(f Fact)
}

// Publisher wraps the kernel, forwards every mutation, and projects the
// affected lane into a Fact on each change. It is safe for concurrent use:
// per-lane serialization comes from the kernel (and from the single adapter
// pump per lane); the publisher's own lock protects its bookkeeping.
type Publisher struct {
	kernel Kernel

	mu               sync.Mutex
	emitter          Emitter
	last             map[lifecycle.LaneID]Fact
	known            map[lifecycle.LaneID]struct{}
	dest             map[lifecycle.DomainID]Destination // ssh children's destinations (nocx-ax79)
	gen              map[estKey]string                  // current establishment generation per episode
	pending          map[estKey]pendingAccept           // accepts awaiting the renderer's ack (decision 9)
	establishTimeout time.Duration
	grantBuilder     GrantBuilder
}

// New builds a Publisher over the kernel. The emitter is bound separately
// with SetEmitter.
func New(k Kernel, opts ...Option) *Publisher {
	o := options{establishTimeout: lifecycle.HelloTimeout}
	for _, opt := range opts {
		opt(&o)
	}
	return &Publisher{
		kernel:           k,
		last:             make(map[lifecycle.LaneID]Fact),
		known:            make(map[lifecycle.LaneID]struct{}),
		dest:             make(map[lifecycle.DomainID]Destination),
		gen:              make(map[estKey]string),
		pending:          make(map[estKey]pendingAccept),
		establishTimeout: o.establishTimeout,
		grantBuilder:     o.grantBuilder,
	}
}

// SetEmitter binds the emitter. Calling it twice replaces the emitter; a nil
// emitter drops facts until one is bound (the startup window, which is empty
// in practice).
func (p *Publisher) SetEmitter(e Emitter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emitter = e
}

// BindTransport forwards to the kernel. Binding a transport creates no lane
// and changes no lifecycle, so nothing is published.
func (p *Publisher) BindTransport(t lifecycle.TransportID, port lifecycle.Port) error {
	return p.kernel.BindTransport(t, port)
}

// RequestDomain forwards to the kernel and records the lane so a later
// TransportLost can find every lane it may have affected. Minting a Pending
// domain changes no lifecycle, so nothing is published; the frontend keys
// enhanced mode on the published prompt_ready fact, which is what the
// handshake produces (decision 3).
func (p *Publisher) RequestDomain(lane lifecycle.LaneID, parent *lifecycle.DomainID, t lifecycle.TransportID) (lifecycle.DomainHandle, error) {
	h, err := p.kernel.RequestDomain(lane, parent, t)
	if err != nil {
		return h, err
	}
	p.mu.Lock()
	p.known[lane] = struct{}{}
	p.mu.Unlock()
	// Seed the projection without announcing it: a fresh lane is native, and
	// telling the renderer "native" about a lane it has never heard of would
	// be noise. The seed is also what keeps TransportLost from announcing
	// every unrelated lane that merely exists on another transport — only
	// lanes whose projection actually changed emit a fact.
	if f, ok := p.derive(lane); ok {
		p.mu.Lock()
		p.last[lane] = f
		p.mu.Unlock()
	}
	return h, nil
}

// buildAndDeliverGrant answers one validated domain_request: the builder
// (composition root) mints the child on the transport of its choice via
// kernel.RequestDomain and composes the opaque bootstrap; the grant is then
// delivered to the parent, enriched with the child's identity. A builder
// refusal (an unsupported environment, a failed ssh transport) delivers the
// request echo with an empty bootstrap: the parent runs its command
// conventionally — never suspended under a child that cannot exist. The
// child's minting is published: a new Pending domain on a known lane is a
// change the renderer must see (its projection follows the active domain).
func (p *Publisher) buildAndDeliverGrant(out lifecycle.Outbound) {
	grant := out.Envelope.Event.DomainGrant
	if grant == nil {
		_ = p.kernel.Deliver(out)
		return
	}
	req := GrantRequest{
		Lane:      out.Envelope.Lane,
		Parent:    out.Envelope.Domain,
		RequestID: grant.RequestID,
		Env:       grant.Env,
		Host:      grant.Host,
		User:      grant.User,
		Port:      grant.Port,
		Opts:      grant.Opts,
	}
	if p.grantBuilder != nil {
		if b, err := p.grantBuilder(req); err == nil {
			grant.Domain = b.Domain
			grant.Epoch = b.Epoch
			grant.Bootstrap = b.Bootstrap
			// The one point where the child's identity and its destination
			// are both in hand (nocx-ax79). The kernel deliberately does not
			// keep the destination — it validates the request and mints,
			// and the composer owns the launch line — so the projection
			// records it here, at the seam that already owns the fact's
			// shape, rather than deriving it a second time anywhere else.
			if req.Env == lifecycle.EnvSSH && b.Domain != "" {
				p.mu.Lock()
				p.dest[b.Domain] = Destination{Host: req.Host, User: req.User, Port: req.Port}
				p.mu.Unlock()
			}
		}
		// A builder refusal delivers the echo with an empty bootstrap: the
		// parent runs its command conventionally, never suspended under a
		// child that cannot exist, and the builder's log line carries the
		// reason (fail-open: the pump never panics).
	}
	p.publishLane(out.Envelope.Lane)
	_ = p.kernel.Deliver(out)
}

// projection, ordering the replies (decision 9): mutation → publish → only
// then the accept, and the accept only on a real acknowledgement. Published
// on failure as well as success: the one mutation a kernel makes on a
// rejected frame (the domain is closed and the lane falls to native while
// the frame is being quarantined) is a state change the renderer must see.
// Every other rejection leaves the projection unchanged and the change-dedupe
// suppresses the emission.
//
// An accept-producing hello opens an establishment episode BEFORE the fact
// goes out: the generation is minted and the pending accept recorded first,
// so an acknowledgement that lands with (or synchronously from) the
// emission finds it. The decision-9 ordering is about the FLUSH, which
// still happens only after the ack. refresh_request is never deferred — it
// restores authority and visible-prompt behaviour, grants no suppression
// authority, and delaying it behind frontend publication can only prolong a
// desynchronization.
func (p *Publisher) Ingest(t lifecycle.TransportID, env lifecycle.Envelope) error {
	outs, err := p.kernel.Ingest(t, env)
	if err != nil {
		p.publishLane(env.Lane)
		return err
	}
	for _, out := range outs {
		switch out.Envelope.Event.Kind {
		case lifecycle.KindAccept:
			if bErr := p.beginEstablishment(out); bErr != nil {
				p.publishLane(env.Lane)
				return bErr
			}
		case lifecycle.KindDomainGrant:
			// The grant is the answer to the parent's own request — it
			// grants no suppression authority and no new state, so it is
			// never deferred behind an acknowledgement (unlike accept):
			// the parent is blocked waiting for it before it can launch
			// the child.
			p.buildAndDeliverGrant(out)
		}
	}
	p.publishLane(env.Lane)
	for _, out := range outs {
		if out.Envelope.Event.Kind == lifecycle.KindAccept || out.Envelope.Event.Kind == lifecycle.KindDomainGrant {
			continue // accept is deferred (decision 9); grants were delivered by buildAndDeliverGrant
		}
		_ = p.kernel.Deliver(out) // best-effort; the shell times out in the safe direction
	}
	return nil
}

// beginEstablishment records one minted accept awaiting the renderer's
// acknowledgement (decision 9). Every accept-producing hello mints a fresh
// backend-minted generation — a fresh episode, so an old connection's
// acknowledgement (an old generation) can never release a newer accept —
// and arms the establishment bound. A later hello for the same domain
// supersedes the pending accept.
func (p *Publisher) beginEstablishment(out lifecycle.Outbound) error {
	env := out.Envelope
	key := estKey{lane: env.Lane, domain: env.Domain, epoch: env.Epoch}
	// Minted before anything is recorded: an establishment that cannot be
	// told apart from the previous one is not begun at all. The domain then
	// never establishes and the shell falls back — the same fail-open every
	// other refusal on this path takes.
	genHex, err := p.randomHex(8)
	if err != nil {
		return err
	}
	gen := "est-" + genHex
	p.mu.Lock()
	if cur, ok := p.pending[key]; ok && cur.timer != nil {
		cur.timer.Stop()
	}
	p.gen[key] = gen
	p.pending[key] = pendingAccept{gen: gen, out: out}
	p.mu.Unlock()
	p.armEstablishmentTimer(key, gen)
	return nil
}

func (p *Publisher) armEstablishmentTimer(key estKey, gen string) {
	t := time.AfterFunc(p.establishTimeout, func() { p.establishmentTimedOut(key, gen) })
	p.mu.Lock()
	defer p.mu.Unlock()
	pend, ok := p.pending[key]
	if !ok || pend.gen != gen {
		t.Stop() // acked or superseded before the timer armed
		return
	}
	pend.timer = t
	p.pending[key] = pend
}

// establishmentTimedOut is the bound of one establishment episode: no
// acknowledgement arrived, so the accept is dropped and the domain rolled
// back if it is still awaiting its accept (decision 9). A reconnect accept
// for an already-live domain is dropped instead — the shell keeps its
// visible prompt either way — and the kernel leaves it live.
func (p *Publisher) establishmentTimedOut(key estKey, gen string) {
	p.mu.Lock()
	pend, ok := p.pending[key]
	if !ok || pend.gen != gen {
		p.mu.Unlock()
		return // acked or superseded meanwhile
	}
	delete(p.pending, key)
	p.mu.Unlock()
	if err := p.kernel.EstablishmentTimeout(key.domain); err == nil {
		p.publishLane(key.lane) // the revoke changed the lane; the dedupe suppresses a no-op
	}
}

// AcknowledgeEstablishment is the renderer's establishment acknowledgement
// (decision 9): the transport has already validated that the acknowledging
// connection owns the session and is its current subscriber, and forwards
// the ack here. The acknowledgement must name the exact generation of the
// pending accept — anything else is stale or foreign and is refused. On a
// match the accept is flushed, and only on a real acknowledgement. The
// domain must still be established and current: Deliver refuses an accept
// for a domain that was revoked or lost in the meantime (its safe state is
// already published or being published).
func (p *Publisher) AcknowledgeEstablishment(lane lifecycle.LaneID, domain lifecycle.DomainID, epoch uint64, generation string) error {
	key := estKey{lane: lane, domain: domain, epoch: epoch}
	p.mu.Lock()
	pend, ok := p.pending[key]
	if !ok {
		p.mu.Unlock()
		return ErrNoPendingEstablishment
	}
	if pend.gen != generation {
		p.mu.Unlock()
		return ErrEstablishmentGeneration
	}
	if pend.timer != nil {
		pend.timer.Stop()
	}
	delete(p.pending, key)
	out := pend.out
	p.mu.Unlock()
	return p.kernel.Deliver(out)
}

// randReader is the randomness seam, the same shape internal/shellintegration
// already uses for its own: a package var so a test can make the source fail,
// because "for every external call your code makes there is a test where that
// call fails" (AGENTS.md) and crypto/rand is one.
var randReader io.Reader = rand.Reader

// randomHex mints the establishment GENERATION. It is not an authenticator,
// but it is a discriminator against a stale actor — a late acknowledgement
// from a previous episode must not release the accept of a newer one — and
// the check is `pend.gen != generation`, an equality. Two zero values compare
// equal, so a source that failed would let exactly the stale ack this value
// exists to reject through, and would let a superseded timer cancel a live
// establishment. It is also echoed by the far side, so it must be
// unguessable as well as distinct. A failed read is therefore an error, not
// a tolerated zero (nocx-s16k8).
func (p *Publisher) randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(randReader, b); err != nil {
		return "", fmt.Errorf("lifecyclepub: the randomness source failed; no establishment generation was minted: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Domain returns the read model of one domain, forwarding to the kernel. The
// lifecyclechannel adapter's Kernel interface requires it — the adapter
// answers its handshake timeout by asking whether the domain it minted ever
// became Established.
func (p *Publisher) Domain(id lifecycle.DomainID) (lifecycle.Domain, bool) {
	return p.kernel.Domain(id)
}

// State returns the read model of one lane. Projection consumers (a future
// lifecycle.status RPC, the reconnect replay) read current state through the
// publisher, never through a singleton.
func (p *Publisher) State(lane lifecycle.LaneID) (lifecycle.LaneSnapshot, error) {
	return p.kernel.State(lane)
}

// Attempt returns a copy of the attempt, if it exists.
func (p *Publisher) Attempt(id lifecycle.AttemptID) (lifecycle.ExecutionAttempt, bool) {
	return p.kernel.Attempt(id)
}

// OpenAttempt returns the single open attempt of a domain, if any.
func (p *Publisher) OpenAttempt(domain lifecycle.DomainID) (lifecycle.ExecutionAttempt, bool) {
	return p.kernel.OpenAttempt(domain)
}

// NotifyGap forwards a framing-gap report and publishes the domain's lane:
// the domain enters Desynchronized (or a desync budget exhausts and it is
// revoked, which is also a published change). The refresh_request the
// transition produces is delivered immediately — never deferred behind
// publication (decision 9: it grants no suppression authority, and delay
// only prolongs the desynchronization).
func (p *Publisher) NotifyGap(t lifecycle.TransportID, d lifecycle.DomainID, garbageBytes, garbageFrames int) error {
	lane := ""
	if dom, ok := p.kernel.Domain(d); ok {
		lane = string(dom.Lane)
	}
	outs, err := p.kernel.NotifyGap(t, d, garbageBytes, garbageFrames)
	if lane != "" {
		p.publishLane(lifecycle.LaneID(lane))
	}
	for _, out := range outs {
		_ = p.kernel.Deliver(out) // best-effort; the shell times out in the safe direction
	}
	return err
}

// TransportLost forwards the loss and publishes every lane the publisher has
// seen: every domain bound to the transport (and its descendants) falls to
// Lost, and each affected lane publishes a lost fact. Unaffected lanes derive
// unchanged and the dedupe suppresses them.
func (p *Publisher) TransportLost(t lifecycle.TransportID) error {
	// Cancel every pending establishment whose domain rides the lost
	// transport: an accept for a dead domain must never flush (decision 9).
	p.mu.Lock()
	for key, pend := range p.pending {
		d, ok := p.kernel.Domain(key.domain)
		if !ok || d.Transport != t {
			continue
		}
		if pend.timer != nil {
			pend.timer.Stop()
		}
		delete(p.pending, key)
	}
	p.mu.Unlock()
	err := p.kernel.TransportLost(t)
	if err != nil {
		return err
	}
	p.mu.Lock()
	lanes := make([]lifecycle.LaneID, 0, len(p.known))
	for l := range p.known {
		lanes = append(lanes, l)
	}
	p.mu.Unlock()
	for _, l := range lanes {
		p.publishLane(l)
	}
	return nil
}

// RecoverLane forwards a restoration acknowledgement (decision 8's composite
// ACK): the lane's Lost → Native transition, permitted only from Lost, and
// published so the renderer sees the session become a usable conventional
// terminal. The domain stays permanently Lost; any future integration is a
// fresh epoch. Idempotent at the kernel (an already-Native lane is a no-op).
func (p *Publisher) RecoverLane(lane lifecycle.LaneID) error {
	err := p.kernel.RecoverLane(lane)
	if err != nil {
		return err
	}
	p.publishLane(lane)
	return nil
}

// SubmitAttempt forwards an app-originated attempt (created synchronously at
// editor submit, before the pty bytes) and publishes the lane's move to
// running.
func (p *Publisher) SubmitAttempt(domain lifecycle.DomainID, command, cwd, host string) (lifecycle.ExecutionAttempt, error) {
	att, err := p.kernel.SubmitAttempt(domain, command, cwd, host)
	if err != nil {
		return att, err
	}
	p.publishLane(att.Lane)
	return att, nil
}

// AbandonAttempt forwards the explicit abandonment (native-mode escape) and
// publishes the attempt's lane: the attempt's state becomes unknown, which is
// a projection change even though the lane stays running.
func (p *Publisher) AbandonAttempt(id lifecycle.AttemptID) error {
	err := p.kernel.AbandonAttempt(id)
	if err != nil {
		return err
	}
	if att, ok := p.kernel.Attempt(id); ok {
		p.publishLane(att.Lane)
	}
	return nil
}

// ReplayLane re-emits the lane's current projection unconditionally —
// bypassing the change-dedupe, which is exactly the point: a reattached
// frontend (AD-9 reconnect, protocol §12) must receive the current state
// even if no transition happened since its last view. The emission also
// refreshes the dedupe baseline, so a replay cannot suppress a later real
// change.
func (p *Publisher) ReplayLane(lane lifecycle.LaneID) {
	f, ok := p.derive(lane)
	if !ok {
		return
	}
	p.mu.Lock()
	p.stampGenLocked(lane, &f)
	p.last[lane] = f
	e := p.emitter
	p.mu.Unlock()
	if e != nil {
		e.PublishLifecycle(f)
	}
}

// publishLane derives the lane's fact and emits it when it changed since the
// last emission for that lane. Derivation runs in the caller's goroutine,
// immediately after the mutation that triggered it; the emitter call happens
// outside the publisher's lock so a slow WebSocket write cannot stall another
// lane's bookkeeping.
func (p *Publisher) publishLane(lane lifecycle.LaneID) {
	f, ok := p.derive(lane)
	if !ok {
		return
	}
	p.mu.Lock()
	p.stampGenLocked(lane, &f)
	if last, seen := p.last[lane]; seen && reflect.DeepEqual(last, f) {
		p.mu.Unlock()
		return
	}
	p.last[lane] = f
	e := p.emitter
	p.mu.Unlock()
	if e != nil {
		e.PublishLifecycle(f)
	}
}

// stampGenLocked attaches the lane's current establishment generation to a
// fact naming a domain. A reconnect hello mints a fresh generation, so the
// replayed fact differs from the previous one and the dedupe lets it out —
// that replay is what carries the fresh generation to the renderer for the
// acknowledgement (decision 9: no deadlock on reconnect).
func (p *Publisher) stampGenLocked(lane lifecycle.LaneID, f *Fact) {
	if f.Domain == "" {
		return
	}
	key := estKey{lane: lane, domain: lifecycle.DomainID(f.Domain), epoch: f.Epoch}
	if g, ok := p.gen[key]; ok {
		f.Generation = g
	}
}
