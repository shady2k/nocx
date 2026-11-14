package hostconn_test

// The in-memory carrier and the supervisor, driven with no sshd and no ssh
// client at all — design §5.1.2's proof that the seam is the policy and the
// policy is the seam. If anything here needed internal/ssh, the seam would be
// wrong, and `go list -deps ./internal/hostconn` is the check that says it is
// not.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/waittest"
)

// memoryLink is one connection a test ends on cue.
type memoryLink struct {
	done chan struct{}
	// closeStarted is closed the moment Close is entered, so a test can hold a
	// teardown at its physical step and ask what admission does meanwhile.
	closeStarted chan struct{}
	closeGate    chan struct{}
	closeOnce    sync.Once
	mu           sync.Mutex
	err          error
	// The far side's behaviour, scripted per test: nil answers at once and the
	// look is answered by the host.
	probe   func() (time.Duration, error)
	nudge   func() error
	look    func() error
	looks   int
	nudges  int
	dead    atomic.Bool
	tainted atomic.Bool
}

func newMemoryLink() *memoryLink {
	return &memoryLink{done: make(chan struct{}), closeStarted: make(chan struct{})}
}

func (l *memoryLink) Done() <-chan struct{} { return l.done }

func (l *memoryLink) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *memoryLink) Close() error {
	l.closeOnce.Do(func() {
		close(l.closeStarted)
		if l.closeGate != nil {
			<-l.closeGate
		}
		close(l.done)
	})
	return nil
}

// Probe, Nudge and Look make this link a Prober and a Looker: the supervisor
// finds them by type, so a test says what the far side does by setting them.
func (l *memoryLink) Probe(context.Context) (time.Duration, error) {
	if l.probe == nil {
		return 0, nil
	}
	return l.probe()
}

