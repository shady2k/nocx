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
	"sync"
	"sync/atomic"
	"time"

	"github.com/shady2k/nocx/internal/connkey"
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

// Key identifies one connection. It IS internal/connkey's key — one type, one
// derivation (internal/ssh's PooledSpec.Key), so the pool's table and the
// supervisor's state cannot disagree about which connections are one.
type Key = connkey.Key

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
	// ProbeInterval is how often this connection should be asked to answer, as
	// the holder's profile states it — zero for "no opinion", which the
	// connection answers with the design's default. The profile's setting
	// reaches here and the policy uses it; the look cadence, the grace and the
	// close budgets are the design's numbers and not a profile's to change.
	//
	// WHICHEVER HOLDER DIALS FIRST, the cadence is the profile's when a holder
	// states one: one connection has one prober, and in the shipped open order
	// the shell-integration publish dials before the shell that carries the
	// profile. The rule is therefore "the smallest STATED interval wins" — a
	// stated interval beats the default, and among stated ones the tightest is
	// the answer to "how often", because a holder that needs a faster answer
	// is the one whose evidence is used (design §5.3's "a profile's keepalive
	// settings still apply and map onto these"). A connection's cadence only
	// ever tightens, so a holder that leaves cannot slow the prober down under
	// another one that asked for speed.
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
	wake    WakeSource
	mu      sync.Mutex
	sups    map[Key]*Supervisor
}

// Option configures a Manager.
type Option func(*Manager)

// WithWakeSource is the option that gives the manager the machine's wake
// signal. Every supervisor is told about every wake: each connection proves
// itself, and one that is already proving itself is not asked twice (design
// §5.3).
func WithWakeSource(src WakeSource) Option {
	return func(m *Manager) {
		if src != nil {
			m.wake = src
		}
	}
}

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
	if m.wake != nil {
		go m.watchControl()
	}
	return m
}

// controlEdge is one OS control edge on its way to a connection's guard: the
// machine is going to sleep, or it has woken, at a clock reading.
//
// The edges are ONE ordered stream rather than two channels, because the order
// is the fact: a wake must never be applied before the sleep that precedes it
// (the connection would stay suspended, which is the defect the review found).
// The guard still compares the readings it receives, so a stream that arrives
// out of order — two goroutines' sends meeting in a select — cannot invert
// them.
type controlEdge struct {
	sleep bool
	at    monoclock.Nanos
}

// watchControl reads both OS inputs in ONE goroutine and fans each edge out to
// every supervisor, tagged with which it is.
func (m *Manager) watchControl() {
	var sleeps <-chan monoclock.Nanos
	if src, ok := m.wake.(SleepSource); ok {
		sleeps = src.Sleeps()
	}
	for {
		select {
		case at, ok := <-m.wake.Wakes():
			if !ok {
				return
			}
			m.controlAll(controlEdge{at: at})
		case at, ok := <-sleeps:
			if !ok {
				// The sleep input ended; the wakes still arrive, and the
				// supervisor's pause is a sleep's to clear only.
				sleeps = nil
				continue
			}
			m.controlAll(controlEdge{sleep: true, at: at})
		}
	}
}

