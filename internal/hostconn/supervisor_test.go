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
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/log/logtest"
)

// memoryLink is one connection a test ends on cue.
type memoryLink struct {
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	err       error
	// The far side's behaviour, scripted per test: nil answers at once and the
	// look is answered by the host.
	probe  func() (time.Duration, error)
	nudge  func() error
	look   func() error
	looks  int
	nudges int
}

func newMemoryLink() *memoryLink {
	return &memoryLink{done: make(chan struct{})}
}

func (l *memoryLink) Done() <-chan struct{} { return l.done }

func (l *memoryLink) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *memoryLink) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
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
}

func (c *memoryCarrier) Name() string { return "memory" }

func (c *memoryCarrier) Capabilities() hostconn.Capabilities {
	return hostconn.Capabilities{Stages: true, Liveness: true, Multiplex: true}
}

func (c *memoryCarrier) Dial(_ context.Context, _ hostconn.Plan, sink hostconn.EventSink) (hostconn.Link, error) {
	c.mu.Lock()
	c.dials++
	behaviour := c.dial
	c.mu.Unlock()
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
	_, logger := logtest.New(t)
	m := hostconn.NewManager(c, logger, hostconn.WithClock(clock))
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
	// rejected credential is how a person's host locks them out.
	waitABit()
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
	waitABit()
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

func waitABit() { time.Sleep(20 * time.Millisecond) }
