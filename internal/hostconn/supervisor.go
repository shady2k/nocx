package hostconn

// One supervisor per connection key, and the leases its holders hold.
//
// A SUPERVISOR EXISTS WHILE THERE IS NO CONNECTION, and that is the reason it
// is not a method on the pool: a pool entry exists only while it carries a
// transport, so the window in which a person most needs an answer — the first
// dial, and the seconds after a link died — is exactly the window a pooled
// entry cannot describe. The pool stays the connection table (design §5.1);
// this package is what says what the table's entry is doing, and what takes a
// dead transport out of it.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/monoclock"
)

// ErrLinkLost is what a holder reports when the transport under its reference
// has ended: the connection is gone, and the thing the holder was doing failed
// for that reason rather than for anything the far side said.
//
// It exists so that a caller can tell "the host refused" from "the connection
// I was on ended", which are two different sentences to a person and two
// different next moves.
var ErrLinkLost = errors.New("hostconn: the connection this reference rides has ended")

// Key identifies one connection, and it is deliberately the same five
// components AD-4's pool key carries: the endpoint, the account, the
// credential principal and the route. A supervisor keyed by less would merge
// two principals into one state; keyed by more (a channel, a pane) it would
// split one connection into several answers.
type Key struct {
	Host     string
	Port     int
	User     string
	Identity string
	Route    string
}

func (k Key) String() string {
	return fmt.Sprintf("%s@%s:%d/%s", k.User, k.Host, k.Port, k.Identity)
}

// Plan is one attempt through a carrier: the identity the supervisor keeps
// the state under, the carrier's name, and the carrier's own request.
//
// Material is opaque here on purpose (design §5.1.1's fourth trap): a plan
// that named x/crypto's types would make this package depend on the library it
// is meant to be independent of, and the carrier is the only party that reads
// it.
type Plan struct {
	Carrier  string
	Key      Key
	Material any
	// Holds is what the holder that is acquiring declares about itself — a
	// plain shell or nothing. The supervisor counts it, because nobody else
	// can: the closing policy may not end a shell that would die with the
	// connection, and the package that knows what a shell is imports the one
	// that dials (design §5.1.1's trap 2).
	Holds Holds
	// ProbeInterval is how often this connection should be asked to answer, or
	// zero for the default. The profile's setting reaches here and the policy
	// uses it; the deadline, the look cadence and the grace are the design's
	// numbers and not a profile's to change.
	//
	// The FIRST holder to dial sets it, and a later holder's request is not
	// applied: one connection has one prober, and a cadence that changed with
	// whoever acquired last would be two answers to "how often" (nocx-oalo1.4.4
	// is where the holders' requests are unified).
	ProbeInterval time.Duration
	// Route is the hops this connection is dialled through, in dial order.
	// A connection that carries a plain shell makes every one of them carry
	// one too: a hop's plain shells are its own plus those of everything
	// riding it.
	Route []Key
}

// Step is one state a supervisor passed through, and when.
//
// The time is a monotonic reading, never wall time: it answers "how long did
// this take" and nothing else, which is the only question a stage path is
// asked. A journal that outlives the process uses a different clock.
type Step struct {
	State State
	At    monoclock.Nanos
}

// Manager answers the supervisor for a key, creating it the first time that
// key is seen.
//
// Supervisors are kept for the life of the process, one per distinct key, the
// same scope and the same reason as the pool's own table: a connection's state
// outlives its transport, and a person who reconnects is looking at the same
// connection they just lost. The number of keys is the number of destinations
// this helper has been asked about, which is bounded by what a person did.
type Manager struct {
	carrier Carrier
	log     log.Logger
	clock   Clock
	mu      sync.Mutex
	sups    map[Key]*Supervisor
}

// Option configures a Manager.
type Option func(*Manager)

// WithClock is the option that makes time the caller's. The tests that state
// "within 30 s" or "after ten minutes" need it to be theirs (design §10): a
// policy whose only clock is time.Now cannot be asserted without waiting for
// it, and waiting is what the fault harness may not do.
func WithClock(clock Clock) Option {
	return func(m *Manager) {
		if clock != nil {
			m.clock = clock
		}
	}
}