// controlAll hands one edge to every supervisor.
func (m *Manager) controlAll(edge controlEdge) {
	m.mu.Lock()
	sups := make([]*Supervisor, 0, len(m.sups))
	for _, sup := range m.sups {
		sups = append(sups, sup)
	}
	m.mu.Unlock()
	for _, sup := range sups {
		sup.control(edge)
	}
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
		sup = &Supervisor{
			key: key, carrier: m.carrier, log: m.log, clock: m.clock, mgr: m,
			changed: make(chan struct{}),
		}
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
	// changed is closed and replaced on every state move, so an Acquire
	// waiting for a stalled connection to recover (or to end) wakes on the
	// change rather than on a duration.
	changed chan struct{}
	// riders is how many connections carrying a plain shell are dialled
	// THROUGH this one. It is the hop half of the count: a hop's plain shells
	// are its own plus those of everything riding it, and a hop with a rider
	// may no more be closed for silence than a connection with a shell.
	riders int
	// wakeAt and sleepAt are the latest OS control edges seen while no
	// connection was live to take them — a wake during the dial's own window
	// must still make the connection prove itself at once (design §5.3, Q4).
	wakeAt  monoclock.Nanos
	sleepAt monoclock.Nanos
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
	// rideHops are the hops this GENERATION holds a rider on, and riding says
	// whether it does. Rider accounting follows the generation the lease
	// holds, never the supervisor's current pointer: an old lease letting go
	// must not subtract a new connection's protection.
	rideHops []Key
	riding   bool
	// closing is set under the mutex when this connection is being retired:
	// admission is fenced at that instant, and Acquire waits on closed rather
	// than dialing into a pool that still holds this transport.
	closing atomic.Bool
	// closed is closed when the physical teardown has finished.
	closed chan struct{}
	// stop ends this connection's guard.
	stop     chan struct{}
	stopOnce sync.Once
	// controlWake nudges the guard when an OS edge is waiting, and the two
	// readings beside it ARE the waiting edges: one newest per kind, coalesced
	// under controlMu. Coalescing rather than a channel means an edge is never
	// dropped — a final wake always arrives — and a burst becomes the newest
	// fact, which is all a probe policy needs.
	controlWake chan struct{}
	controlMu   sync.Mutex
	sleepEdge   monoclock.Nanos
	wakeEdge    monoclock.Nanos
	// ask asks the guard for one probe now, for a caller that needs the far
	// side's own ordering (the pool's release barrier).
	ask chan struct{}
	// waiters are the callers waiting for the next probe's outcome.
	waiters map[chan error]struct{}
	// suspended is set by the guard when the machine is going to sleep and
	// cleared by the wake. The guard writes it; a holder and the journal read
	// it ("my connection is paused because the machine is asleep").
	suspended atomic.Bool
	// nextProbe is when the guard asks this connection to answer, and
	// initialWokeAt carries a wake that arrived while this connection was
	// being dialled — the guard starts with it, so the five-second proof is
	// not lost between a successful dial and its publication.
	nextProbe    monoclock.Nanos
	initialWoke  monoclock.Nanos
	initialSleep monoclock.Nanos
	// probeEvery is the cadence a holder STATED, in nanoseconds, or zero when
	// no holder has stated one. It is atomic because the guard reads it on
	// every pass while Acquire writes it under the supervisor mutex: the guard
	// is the loop that must not wait for a lock a dial holds.
	//
	// Zero means "nobody asked", and the effective cadence is then the
	// design's default — which is why a stored value of zero and an unstated
	// one can share this field: the default is not a stated value and must not
	// win over one.
	probeEvery atomic.Int64
	// observers are the holders that asked to be told what a probe finds.
	observers map[*Lease]func(Observation)
}

// stopGuard ends this connection's prober work.
func (l *live) stopGuard() {
	l.stopOnce.Do(func() { close(l.stop) })
}

// interval answers the cadence this connection is asked at: the tightest
// interval a holder stated, or the design's default when none has.
func (l *live) interval() time.Duration {
	if stated := l.probeEvery.Load(); stated > 0 {
		return time.Duration(stated)
	}
	return ProbeInterval
}

// deadline answers the soft deadline for a probe sent now: the design's 15 s,
// or the interval itself when that is shorter. A probe must have been given
// its answer before the next one is due, so a one-second cadence means a
// one-second deadline — otherwise a person who asked to be told quickly would
// be told a quarter of a minute later at the earliest.
func (l *live) deadline() time.Duration {
	if every := l.interval(); every < ProbeDeadline {
		return every
	}
	return ProbeDeadline
}

