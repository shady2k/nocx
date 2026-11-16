package hostconn

// The three facts a close for silence rests on, asserted where they are decided
// (nocx-oalo1.4.3). None of the three has a black-box shape: a refused close and
// a refused control edge produce no state change at all, and a schedule that
// names a moment the close does not use produces nothing a surface could show.
// An internal test is the only witness they can have.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/monoclock"
)

// fixedClock is a clock these tests read from and never advance: the guard is
// driven by hand here, so nothing waits on time. It exists because the shared
// test clock lives in hostconntest, which imports this package — an internal
// test may not import it.
type fixedClock struct{ at monoclock.Nanos }

func (c *fixedClock) Now() monoclock.Nanos { return c.at }

func (c *fixedClock) After(time.Duration) <-chan monoclock.Nanos { return nil }

// stillCarrier is only ever asked its name: nothing here dials.
type stillCarrier struct{}

func (stillCarrier) Name() string               { return "still" }
func (stillCarrier) Capabilities() Capabilities { return Capabilities{} }
func (stillCarrier) Dial(context.Context, Plan, EventSink) (Link, error) {
	return nil, errors.New("stillCarrier: nothing dials here")
}

// stillLink is a transport a test closes by hand.
type stillLink struct {
	mu     sync.Mutex
	closes int
}

func (l *stillLink) Done() <-chan struct{} { return nil }

func (l *stillLink) Err() error { return nil }

func (l *stillLink) Close() error {
	l.mu.Lock()
	l.closes++
	l.mu.Unlock()
	return nil
}

func (l *stillLink) closeCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closes
}

// closingStand builds one supervisor with one live connection, and no goroutine
// at all: the guard is the test's to drive.
func closingStand(t *testing.T) (*Supervisor, *live, *stillLink) {
	t.Helper()
	_, logger := logtest.New(t)
	link := &stillLink{}
	sup := &Supervisor{
		key:     Key{Host: "host.example", Port: 22, User: "dev", Identity: "key"},
		carrier: stillCarrier{},
		log:     logger,
		// A reading that is NOT zero, so zero keeps its "unset" meaning in the
		// guard's arithmetic (hostconntest.New makes the same choice for the same
		// reason).
		clock:   &fixedClock{at: monoclock.Now()},
		changed: make(chan struct{}),
		state:   State{Kind: Connected},
	}
	l := &live{
		link:      link,
		holders:   1,
		stop:      make(chan struct{}),
		closed:    make(chan struct{}),
		ready:     make(chan struct{}, 1),
		waiters:   map[chan error]struct{}{},
		observers: map[*Lease]func(Observation){},
	}
	sup.live = l
	sup.generation = 1
	return sup, l, link
}

// TestAnAnsweredConnectionIsNotClosedForSilence is the fence: "the far side
// answered" and "close for silence" are one decision under one lock, so a close
// can never run over an answer that is already recorded — and an answer cannot
// land on a connection the fence has already taken.
func TestAnAnsweredConnectionIsNotClosedForSilence(t *testing.T) {
	sup, l, link := closingStand(t)

	seq := l.beginProbe()
	l.recordAnswer(seq, time.Millisecond, nil)

	if sup.endForSilence(l, sup.generation, "test") {
		t.Fatal("a connection whose probe had answered was closed for silence")
	}
	if got := link.closeCount(); got != 0 {
		t.Fatalf("the transport was closed %d time(s) over an answer already recorded", got)
	}
	if l.closing.Load() {
		t.Fatal("the connection was fenced over an answer already recorded")
	}
	if _, ok := l.takeAnswer(seq); !ok {
		t.Fatal("the recorded answer was lost: the guard would keep reading the connection as silent")
	}
}

// TestASilentConnectionIsFencedAndItsLateAnswerIsDropped is the paired fact: a
// probe that has NOT returned is silence, the close proceeds, and an answer that
// arrives afterwards is moot — recording it would put the two facts back in a
// race.
func TestASilentConnectionIsFencedAndItsLateAnswerIsDropped(t *testing.T) {
	sup, l, link := closingStand(t)

	seq := l.beginProbe()
	if !sup.endForSilence(l, sup.generation, "test") {
		t.Fatal("a connection whose probe had not returned refused the close")
	}
	if got := link.closeCount(); got != 1 {
		t.Fatalf("the transport was closed %d time(s), want 1", got)
	}
	l.recordAnswer(seq, time.Millisecond, nil)
	if _, ok := l.takeAnswer(seq); ok {
		t.Fatal("an answer was recorded on a connection already closed for silence")
	}
}

