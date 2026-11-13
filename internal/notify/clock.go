package notify

import (
	"sort"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/operation/catalog"
)

// Clock is the time source Policy schedules its debounce windows on. The
// production clock is real time; tests inject a manual clock and advance
// windows deterministically, so no test ever sleeps a window (AGENTS.md: a
// test may not depend on timing).
type Clock interface {
	// Now returns the current time.
	Now() time.Time

	// AfterFunc schedules fn to run after d and returns a handle that can
	// stop the call before it runs.
	AfterFunc(d time.Duration, fn func()) Timer
}

// Timer is a scheduled call as returned by Clock.AfterFunc.
type Timer = catalog.Timer

// RealClock is the production clock: time.Now and time.AfterFunc.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) AfterFunc(d time.Duration, fn func()) Timer {
	return time.AfterFunc(d, fn)
}

// ManualClock is a deterministic Clock for tests. Time moves only when
// Advance is called, and every scheduled call whose deadline has passed
// fires synchronously inside that same Advance, in deadline order — a test
// drives a debounce window without sleeping a millisecond. Advance runs
// calls until none are due, so a call that schedules another call with a
// deadline already past is handled correctly.
type ManualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
}

type manualTimer struct {
	clock   *ManualClock
	at      time.Time
	fn      func()
	stopped bool
	fired   bool
}

// NewManualClock returns a clock stopped at the zero time; Advance moves it.
func NewManualClock() *ManualClock { return &ManualClock{} }

func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ManualClock) AfterFunc(d time.Duration, fn func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &manualTimer{clock: c, at: c.now.Add(d), fn: fn}
	c.timers = append(c.timers, t)
	return t
}

func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.fired {
		return false
	}
	t.stopped = true
	return true
}

// Advance moves the clock forward by d exactly once and fires every
// scheduled call whose deadline has passed, in deadline order. A call that
// fires may schedule further calls; Advance keeps firing until none are
// due, but never advances the clock again.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	for {
		var due []*manualTimer
		remaining := c.timers[:0]
		for _, t := range c.timers {
			if t.stopped {
				continue
			}
			if !t.at.After(c.now) {
				t.fired = true
				due = append(due, t)
			} else {
				remaining = append(remaining, t)
			}
		}
		c.timers = remaining
		if len(due) == 0 {
			c.mu.Unlock()
			return
		}
		sort.Slice(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
		c.mu.Unlock()
		for _, t := range due {
			t.fn()
		}
		c.mu.Lock()
	}
}
