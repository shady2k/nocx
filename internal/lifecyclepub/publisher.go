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
	"encoding/hex"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
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
	Attempt(id lifecycle.AttemptID) (lifecycle.ExecutionAttempt, bool)
	OpenAttempt(domain lifecycle.DomainID) (lifecycle.ExecutionAttempt, bool)
}

// Option configures a Publisher.
type Option func(*options)

type options struct {
	grantBuilder  GrantBuilder
	agentEnroller AgentEnroller
	agentReporter AgentReporter
	log           nocxlog.Logger
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

// AgentReporter records what a worker participant says its own work produced
// (nocx-dkawo.7). It is a SEPARATE seam from the enroller above, not a third
// method on it, because the two answer different questions and one of them is
// almost always absent: every integrated shell may enrol, and only a pane the
// backend spawned as a participant has a record to declare into.
//
// Report returns an error the participant is shown, for the same reason
// Enrol does: a declaration that could not be recorded must say so in the
// person's own pane rather than in a log they never read.
type AgentReporter interface {
	Report(lane lifecycle.LaneID, ok bool, summary string) error
}

// WithAgentReporter wires the seam behind the agent_report / agent_reported
// pair. Without it every report is REFUSED and says so — a declaration that
// looked accepted while nothing recorded it is the silent degrade this whole
// record exists to prevent.
func WithAgentReporter(r AgentReporter) Option {
	return func(o *options) { o.agentReporter = r }
}

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

// Emitter is where published facts go: the WSServer at the composition root,
// which routes them to the lane's session's current subscriber. The emitter
// is bound post-construction (SetEmitter) because it is the transport, which
// is built after the kernel; facts cannot exist before a session spawns a
// shell, which is long after both exist, so the unbound window is empty in
// practice.
type Emitter interface {
	PublishLifecycle(f Fact)
}

// ProjectionEmitter receives lifecycle facts that must update server-owned
// projections without creating a duplicate renderer notification.
type ProjectionEmitter interface {
	PublishLifecycleProjection(f Fact)
}

// Publisher wraps the kernel, forwards every mutation, and projects the
// affected lane into a Fact on each change. It is safe for concurrent use:
// per-lane serialization comes from the kernel (and from the single adapter
// pump per lane); the publisher's own lock protects its bookkeeping.
type Publisher struct {
	kernel Kernel

	mu            sync.Mutex
	emitter       Emitter
	last          map[lifecycle.LaneID]Fact
	known         map[lifecycle.LaneID]struct{}
	dest          map[lifecycle.DomainID]Destination // ssh children's destinations (nocx-ax79)
	grantBuilder  GrantBuilder
	agentEnroller AgentEnroller
	agentReporter AgentReporter
	log           nocxlog.Logger
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
		kernel:        k,
		last:          make(map[lifecycle.LaneID]Fact),
		known:         make(map[lifecycle.LaneID]struct{}),
		dest:          make(map[lifecycle.DomainID]Destination),
		grantBuilder:  o.grantBuilder,
		agentEnroller: o.agentEnroller,
		agentReporter: o.agentReporter,
		log:           o.log,
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
	p.publishLane(lane)
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
func (p *Publisher) answerAgentEnrolment(ask lifecycle.Envelope, out lifecycle.Outbound) {
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
			err := p.agentEnroller.Enrol(lane, ans.Agent, req.Cols, req.Rows)
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

// answerAgentReport fills the verdict and delivers it.
//
// Every silent path is a refusal, exactly as in answerAgentEnrolment: a nil
// seam, a nil payload or a seam that errored all leave Recorded false. Nothing
// here can produce a "recorded" except a seam that actually wrote the
// declaration and said so.
func (p *Publisher) answerAgentReport(ask lifecycle.Envelope, out lifecycle.Outbound) {
	ans := out.Envelope.Event.AgentReported
	if ans == nil {
		_ = p.kernel.Deliver(out)
		return
	}
	req := ask.Event.AgentReport
	switch {
	case p.agentReporter == nil:
		ans.Reason = "this backend is not wired to record what an agent produced"
	case req == nil:
		ans.Reason = "the report carried no declaration"
	default:
		if err := p.agentReporter.Report(out.Envelope.Lane, req.OK, req.Summary); err != nil {
			ans.Reason = err.Error()
		} else {
			ans.Recorded = true
		}
	}
	_ = p.kernel.Deliver(out)
}

// projection, ordering the replies: mutation → publish → deliver. Published
// on failure as well as success: the one mutation a kernel makes on a
// rejected frame (the domain is closed and the lane falls to native while
// the frame is being quarantined) is a state change the renderer must see.
// Every other rejection leaves the projection unchanged and the change-dedupe
// suppresses the emission.
//
// ADR-0062 retired the wait this comment used to describe: an accept-
// producing hello used to open an establishment episode and hold the accept
// until a renderer acknowledgement flushed it, so a pane the backend itself
// opened — which subscribes nobody — could never establish. The accept now
// goes out with refresh_request in the same delivery pass below, on the
// backend's own authority, as soon as the kernel has minted it. Nothing
// about the ORDER changed: publish still precedes delivery, and
// refresh_request is still never deferred behind it — it restores authority
// and visible-prompt behaviour, grants no suppression authority, and
// delaying it behind frontend publication can only prolong a
// desynchronization.
func (p *Publisher) shouldPublishStartedAttempt(env lifecycle.Envelope) bool {
	if env.Event.Kind != lifecycle.KindStart || env.Event.Start == nil || env.Event.Start.AttemptID != nil {
		return false
	}
	before, ok := p.derive(env.Lane)
	if !ok || before.Attempt == nil {
		return false
	}
	attempt, ok := p.kernel.Attempt(lifecycle.AttemptID(before.Attempt.ID))
	return ok && !attempt.Started
}

func (p *Publisher) Ingest(t lifecycle.TransportID, env lifecycle.Envelope) error {
	forceStartedProjection := p.shouldPublishStartedAttempt(env)
	outs, err := p.kernel.Ingest(t, env)
	if err != nil {
		p.publishLane(env.Lane)
		return err
	}
	for _, out := range outs {
		switch out.Envelope.Event.Kind {
		case lifecycle.KindDomainGrant:
			// The grant is the answer to the parent's own request — it
			// grants no suppression authority and no new state, so it is
			// never deferred behind an acknowledgement (unlike accept):
			// the parent is blocked waiting for it before it can launch
			// the child.
			p.buildAndDeliverGrant(out)
		case lifecycle.KindAgentReported:
			// A declaration is answered on the same terms as an enrolment:
			// the participant is blocked on the verdict, and Recorded stays
			// false unless a seam actually wrote the fact.
			p.answerAgentReport(env, out)
		case lifecycle.KindAgentEnrolled, lifecycle.KindAgentWithdrawn:
			// Same shape and the same reason: the caller is blocked waiting
			// for the verdict before it launches the agent, and the answer
			// grants no suppression authority. The ORDER matters — the grid
			// is opened before the answer goes out, so a caller that reads
			// "enrolled" and starts the agent in the next instruction cannot
			// beat the watch it was promised. That is the byte-zero guarantee
			// the whole grid rests on.
			p.answerAgentEnrolment(env, out)
		}
	}
	if forceStartedProjection {
		p.publishLaneProjection(env.Lane)
	}
	p.publishLane(env.Lane)
	for _, out := range outs {
		switch out.Envelope.Event.Kind {
		case lifecycle.KindDomainGrant,
			lifecycle.KindAgentEnrolled, lifecycle.KindAgentWithdrawn:
			continue // already delivered above, with their answers
		case lifecycle.KindAccept:
			p.deliverAccept(out)
			continue
		}
		_ = p.kernel.Deliver(out) // best-effort; the shell times out in the safe direction
	}
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
	p.mu.Lock()
	lanes := make([]lifecycle.LaneID, 0, len(p.known))
	for l := range p.known {
		lanes = append(lanes, l)
	}
	p.mu.Unlock()
	attempts := make(map[lifecycle.LaneID]lifecycle.AttemptID)
	for _, l := range lanes {
		st, err := p.kernel.State(l)
		if err != nil || st.Attempt == "" || st.Domain == "" {
			continue
		}
		d, exists := p.kernel.Domain(st.Domain)
		if exists && d.Transport == t {
			attempts[l] = st.Attempt
		}
	}
	if err := p.kernel.TransportLost(t); err != nil {
		return err
	}
	for _, l := range lanes {
		if attemptID, ok := attempts[l]; ok {
			p.publishLostLane(l, attemptID)
			continue
		}
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
func (p *Publisher) SubmitAttempt(domain lifecycle.DomainID, command, cwd, host, submitID string) (lifecycle.ExecutionAttempt, error) {
	att, err := p.kernel.SubmitAttempt(domain, command, cwd, host, submitID)
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
	p.last[lane] = f
	e := p.emitter
	p.mu.Unlock()
	if e != nil {
		e.PublishLifecycle(f)
	}
}

// publishLane derives the lane's fact and emits it when it changed since the
// last emission for that lane. Derivation runs in the caller's goroutine,
func (p *Publisher) publishLaneProjection(lane lifecycle.LaneID) {
	f, ok := p.derive(lane)
	if !ok {
		return
	}
	p.mu.Lock()
	e := p.emitter
	p.mu.Unlock()
	if pe, ok := e.(ProjectionEmitter); ok {
		pe.PublishLifecycleProjection(f)
	}
}

// immediately after the mutation that triggered it; the emitter call happens
// outside the publisher's lock so a slow WebSocket write cannot stall another
// lane's bookkeeping.
func (p *Publisher) publishLane(lane lifecycle.LaneID) {
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
		e.PublishLifecycle(f)
	}
}

func (p *Publisher) publishLostLane(lane lifecycle.LaneID, attemptID lifecycle.AttemptID) {
	f, ok := p.derive(lane)
	if !ok {
		return
	}
	attempt, ok := p.kernel.Attempt(attemptID)
	if !ok {
		p.publishLane(lane)
		return
	}
	f.Attempt = attemptFact(attempt)
	if pe, ok := p.emitter.(ProjectionEmitter); ok {
		pe.PublishLifecycleProjection(f)
	}
	p.publishLane(lane)
}
