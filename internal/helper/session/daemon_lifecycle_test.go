package session

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestDaemonLifecycleDeadlineAdmissionWinsAtomically(t *testing.T) {
	drained := make(chan struct{})
	l := newDaemonLifecycle(time.Hour, time.Hour, func() { close(drained) })
	release, err := l.admit()
	if err != nil {
		t.Fatal(err)
	}
	l.fireStartupDeadline()
	if _, err := l.admit(); !errors.Is(err, ErrDraining) {
		t.Fatalf("admission after deadline = %v, want draining", err)
	}
	l.sessionStarted()
	release()
	select {
	case <-drained:
		t.Fatal("daemon drained despite winning admission")
	default:
	}
	l.fireDrained(l.markSessionEnded())
	select {
	case <-drained:
	default:
		t.Fatal("last session did not drain daemon")
	}
	l.stop()
}

func TestDaemonLifecycleScheduledGraceExpires(t *testing.T) {
	drained := make(chan struct{})
	var scheduled time.Duration
	var fire func()
	l := newDaemonLifecycleWithTimer(time.Hour, time.Hour, func() { close(drained) }, func(d time.Duration, f func()) lifecycleTimer {
		scheduled, fire = d, f
		return inertLifecycleTimer{}
	})
	if scheduled != time.Hour {
		t.Fatalf("scheduled grace = %s, want %s", scheduled, time.Hour)
	}
	if fire == nil {
		t.Fatal("startup grace did not schedule its expiry")
	}
	select {
	case <-drained:
		t.Fatal("daemon drained before scheduled grace fired")
	default:
	}
	fire()
	if _, err := l.admit(); !errors.Is(err, ErrDraining) {
		t.Fatalf("post-grace admission = %v, want draining", err)
	}
	select {
	case <-drained:
	default:
		t.Fatal("empty daemon did not drain")
	}
	l.stop()
}

type inertLifecycleTimer struct{}

func (inertLifecycleTimer) Stop() bool { return true }

func TestStartupTimerMayFireDuringRegistration(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	drained := make(chan struct{})
	l := newDaemonLifecycleWithTimer(time.Hour, 0, func() { close(drained) }, func(_ time.Duration, fire func()) lifecycleTimer {
		go func() {
			close(started)
			fire()
			close(finished)
		}()
		<-started
		return inertLifecycleTimer{}
	})
	<-finished
	select {
	case <-drained:
	default:
		t.Fatal("the startup deadline fired without draining an unused daemon")
	}
	if l.timer != nil {
		t.Fatal("the expired startup timer is still registered")
	}
}

func TestDaemonLifecycleFailedAdmittedSpawnReleasesReservation(t *testing.T) {
	drained := make(chan struct{})
	var fire func()
	l := newDaemonLifecycleWithTimer(time.Hour, time.Hour, func() { close(drained) }, func(_ time.Duration, f func()) lifecycleTimer {
		fire = f
		return inertLifecycleTimer{}
	})
	if fire == nil {
		t.Fatal("startup grace did not schedule its expiry")
	}
	finishAdmission, err := l.admit()
	if err != nil {
		t.Fatalf("admit spawn: %v", err)
	}
	// The scheduled deadline may arrive while an already-admitted spawn is
	// failing. Its deferred reservation release must be what lets retirement run.
	fire()
	select {
	case <-drained:
		t.Fatal("daemon drained while admitted spawn was still in flight")
	default:
	}
	finishAdmission()
	select {
	case <-drained:
	default:
		t.Fatal("failed admitted spawn kept daemon admission reserved")
	}
	if _, err := l.admit(); !errors.Is(err, ErrDraining) {
		t.Fatalf("admission after failed spawn and grace = %v, want draining", err)
	}
	l.stop()
}

