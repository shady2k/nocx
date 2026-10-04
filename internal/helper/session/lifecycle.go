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
const MeasuredStartupGrace = 250 * time.Millisecond

type daemonLifecycle struct {
	mu                sync.Mutex
	enabled           bool
	grace             time.Duration
	deadline          time.Time
	admissions        int
	sessions          int
	everOwned         bool
	deadlineExpired   bool
	closingAdmissions bool
	draining          bool
	onDrained         func()
	once              sync.Once
	timer             *time.Timer
}

func newDaemonLifecycle(grace time.Duration, onDrained func()) *daemonLifecycle {
	l := &daemonLifecycle{enabled: grace > 0 || onDrained != nil, grace: grace, onDrained: onDrained}
	if grace > 0 {
		l.deadline = time.Now().Add(grace)
		l.timer = time.AfterFunc(grace, l.expire)
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
	if !l.everOwned && (l.deadlineExpired || (l.grace > 0 && !time.Now().Before(l.deadline))) {
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

func (l *daemonLifecycle) expire() {
	l.mu.Lock()
	l.deadlineExpired = true
	shouldFire := false
	if !l.everOwned && l.admissions == 0 {
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

func (l *daemonLifecycle) sessionEnded() { l.fireDrained(l.markSessionEnded()) }

func (l *daemonLifecycle) maybeDrainLocked() bool {
	if l.draining || l.admissions != 0 {
		return false
	}
	if (!l.everOwned && l.deadlineExpired) || (l.everOwned && l.sessions == 0 && l.closingAdmissions) {
		l.draining = true
		return true
	}
	return false
}

func (l *daemonLifecycle) stop() {
	if l.timer != nil {
		l.timer.Stop()
	}
}