func (l *memoryLink) Nudge() error {
	l.mu.Lock()
	l.nudges++
	fn := l.nudge
	l.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (l *memoryLink) Look(context.Context) error {
	l.mu.Lock()
	l.looks++
	fn := l.look
	l.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

// Dead and Tainted make this link answer the two synchronous facts a real
// carrier offers: has the transport ended, and has the pool retired it.
func (l *memoryLink) Dead() bool { return l.dead.Load() }

func (l *memoryLink) Tainted() bool { return l.tainted.Load() }

// counters reads what the supervisor asked this link to do.
func (l *memoryLink) counters() (looks, nudges int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.looks, l.nudges
}

// end is the network dying, rather than the holder closing it: the case the
// supervisor exists to notice.
func (l *memoryLink) end(err error) {
	l.mu.Lock()
	l.err = err
	l.mu.Unlock()
	_ = l.Close()
}

// memoryCarrier is one scripted transport: what a dial does, and every link it
// handed out.
type memoryCarrier struct {
	mu    sync.Mutex
	dials int
	// dial is this carrier's behaviour. Nil answers a healthy connection that
	// reported the whole stage list.
	dial func(sink hostconn.EventSink) (hostconn.Link, error)
	// dialPlan is the same with the plan in hand, for a test that gives each
	// CONNECTION its own link (two identities on one host).
	dialPlan func(plan hostconn.Plan, sink hostconn.EventSink) (hostconn.Link, error)
}

func (c *memoryCarrier) Name() string { return "memory" }

func (c *memoryCarrier) Capabilities() hostconn.Capabilities {
	return hostconn.Capabilities{Stages: true, Liveness: true, Multiplex: true}
}

func (c *memoryCarrier) Dial(_ context.Context, plan hostconn.Plan, sink hostconn.EventSink) (hostconn.Link, error) {
	c.mu.Lock()
	c.dials++
	behaviour := c.dial
	byPlan := c.dialPlan
	c.mu.Unlock()
	if byPlan != nil {
		return byPlan(plan, sink)
	}
	if behaviour != nil {
		return behaviour(sink)
	}
	for _, stage := range []hostconn.Stage{
		hostconn.StageTCP, hostconn.StageSSHVersion,
		hostconn.StageKeyExchange, hostconn.StageSignIn,
	} {
		sink(hostconn.Event{Kind: hostconn.StageReached, Stage: stage})
	}
	return newMemoryLink(), nil
}

func (c *memoryCarrier) dialCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dials
}

// manager answers the registry and the supervisor a test drives, which is the
// same pair a person's reconnect reaches: the registry survives a transport,
// the supervisor IS the connection's state.
func manager(t *testing.T, c *memoryCarrier) (*hostconn.Manager, *hostconn.Supervisor) {
	t.Helper()
	_, logger := logtest.New(t)
	m := hostconn.NewManager(c, logger)
	return m, m.Supervisor(plan().Key)
}

// managerOn puts the same pair on a clock the test advances.
func managerOn(t *testing.T, c *memoryCarrier, clock hostconn.Clock) (*hostconn.Manager, *hostconn.Supervisor) {
	t.Helper()
	return managerWith(t, c, clock, nil)
}

// managerWith puts the pair on an injected clock and, when given, a wake
// source.
func managerWith(t *testing.T, c *memoryCarrier, clock hostconn.Clock, wake hostconn.WakeSource) (*hostconn.Manager, *hostconn.Supervisor) {
	t.Helper()
	_, logger := logtest.New(t)
	m := hostconn.NewManager(c, logger, hostconn.WithClock(clock), hostconn.WithWakeSource(wake))
	return m, m.Supervisor(plan().Key)
}

func plan() hostconn.Plan {
	return hostconn.Plan{
		Carrier: "memory",
		Key:     hostconn.Key{Host: "host.example", Port: 22, User: "dev", Identity: "key"},
	}
}

// waitFor polls the supervisor for an observable state rather than sleeping for
// a duration: a test that needs a slow machine to pass is broken on a fast one
// too (AGENTS.md).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// stagesOf is the opening stages a state path passed, in order.
func stagesOf(states []hostconn.State) []hostconn.Stage {
	var stages []hostconn.Stage
	for _, state := range states {
		if state.Kind == hostconn.Opening {
			stages = append(stages, state.Stage)
		}
	}
	return stages
}

func TestOpeningPassesEachStageToConnected(t *testing.T) {
	carrier := &memoryCarrier{}
	mgr, _ := manager(t, carrier)

	lease, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(lease.Release)

	if got := lease.States(); last(got) != (hostconn.State{Kind: hostconn.Connected}) {
		t.Fatalf("a healthy dial ended in %v, want connected", last(got))
	}
	want := []hostconn.Stage{
		hostconn.StageResolve, hostconn.StageTCP, hostconn.StageSSHVersion,
		hostconn.StageKeyExchange, hostconn.StageSignIn,
	}
	got := stagesOf(lease.States())
	if len(got) != len(want) {
		t.Fatalf("opening passed stages %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stage %d is %q, want %q (whole path %v)", i, got[i], want[i], got)
		}
	}
	if lease.Lost() {
		t.Fatal("a connection nobody interfered with reads as lost")
	}
}

func TestAPersonNeedingStepReadsWaitingPerson(t *testing.T) {
	carrier := &memoryCarrier{dial: func(sink hostconn.EventSink) (hostconn.Link, error) {
		sink(hostconn.Event{Kind: hostconn.StageReached, Stage: hostconn.StageTCP})
		sink(hostconn.Event{Kind: hostconn.NeedsPerson, Wait: hostconn.WaitHostKey})
		return nil, errors.New("unknown host key for host.example")
	}}
	mgr, sup := manager(t, carrier)

	if _, err := mgr.Acquire(context.Background(), plan()); err == nil {
		t.Fatal("a dial that stopped for a person answered no error")
	}
	states := sup.States()
	if got := last(states); got.Kind != hostconn.WaitingPerson || got.Wait != hostconn.WaitHostKey {
		t.Fatalf("a step needing a person reads %v, want waiting_person(host-key) (path %v)", got, states)
	}
	// And the step that asked is remembered, so an answer resumes there.
	if got := last(states).Stage; got != hostconn.StageTCP {
		t.Fatalf("waiting_person resumed at %q, want the step that asked (tcp)", got)
	}
	if carrier.dialCount() != 1 {
		t.Fatalf("a wait caused %d dials, want 1: waiting is a state, not a retry", carrier.dialCount())
	}
}

