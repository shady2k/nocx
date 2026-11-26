package session

import (
	"errors"
	"sync"
	"time"
)

// ErrDraining means this generation has atomically stopped admitting sessions.
var ErrDraining = errors.New("session: helper generation is draining")

// MeasuredStartupGrace is 250ms: three 30-sample launch-to-first-spawn runs
// on the owner VM (2026-10-04) had p99 values 27.73ms, 26.12ms and 25.14ms
// (spread 2.59ms). It is 9x the worst observed p99 to absorb scheduling and
// startup variance. Reproduce with RUN_DAEMON_LIFECYCLE_MEASUREMENT=1 go
// test -tags gtk3 -run TestMeasureDaemonLaunchToFirstSpawn -count=3 -v ./cmd/nocx-helper.
//
// IT BOUNDS A DAEMON NOBODY HAS REACHED, and only that. The number was
// measured as launch-to-first-spawn, which made it look like a bound on the
// coordinator's work between launching a helper and spawning on it — and it
// is not, because that work is network work whose length belongs to the
// network (a far helper's first spawn waits behind an SFTP publish and the
// tool socket's directories, which is milliseconds on one link and seconds on
// another). A helper whose endpoint has a CONNECTED client is in use and is
// not this deadline's business; see clientBound and ConnectedIdleGrace. What
// remains here is the case the measurement really covers: a daemon started
// and never dialed — endpoint.Ensure started one and its dial failed, or a
// person ran `nocx-helper serve` — exits rather than holding its endpoint and
// its lock for ever (nocx-2b0mq).
const MeasuredStartupGrace = 250 * time.Millisecond

// ConnectedIdleGrace bounds a daemon that HAS a client and has never owned a
// session: the client is on its way to a spawn, and this is how long it may
// take. It is not the startup grace re-armed with a bigger number, because
// the question is different — "has the coordinator abandoned this helper"
// rather than "was the helper ever reached" — and the answer must hold for
// every network.
//
// Two minutes is chosen from what the client has left to do, not from a
// machine: between connecting and spawning, the far route publishes the
// shell-integration bundle and creates the tool socket's directory (SFTP
// round trips on a connection that is already authenticated), and the product
// already grants a shell thirty seconds to prove itself AFTER that spawn
// (internal/app's remoteShellSetupTimeout). Four times that budget, with no
// network round trip in it that the open's own steps do not already bound, is
// a client that has stopped being on its way. A client that reaches this is
// retired, and its open fails visibly instead of pinning an execution-lane
// permit for the life of the server (nocx-398ca).
const ConnectedIdleGrace = 2 * time.Minute

// LifecycleTimer is one scheduled deadline, as the daemon needs to stop it.
type LifecycleTimer interface {
	Stop() bool
}

// LifecycleTimers schedules the daemon's deadlines. Production leaves
// Options.LifecycleTimers nil and the lifecycle uses time.AfterFunc; a test
// supplies one so it can fire a deadline at the moment the RULE is about
// instead of waiting for a clock (AGENTS.md: a test may not depend on a
// duration it cannot measure).
type LifecycleTimers interface {
	AfterFunc(time.Duration, func()) LifecycleTimer
}

type lifecycleTimer = LifecycleTimer

type lifecycleAfterFunc func(time.Duration, func()) lifecycleTimer