func TestDaemonLifecycleDeadlineRacesAdmission(t *testing.T) {
	for i := 0; i < 100; i++ {
		var mu sync.Mutex
		admitted, refused, drained := false, false, false
		l := newDaemonLifecycle(time.Hour, time.Hour, func() { mu.Lock(); drained = true; mu.Unlock() })
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; l.fireStartupDeadline() }()
		go func() {
			defer wg.Done()
			<-start
			release, err := l.admit()
			if err != nil {
				refused = true
				return
			}
			admitted = true
			l.sessionStarted()
			release()
		}()
		close(start)
		wg.Wait()
		mu.Lock()
		d := drained
		mu.Unlock()
		if admitted == refused {
			t.Fatalf("iteration %d: admitted=%t refused=%t", i, admitted, refused)
		}
		if admitted && d {
			t.Fatalf("iteration %d: acknowledged session but daemon drained", i)
		}
		if admitted {
			l.fireDrained(l.markSessionEnded())
		}
		if refused && !d {
			t.Fatalf("iteration %d: refused admission but daemon did not drain", i)
		}
		l.stop()
	}
}

type failedSpawnLifecycleSpawner struct {
	fire    func()
	drained <-chan struct{}
	calls   int
}

func (s *failedSpawnLifecycleSpawner) Spawn(SpawnRequest) (Process, error) {
	s.calls++
	s.fire()
	select {
	case <-s.drained:
		panic("daemon drained while a local spawn reservation was held")
	default:
	}
	return nil, errors.New("local spawn failed")
}

func (s *failedSpawnLifecycleSpawner) SpawnSSH(context.Context, SSHSpawnRequest) (Process, error) {
	s.calls++
	s.fire()
	select {
	case <-s.drained:
		panic("daemon drained while an SSH spawn reservation was held")
	default:
	}
	return nil, errors.New("SSH spawn failed")
}

func TestServiceFailedLocalSpawnReleasesLifecycleReservation(t *testing.T) {
	testFailedServiceSpawnReleasesLifecycleReservation(t, proto.OpSpawn, proto.SpawnParams{Cols: 80, Rows: 24})
}

func TestServiceFailedSSHSpawnReleasesLifecycleReservation(t *testing.T) {
	testFailedServiceSpawnReleasesLifecycleReservation(t, proto.OpSpawnSSH, proto.SSHSpawnParams{
		Destination: proto.SSHDestination{Host: "example.com", Port: 22, User: "test", Identity: proto.SSHIdentity{Auth: proto.SSHAuthInteractive}},
	})
}

func testFailedServiceSpawnReleasesLifecycleReservation(t *testing.T, op string, params any) {
	t.Helper()
	drained := make(chan struct{})
	var drainedCalls atomic.Int32
	spawner := &failedSpawnLifecycleSpawner{drained: drained}
	options := Options{
		Generation: "gen-under-test",
		Spawner:    spawner,
		SSHSpawner: spawner,
		OnDrained: func() {
			drainedCalls.Add(1)
			close(drained)
		},
		StartupGrace: time.Hour,
	}
	svc := newWithLifecycleTimer(options, func(_ time.Duration, fire func()) lifecycleTimer {
		spawner.fire = fire
		return inertLifecycleTimer{}
	})
	t.Cleanup(svc.Close)
	payload, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Call(context.Background(), op, payload); err == nil {
		t.Fatalf("%s reported success after spawner failure", op)
	}
	select {
	case <-drained:
	default:
		t.Fatal("failed admitted spawn did not release the reservation and drain daemon")
	}
	if drainedCalls.Load() != 1 {
		t.Fatalf("OnDrained called %d times, want exactly once", drainedCalls.Load())
	}
	if _, err := svc.Call(context.Background(), op, payload); !errors.Is(err, ErrDraining) {
		t.Fatalf("%s after drain = %v, want ErrDraining", op, err)
	}
	if spawner.calls != 1 {
		t.Fatalf("spawner called %d times, want 1 (later call must be refused before spawn)", spawner.calls)
	}
	if drainedCalls.Load() != 1 {
		t.Fatalf("OnDrained called %d times after refused spawn, want exactly once", drainedCalls.Load())
	}
}