// adoptInterval answers a holder's stated cadence, and reports whether it
// changed anything. The rule is Plan.ProbeInterval's: the smallest STATED
// interval wins, a stated one beats the default, and a connection's cadence
// only ever tightens.
//
// A CHANGE ASKS FOR ONE PROBE AT ONCE. A connection dialled against the
// fifteen-second default has its first probe due fifteen seconds later, and a
// shell that joins in the same open order asking for a second must not wait
// that long to be answered: that answer is also what fixes the cadence from
// then on (probeResult measures the next probe from the send). The signal is
// the pool barrier's own — "ask now" — so this adds no second mechanism.
func (l *live) adoptInterval(stated time.Duration) bool {
	if stated <= 0 {
		return false
	}
	for {
		cur := l.probeEvery.Load()
		if cur > 0 && cur <= int64(stated) {
			return false
		}
		if l.probeEvery.CompareAndSwap(cur, int64(stated)) {
			if !l.suspended.Load() {
				select {
				case l.ask <- struct{}{}:
				default:
				}
			}
			return true
		}
	}
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
		var (
			fenced   chan struct{}
			retire   *live
			cause    Cause
			detach   *live
			detached bool
			changed  chan struct{}
		)
		s.mu.Lock()
		if l := s.live; l != nil {
			switch {
			case l.closing.Load():
				// Admission is fenced while the physical teardown runs. The
				// caller waits for it rather than dialing: the old transport is
				// still in the pool until the close finishes, and handing it
				// out is the defect nocx-oalo1.4.6 exists for.
				fenced = l.closed
			case linkDead(l.link):
				// The transport has ended, whether or not the watcher has run:
				// the carrier marks it dead at the moment the library closes
				// the socket, so this window is not "before the goroutine ran".
				retire, cause = l, CauseLostLink
			case linkTainted(l.link):
				// THE POOL HAS RETIRED THIS CONNECTION FROM NEW ADMISSION, and
				// that is all it means: the holders already on it — a plain
				// shell among them — keep their connection until their last
				// release (the pool's own behaviour, except the explicit cap
				// escalation, which closes at once and whose dead mark this
				// supervisor sees). So the connection is DETACHED here, not
				// retired: a new caller dials afresh, and nobody's session is
				// killed by a decision about admission.
				detach = l
			default:
				select {
				case <-l.link.Done():
					retire, cause = l, CauseLostLink
				default:
				}
			}
			if detach != nil {
				// Detached under this hold, so no acquire can join it, and its
				// transport is left to its holders.
				s.live = nil
				s.generation++
				s.advance(Event{Kind: Lost, Cause: CauseTainted})
				detached = true
			}
			if retire == nil && fenced == nil && !detached {
				if s.state.Kind == Stalled {
					// NO CHANNEL IS ASKED OF A STALLED CONNECTION (design
					// §5.3): NewSession takes no deadline and would park. The
					// acquire waits for the state to move — recovered, ended,
					// or the caller's deadline.
					changed = s.changed
				} else {
					// A HOLDER THAT STATES A CADENCE REACHES THE ONE PROBER
					// (design §5.3). The shipped open order dials through the
					// shell-integration publish before the shell exists, so the
					// profile's number arrives second — and it is the shell's,
					// the one a person set.
					l.adoptInterval(plan.ProbeInterval)
					l.holders++
					if plan.Holds&HoldsPlainShell != 0 {
						l.plainShells++
						if l.plainShells == 1 {
							s.rideLocked(l, 1)
						}
					}
					lease := &Lease{sup: s, held: l, link: l.link, holds: plan.Holds}
					s.mu.Unlock()
					return lease, nil
				}
			}
			s.mu.Unlock()
			switch {
			case fenced != nil:
				select {
				case <-fenced:
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			case changed != nil:
				select {
				case <-changed:
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			default:
				s.retireLive(retire, Event{Kind: Lost, Cause: cause}, "")
				continue
			}
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
		dialStarted := s.clock.Now()
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
		live := &live{
			link: link, holders: 1, route: plan.Route, rideHops: plan.Route,
			stop:        make(chan struct{}),
			closed:      make(chan struct{}),
			controlWake: make(chan struct{}, 1),
			ask:         make(chan struct{}, 1),
			waiters:     make(map[chan error]struct{}),
			observers:   make(map[*Lease]func(Observation)),
		}
		if plan.ProbeInterval > 0 {
			// The dialling holder's own cadence, recorded as STATED: a later
			// holder's request is measured against a stated one, never against
			// the default.
			live.probeEvery.Store(int64(plan.ProbeInterval))
		}
		live.nextProbe = s.clock.Now() + span(live.interval())
		if plan.Holds&HoldsPlainShell != 0 {
			live.plainShells = 1
		}
		// AN EDGE THAT ARRIVED WHILE THIS CONNECTION WAS BEING DIALED IS NOT
		// LOST (design §5.3): a wake makes it prove itself at once, with the
		// wake's own five seconds; a sleep with no later wake publishes the
		// connection suspended, so it asks a sleeping machine nothing.
		if s.wakeAt != 0 && s.wakeAt >= dialStarted && s.wakeAt >= s.sleepAt {
			live.initialWoke = s.wakeAt
			live.nextProbe = s.clock.Now()
		} else if s.sleepAt != 0 && s.sleepAt >= dialStarted {
			live.initialSleep = s.sleepAt
		}
		s.live = live
		s.generation++
		gen := s.generation
		s.advance(Event{Kind: SignedIn})
		if live.plainShells > 0 {
			s.rideLocked(live, 1)
		}
		lease := &Lease{sup: s, held: live, link: link, holds: plan.Holds}
		s.mu.Unlock()

		go s.watch(link, gen)
		go s.guard(live, gen)
		return lease, nil
	}
}

// fenceLocked marks one connection as closing, and reports whether THIS caller
// did it. It must be called with the admission mutex held, by every closer:
// once it returns true no acquire can join the connection, because Acquire
// reads `closing` under the same mutex it increments holders under.
func (s *Supervisor) fenceLocked(l *live) bool {
	return l != nil && l.closing.CompareAndSwap(false, true)
}

// finishFenced tears a connection down after its fence: the physical close runs
// OUTSIDE the supervisor mutex — the library's own close waits for its
// key-exchange loop, and that loop's host-key callback must never find this
// mutex held (a rekey would deadlock) — and the connection is then cleared and
// its `closed` fence announced, under one hold. The live stays in `s.live` with
// `closing` set for the whole physical close, so an acquire that arrives during
// it waits on `closed` instead of dialing into a pool that still holds the
// transport.
func (s *Supervisor) finishFenced(l *live, event Event) {
	l.stopGuard()
	_ = l.link.Close()

	s.mu.Lock()
	if s.live == l {
		s.live = nil
		s.generation++
		s.advance(event)
	}
	close(l.closed)
	s.mu.Unlock()
}

// retireLive ends one connection: the fence happens under the admission mutex,
// the transport is closed outside it, and the connection is cleared and
// announced after.
func (s *Supervisor) retireLive(l *live, event Event, why string) {
	s.mu.Lock()
	if !s.fenceLocked(l) {
		s.mu.Unlock()
		return
	}
	s.dropRidersLocked(l)
	s.notifyWaiters(l, ErrLinkLost)
	s.mu.Unlock()

	s.finishFenced(l, event)
	if why != "" {
		s.log.Warn("hostconn: closing a connection", "connection", s.key.String(), "why", why)
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
	if s.changed != nil {
		close(s.changed)
		s.changed = make(chan struct{})
	}
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
// until somebody asks it something. The carrier marks the transport dead the
// moment the library closes it (so admission stops at that instant, not when
// this goroutine runs), and this watcher retires the connection and reports it.
func (s *Supervisor) watch(link Link, gen uint64) {
	<-link.Done()
	s.mu.Lock()
	live := s.live
	if live == nil || live.link != link || s.generation != gen {
		// The connection this watcher belongs to has already been replaced or
		// retired; its retirer closed it.
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.log.Warn("hostconn: the connection's transport ended", "connection", s.key.String(), "error", link.Err())
	s.retireLive(live, Event{Kind: Lost, Cause: CauseLostLink}, "")
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
			s.rideLocked(held, -1)
		}
	}
	delete(held.observers, lease)
	last = held.holders == 0
	// THE FENCE HAPPENS UNDER THIS SAME HOLD (review verification, the
	// last-release race). Decrementing to zero, unlocking, and only then
	// marking the connection closing left a window in which an acquire joined
	// a connection nobody held — and the release it had already committed to
	// then closed it under the new holder.
	fenced := last && s.fenceLocked(held)
	if fenced {
		s.dropRidersLocked(held)
		s.notifyWaiters(held, ErrLinkLost)
		if s.live == nil && s.state.Kind == Reconnecting {
			// The transport ended while this holder still had it, and now the
			// last one has let go: there is nothing left to reconnect for.
			s.advance(Event{Kind: Closed, Cause: CauseReleased})
		}
	}
	s.mu.Unlock()
	if fenced {
		// The live stays in s.live (closing) through the physical close, so an
		// acquire in that window waits on its `closed` fence.
		s.finishFenced(held, Event{Kind: Closed, Cause: CauseReleased})
	}
}

// rideLocked tells the hops THIS GENERATION is dialled through that a plain
// shell rides them, or that the last one has left. The hops are the live's own,
// not the supervisor's current pointer: an old lease letting go must not
// subtract a new connection's protection. Called with the mutex held; `delta`
// is +1 or -1.
func (s *Supervisor) rideLocked(l *live, delta int) {
	if s.mgr == nil || l == nil {
		return
	}
	if delta > 0 {
		if l.riding {
			return
		}
		l.riding = true
		for _, hop := range l.rideHops {
			s.mgr.Supervisor(hop).addRider(1)
		}
		return
	}
	s.dropRidersLocked(l)
}

// dropRidersLocked removes this generation's rider contribution once.
func (s *Supervisor) dropRidersLocked(l *live) {
	if s.mgr == nil || l == nil || !l.riding {
		return
	}
	l.riding = false
	for _, hop := range l.rideHops {
		s.mgr.Supervisor(hop).addRider(-1)
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
	return s.holdsPlainShellLocked()
}

// holdsPlainShellLocked is HoldsPlainShell for a caller that already holds the
// mutex: the closing policy rechecks it under the same lock it closes under, so
// a shell acquired in between cannot be closed for silence.
func (s *Supervisor) holdsPlainShellLocked() bool {
	if s.riders > 0 {
		return true
	}
	if s.live == nil {
		return false
	}
	return s.live.plainShells > 0
}

// Observe asks to be told what each probe of this connection finds. The
// registration belongs to the lease that made it and ends with it.
func (l *Lease) Observe(fn func(Observation)) {
	if l == nil || fn == nil {
		return
	}
	l.sup.mu.Lock()
	if l.sup.live == l.held && !l.held.closing.Load() {
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

// control hands one OS edge to this connection, or remembers it while there is
// no connection to take it (a wake during the dial must not be lost).
func (s *Supervisor) control(edge controlEdge) {
	s.mu.Lock()
	if edge.sleep {
		s.sleepAt = edge.at
	} else {
		s.wakeAt = edge.at
	}
	live := s.live
	closing := live != nil && live.closing.Load()
	s.mu.Unlock()
	if live == nil || closing {
		return
	}
	live.queue(edge)
}

// queue keeps the NEWEST edge of its kind and nudges the guard. Nothing is
// dropped: a wake that arrives after a sleep replaces the sleep's kind's
// pending value, and the last wake of a burst is what the guard applies.
func (l *live) queue(edge controlEdge) {
	l.controlMu.Lock()
	if edge.sleep {
		if edge.at >= l.sleepEdge {
			l.sleepEdge = edge.at
		}
	} else if edge.at >= l.wakeEdge {
		l.wakeEdge = edge.at
	}
	l.controlMu.Unlock()
	select {
	case l.controlWake <- struct{}{}:
	default:
	}
}

// takeEdges answers the edges waiting, oldest kind first, and clears them.
func (l *live) takeEdges() []controlEdge {
	l.controlMu.Lock()
	defer l.controlMu.Unlock()
	var edges []controlEdge
	if l.sleepEdge != 0 {
		edges = append(edges, controlEdge{sleep: true, at: l.sleepEdge})
		l.sleepEdge = 0
	}
	if l.wakeEdge != 0 {
		edges = append(edges, controlEdge{at: l.wakeEdge})
		l.wakeEdge = 0
	}
	return edges
}

// Wake tells this connection the machine has woken from sleep: prove yourself
// now, in the policy's five seconds from `at`.
func (s *Supervisor) Wake(at monoclock.Nanos) { s.control(controlEdge{at: at}) }

// Suspend tells this connection the machine is about to sleep. The guard stops
// asking for answers until a wake arrives: a probe sent into the sleep would
// park across it and its silence would be evidence of nothing.
func (s *Supervisor) Suspend(at monoclock.Nanos) { s.control(controlEdge{sleep: true, at: at}) }

// Suspended reports whether the machine is known to be going to sleep and this
// connection has not been woken since. It is the fact a connection's journal
// records and a surface will draw (stage 3); today the guard and the tests read
// it.
func (s *Supervisor) Suspended() bool {
	s.mu.Lock()
	live := s.live
	s.mu.Unlock()
	return live != nil && live.suspended.Load()
}

// Probe asks this connection to produce evidence that the far side is
// answering, and waits for it, bounded by ctx.
//
// It is the pool's release barrier: after a session was closed and the next
// open was refused, the far side has to have completed one request before the
// request is re-asked, and the connection has exactly ONE prober that can get
// that answer. A probe already parked is the evidence (it will answer on the
// same far side pass), so no second request is sent; if none is outstanding one
// is started now.
func (s *Supervisor) Probe(ctx context.Context) error {
	s.mu.Lock()
	live := s.live
	if live == nil || live.closing.Load() {
		s.mu.Unlock()
		return ErrLinkLost
	}
	out := make(chan error, 1)
	live.waiters[out] = struct{}{}
	s.mu.Unlock()

	select {
	case live.ask <- struct{}{}:
	default:
	}

	select {
	case err := <-out:
		return err
	case <-ctx.Done():
		s.mu.Lock()
		delete(live.waiters, out)
		s.mu.Unlock()
		return ctx.Err()
	}
}

// notifyWaiters tells every caller waiting for a probe answer what happened.
// Called with the mutex held.
func (s *Supervisor) notifyWaiters(l *live, err error) {
	for ch := range l.waiters {
		select {
		case ch <- err:
		default:
		}
		delete(l.waiters, ch)
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

// Current answers the state of the connection this lease rides.
func (l *Lease) Current() State {
	if l == nil || l.sup == nil {
		return State{}
	}
	return l.sup.Current()
}

// Suspended reports whether the connection this lease rides is paused because
// the machine is going to sleep.
func (l *Lease) Suspended() bool {
	if l == nil || l.held == nil {
		return false
	}
	return l.held.suspended.Load()
}

// Lost reports whether the transport under this lease has ended.
func (l *Lease) Lost() bool {
	if l == nil || l.link == nil {
		return false
	}
	if dead, ok := l.link.(DeadLink); ok && dead.Dead() {
		return true
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

// DeadLink is what a Link offers when it can say, synchronously, that its
// transport has ended. The supervisor consults it before handing a connection
// to a caller: a carrier that can answer closes the window between the
// transport dying and the Done channel closing.
type DeadLink interface {
	Dead() bool
}

// TaintedLink is what a Link offers when its connection has been retired from
// new admission by its own transport (the pool's detached-writer cap). The
// supervisor honours it: a connection the pool will not hand out is not one it
// hands out either.
type TaintedLink interface {
	Tainted() bool
}

// linkDead answers whether a link's transport has ended, by the best evidence
// the carrier can give: its synchronous check first, the Done channel after.
func linkDead(l Link) bool {
	if dead, ok := l.(DeadLink); ok && dead.Dead() {
		return true
	}
	select {
	case <-l.Done():
		return true
	default:
		return false
	}
}

// linkTainted answers whether the carrier's own transport has retired this
// connection from new admission.
func linkTainted(l Link) bool {
	if tainted, ok := l.(TaintedLink); ok {
		return tainted.Tainted()
	}
	return false
}