type daemonLifecycle struct {
	mu             sync.Mutex
	enabled        bool
	grace          time.Duration
	connectedGrace time.Duration
	afterFunc      lifecycleAfterFunc
	deadline       time.Time
	admissions     int
	sessions       int
	// clients counts the connections currently bound (Service.Bind), and
	// reached records that one has ever been bound. Together they are the
	// event this daemon's retirement is built on: a helper a coordinator has
	// reached is in use, whatever its network is doing.
	clients int
	// working counts the bound connections that have asked for panel work — a
	// `git` or `fs` request — and have not been released. It is not a second
	// clients count: a connection is in both while it is working, and this is
	// what the connected-idle bound is disarmed by (nocx-oalo1.12.5).
	working           int
	reached           bool
	everOwned         bool
	deadlineExpired   bool
	connectedExpired  bool
	closingAdmissions bool
	draining          bool
	onDrained         func()
	once              sync.Once
	timer             lifecycleTimer
	// timerGen names the deadline currently entitled to act. A timer's
	// callback runs on its own goroutine, so Stop is not a happens-before
	// edge: a callback that started before the disarm may reach the lock
	// after a fresh deadline was armed, and without this it would decide on
	// state it no longer owns — retiring a daemon a panel client is working
	// on, or poisoning the startup answer a bound client already gave
	// (review M1). Every arm and every disarm moves the generation, and a
	// callback whose generation is no longer current returns untouched.
	timerGen uint64
}

func newDaemonLifecycle(grace, connectedGrace time.Duration, onDrained func()) *daemonLifecycle {
	return newDaemonLifecycleWithTimer(grace, connectedGrace, onDrained, func(d time.Duration, f func()) lifecycleTimer {
		return time.AfterFunc(d, f)
	})
}

// newDaemonLifecycleWithTimer keeps deadline scheduling deterministic in tests;
// production uses time.AfterFunc through newDaemonLifecycle.
func newDaemonLifecycleWithTimer(grace, connectedGrace time.Duration, onDrained func(), afterFunc lifecycleAfterFunc) *daemonLifecycle {
	l := &daemonLifecycle{
		enabled:        grace > 0 || connectedGrace > 0 || onDrained != nil,
		grace:          grace,
		connectedGrace: connectedGrace,
		afterFunc:      afterFunc,
		onDrained:      onDrained,
	}
	if grace > 0 {
		l.mu.Lock()
		l.deadline = time.Now().Add(grace)
		l.timerGen++
		gen := l.timerGen
		l.timer = afterFunc(grace, func() { l.expire(gen) })
		l.mu.Unlock()
	}
	return l
}

func (l *daemonLifecycle) fire() {
	if l.onDrained != nil {
		l.once.Do(l.onDrained)
	}
}

// admit reserves the right to finish a spawn. The reservation covers the
// whole spawn operation, so a timer cannot stop the daemon after acknowledging
// an admission but before its session has been registered.
func (l *daemonLifecycle) admit() (func(), error) {
	if !l.enabled {
		return func() {}, nil
	}
	l.mu.Lock()
	if l.draining || l.closingAdmissions {
		l.mu.Unlock()
		return nil, ErrDraining
	}
	// THE STARTUP DEADLINE IS ASKED ONLY WHEN NOBODY HAS REACHED THE DAEMON.
	// A client bound before the deadline owns it: the deadline was the
	// question "was this helper ever dialed", and it has been answered. A
	// daemon whose client is on its way to a spawn therefore admits it, at
	// whatever the network is doing, and ConnectedIdleGrace is the bound for
	// a client that never gets there.
	if !l.reached && !l.everOwned && (l.deadlineExpired || (l.grace > 0 && !time.Now().Before(l.deadline))) {
		l.deadlineExpired = true
		if l.admissions == 0 {
			l.draining = true
			l.mu.Unlock()
			l.fire()
			return nil, ErrDraining
		}
		// Exactly one already-reserved admission may win the expired grace.
		l.mu.Unlock()
		return nil, ErrDraining
	}
	l.admissions++
	l.mu.Unlock()
	var once sync.Once
	return func() { once.Do(l.finishAdmission) }, nil
}

func (l *daemonLifecycle) finishAdmission() {
	l.mu.Lock()
	l.admissions--
	shouldFire := l.maybeDrainLocked()
	l.mu.Unlock()
	if shouldFire {
		l.fire()
	}
}