// recordingTimers records every deadline a lifecycle arms, so a test can fire
// one at the moment its rule is about. Nothing here waits for a clock.
type recordingTimers struct {
	mu    sync.Mutex
	deadl []recordedDeadline
}

type recordedDeadline struct {
	after time.Duration
	fire  func()
}

func (r *recordingTimers) afterFunc(after time.Duration, fire func()) lifecycleTimer {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deadl = append(r.deadl, recordedDeadline{after: after, fire: fire})
	return inertLifecycleTimer{}
}

func (r *recordingTimers) fire(after time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	fired := false
	for _, d := range r.deadl {
		if d.after == after {
			d.fire()
			fired = true
		}
	}
	return fired
}

// count is how many deadlines of that length have been armed, which is how a
// test tells "the release armed nothing" from "the release re-armed what was
// already running".
func (r *recordingTimers) count(after time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, d := range r.deadl {
		if d.after == after {
			n++
		}
	}
	return n
}

// last returns the callback of the most recently armed deadline of that length.
// A test fires it at the moment the rule is about — including a deadline that
// has since been stopped, which is the race a real timer loses.
func (r *recordingTimers) last(after time.Duration) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	var fire func()
	for _, d := range r.deadl {
		if d.after == after {
			fire = d.fire
		}
	}
	return fire
}

// fireStartupDeadline is the scheduled startup deadline arriving, as the timer
// itself would deliver it: the generation current at that moment is the one the
// callback carries. A test that needs the stale case captures the callback with
// recordingTimers.last instead.
func (l *daemonLifecycle) fireStartupDeadline() {
	l.mu.Lock()
	gen := l.timerGen
	l.mu.Unlock()
	l.expire(gen)
}

// TestDaemonLifecycleABoundClientSurvivesTheStartupDeadline is nocx-398ca's
// first rule: the startup deadline asks whether a daemon was ever REACHED, and
// a client that is connected has answered that question. The deadline firing
// after the bind — which is the race a real timer loses under load — must not
// retire a daemon whose coordinator is on its way to a spawn.
func TestDaemonLifecycleABoundClientSurvivesTheStartupDeadline(t *testing.T) {
	drained := make(chan struct{})
	timers := &recordingTimers{}
	l := newDaemonLifecycleWithTimer(time.Hour, 2*time.Hour, func() { close(drained) }, timers.afterFunc)

	l.clientBound()
	if !timers.fire(time.Hour) {
		t.Fatal("the startup deadline was not armed at construction")
	}
	select {
	case <-drained:
		t.Fatal("a connected client did not stop the startup deadline from retiring its helper")
	default:
	}

	// The spawn the client was on its way to make is admitted.
	release, err := l.admit()
	if err != nil {
		t.Fatalf("a client that connected before the deadline was refused its spawn: %v", err)
	}
	l.sessionStarted()
	release()
	select {
	case <-drained:
		t.Fatal("the daemon drained after admitting the spawn its connected client made")
	default:
	}

	// The last-session rule still ends it: a connection is not a session.
	l.fireDrained(l.markSessionEnded())
	select {
	case <-drained:
	default:
		t.Fatal("the last session's end did not retire a daemon whose client was still connected")
	}
	l.stop()
}

// TestDaemonLifecycleAClientThatNeverSpawnsIsBounded is the other half of the
// same rule: a client that connects and then does nothing must not keep a
// helper for ever. ConnectedIdleGrace is that bound, and it retires even with
// the client still bound.
func TestDaemonLifecycleAClientThatNeverSpawnsIsBounded(t *testing.T) {
	drained := make(chan struct{})
	timers := &recordingTimers{}
	l := newDaemonLifecycleWithTimer(time.Hour, 2*time.Hour, func() { close(drained) }, timers.afterFunc)

	l.clientBound()
	if !timers.fire(2 * time.Hour) {
		t.Fatal("binding a client armed no connected-idle deadline")
	}
	select {
	case <-drained:
	default:
		t.Fatal("a client that held the helper for the whole bound without spawning did not retire it")
	}
	if _, err := l.admit(); !errors.Is(err, ErrDraining) {
		t.Fatalf("admission after the connected-idle bound = %v, want draining", err)
	}
	l.stop()
}

