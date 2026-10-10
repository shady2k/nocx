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
	"context"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/lifecyclecommit"
	nocxlog "github.com/shady2k/nocx/internal/log"
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
	SubmitID    string     `json:"submitId,omitempty"`
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
// the kernel's snapshot at the moment of the call. It is NOT true that only
// one goroutine publishes a lane — the adapter's pump does, and so do the
// replays (session open and attach, a Stop's settlement) and the submit and
// recover handlers — so a derived fact can be stale by the time it is
// emitted unless the emission is ordered with it. That ordering is the
// lane's emission turn, and every caller that emits takes it (publishLane,
// ReplayLane).
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
		SubmitID:  att.SubmitID,
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
	AdoptDomain(lane lifecycle.LaneID, domain lifecycle.DomainID, epoch uint64, capability lifecycle.Capability, recovery lifecycle.FenceNonce, t lifecycle.TransportID) (lifecycle.DomainHandle, error)
	Ingest(t lifecycle.TransportID, env lifecycle.Envelope) ([]lifecycle.Outbound, error)
	NotifyGap(t lifecycle.TransportID, d lifecycle.DomainID, garbageBytes, garbageFrames int) ([]lifecycle.Outbound, error)
	Deliver(out lifecycle.Outbound) error
	TransportLost(t lifecycle.TransportID) error
	RecoverLane(lane lifecycle.LaneID) error
	SubmitAttempt(domain lifecycle.DomainID, command, cwd, host, submitID string) (lifecycle.ExecutionAttempt, error)
	AbandonAttempt(id lifecycle.AttemptID) error
	State(lane lifecycle.LaneID) (lifecycle.LaneSnapshot, error)
	Domain(id lifecycle.DomainID) (lifecycle.Domain, bool)
	TerminalDomainOfLane(lane lifecycle.LaneID) (lifecycle.Domain, bool)
	Attempt(id lifecycle.AttemptID) (lifecycle.ExecutionAttempt, bool)
	OpenAttempt(domain lifecycle.DomainID) (lifecycle.ExecutionAttempt, bool)
}

// Option configures a Publisher.
type Option func(*options)

type options struct {
	grantBuilder        GrantBuilder
	agentEnroller       AgentEnroller
	agentLaunchResolver AgentLaunchResolver
	log                 nocxlog.Logger
}