// clientBound records one connection to this daemon. The FIRST one replaces
// the startup deadline with the connected-idle bound: the daemon has been
// reached, and what is left to bound is how long its client may take to
// spawn. It is called from Service.Bind, which the accept loop calls before
// the protocol engine runs, so a client is counted from the moment its
// connection is accepted — including the handshake.
func (l *daemonLifecycle) clientBound() {
	if !l.enabled {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.draining {
		// Too late: the daemon is already on its way out and endpoint.Serve
		// will close this connection. Un-draining would keep a daemon alive
		// that every other caller has been told is gone.
		return
	}
	l.clients++
	if l.clients != 1 || l.everOwned {
		return
	}
	l.reached = true
	l.connectedExpired = false
	l.stopTimerLocked()
	l.armConnectedDeadlineLocked()
}

// panelWorking records that a bound connection has asked for panel work: a
// `git` or `fs` request, which is a pane's panel working on the connection its
// tab holds. ConnectedIdleGrace asks whether a client is still on its way to a
// spawn, and this client has answered for itself — so the deadline is stopped,
// and the first working client is the one that stops it. Further requests of
// the same client change nothing: the connection is already counted, and the
// panel's next call is not a new errand.
func (l *daemonLifecycle) panelWorking() {
	if !l.enabled {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.draining {
		// Too late: the daemon is on its way out, and an answer that arrived
		// after that cannot un-retire it — every caller has been told it is
		// gone (the same reason clientBound keeps the count and stops).
		return
	}
	l.working++
	if l.working != 1 || l.everOwned {
		return
	}
	// The deadline fired while this request was in flight is not evidence any
	// more: it asked a question this connection has just answered.
	l.connectedExpired = false
	l.stopTimerLocked()
}

// clientReleased records one connection ending. A helper a client reached and
// left without ever spawning a session is bounded by ConnectedIdleGrace from
// the release, not retired at once: the release is not evidence that nobody is
// arriving. A liveness probe connects and leaves as an ordinary part of asking
// "is this generation served", and retiring the daemon it asked would make the
// answer remove the thing it reported.
// worked is the connection's own answer to "did you ask for panel work": the
// bound is armed again by a client that asked nothing (nocx-398ca's abandoned
// coordinator) and by the last panel client to leave, and this is what tells
// the two apart.
func (l *daemonLifecycle) clientReleased(worked bool) {
	if !l.enabled {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.clients > 0 {
		l.clients--
	}
	if worked && l.working > 0 {
		l.working--
	}
	// THE BOUND IS ARMED BY THE LAST WORKING CLIENT LEAVING, NOT BY THE
	// connection count reaching zero (review B1). A silent client may be
	// sitting behind the panel client that just left, and it is exactly the
	// abandoned coordinator the bound exists for: waiting for `clients == 0`
	// meant nothing armed the deadline again, and the daemon was held for ever
	// by a connection that never asked for anything. A silent client leaving
	// while another silent client remains changes nothing — the deadline
	// already in flight is that one's own answer.
	lastWorker := worked && l.working == 0
	if l.clients != 0 && !lastWorker {
		return
	}
	if l.everOwned || l.draining || l.working != 0 {
		return
	}
	l.stopTimerLocked()
	l.connectedExpired = false
	l.armConnectedDeadlineLocked()
}

// connectedExpire is ConnectedIdleGrace arriving: a client is bound and has
// not spawned a session in the whole bound. It retires even with clients
// bound — that is the bound's purpose — and a request already admitted is
// still allowed to finish, exactly as the startup deadline allows one.
func (l *daemonLifecycle) connectedExpire(gen uint64) {
	l.mu.Lock()
	if gen != l.timerGen {
		// A deadline this daemon has already replaced or disarmed: it has no
		// answer to give about the state in front of it (review M1).
		l.mu.Unlock()
		return
	}
	l.timer = nil
	l.connectedExpired = true
	shouldFire := false
	// A panel client is working: the deadline that just fired asked whether
	// this daemon's client was still on its way to a spawn, and it is not
	// (nocx-oalo1.12.5). The race is real — the timer runs on its own
	// goroutine, so a `git` call can land after it started — which is why the
	// answer is decided here, under the lock, and not by whether Stop won.
	if !l.draining && !l.everOwned && l.sessions == 0 && l.admissions == 0 && l.working == 0 {
		l.draining = true
		shouldFire = true
	}
	l.mu.Unlock()
	if shouldFire {
		l.fire()
	}
}

func (l *daemonLifecycle) expire(gen uint64) {
	l.mu.Lock()
	if gen != l.timerGen {
		// The startup deadline was stopped, or replaced by the connected-idle
		// bound, before this callback reached the lock. Deciding now would
		// answer a question the daemon has moved past (review M1).
		l.mu.Unlock()
		return
	}
	l.timer = nil
	l.deadlineExpired = true
	shouldFire := false
	if !l.reached && !l.everOwned && l.admissions == 0 {
		l.draining = true
		shouldFire = true
	}
	l.mu.Unlock()
	if shouldFire {
		l.fire()
	}
}

func (l *daemonLifecycle) sessionStarted() {
	if !l.enabled {
		return
	}
	l.mu.Lock()
	l.sessions++
	l.everOwned = true
	l.closingAdmissions = false
	l.stopTimerLocked()
	l.mu.Unlock()
}

func (l *daemonLifecycle) markSessionEnded() bool {
	if !l.enabled {
		return false
	}
	l.mu.Lock()
	if l.sessions > 0 {
		l.sessions--
	}
	if l.everOwned && l.sessions == 0 {
		l.closingAdmissions = true
	}
	shouldFire := l.maybeDrainLocked()
	l.mu.Unlock()
	return shouldFire
}

func (l *daemonLifecycle) fireDrained(shouldFire bool) {
	if shouldFire {
		l.fire()
	}
}

// maybeDrainLocked answers whether the daemon is done. The LAST-SESSION rule
// deliberately ignores clients: D2's guarantee is that a daemon whose last
// session exits shuts down, and the local coordinator keeps its connection
// after closing its last pane (nocx-2b0mq). Only the never-owned half asks
// how many clients are bound.
func (l *daemonLifecycle) maybeDrainLocked() bool {
	if l.draining || l.admissions != 0 {
		return false
	}
	if l.everOwned {
		if l.sessions == 0 && l.closingAdmissions {
			l.draining = true
			return true
		}
		return false
	}
	// NEVER OWNED. The startup deadline covers a daemon no client reached;
	// the connected deadline covers one whose client came and left (or never
	// spawned) — and it may have fired while an admission was in flight, in
	// which case this is where the daemon actually ends. The startup half is
	// asked only while nobody has reached the daemon: the deadline it names
	// was the question "was this helper ever dialed", and a bound client has
	// answered it even if a stale callback once said otherwise (review M1).
	if (!l.reached && l.deadlineExpired) || (l.connectedExpired && l.clients == 0 && l.sessions == 0) {
		l.draining = true
		return true
	}
	return false
}

// armConnectedDeadlineLocked replaces whatever deadline is running with the
// connected-idle bound. It is the daemon's only second question: the startup
// deadline asks whether anybody reached it, and this asks how long the client
// that did may take to spawn.
func (l *daemonLifecycle) armConnectedDeadlineLocked() {
	if l.connectedGrace > 0 {
		l.timerGen++
		gen := l.timerGen
		l.timer = l.afterFunc(l.connectedGrace, func() { l.connectedExpire(gen) })
	}
}

// stopTimerLocked disarms the deadline in flight and invalidates every
// callback already on its way to the lock. The generation moves even when no
// timer is held: a callback that started before an earlier disarm can still be
// waiting for the mutex, and this is the moment its answer stops being current.
func (l *daemonLifecycle) stopTimerLocked() {
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	l.timerGen++
}

func (l *daemonLifecycle) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopTimerLocked()
}