// TestDaemonLifecycleAClientLeavingWithoutSpawningIsStillBounded: a release
// is not evidence that nobody is arriving — a liveness probe connects and
// leaves as an ordinary part of asking whether the generation is served, and
// retiring the daemon it asked would delete the thing it reported. So the
// release re-arms the connected-idle bound, and the daemon ends when that
// expires rather than at once or never.
func TestDaemonLifecycleAClientLeavingWithoutSpawningIsStillBounded(t *testing.T) {
	drained := make(chan struct{})
	timers := &recordingTimers{}
	l := newDaemonLifecycleWithTimer(time.Hour, 2*time.Hour, func() { close(drained) }, timers.afterFunc)

	l.clientBound()
	l.clientReleased(false)
	select {
	case <-drained:
		t.Fatal("a client's release retired the helper at once; a probe that leaves must not delete the daemon it asked")
	default:
	}
	if !timers.fire(2 * time.Hour) {
		t.Fatal("a client's release armed no connected-idle deadline")
	}
	select {
	case <-drained:
	default:
		t.Fatal("a helper reached and left with no session never retired")
	}
	l.stop()
}

// TestDaemonLifecycleAnUnreachedHelperStillExpires pins the guarantee the
// startup grace exists for (nocx-2b0mq): a daemon nobody ever dialed exits.
func TestDaemonLifecycleAnUnreachedHelperStillExpires(t *testing.T) {
	drained := make(chan struct{})
	timers := &recordingTimers{}
	l := newDaemonLifecycleWithTimer(time.Hour, 2*time.Hour, func() { close(drained) }, timers.afterFunc)

	if !timers.fire(time.Hour) {
		t.Fatal("the startup deadline was not armed at construction")
	}
	select {
	case <-drained:
	default:
		t.Fatal("a daemon nobody reached did not retire when its startup deadline expired")
	}
	l.stop()
}

// TestDaemonLifecycleAPanelClientIsNotRetiredByTheConnectedBound is
// nocx-oalo1.12.5's first rule. ConnectedIdleGrace asks whether a client that
// connected is still on its way to a spawn; a client that asked `git` or `fs`
// has answered for itself, because that is a pane's panel working on the
// connection rather than a spawn that never came (nocx-398ca bounds the other
// one). The bound stops asking from the first such request until that client is
// released, and only then is it armed again.
func TestDaemonLifecycleAPanelClientIsNotRetiredByTheConnectedBound(t *testing.T) {
	drained := make(chan struct{})
	timers := &recordingTimers{}
	l := newDaemonLifecycleWithTimer(time.Hour, 2*time.Hour, func() { close(drained) }, timers.afterFunc)

	l.clientBound()
	l.panelWorking()
	// The deadline armed by the bind is fired AS ARMED: a stopped timer that
	// still runs its callback is exactly the case the rule must survive.
	timers.fire(2 * time.Hour)
	select {
	case <-drained:
		t.Fatal("a panel client's git or fs request did not stop the connected-idle bound from retiring its helper")
	default:
	}
	// A panel client may still spawn, and the admitted spawn is given back
	// here so the daemon's retirement below is the bound's decision alone.
	finishSpawn, err := l.admit()
	if err != nil {
		t.Fatalf("a spawn from a working panel client was refused: %v", err)
	}
	finishSpawn()

	// The release re-arms it, because a connection that ended is not evidence
	// that nobody is arriving: the daemon is retired by the bound, not at once.
	l.clientReleased(true)
	if !timers.fire(2 * time.Hour) {
		t.Fatal("a panel client's release armed no connected-idle deadline")
	}
	select {
	case <-drained:
	default:
		t.Fatal("a helper whose panel client left with no session never retired")
	}
	l.stop()
}