// TestTheBudgetTheGuardSchedulesFromIsTheBudgetItClosesOn is the spin: the
// schedule and the close used to name different moments (the probe's send and
// the guard's own reading of the silence), and in the gap between them the loop
// took its due branch for a move that was not due, forever — never reaching the
// select that watches stop, the answer and a caller's ask.
func TestTheBudgetTheGuardSchedulesFromIsTheBudgetItClosesOn(t *testing.T) {
	sup, l, _ := closingStand(t)
	clock, ok := sup.clock.(*fixedClock)
	if !ok {
		t.Fatalf("the stand's clock is %T", sup.clock)
	}

	sent := clock.Now()
	stalledAt := sent + monoclock.Nanos(ProbeDeadline)
	// A moment inside the old gap: the send's budget has run out, the guard's
	// first reading of the silence is a deadline younger, and nothing is due.
	now := stalledAt + monoclock.Nanos(SilenceCloseBudget) - monoclock.Nanos(time.Second)

	clock.at = now
	seq := l.beginProbe()
	g := &prober{
		sup: sup, live: l, gen: sup.generation, clock: clock,
		st: guardState{
			probeSent: true, probeSeq: seq,
			probedAt: sent, silentSince: sent, silentSeen: stalledAt,
			lookAnswered: true, lookAt: now,
		},
	}

	if g.due(now) {
		t.Fatal("the guard ended at a moment when nothing was due")
	}
	if next := g.nextStalled(now); next <= now {
		t.Fatalf("the policy schedules its next move %v in the past while nothing is due: the loop would take its due branch forever and never reach the select", now-next)
	}
}

// TestAWakeOlderThanTheReadingOfSilenceCannotClose is the same protection for the
// wake's own bound: a signal delivered late — a frozen process, a clock jump —
// must not put the guard past wokeAt+30 s in the very pass that declared the
// silence. The bound is spent from the later of the signal and the guard's own
// reading.
func TestAWakeOlderThanTheReadingOfSilenceCannotClose(t *testing.T) {
	sup, l, link := closingStand(t)
	clock, ok := sup.clock.(*fixedClock)
	if !ok {
		t.Fatalf("the stand's clock is %T", sup.clock)
	}

	now := clock.Now()
	// The signal is ten minutes old and the silence was read just now.
	signal := now - monoclock.Nanos(10*time.Minute)

	clock.at = now
	seq := l.beginProbe()
	g := &prober{
		sup: sup, live: l, gen: sup.generation, clock: clock,
		st: guardState{
			probeSent: true, probeSeq: seq,
			probedAt: signal, silentSince: signal, silentSeen: now,
			wokeAt: signal, lookAnswered: true, lookAt: now,
		},
	}

	if g.due(now) {
		t.Fatal("a wake signal ten minutes older than the guard's reading of the silence closed the connection")
	}
	if got := link.closeCount(); got != 0 {
		t.Fatalf("the transport was closed %d time(s) on a stale wake signal", got)
	}
}

// parkLink is a connection whose look answers at once and whose look WORK never
// ends: the shape a routed look has through a hop that has gone quiet — the call
// returns when its context ends, and the read it started waits for the hop's
// connection to end.
type parkLink struct {
	stillLink
	parked atomic.Bool
	looks  atomic.Int32
}

func (l *parkLink) Look(context.Context) error {
	l.looks.Add(1)
	return errors.New("the hop is silent")
}

func (l *parkLink) LookParked() bool { return l.parked.Load() }

// TestTheGuardsLookCadenceLeavesOneLookParkedAtMost drives the guard by hand
// across several look intervals with the previous look's work still out there,
// which is the state a long stall through a silent hop leaves: at a fifteen
// second cadence an unbounded guard would add one parked goroutine and one open
// channel per interval, for hours.
func TestTheGuardsLookCadenceLeavesOneLookParkedAtMost(t *testing.T) {
	sup, l, _ := closingStand(t)
	clock, ok := sup.clock.(*fixedClock)
	if !ok {
		t.Fatalf("the stand's clock is %T", sup.clock)
	}
	link := &parkLink{}
	l.link = link

	now := clock.Now()
	clock.at = now
	seq := l.beginProbe()
	g := &prober{
		sup: sup, live: l, gen: sup.generation, clock: clock, looker: link,
		st: guardState{
			probeSent: true, probeSeq: seq,
			probedAt: now, silentSince: now, silentSeen: now, nextLook: now,
		},
	}

	// The previous look's work is still out there, and three intervals' worth of
	// due moments pass.
	link.parked.Store(true)
	for i := 0; i < 3; i++ {
		now += monoclock.Nanos(LookInterval)
		clock.at = now
		if g.due(now) {
			t.Fatal("the guard ended on a look's due moment")
		}
		// The guard loop would take the look's result next; drain it so the
		// look slot is free again, which is the state the bound exists for (the
		// work behind it is still parked, and LookParked says so).
		if g.st.lookOut != nil {
			g.lookResult(<-g.st.lookOut)
		}
	}
	if got := link.looks.Load(); got != 0 {
		t.Fatalf("%d look(s) were started while the previous look's work was still parked, want 0", got)
	}

	// The work ends, and the next interval's look goes out.
	link.parked.Store(false)
	now += monoclock.Nanos(LookInterval)
	clock.at = now
	if g.due(now) {
		t.Fatal("the guard ended on a look's due moment")
	}
	if g.st.lookOut == nil {
		t.Fatal("no look was started once the previous look's work ended")
	}
	g.lookResult(<-g.st.lookOut)
	if got := link.looks.Load(); got != 1 {
		t.Fatalf("%d look(s) were started once the previous look's work ended, want 1", got)
	}
}