func TestARefusedSignInFailsWithNoRetry(t *testing.T) {
	carrier := &memoryCarrier{dial: func(sink hostconn.EventSink) (hostconn.Link, error) {
		sink(hostconn.Event{Kind: hostconn.StageReached, Stage: hostconn.StageSignIn})
		sink(hostconn.Event{Kind: hostconn.Refused, Cause: hostconn.CauseAuthRejected})
		return nil, errors.New("ssh authentication failed")
	}}
	mgr, sup := manager(t, carrier)

	if _, err := mgr.Acquire(context.Background(), plan()); err == nil {
		t.Fatal("a refused sign-in answered no error")
	}
	if got := sup.Current(); got.Kind != hostconn.Failed || got.Cause != hostconn.CauseAuthRejected {
		t.Fatalf("a refused sign-in reads %v, want failed(auth-rejected)", got)
	}
	// NOTHING RETRIED, and the refusal stays: a supervisor that retried a
	// rejected credential is how a person's host locks them out. There is no
	// retry loop to wait out — the states are asserted where the attempt left
	// them, which is the observable.
	if carrier.dialCount() != 1 {
		t.Fatalf("a refused sign-in was retried: %d dials", carrier.dialCount())
	}
	if got := sup.Current(); got.Kind != hostconn.Failed {
		t.Fatalf("a refused sign-in moved on by itself: %v", got)
	}
}

func TestAChangedHostKeyFailsWithNoRetry(t *testing.T) {
	carrier := &memoryCarrier{dial: func(sink hostconn.EventSink) (hostconn.Link, error) {
		sink(hostconn.Event{Kind: hostconn.StageReached, Stage: hostconn.StageKeyExchange})
		sink(hostconn.Event{Kind: hostconn.Refused, Cause: hostconn.CauseHostKeyChanged})
		return nil, errors.New("host key mismatch")
	}}
	mgr, sup := manager(t, carrier)

	if _, err := mgr.Acquire(context.Background(), plan()); err == nil {
		t.Fatal("a changed host key answered no error")
	}
	if got := sup.Current(); got.Kind != hostconn.Failed || got.Cause != hostconn.CauseHostKeyChanged {
		t.Fatalf("a changed host key reads %v, want failed(host-key-changed)", got)
	}
	if carrier.dialCount() != 1 {
		t.Fatalf("a changed host key was retried: %d dials", carrier.dialCount())
	}
}

func TestATransportErrorReadsReconnecting(t *testing.T) {
	carrier := &memoryCarrier{}
	mgr, sup := manager(t, carrier)

	lease, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	link := lease.Link()
	if link == nil {
		t.Fatal("a held lease has no link")
	}
	endMemoryLink(link, errors.New("read tcp: operation timed out"))

	waitFor(t, "the supervisor to read the transport's end as reconnecting", func() bool {
		return sup.Current().Kind == hostconn.Reconnecting
	})
	if got := sup.Current(); got.Cause != hostconn.CauseLostLink {
		t.Fatalf("a lost link reads %v, want cause lost-link", got)
	}
	if !lease.Lost() {
		t.Fatal("a lease over a dead transport does not report lost")
	}
	lease.Release()
}

// TestAnEndedTransportIsNotHandedOutAgain is the supervisor's half of
// nocx-oalo1.4.6: the dead connection leaves the table when its transport ends,
// so the next acquire dials instead of being given it.
func TestAnEndedTransportIsNotHandedOutAgain(t *testing.T) {
	carrier := &memoryCarrier{}
	mgr, sup := manager(t, carrier)

	held, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	first := held.Link()
	endMemoryLink(first, errors.New("connection reset by peer"))
	waitFor(t, "the transport's end", func() bool { return sup.Current().Kind == hostconn.Reconnecting })

	fresh, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatalf("Acquire after the loss: %v", err)
	}
	t.Cleanup(fresh.Release)
	// THE SAME HOLDER IS STILL HOLDING THE OLD LEASE, as the owner's old tab
	// was. That is the whole point: the fresh acquire must not be given what
	// the old one rides.
	if fresh.Link() == first {
		t.Fatal("a fresh acquire was handed the transport that had ended")
	}
	if carrier.dialCount() != 2 {
		t.Fatalf("%d dials for one loss and one acquire, want 2", carrier.dialCount())
	}
	if got := last(fresh.States()); got.Kind != hostconn.Connected {
		t.Fatalf("the fresh connection reads %v, want connected", got)
	}
	held.Release()
}