// TestDaemonLifecycleASecondPanelClientKeepsTheBoundDisarmed: the bound is a
// statement about the daemon's clients as a whole, so one panel client leaving
// while another is still bound must not arm it — the remaining client is doing
// work, and two minutes from now it may be mid-upload.
func TestDaemonLifecycleASecondPanelClientKeepsTheBoundDisarmed(t *testing.T) {
	drained := make(chan struct{})
	timers := &recordingTimers{}
	l := newDaemonLifecycleWithTimer(time.Hour, 2*time.Hour, func() { close(drained) }, timers.afterFunc)

	l.clientBound()
	l.panelWorking()
	l.clientBound()
	l.panelWorking()
	l.clientReleased(true)
	timers.fire(2 * time.Hour)
	select {
	case <-drained:
		t.Fatal("one panel client's release retired a helper another panel client was still working on")
	default:
	}

	l.clientReleased(true)
	if !timers.fire(2 * time.Hour) {
		t.Fatal("the last panel client's release armed no connected-idle deadline")
	}
	select {
	case <-drained:
	default:
		t.Fatal("a helper whose panel clients all left with no session never retired")
	}
	l.stop()
}

// TestDaemonLifecycleAnOpenWriteSessionHoldsTheLastSessionDrain is
// nocx-oalo1.12.5's third rule: the last hosted session closing drains the
// daemon, but a write session still open on the fs service is work the drain
// must wait for. The write session holds an admission for its whole life — from
// before its temporary exists until it commits or aborts — and the drain
// happens at the admission's release, not at the last session's end.
func TestDaemonLifecycleAnOpenWriteSessionHoldsTheLastSessionDrain(t *testing.T) {
	drained := make(chan struct{})
	timers := &recordingTimers{}
	l := newDaemonLifecycleWithTimer(time.Hour, 2*time.Hour, func() { close(drained) }, timers.afterFunc)

	l.clientBound()
	finishSpawn, err := l.admit()
	if err != nil {
		t.Fatal(err)
	}
	l.sessionStarted()
	finishSpawn()

	// The fs service's write-open reserves the daemon before its temporary
	// exists, and the panel's shell closes while that session is still open.
	finishWrite, err := l.admit()
	if err != nil {
		t.Fatalf("a write session was refused its admission: %v", err)
	}
	l.fireDrained(l.markSessionEnded())
	select {
	case <-drained:
		t.Fatal("the last session's end drained the daemon with a write session still open")
	default:
	}

	// The commit (or the abort) releases it, and only then does the daemon end.
	finishWrite()
	select {
	case <-drained:
	default:
		t.Fatal("the write session's end did not release the drain the last session asked for")
	}
	l.stop()
}

// TestDaemonLifecycleASilentClientIsBoundedOnceThePanelClientLeaves is review
// B1. The bound was armed by the connection count reaching zero, so a silent
// client sitting behind a panel client fell out of it: the panel client's
// release left `clients == 1`, nothing armed the deadline again, and the
// abandoned connection held the daemon for ever — the state nocx-398ca exists
// to end. The last WORKING client leaving is the event that arms the bound.
func TestDaemonLifecycleASilentClientIsBoundedOnceThePanelClientLeaves(t *testing.T) {
	drained := make(chan struct{})
	timers := &recordingTimers{}
	l := newDaemonLifecycleWithTimer(time.Hour, 2*time.Hour, func() { close(drained) }, timers.afterFunc)

	l.clientBound()  // silent A
	l.clientBound()  // panel B
	l.panelWorking() // B asks for panel work, which disarms the bound
	armed := timers.count(2 * time.Hour)
	l.clientReleased(true)
	// The event the bound waits for is the last WORKING client leaving: A is
	// still bound and still silent, and it has its own two minutes from here.
	if got := timers.count(2 * time.Hour); got != armed+1 {
		t.Fatalf("the last panel client's release armed %d deadlines, want 1 (the silent client behind it is left unbounded)", got-armed)
	}
	timers.fire(2 * time.Hour)
	select {
	case <-drained:
	default:
		t.Fatal("a silent client behind the panel client that left kept the daemon for ever")
	}
	l.stop()
}

