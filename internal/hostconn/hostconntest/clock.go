// Package hostconntest is the clock the supervisor's tests advance, and the
// one the fault harness in the helper's own package uses too.
//
// It exists as a package rather than a file in hostconn's tests because two
// layers state the same policy: hostconn asserts the machine's transitions, and
// the helper's fault harness asserts the same policy over a real sshd and the
// switchable proxy. "Within 30 s" and "after ten minutes" have to mean the same
// thing in both, and a second fake clock would be a second answer.
//
// It is a test-support package: deadcode's ratchet excludes `…test` packages
// for the same reason the repo's other `…test` packages exist, and production
// code never imports it.
package hostconntest

import (
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/monoclock"
)

// Clock is a clock that only moves when a test moves it. It satisfies
// hostconn.Clock.
type Clock struct {
	mu      sync.Mutex
	now     monoclock.Nanos
	waiters []waiter
}

type waiter struct {
	at monoclock.Nanos
	ch chan monoclock.Nanos
}

// New builds a clock whose first reading is the process's monotonic clock, so
// that zero keeps its "unset" meaning in the supervisor's arithmetic.
func New() *Clock {
	return &Clock{now: monoclock.Now()}
}

// Now reads the clock.
func (c *Clock) Now() monoclock.Nanos {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After answers a channel that fires once d has elapsed on this clock.
func (c *Clock) After(d time.Duration) <-chan monoclock.Nanos {
	ch := make(chan monoclock.Nanos, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, waiter{at: c.now + monoclock.Nanos(d), ch: ch})
	return ch
}

// Advance moves the clock forward and fires every waiter that is now due, with
// the clock's reading at delivery. A wait registered while an advance is
// running is served by a later advance, which is what a test that advances once
// then waits for the supervisor's reaction expects.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now += monoclock.Nanos(d)
	now := c.now
	due := make([]waiter, 0, len(c.waiters))
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at <= now {
			due = append(due, w)
			continue
		}
		keep = append(keep, w)
	}
	c.waiters = keep
	c.mu.Unlock()
	for _, w := range due {
		// The channel is buffered (size 1) and never closed: a waiter fires
		// once, and a supervisor that has moved on simply never reads it.
		w.ch <- now
	}
}