func TestAHealthyConnectionIsReusedWithoutADial(t *testing.T) {
	carrier := &memoryCarrier{}
	mgr, sup := manager(t, carrier)

	first, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	second, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if first.Link() != second.Link() {
		t.Fatal("two acquires of one connection got two links")
	}
	if carrier.dialCount() != 1 {
		t.Fatalf("%d dials for two acquires, want 1: a pooled connection is reused", carrier.dialCount())
	}
	second.Release()
	// ONE HOLDER RELEASED, ONE LEFT: the connection stays.
	if got := sup.Current().Kind; got != hostconn.Connected {
		t.Fatalf("one release of two ended the connection: %v", sup.Current())
	}
	first.Release()
	if got := sup.Current(); got.Kind != hostconn.Ended || got.Cause != hostconn.CauseReleased {
		t.Fatalf("the last release reads %v, want ended(released)", got)
	}
}

func TestReconnectAfterAFailureStartsAFreshAttempt(t *testing.T) {
	refuse := true
	carrier := &memoryCarrier{dial: func(sink hostconn.EventSink) (hostconn.Link, error) {
		if refuse {
			sink(hostconn.Event{Kind: hostconn.Refused, Cause: hostconn.CauseAuthRejected})
			return nil, errors.New("ssh authentication failed")
		}
		sink(hostconn.Event{Kind: hostconn.StageReached, Stage: hostconn.StageSignIn})
		link := newMemoryLink()
		return link, nil
	}}
	mgr, sup := manager(t, carrier)

	if _, err := mgr.Acquire(context.Background(), plan()); err == nil {
		t.Fatal("the refused dial answered no error")
	}
	refuse = false
	lease, err := mgr.Acquire(context.Background(), plan())
	if err != nil {
		t.Fatalf("Acquire after the refusal: %v", err)
	}
	t.Cleanup(lease.Release)
	if got := sup.Current().Kind; got != hostconn.Connected {
		t.Fatalf("a reconnect after a refusal reads %v, want connected", got)
	}
}

func endMemoryLink(link hostconn.Link, err error) {
	if memory, ok := link.(*memoryLink); ok {
		memory.end(err)
		return
	}
	_ = link.Close()
}

func last(states []hostconn.State) hostconn.State {
	if len(states) == 0 {
		return hostconn.State{}
	}
	return states[len(states)-1]
}

// TestTwoIdentitiesOnOneHostDoNotShareAState is nocx-oalo1.4.4's third sentence:
// the connection is the key, not the host name. Two principals on one endpoint
// are two connections, two probers and two states.
func TestTwoIdentitiesOnOneHostDoNotShareAState(t *testing.T) {
	clock := hostconntest.New()
	links := map[string]*quietLink{}
	var mu sync.Mutex
	carrier := &memoryCarrier{dialPlan: func(plan hostconn.Plan, _ hostconn.EventSink) (hostconn.Link, error) {
		mu.Lock()
		defer mu.Unlock()
		link := newQuietLink()
		if plan.Key.Identity == "key-b" {
			// This connection answers; only the first goes quiet.
			link.memoryLink.probe = func() (time.Duration, error) { return time.Millisecond, nil }
		}
		links[plan.Key.Identity] = link
		return link, nil
	}}
	_, logger := logtest.New(t)
	mgr := hostconn.NewManager(carrier, logger, hostconn.WithClock(clock))

	acquireAs := func(identity string) *hostconn.Lease {
		t.Helper()
		p := plan()
		p.Key.Identity = identity
		lease, err := mgr.Acquire(context.Background(), p)
		if err != nil {
			t.Fatalf("Acquire %s: %v", identity, err)
		}
		t.Cleanup(lease.Release)
		return lease
	}
	first := acquireAs("key-a")
	second := acquireAs("key-b")
	if got := len(links); got != 2 {
		t.Fatalf("%d connections for two identities on one host, want 2", got)
	}

	// The first goes silent; the second must not inherit it.
	clock.Advance(hostconn.ProbeInterval)
	clock.Advance(hostconn.ProbeDeadline)
	waittest.WaitFor(t, "the first connection to read stalled", func() bool {
		return last(first.States()).Kind == hostconn.Stalled
	})
	if got := last(second.States()).Kind; got != hostconn.Connected {
		t.Fatalf("the second identity reads %v because the first went silent, want connected", got)
	}
	if second.Lost() {
		t.Fatal("the second identity's transport was closed because the first went silent")
	}
}