// TestDaemonLifecycleASilentClientLeavingDoesNotExtendTheBound is the other end
// of B1: releasing one of two silent clients is not the event the bound is
// waiting for, so it must neither stop nor re-arm the deadline already running
// for the remaining one.
func TestDaemonLifecycleASilentClientLeavingDoesNotExtendTheBound(t *testing.T) {
	drained := make(chan struct{})
	timers := &recordingTimers{}
	l := newDaemonLifecycleWithTimer(time.Hour, 2*time.Hour, func() { close(drained) }, timers.afterFunc)

	l.clientBound()
	armed := timers.count(2 * time.Hour)
	l.clientBound()
	l.clientReleased(false)
	if got := timers.count(2 * time.Hour); got != armed {
		t.Fatalf("a silent client's release armed %d deadlines, want the %d already running", got-armed, armed)
	}
	// The bound the first bind armed is the remaining silent client's answer.
	timers.fire(2 * time.Hour)
	select {
	case <-drained:
	default:
		t.Fatal("a silent client whose fellow left was never bounded")
	}
	l.stop()
}

// TestDaemonLifecycleAStaleConnectedDeadlineCannotRetireTheNewClient is review
// M1(a). A timer callback runs on its own goroutine, so Stop is not a
// happens-before edge: a callback that started before the disarm can reach the
// lock after the release armed a fresh bound, see `working == 0` (the panel
// client that left) and retire a daemon the NEW client is working on.
func TestDaemonLifecycleAStaleConnectedDeadlineCannotRetireTheNewClient(t *testing.T) {
	drained := make(chan struct{})
	timers := &recordingTimers{}
	l := newDaemonLifecycleWithTimer(time.Hour, 2*time.Hour, func() { close(drained) }, timers.afterFunc)

	l.clientBound() // silent A
	l.clientBound() // panel B
	// The bound armed by the FIRST bind is the one already running when B
	// arrives; a real timer keeps counting from the moment it was armed.
	stale := timers.last(2 * time.Hour)
	l.panelWorking()
	l.clientReleased(true)
	// B's release armed a FRESH bound for the silent client it left behind.
	// The stale callback now arrives, which is the loaded-machine case: it
	// began before the disarm, so Stop did not stop it. It must decide
	// nothing — the silent client has its own two minutes from the release,
	// not whatever was left of B's.
	stale()
	select {
	case <-drained:
		t.Fatal("a deadline the daemon had already replaced retired its helper the moment the panel client left")
	default:
	}

	timers.fire(2 * time.Hour)
	select {
	case <-drained:
	default:
		t.Fatal("the current connected-idle deadline did not bound the helper after its panel client left")
	}
	l.stop()
}

// TestDaemonLifecycleAStaleStartupDeadlineCannotOutliveItsClient is review
// M1(b). A stopped startup callback that runs after a client bound used to set
// `deadlineExpired`; the first admission finishing then read it as "nobody ever
// reached this helper" and retired a daemon with a panel client on it.
func TestDaemonLifecycleAStaleStartupDeadlineCannotOutliveItsClient(t *testing.T) {
	drained := make(chan struct{})
	timers := &recordingTimers{}
	l := newDaemonLifecycleWithTimer(time.Hour, 2*time.Hour, func() { close(drained) }, timers.afterFunc)

	finishWrite, err := l.admit()
	if err != nil {
		t.Fatalf("admission before the deadline = %v", err)
	}
	stale := timers.last(time.Hour)
	l.clientBound()
	stale()
	finishWrite()
	select {
	case <-drained:
		t.Fatal("a stale startup deadline retired a helper whose client had already answered it")
	default:
	}

	// The daemon is not immortal: the connected-idle bound still ends it once
	// its client stops working and leaves.
	l.clientReleased(false)
	timers.fire(2 * time.Hour)
	select {
	case <-drained:
	default:
		t.Fatal("the stale startup deadline left the daemon without a bound")
	}
	l.stop()
}