// WithLogger gives the publisher a voice (nocx-n14oo.8).
//
// This package decides every handshake in the product and, until this
// existed, wrote nothing at all. An accept that never reaches the shell (a
// dead port, a lost transport) and an accept the shell simply never got
// around to reading both ended as one bare `hello-timeout` line from the
// adapter ten seconds later — which is the difference between a broken
// transport and a shell that stalled reading its own channel, read as the
// same event. Without a logger the default is silent, which is what a test
// and an embedding without wiring want.
func WithLogger(l nocxlog.Logger) Option {
	return func(o *options) { o.log = l }
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

// AgentEnroller opens and closes the backend's watch on a pane (protocol doc
// §15, and the AD-6 amendment's INTERVAL constraint). It is the seam that owns
// grids; this package owns none and knows what none of them are.
//
// The asymmetry between the two methods is the amendment's, not a style
// choice. Enrol may FAIL and its failure must reach the person — no enrolment,
// no orchestration, and the pane says so. Withdraw cannot fail and returns
// nothing: closing something already closed is not an error, because a caller
// racing a session teardown should not have to care who won, and a close that
// could be refused would be an interval with one end.
type AgentEnroller interface {
	Enrol(lane lifecycle.LaneID, agent string, cols, rows int) error
	Withdraw(lane lifecycle.LaneID)
}

// AgentLaunchBinding is derived only from the authenticated envelope and the
// transport selected by the adapter. The caller supplies no local/remote flag.
type AgentLaunchBinding struct {
	Transport lifecycle.TransportID
	Lane      lifecycle.LaneID
	Domain    lifecycle.DomainID
	Epoch     uint64
	Agent     string
}

// AgentLaunchResolution is the backend's result for one launch-record lookup.
// Local says a local record applies to this invocation; false also covers
// server-classified worker panes that keep their literal command. Payload is
// already encoded as shell-safe data; it must never be logged.
type AgentLaunchResolution struct {
	Local   bool
	Ticket  string
	Payload string
	Reason  string
}

// AgentLaunchResolver owns the agent-record lookup and ticket lifecycle.
// Cancel is idempotent and best effort; it receives the authenticated binding.
type AgentLaunchResolver interface {
	Resolve(ctx context.Context, binding AgentLaunchBinding) (AgentLaunchResolution, error)
	Cancel(binding AgentLaunchBinding, ticket string)
	InvalidateBinding(binding AgentLaunchBinding)
	InvalidateDomain(transport lifecycle.TransportID, lane lifecycle.LaneID, domain lifecycle.DomainID)
	InvalidateTransport(transport lifecycle.TransportID)
}

// AgentLaunchEnroller consumes a launch ticket at the same server boundary
// that handles consent. Implementations must reject a local enrolment without
// a ticket and reject tickets from any other binding.
type AgentLaunchEnroller interface {
	EnrolResolved(ctx context.Context, binding AgentLaunchBinding, ticket string, cols, rows int) error
}

// EnrolmentPending is the verdict an enroller returns when the answer waits on
// a person: nocx is asking whether the agent may use its tools (nocx-cyhfw).
//
// It exists because the two waits cannot nest. The enrolment is a handshake
// between programs, bounded at seconds by the shell; the question waits on
// somebody reading it and has no deadline. Blocking the first on the second
// meant nobody could answer in time (nocx-t7xds), and refusing instead meant
// the agent started before anybody had answered. So the caller is told to wait
// — inside the handshake's bound — and told again, on the same request, when
// the question closes.
//
// Settled delivers exactly once: nothing when a person answered, and a
// sentence when the question could not be put or its answer could not be kept.
// It opens nothing and admits nothing. The caller enrols again after it, and
// that enrolment is the one a grid opens for.
type EnrolmentPending struct {
	Reason  string
	Settled <-chan string
}

func (p *EnrolmentPending) Error() string { return p.Reason }

// WithAgentEnroller wires the seam that keeps a pane's grid behind the
// agent_enrol / agent_withdraw pair.
//
// Without it every enrolment is REFUSED, and that is the whole difference
// between this seam and the grant builder beside it. An unwired grant builder
// answers with an empty bootstrap and the parent runs its command
// conventionally, which is right for an optional enhancement. An unwired
// enroller answers "not orchestrated", because something an invariant rests on
// must not be able to look established while nothing is watching (D4).
func WithAgentEnroller(e AgentEnroller) Option {
	return func(o *options) { o.agentEnroller = e }
}

// WithAgentLaunchResolver wires the record lookup and opaque-ticket owner.
func WithAgentLaunchResolver(r AgentLaunchResolver) Option {
	return func(o *options) { o.agentLaunchResolver = r }
}

// Emitter is where published facts go: the WSServer at the composition root,
// which routes them to the lane's session's current subscriber. The emitter
// is bound post-construction (SetEmitter) because it is the transport, which
// is built after the kernel; facts cannot exist before a session spawns a
// shell, which is long after both exist, so the unbound window is empty in
// practice.
type Emitter interface {
	PublishLifecycle(ctx context.Context, f Fact)
}

// ProjectionEmitter receives lifecycle facts that must update server-owned
// projections without creating a duplicate renderer notification.
type ProjectionEmitter interface {
	PublishLifecycleProjection(ctx context.Context, f Fact)
}

// AttemptTransitionEmitter receives the two transitions of one attempt that
// the published Fact cannot carry, and cannot be extended to carry without
// claiming something else:
//
//   - the shell AUTHENTICATED ITS START. Fact.Attempt.StartedAt is the SUBMIT
//     time (the attempt exists from submit, before its bytes reach the pty —
//     decision 5), so a lane's projection is byte-identical before and after
//     the start attaches and the change-dedupe suppresses the notification.
//     publishLaneProjection exists for the ledger's half of this transition;
//     this is the transport's.
//   - the attempt LEFT OPEN WITHOUT ONE. applyPromptReady clears the lane's
//     attempt reference as soon as it PRIMES it (kernel.go: an open attempt
//     the shell reached a prompt over may still be the start's target), so the
//     prompt_ready that later closes that attempt derives a fact identical to
//     the one already emitted — the closer is invisible in the fact stream and
//     in the lane's own projection, which by then names no attempt at all.
//
// The two are one interface because they are two answers about one attempt,
// both read the same way: the open attempts of a lane, before and after a
// mutation that succeeded (transitionsBelow). An emitter that does not
// implement this interface is not told, which is every emitter that has no
// state resting on either transition.
type AttemptTransitionEmitter interface {
	// PublishAttemptStarted reports that this already-open attempt is now
	// started — the shell has authenticated the line it belongs to, which is
	// the first moment an interrupt may be written for it without landing in
	// bash's parser (nocx-zas0d).
	PublishAttemptStarted(ctx context.Context, attempt lifecycle.AttemptID)
	// PublishAttemptClosed reports that this attempt has left `open`, with no
	// start ever having been authenticated for it. An obligation held against
	// that start can never be discharged and must be dropped.
	PublishAttemptClosed(ctx context.Context, attempt lifecycle.AttemptID)
}

// Publisher wraps the kernel, forwards every mutation, and projects the
// affected lane into a Fact on each change. It is safe for concurrent use:
// the kernel serializes mutations, each lane's emission turn serializes its
// derive-record-emit (publishLane), and the publisher's own lock protects its
// bookkeeping.
type Publisher struct {
	kernel Kernel

	mu                  sync.Mutex
	emitter             Emitter
	last                map[lifecycle.LaneID]Fact
	emitting            map[lifecycle.LaneID]chan struct{} // each lane's emission turn (laneEmission)
	known               map[lifecycle.LaneID]struct{}
	dest                map[lifecycle.DomainID]Destination // ssh children's destinations (nocx-ax79)
	grantBuilder        GrantBuilder
	agentEnroller       AgentEnroller
	agentLaunchResolver AgentLaunchResolver
	log                 nocxlog.Logger
}

// New builds a Publisher over the kernel. The emitter is bound separately
// with SetEmitter.
func New(k Kernel, opts ...Option) *Publisher {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.log == nil {
		o.log = nocxlog.NewSlogAdapter(nil)
	}
	return &Publisher{
		kernel:              k,
		last:                make(map[lifecycle.LaneID]Fact),
		emitting:            make(map[lifecycle.LaneID]chan struct{}),
		known:               make(map[lifecycle.LaneID]struct{}),
		dest:                make(map[lifecycle.DomainID]Destination),
		grantBuilder:        o.grantBuilder,
		agentEnroller:       o.agentEnroller,
		agentLaunchResolver: o.agentLaunchResolver,
		log:                 o.log,
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

// AdoptDomain forwards a takeover of a still-living domain to the kernel
// (nocx-k6p18.31) and PUBLISHES the resulting lane, which is the one way it
// differs from RequestDomain above.
//
// RequestDomain publishes nothing because minting a Pending domain moves no
// lifecycle: the handshake is what makes a lane live, and the renderer keys
// enhanced mode on the fact the handshake produces. An adoption IS that
// moment — the shell is already past its accept, so the lane goes live in one
// step and the renderer has to be told, or the pane it just took back would
// hold a live authenticated domain and go on rendering as a plain terminal.
func (p *Publisher) AdoptDomain(lane lifecycle.LaneID, domain lifecycle.DomainID, epoch uint64, capability lifecycle.Capability, recovery lifecycle.FenceNonce, t lifecycle.TransportID) (lifecycle.DomainHandle, error) {
	h, err := p.kernel.AdoptDomain(lane, domain, epoch, capability, recovery, t)
	if err != nil {
		return h, err
	}
	p.mu.Lock()
	p.known[lane] = struct{}{}
	p.mu.Unlock()
	p.publishLane(context.Background(), lane)
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
func (p *Publisher) buildAndDeliverGrant(ctx context.Context, out lifecycle.Outbound) {
	grant := out.Envelope.Event.DomainGrant
	if grant == nil {
		lifecyclecommit.OnCommit(ctx, func() { _ = p.kernel.Deliver(out) })
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
	p.publishLane(ctx, out.Envelope.Lane)
	lifecyclecommit.OnCommit(ctx, func() { _ = p.kernel.Deliver(out) })
}

// answerAgentEnrolment fills the verdict and delivers it.
//
// Everything here is written so that the silent paths are refusals. A nil
// seam, a nil payload, a seam that errors: all of them leave Enrolled false,
// which is what the caller reads as "not orchestrated". Nothing in this
// function can produce consent except a seam that actually opened a grid and
// said so.
// The ASK is read from the inbound envelope rather than echoed through the
// answer, unlike the grant beside it: the geometry is the seam's business and
// not the shell's, so putting it on the outbound would send the caller back a
// number it told us in the previous frame.
func (p *Publisher) answerAgentLaunchResolve(ctx context.Context, transport lifecycle.TransportID, ask lifecycle.Envelope, out lifecycle.Outbound) {
	ans := out.Envelope.Event.AgentLaunchResolved
	if ans == nil {
		_ = p.kernel.Deliver(out)
		return
	}
	req := ask.Event.AgentLaunchResolve
	binding := AgentLaunchBinding{
		Transport: transport,
		Lane:      ask.Lane,
		Domain:    ask.Domain,
		Epoch:     ask.Epoch,
	}
	if req == nil {
		ans.Local = true
		ans.Reason = "the launch request was incomplete"
	} else {
		binding.Agent = req.Agent
		if p.agentLaunchResolver == nil {
			// This event is sent only by the local delivery. Without an owner
			// for records, fail closed rather than executing the record ID.
			ans.Local = true
			ans.Reason = "local agent launch resolution is not wired"
		} else if resolved, err := p.agentLaunchResolver.Resolve(ctx, binding); err != nil {
			ans.Local = true
			ans.Reason = "the local agent record could not be resolved"
		} else {
			ans.Local = resolved.Local
			ans.Ticket = resolved.Ticket
			ans.Payload = resolved.Payload
			ans.Reason = resolved.Reason
			if !resolved.Local {
				// A non-record classification (remote or worker-literal) carries
				// names only, regardless of what an implementation returned.
				ans.Ticket = ""
				ans.Payload = ""
			}
		}
	}
	if _, err := lifecyclecodec.Encode(io.Discard, out.Envelope); err != nil {
		if ans.Ticket != "" && p.agentLaunchResolver != nil {
			p.agentLaunchResolver.Cancel(binding, ans.Ticket)
		}
		ans.Ticket = ""
		ans.Payload = ""
		ans.Local = true
		ans.Reason = "the local agent launch configuration exceeds the lifecycle frame limit"
	}
	_ = p.kernel.Deliver(out)
}

func (p *Publisher) answerAgentEnrolment(ctx context.Context, transport lifecycle.TransportID, ask lifecycle.Envelope, out lifecycle.Outbound) {
	lane := out.Envelope.Lane
	switch out.Envelope.Event.Kind {
	case lifecycle.KindAgentEnrolled:
		ans := out.Envelope.Event.AgentEnrolled
		if ans == nil {
			_ = p.kernel.Deliver(out)
			return
		}
		req := ask.Event.AgentEnrol
		switch {
		case p.agentEnroller == nil:
			ans.Reason = "this backend is not wired to watch panes"
		case req == nil:
			ans.Reason = "the enrolment carried no request"
		default:
			binding := AgentLaunchBinding{Transport: transport, Lane: ask.Lane, Domain: ask.Domain, Epoch: ask.Epoch, Agent: ans.Agent}
			var err error
			if resolved, ok := p.agentEnroller.(AgentLaunchEnroller); ok {
				err = resolved.EnrolResolved(ctx, binding, req.LaunchTicket, req.Cols, req.Rows)
			} else if req.LaunchTicket != "" {
				err = errors.New("this backend cannot validate local agent launch tickets")
			} else {
				err = p.agentEnroller.Enrol(lane, ans.Agent, req.Cols, req.Rows)
			}
			var pending *EnrolmentPending
			switch {
			case err == nil:
				ans.Enrolled = true
			case errors.As(err, &pending) && pending.Settled != nil:
				// The caller is told to wait, now, inside the handshake's
				// bound — and told again when the question closes. A wait
				// with no channel to close it would hold the caller for ever,
				// so it falls to the refusal below instead.
				ans.Pending = true
				ans.Reason = pending.Reason
				_ = p.kernel.Deliver(out)
				go p.closeQuestion(out, ans.RequestID, ans.Agent, pending.Settled)
				return
			default:
				ans.Reason = err.Error()
			}
		}
	case lifecycle.KindAgentWithdrawn:
		if p.agentEnroller != nil {
			p.agentEnroller.Withdraw(lane)
		}
	}
	_ = p.kernel.Deliver(out)
}

// closeQuestion tells the caller that the question its enrolment waited on has
// closed, on the same request and to the same domain. It runs on a goroutine of
// its own because a person is not on the pump's clock.
//
// The frame is built from the request's identity and nothing else. It carries
// no Enrolled and no Pending, so nothing in it can be read as consent: consent
// only ever answers an enrolment that opened a grid, and the caller sends that
// enrolment after reading this. A domain that ended while the person was
// reading is not an error here — the shell that asked is gone with it.
func (p *Publisher) closeQuestion(asked lifecycle.Outbound, rid lifecycle.RequestID, agent string, settled <-chan string) {
	reason := <-settled
	closing := asked
	closing.Envelope.Event = lifecycle.Event{
		Kind:          lifecycle.KindAgentEnrolled,
		AgentEnrolled: &lifecycle.AgentEnrolled{RequestID: rid, Agent: agent, Reason: reason},
	}
	if err := p.kernel.Deliver(closing); err != nil {
		p.log.Debug("lifecycle: the frame closing an agent question was not delivered",
			"lane", string(closing.Envelope.Lane), "domain", string(closing.Envelope.Domain),
			"request", string(rid), "error", err)
	}
}

// shouldPublishStartedAttempt reports whether env is a start attaching to a
// pending app attempt — the one transition the lane's fact cannot show, since
// the lane is already running that attempt before and after. It holds for a
// named start exactly as for an unnamed one: the shipped shells name every
// start with their own id, which the kernel records as the app attempt's
// alias, and excluding them left every command submitted from nocx's editor
// with no execution and no block (nocx-zg3k3.5.11).
func (p *Publisher) shouldPublishStartedAttempt(env lifecycle.Envelope) bool {
	if env.Event.Kind != lifecycle.KindStart || env.Event.Start == nil {
		return false
	}
	before, ok := p.derive(env.Lane)
	if !ok || before.Attempt == nil {
		return false
	}
	attempt, ok := p.kernel.Attempt(lifecycle.AttemptID(before.Attempt.ID))
	return ok && !attempt.Started
}

// openAttemptsOf reads the lane's open attempts with the one bit the published
// Fact cannot carry — whether each has been STARTED — and it is the read both
// transition reports are defined against (AttemptTransitionEmitter). It must be
// taken BEFORE the mutation whose transitions it is asked about, and it returns
// nil when the lane holds nothing open, which is the ordinary state between
// commands.
func (p *Publisher) openAttemptsOf(lane lifecycle.LaneID) map[lifecycle.AttemptID]bool {
	if lane == "" {
		return nil
	}
	snap, err := p.kernel.State(lane)
	if err != nil {
		return nil
	}
	return p.openAttemptsIn(snap)
}

// openAttemptsIn is openAttemptsOf over a snapshot the caller already read.
func (p *Publisher) openAttemptsIn(snap lifecycle.LaneSnapshot) map[lifecycle.AttemptID]bool {
	if len(snap.OpenAttempts) == 0 {
		return nil
	}
	open := make(map[lifecycle.AttemptID]bool, len(snap.OpenAttempts))
	for _, id := range snap.OpenAttempts {
		att, ok := p.kernel.Attempt(id)
		if !ok {
			continue
		}
		open[id] = att.Started
	}
	return open
}

// transitionsBelow reports to the emitter every transition of the attempts in
// `before` that the published Fact cannot carry, and it is the ONLY place
// either one is reported from, whatever mutation caused it. It runs after
// that mutation succeeded, outside every lock, and reads nothing further when
// the emitter has not asked for these transitions.
//
// IT RUNS BEFORE THE CALLER'S OWN publishLane/publishLaneProjection, and that
// is load-bearing: PublishAttemptClosed is the ONLY path that raises the
// attempt's completion notification (block-finished; history.recorded is
// PART of that report too, historically), so a lane fact that beats it to
// closing the ledger row leaves nothing for it to report — the notification
// is silently never raised at all, not merely late (measured: swapping this
// order once made TestLifecycleCompletion_RaisesAttestedBlockFinished
// observe zero events instead of one). What DOES need to wait for the lane's
// own fact is only the WIRE DELIVERY of history.recorded, which the emitter
// (PublishAttemptClosed → publishClosedAttemptHistory, ws_lifecycle.go)
// defers by stashing it for PublishLifecycle to flush right after it sends
// the fact naming the same attempt's completion (nocx-2v80t.3.22) — a
// renderer that read the receipt before that fact still held the attempt
// open, and a receipt naming an open attempt attaches to nothing and is
// dropped for good.
func (p *Publisher) transitionsBelow(ctx context.Context, before map[lifecycle.AttemptID]bool) {
	if len(before) == 0 {
		return
	}
	p.mu.Lock()
	e := p.emitter
	p.mu.Unlock()
	te, ok := e.(AttemptTransitionEmitter)
	if !ok {
		return
	}
	for attempt, wasStarted := range before {
		current, ok := p.kernel.Attempt(attempt)
		switch {
		case !ok || current.State != lifecycle.AttemptOpen:
			te.PublishAttemptClosed(ctx, attempt)
		case !wasStarted && current.Started:
			te.PublishAttemptStarted(ctx, attempt)
		}
	}
}

// Ingest applies one frame and publishes what it changed.
//
// THE LANE'S EMISSION TURN IS HELD BEFORE THE FRAME'S FIRST STORE WRITE AND
// THROUGH ITS POST-COMMIT EFFECTS. ADR-0077 decision 8 requires taking it
// before writes: otherwise a replay may hold the store connection while the
// frame waits for the turn. Keeping it through queued effects also prevents
// session.open from publishing post-mutation prompt_ready before the shell
// receives ACCEPT.
//
// A frame's projections write the store, and its writes are one transaction
// that holds the store's only connection until the frame ends (content's
// ApplyLifecycleFrame). ReplayLane takes this same turn and then writes the
// store through the emitter; were the frame to take the turn only after its
// first write — the attempt transitions report before the lane fact — the two
// would each hold what the other waits for. Taken first, the turn orders every
// frame's store work after any replay of its lane, and its own emissions run
// under the turn it already holds (holdsTurn).
//
// ctx is the frame's: every emission the frame causes carries it, and with it
// the store transaction the frame's writes join. What the frame sends the
// shell — the ACCEPT, a grant, an enrolment's answer, any other outbound —
// waits in the frame's post-commit queue (lifecyclecommit, ADR-0077 decision
// 12) in the order the kernel minted it, and goes out only if the frame is
// stored; outside a frame it goes out at once. The turn's release is queued
// after these effects, so session.open cannot replay the new prompt_ready
// state before ACCEPT and the frame's own fact are delivered.
func (p *Publisher) Ingest(ctx context.Context, t lifecycle.TransportID, env lifecycle.Envelope) error {
	turn := p.laneEmission(env.Lane)
	turn <- struct{}{}
	releaseOnReturn := true
	defer func() {
		if releaseOnReturn {
			<-turn
		}
	}()
	releaseAfterFrame := func() {
		releaseOnReturn = false
		lifecyclecommit.After(ctx, nil, func(bool) { <-turn })
	}
	ctx = context.WithValue(ctx, heldTurnKey{}, env.Lane)

	// The lane's open attempts are read BEFORE the mutation — the only moment
	// they can be — because a prompt_ready that closes one also clears the
	// lane's own reference to it, leaving nothing afterwards to compare
	// against. The ledger's own forced projection keeps the narrower condition
	// it was written with (shouldPublishStartedAttempt above).
	before := p.openAttemptsOf(env.Lane)
	forceStartedProjection := p.shouldPublishStartedAttempt(env)
	outs, err := p.kernel.Ingest(t, env)
	if err != nil {
		// A REFUSED frame can still have mutated the kernel, and one case
		// always does: the desync budget is checked AHEAD of the transition
		// (kernel.go's ingestLocked), so an expired episode revokes the domain
		// — closing its open attempts — and only then is this frame refused
		// for arriving at a desynchronized domain. Reporting transitions on
		// the success path alone left those closures unsaid and a Stop held for
		// one of them behind, until unrelated session teardown (nocx-zas0d,
		// review finding 5 of 1e899f6a).
		p.transitionsBelow(ctx, before)
		p.publishLane(ctx, env.Lane)
		releaseAfterFrame()
		return err
	}
	if req := env.Event.AgentLaunchCancel; env.Event.Kind == lifecycle.KindAgentLaunchCancel && req != nil && p.agentLaunchResolver != nil {
		binding := AgentLaunchBinding{Transport: t, Lane: env.Lane, Domain: env.Domain, Epoch: env.Epoch, Agent: req.Agent}
		ticket := req.Ticket
		lifecyclecommit.OnCommit(ctx, func() { p.agentLaunchResolver.Cancel(binding, ticket) })
	}
	if env.Event.Kind == lifecycle.KindAgentWithdraw && p.agentLaunchResolver != nil {
		binding := AgentLaunchBinding{Transport: t, Lane: env.Lane, Domain: env.Domain, Epoch: env.Epoch}
		lifecyclecommit.OnCommit(ctx, func() { p.agentLaunchResolver.InvalidateBinding(binding) })
	}
	switch env.Event.Kind {
	case lifecycle.KindDomainActivated, lifecycle.KindDomainSuspended, lifecycle.KindDomainClosed:
		if p.agentLaunchResolver != nil {
			transport, lane, domain := t, env.Lane, env.Domain
			lifecyclecommit.OnCommit(ctx, func() { p.agentLaunchResolver.InvalidateDomain(transport, lane, domain) })
		}
	}
	for _, out := range outs {
		switch out.Envelope.Event.Kind {
		case lifecycle.KindDomainGrant:
			// The grant is the answer to the parent's own request — it
			// grants no suppression authority and no new state, so it is
			// never deferred behind an acknowledgement (unlike accept):
			// the parent is blocked waiting for it before it can launch
			// the child.
			p.buildAndDeliverGrant(ctx, out)
		case lifecycle.KindAgentLaunchResolved:
			lifecyclecommit.OnCommit(ctx, func() { p.answerAgentLaunchResolve(ctx, t, env, out) })
		case lifecycle.KindAgentEnrolled, lifecycle.KindAgentWithdrawn:
			// Same shape and the same reason: the caller is blocked waiting
			// for the verdict before it launches the agent, and the answer
			// grants no suppression authority. The ORDER matters — the grid
			// is opened before the answer goes out, so a caller that reads
			// "enrolled" and starts the agent in the next instruction cannot
			// beat the watch it was promised. That is the byte-zero guarantee
			// the whole grid rests on.
			lifecyclecommit.OnCommit(ctx, func() { p.answerAgentEnrolment(ctx, t, env, out) })
		case lifecycle.KindAccept:
			// The shell must receive ACCEPT before the lifecycle.changed
			// prompt_ready publication. Otherwise a renderer can submit
			// against the prompt_ready fact while the domain still waits
			// for the shell's authenticated admission.
			lifecyclecommit.OnCommit(ctx, func() { p.deliverAccept(out) })
		}
	}
	p.transitionsBelow(ctx, before)
	if forceStartedProjection {
		p.publishLaneProjection(ctx, env.Lane)
	}
	p.publishLane(ctx, env.Lane)
	for _, out := range outs {
		switch out.Envelope.Event.Kind {
		case lifecycle.KindDomainGrant, lifecycle.KindAgentLaunchResolved, lifecycle.KindAgentEnrolled, lifecycle.KindAgentWithdrawn, lifecycle.KindAccept:
			continue // already delivered above, with their answers
		}
		lifecyclecommit.OnCommit(ctx, func() {
			_ = p.kernel.Deliver(out) // best-effort; the shell times out in the safe direction
		})
	}
	releaseAfterFrame()
	return nil
}

// deliverAccept flushes a minted accept on the backend's own authority
// (ADR-0062): the accept goes out exactly like refresh_request, as soon as
// the kernel has minted it, rather than waiting for a renderer that a
// backend-opened pane (WSServer.OpenSession) has none to acknowledge it.
// Logged because "flushed" and "never reached the shell" otherwise arrive
// identically — one bare `hello-timeout` line from the adapter ten seconds
// later — which is the distinction that mattered when this used to be a
// wait rather than a delivery (nocx-n14oo.8).
func (p *Publisher) deliverAccept(out lifecycle.Outbound) {
	env := out.Envelope
	if err := p.kernel.Deliver(out); err != nil {
		p.log.Warn("lifecycle: the accept could not be flushed",
			"lane", string(env.Lane), "domain", string(env.Domain), "epoch", env.Epoch,
			"error", err)
		return
	}
	p.log.Debug("lifecycle: the accept was flushed",
		"lane", string(env.Lane), "domain", string(env.Domain), "epoch", env.Epoch)
}

// Domain returns the read model of one domain, forwarding to the kernel. The
// lifecyclechannel adapter's Kernel interface requires it — the adapter
// answers its handshake timeout by asking whether the domain it minted ever
// became Established.
func (p *Publisher) Domain(id lifecycle.DomainID) (lifecycle.Domain, bool) {
	return p.kernel.Domain(id)
}

// TerminalDomainOfLane returns the lane's most recent domain in a terminal
// state (closed or lost), and whether one exists — the helper's own recorded
// end, replayed for a re-adopting attach (nocx-zg3k3.5.3 Round 10).
func (p *Publisher) TerminalDomainOfLane(lane lifecycle.LaneID) (lifecycle.Domain, bool) {
	return p.kernel.TerminalDomainOfLane(lane)
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
	var before map[lifecycle.AttemptID]bool
	if dom, ok := p.kernel.Domain(d); ok {
		lane = string(dom.Lane)
		// Framing corruption can REVOKE a lane outright (the desync budgets),
		// and a revoke closes every attempt it holds open — the same closure as
		// any other, read the same way, before the mutation.
		before = p.openAttemptsOf(dom.Lane)
	}
	outs, err := p.kernel.NotifyGap(t, d, garbageBytes, garbageFrames)
	p.transitionsBelow(context.Background(), before)
	if lane != "" {
		p.publishLane(context.Background(), lifecycle.LaneID(lane))
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
	p.mu.Lock()
	lanes := make([]lifecycle.LaneID, 0, len(p.known))
	for l := range p.known {
		lanes = append(lanes, l)
	}
	p.mu.Unlock()
	attempts := make(map[lifecycle.LaneID]lifecycle.AttemptID)
	// Loss CLOSES every attempt the affected lanes held open (the kernel marks
	// them unknown), and that closure is reported like any other — read from
	// the same snapshot the attempt below is read from, before the mutation.
	openBefore := make(map[lifecycle.LaneID]map[lifecycle.AttemptID]bool)
	for _, l := range lanes {
		st, err := p.kernel.State(l)
		if err != nil || st.Domain == "" {
			continue
		}
		d, exists := p.kernel.Domain(st.Domain)
		if !exists || d.Transport != t {
			continue
		}
		if st.Attempt != "" {
			attempts[l] = st.Attempt
		}
		openBefore[l] = p.openAttemptsIn(st)
	}
	if err := p.kernel.TransportLost(t); err != nil {
		return err
	}
	if p.agentLaunchResolver != nil {
		p.agentLaunchResolver.InvalidateTransport(t)
	}
	for _, l := range lanes {
		p.transitionsBelow(context.Background(), openBefore[l])
		if attemptID, ok := attempts[l]; ok {
			p.publishLostLane(context.Background(), l, attemptID)
			continue
		}
		p.publishLane(context.Background(), l)
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
	p.publishLane(context.Background(), lane)
	return nil
}

// SubmitAttempt forwards an app-originated attempt (created synchronously at
// editor submit, before the pty bytes) and publishes the lane's move to
// running.
func (p *Publisher) SubmitAttempt(domain lifecycle.DomainID, command, cwd, host, submitID string) (lifecycle.ExecutionAttempt, error) {
	// A submit can CLOSE an attempt: one the shell reached a prompt over and no
	// start ever attached to is closed by the next submit (kernel.go's
	// SubmitAttempt), and that closure is invisible in the fact stream for the
	// same reason the prompt_ready one is — it is read before the mutation.
	var before map[lifecycle.AttemptID]bool
	if d, ok := p.kernel.Domain(domain); ok {
		before = p.openAttemptsOf(d.Lane)
	}
	att, err := p.kernel.SubmitAttempt(domain, command, cwd, host, submitID)
	if err != nil {
		return att, err
	}
	p.transitionsBelow(context.Background(), before)
	p.publishLane(context.Background(), att.Lane)
	return att, nil
}

// AbandonAttempt forwards the explicit abandonment (native-mode escape) and
// publishes the attempt's lane: the attempt's state becomes unknown, which is
// a projection change even though the lane stays running.
func (p *Publisher) AbandonAttempt(id lifecycle.AttemptID) error {
	var before map[lifecycle.AttemptID]bool
	if att, ok := p.kernel.Attempt(id); ok {
		before = p.openAttemptsOf(att.Lane)
	}
	err := p.kernel.AbandonAttempt(id)
	if err != nil {
		return err
	}
	p.transitionsBelow(context.Background(), before)
	if att, ok := p.kernel.Attempt(id); ok {
		p.publishLane(context.Background(), att.Lane)
	}
	return nil
}

// heldTurnKey marks a context whose goroutine already holds a lane's
// emission turn (Ingest).
type heldTurnKey struct{}

// holdsTurn reports whether ctx's goroutine already holds lane's turn.
func holdsTurn(ctx context.Context, lane lifecycle.LaneID) bool {
	held, ok := ctx.Value(heldTurnKey{}).(lifecycle.LaneID)
	return ok && held == lane
}

// laneEmission returns the lane's emission turn: a one-slot semaphore that
// makes deriving a lane's fact, recording it as the dedupe baseline and
// handing it to the emitter ONE step per lane (see publishLane).
//
// A channel rather than a sync.Mutex so a goroutine waiting for its turn is
// durably blocked in the sense testing/synctest understands, which is what
// lets the race this exists for be written as a deterministic test
// (TestPublisherReplayCannotOvertakeTheFactItRaced).
func (p *Publisher) laneEmission(lane lifecycle.LaneID) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	turn, ok := p.emitting[lane]
	if !ok {
		turn = make(chan struct{}, 1)
		p.emitting[lane] = turn
	}
	return turn
}

// ReplayLane re-emits the lane's current projection unconditionally —
// bypassing the change-dedupe, which is exactly the point: a reattached
// frontend (AD-9 reconnect, protocol §12) must receive the current state
// even if no transition happened since its last view. The emission also
// refreshes the dedupe baseline, so a replay cannot suppress a later real
// change.
//
// It takes the lane's emission turn like publishLane does, because it is the
// SECOND goroutine that publishes a lane: session.open replays from the
// request handler while the lifecycle bridge it has just started ingests the
// shell's hello from its own (transport's handleOpen).
func (p *Publisher) ReplayLane(lane lifecycle.LaneID) {
	turn := p.laneEmission(lane)
	turn <- struct{}{}
	defer func() { <-turn }()
	f, ok := p.derive(lane)
	if !ok {
		return
	}
	p.mu.Lock()
	p.last[lane] = f
	e := p.emitter
	p.mu.Unlock()
	if e != nil {
		e.PublishLifecycle(context.Background(), f)
	}
}

// publishLaneProjection hands the lane's current fact to a ProjectionEmitter
// only — server-owned projections, no renderer notification, no dedupe.
func (p *Publisher) publishLaneProjection(ctx context.Context, lane lifecycle.LaneID) {
	f, ok := p.derive(lane)
	if !ok {
		return
	}
	p.mu.Lock()
	e := p.emitter
	p.mu.Unlock()
	if pe, ok := e.(ProjectionEmitter); ok {
		pe.PublishLifecycleProjection(ctx, f)
	}
}

// publishLane derives the lane's fact and emits it when it changed since the
// last emission for that lane. Derivation runs in the caller's goroutine,
// immediately after the mutation that triggered it.
//
// DERIVE, RECORD AND EMIT ARE ONE STEP PER LANE, and they were not. The
// baseline was recorded under the publisher's lock and the emitter called
// after it was released, so two goroutines publishing one lane could hand the
// emitter their facts in the opposite order to the one they derived them in.
// That is not hypothetical: session.open's replay derived `native` a moment
// before the bridge ingested the hello, the bridge then recorded and emitted
// `prompt_ready`, and the replay's `native` reached the renderer LAST. The
// baseline said prompt_ready, so the shell's own prompt_ready a second later
// was deduped as "no change" — and the pane sat at `native` with its editor
// hidden and nothing left to correct it (CI, 2026-09-19,
// remote-coordinator-reclaim: `.nocx-editor-input` hidden for the whole
// wait). Under the lane's turn, the last fact emitted is always the lane's
// state at the last derive, which follows the last mutation.
//
// The turn is per LANE, so a slow emitter (a completed attempt's fact waits
// for its Stop to settle, WSServer.signalDeliveryFor) still stalls no other
// lane's bookkeeping — only a publisher of the same lane, which has to wait
// for the earlier fact to be delivered before its own may be.
func (p *Publisher) publishLane(ctx context.Context, lane lifecycle.LaneID) {
	if !holdsTurn(ctx, lane) {
		turn := p.laneEmission(lane)
		turn <- struct{}{}
		defer func() { <-turn }()
	}
	f, ok := p.derive(lane)
	if !ok {
		return
	}
	p.mu.Lock()
	if last, seen := p.last[lane]; seen && reflect.DeepEqual(last, f) {
		p.mu.Unlock()
		return
	}
	p.last[lane] = f
	e := p.emitter
	p.mu.Unlock()
	if e != nil {
		e.PublishLifecycle(ctx, f)
	}
}

func (p *Publisher) publishLostLane(ctx context.Context, lane lifecycle.LaneID, attemptID lifecycle.AttemptID) {
	f, ok := p.derive(lane)
	if !ok {
		return
	}
	attempt, ok := p.kernel.Attempt(attemptID)
	if !ok {
		p.publishLane(ctx, lane)
		return
	}
	f.Attempt = attemptFact(attempt)
	if pe, ok := p.emitter.(ProjectionEmitter); ok {
		pe.PublishLifecycleProjection(ctx, f)
	}
	p.publishLane(ctx, lane)
}