// NewManager builds the manager over one carrier.
func NewManager(carrier Carrier, logger log.Logger, opts ...Option) *Manager {
	m := &Manager{carrier: carrier, log: logger, clock: RealClock{}, sups: make(map[Key]*Supervisor)}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Acquire answers a held reference to the connection described by plan,
// dialing it when the supervisor has none.
func (m *Manager) Acquire(ctx context.Context, plan Plan) (*Lease, error) {
	return m.Supervisor(plan.Key).Acquire(ctx, plan)
}

// Supervisor answers the one supervisor for a key, creating it the first time
// that key is seen. It is the same connection a person reconnects to, which is
// why it outlives every transport the key ever carried.
func (m *Manager) Supervisor(key Key) *Supervisor {
	m.mu.Lock()
	defer m.mu.Unlock()
	sup := m.sups[key]
	if sup == nil {
		sup = &Supervisor{key: key, carrier: m.carrier, log: m.log, clock: m.clock, mgr: m}
		m.sups[key] = sup
	}
	return sup
}

// Supervisor is one connection's life and state.
type Supervisor struct {
	key     Key
	carrier Carrier
	log     log.Logger
	clock   Clock
	mgr     *Manager

	mu sync.Mutex
	// state is the current point of the machine.
	state State
	// steps is the path this supervisor has taken, oldest first, bounded: a
	// connection that reconnects for a day must not grow a slice for a day.
	steps []Step
	// live is the connection the supervisor holds, with how many leases are
	// out on it; nil while there is no connection.
	live *live
	// dialing is non-nil while this supervisor is dialing. It is a channel
	// rather than a flag so that a second Acquire waits for the first dial
	// instead of starting a second one — the same shape the pool uses for its
	// own per-key single flight.
	dialing chan struct{}
	// generation is bumped for every link, so a watcher belonging to a link
	// that has since been replaced cannot move the state of the new one.
	generation uint64
	// riders is how many connections carrying a plain shell are dialled
	// THROUGH this one. It is the hop half of the count: a hop's plain shells
	// are its own plus those of everything riding it, and a hop with a rider
	// may no more be closed for silence than a connection with a shell.
	riders int
}

// live is one connection under the supervisor, and the count that decides when
// it closes. The count belongs to the CONNECTION and not to the key (design
// §5.1.1's second trap): a new connection under the same key starts at zero,
// because the holders of the connection that died are not holders of the one
// that replaced it.
type live struct {
	link        Link
	holders     int
	plainShells int
	route       []Key
	// stop ends this connection's guard: its probe cadence, its looks and any
	// parked probe's reporting. It is closed once.
	stop     chan struct{}
	stopOnce sync.Once
	// nextProbe is when the guard asks this connection to answer. It is set
	// when the connection becomes live rather than when the goroutine happens
	// to start: the cadence belongs to the connection, and a guard scheduled a
	// moment late must not move it.
	nextProbe monoclock.Nanos
	// probeInterval is this connection's probe cadence: the plan's, or the
	// design's default.
	probeInterval time.Duration
	// observers are the holders that asked to be told what a probe finds.
	// They are per lease because they are per shell: the app's liveness
	// surface is keyed by the session that asked for it.
	observers map[*Lease]func(Observation)
}

// stopGuard ends this connection's prober work.
func (l *live) stopGuard() {
	l.stopOnce.Do(func() { close(l.stop) })
}

const maxSteps = 32

// Acquire answers a held reference to this connection, dialing it when there
// is no live one.
//
// This is the supervisor's own door — the one the helper's ssh service reaches
// through the x/crypto carrier — and it is what makes a connection's state
// exist: before the first call there is a supervisor and no connection, which
// is the state a person is in while the first dial runs.
func (s *Supervisor) Acquire(ctx context.Context, plan Plan) (*Lease, error) {
	for {
		s.mu.Lock()
		if s.live != nil {
			select {
			case <-s.live.link.Done():
				// The transport is over and the watcher has not run yet. A
				// corpse is not a connection to hand to a caller — that is the
				// whole of nocx-oalo1.4.6 — so it is recorded as lost and a
				// fresh dial follows.
				stale := s.live
				s.live = nil
				s.generation++
				s.advance(Event{Kind: Lost, Cause: CauseLostLink})
				s.mu.Unlock()
				_ = stale.link.Close()
				continue
			default:
			}
			s.live.holders++
			if plan.Holds&HoldsPlainShell != 0 {
				s.live.plainShells++
				if s.live.plainShells == 1 {
					s.rideLocked(1)
				}
			}
			lease := &Lease{sup: s, held: s.live, link: s.live.link, holds: plan.Holds}
			s.mu.Unlock()
			return lease, nil
		}
		if wait := s.dialing; wait != nil {
			s.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		flight := make(chan struct{})
		s.dialing = flight
		s.beginAttempt()
		s.mu.Unlock()

		link, err := s.carrier.Dial(ctx, plan, s.report)

		s.mu.Lock()
		s.dialing = nil
		close(flight)
		if err != nil {
			// The carrier has already said what kind of failure this was
			// through the sink (refused, waiting for a person, or lost); a
			// carrier that said nothing leaves the attempt where its last stage
			// was, which is honest.
			s.mu.Unlock()
			return nil, err
		}
		probeInterval := plan.ProbeInterval
		if probeInterval <= 0 {
			probeInterval = ProbeInterval
		}
		live := &live{
			link: link, holders: 1, route: plan.Route, probeInterval: probeInterval,
			nextProbe: s.clock.Now() + span(probeInterval),
			stop:      make(chan struct{}), observers: make(map[*Lease]func(Observation)),
		}
		if plan.Holds&HoldsPlainShell != 0 {
			live.plainShells = 1
		}
		s.live = live
		s.generation++
		gen := s.generation
		s.advance(Event{Kind: SignedIn})
		if live.plainShells > 0 {
			s.rideLocked(1)
		}
		lease := &Lease{sup: s, held: live, link: link, holds: plan.Holds}
		s.mu.Unlock()

		go s.watch(link, gen)
		go s.guard(live, gen)
		return lease, nil
	}
}

// beginAttempt puts an attempt at its first step. The helper receives a
// destination the coordinator has already resolved, so nothing here resolves
// anything; `resolve` is the first state of an attempt because it is the first
// step of the path a person is shown, and the resolver moves into it with the
// pane driver (stage 3). Called with the mutex held.
func (s *Supervisor) beginAttempt() {
	s.advance(Event{Kind: Attempt})
}

// report is the sink the carrier calls while it dials.
func (s *Supervisor) report(event Event) {
	s.mu.Lock()
	s.advance(event)
	s.mu.Unlock()
}

// advance moves the machine and records where it went. Called with the mutex
// held.
func (s *Supervisor) advance(event Event) {
	next := Next(s.state, event)
	if next == s.state {
		return
	}
	moved := !samePoint(next, s.state)
	s.state = next
	if !moved {
		return
	}
	if len(s.steps) == maxSteps {
		copy(s.steps, s.steps[1:])
		s.steps = s.steps[:maxSteps-1]
	}
	s.steps = append(s.steps, Step{State: next, At: monoclock.Now()})
	// Debug: a stage path is diagnostic detail, and the wire carries the state
	// a person sees (design §5.6, stage 3). A lost connection is a Warn below
	// — that one is about a person's session, not about a dial's progress.
	s.log.Debug("hostconn: connection state",
		"connection", s.key.String(), "carrier", s.carrier.Name(), "state", next.String())
}

// watch is the one thing that notices a transport ending on its own.
//
// Nothing else in the process learns it: x/crypto stores the read error and
// returns it from every later write, so a shell that is not writing, and a
// helper path that acquires with no prober, see a connection that looks alive
// until somebody asks it something. The transport is taken out of the pool
// HERE, at the moment it ends, rather than when the last holder happens to
// unwind — which is what makes the next acquire dial afresh instead of being
// handed the corpse the owner met on 2026-11-13.
func (s *Supervisor) watch(link Link, gen uint64) {
	<-link.Done()
	s.mu.Lock()
	if s.generation == gen && s.live != nil && s.live.link == link {
		s.live = nil
		s.advance(Event{Kind: Lost, Cause: CauseLostLink})
		// The holders' leases keep pointing at this link so they can say the
		// link is lost; the supervisor's own reference ends here.
		s.log.Warn("hostconn: the connection's transport ended", "connection", s.key.String(), "error", link.Err())
	}
	s.mu.Unlock()
	// Leaving the pool is Close's whole job for the carrier (sshconn's link
	// closes the pooled reference), and it is idempotent.
	_ = link.Close()
}

// release gives one holder's reference back, and closes the connection when it
// was the last reference to THAT connection.
func (s *Supervisor) release(lease *Lease) {
	held := lease.held
	var last bool
	s.mu.Lock()
	if held.holders > 0 {
		held.holders--
	}
	if lease.holds&HoldsPlainShell != 0 && held.plainShells > 0 {
		held.plainShells--
		if held.plainShells == 0 {
			s.rideLocked(-1)
		}
	}
	delete(held.observers, lease)
	last = held.holders == 0
	if last {
		switch {
		case s.live == held:
			// The ordinary end: the last holder of the live connection lets
			// go, so it is nobody's connection any more.
			s.live = nil
			s.generation++
			s.advance(Event{Kind: Closed, Cause: CauseReleased})
		case s.live == nil && s.state.Kind == Reconnecting:
			// The transport ended while this holder still had it, and now the
			// last one has let go: there is nothing left to reconnect for.
			s.advance(Event{Kind: Closed, Cause: CauseReleased})
		}
	}
	s.mu.Unlock()
	if last {
		held.stopGuard()
		_ = lease.link.Close()
	}
}

// rideLocked tells the hops this connection is dialled through that a plain
// shell rides them, or that the last one has left. Called with the mutex held;
// `delta` is +1 or -1.
func (s *Supervisor) rideLocked(delta int) {
	if s.mgr == nil || s.live == nil {
		return
	}
	for _, hop := range s.live.route {
		s.mgr.Supervisor(hop).addRider(delta)
	}
}

// addRider counts one connection carrying a plain shell that is dialled through
// this one. A hop with a rider may not be closed for silence, exactly as a
// connection with a shell may not (design §5.1.1's second trap).
func (s *Supervisor) addRider(delta int) {
	s.mu.Lock()
	s.riders += delta
	if s.riders < 0 {
		s.riders = 0
	}
	s.mu.Unlock()
}

// HoldsPlainShell reports whether some holder of this connection — its own or
// a connection riding it — carries a plain shell. It is what the closing policy
// asks, and the answer is never allowed to end one.
func (s *Supervisor) HoldsPlainShell() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.riders > 0 {
		return true
	}
	return s.live != nil && s.live.plainShells > 0
}

// Observe asks to be told what each probe of this connection finds. The
// registration belongs to the lease that made it and ends with it.
func (l *Lease) Observe(fn func(Observation)) {
	if l == nil || fn == nil {
		return
	}
	l.sup.mu.Lock()
	if l.sup.live == l.held {
		l.held.observers[l] = fn
	}
	l.sup.mu.Unlock()
}

// observe reports one probe result to every holder that asked.
func (s *Supervisor) observe(held *live, obs Observation) {
	s.mu.Lock()
	fns := make([]func(Observation), 0, len(held.observers))
	for _, fn := range held.observers {
		fns = append(fns, fn)
	}
	current := s.live == held
	s.mu.Unlock()
	if !current {
		return
	}
	for _, fn := range fns {
		fn(obs)
	}
}

// States answers the states this supervisor has passed through, oldest first.
func (s *Supervisor) States() []State {
	steps := s.Steps()
	states := make([]State, len(steps))
	for i, step := range steps {
		states[i] = step.State
	}
	return states
}

// Steps answers the path this connection has taken, with the monotonic reading
// each state was reached at.
func (s *Supervisor) Steps() []Step {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Step(nil), s.steps...)
}

