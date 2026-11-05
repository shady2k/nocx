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
	l.expire()
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
		go func() { defer wg.Done(); <-start; l.expire() }()
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
	l.clientReleased()
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