// TestAHoldersStatedCadenceReachesTheOneProber is design §5.3's "a profile's
// keepalive settings still apply and map onto these", at the seam that decides
// it. Every keeper of a connection but a shell states no cadence, and in the
// shipped open order the shell joins a connection that already exists — so the
// rule is "the smallest stated interval wins, and a stated one beats the
// default", not "whoever dialled first".
func TestAHoldersStatedCadenceReachesTheOneProber(t *testing.T) {
	link := newMemoryLink()
	var probes atomic.Int32
	link.probe = func() (time.Duration, error) {
		probes.Add(1)
		return time.Millisecond, nil
	}
	carrier := &memoryCarrier{dial: func(hostconn.EventSink) (hostconn.Link, error) { return link, nil }}
	clock := hostconntest.New()
	m, _ := managerOn(t, carrier, clock)

	first := plan()
	lease, err := m.Acquire(context.Background(), first)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(lease.Release)

	// The connection was dialled with no cadence stated, so the design's
	// default is what runs: nothing is asked before fifteen seconds.
	clock.Advance(hostconn.ProbeInterval - time.Second)
	if got := probes.Load(); got != 0 {
		t.Fatalf("%d probe(s) were sent inside the default interval", got)
	}
	clock.Advance(time.Second)
	waittest.WaitFor(t, "the default cadence's first probe", func() bool { return probes.Load() == 1 })

	// A shell joins and states a second. The change is asked at once — the
	// fifteen seconds the default had put in front of the next probe is not
	// time a person who asked to be told quickly should wait — and the cadence
	// is a second from then on.
	stated := plan()
	stated.ProbeInterval = time.Second
	second, err := m.Acquire(context.Background(), stated)
	if err != nil {
		t.Fatalf("the shell's Acquire: %v", err)
	}
	t.Cleanup(second.Release)
	waittest.WaitFor(t, "the stated cadence's probe", func() bool { return probes.Load() == 2 })

	clock.Advance(time.Second)
	waittest.WaitFor(t, "the second probe on the stated cadence", func() bool { return probes.Load() == 3 })

	// And a holder that asks for something quieter does not take it back: the
	// tightest stated interval is the answer to "how often", so a connection
	// only ever gets more attentive while its holders disagree.
	quieter := plan()
	quieter.ProbeInterval = 10 * time.Second
	third, err := m.Acquire(context.Background(), quieter)
	if err != nil {
		t.Fatalf("the quieter Acquire: %v", err)
	}
	t.Cleanup(third.Release)
	clock.Advance(time.Second)
	waittest.WaitFor(t, "the tightest stated cadence to keep running", func() bool { return probes.Load() == 4 })
}

// TestAConnectionThatAnswersEveryProbeNeverReadsStalled is the shape a cadence
// LONGER than the soft deadline must not break: a person may ask to be probed
// every thirty seconds, the deadline is fifteen, and a healthy link answers
// each probe at once — so the deadline must never fire on an answer that was
// given. Silence is a property of a probe that is OUTSTANDING (sent and
// unanswered); it is never measured from the last answer, and nothing may
// report a stall the host did not cause.
//
// Ten minutes of the injected clock, one second at a time, on a thirty-second
// cadence: twenty probes, every one answered, and the supervisor's own path
// must contain no stalled state at all.
func TestAConnectionThatAnswersEveryProbeNeverReadsStalled(t *testing.T) {
	var probes atomic.Int32
	link := newMemoryLink()
	link.probe = func() (time.Duration, error) {
		probes.Add(1)
		return time.Millisecond, nil
	}
	carrier := &memoryCarrier{dial: func(hostconn.EventSink) (hostconn.Link, error) { return link, nil }}
	clock := hostconntest.New()
	_, sup := managerOn(t, carrier, clock)

	slow := plan()
	slow.ProbeInterval = 30 * time.Second
	lease, err := sup.Acquire(context.Background(), slow)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(lease.Release)

	for elapsed := time.Duration(0); elapsed < 10*time.Minute; elapsed += time.Second {
		clock.Advance(time.Second)
	}

	// Every probe was answered, and the path says so.
	waittest.WaitFor(t, "the ten minutes' probes to be counted", func() bool { return probes.Load() >= 19 })
	for _, state := range sup.States() {
		if state.Kind == hostconn.Stalled {
			t.Fatalf("a healthy link on a 30s cadence read %v after %d probe(s): silence was measured from something that answered",
				state, probes.Load())
		}
	}
	if got := sup.Current().Kind; got != hostconn.Connected {
		t.Fatalf("a healthy connection reads %v after ten minutes, want connected", got)
	}
}