// Current answers the state the machine is in.
func (s *Supervisor) Current() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Lease is one holder's reference to the connection a supervisor owns.
//
// The holder does not own a POOL reference of its own: the connection's
// reference is the supervisor's, and what a holder gets is the right to use it
// for as long as it holds the lease. That is design §5.1's second trap taken
// literally — the count that decides when a connection closes belongs to the
// connection, not to each caller's chance of being first to release it.
type Lease struct {
	sup   *Supervisor
	held  *live
	link  Link
	holds Holds
	once  sync.Once
}

// Link is the connection this lease rides.
func (l *Lease) Link() Link {
	if l == nil {
		return nil
	}
	return l.link
}

// Release gives the reference back. It is idempotent: a caller whose own
// teardown runs twice must not close a connection somebody else is using.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		l.sup.release(l)
	})
}

// Lost reports whether the transport under this lease has ended.
func (l *Lease) Lost() bool {
	if l == nil || l.link == nil {
		return false
	}
	select {
	case <-l.link.Done():
		return true
	default:
		return false
	}
}

// States answers the states the connection this lease rides has passed
// through, oldest first.
func (l *Lease) States() []State {
	if l == nil || l.sup == nil {
		return nil
	}
	return l.sup.States()
}

// Steps answers the path the connection this lease rides has taken.
func (l *Lease) Steps() []Step {
	if l == nil || l.sup == nil {
		return nil
	}
	return l.sup.Steps()
}
