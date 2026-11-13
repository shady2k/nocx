// Package hostconntest is the clock the supervisor's tests advance and the wake
// source they pull.
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

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/monoclock"
)

// WakeSource is a wake source a test pulls: the OS sources report a real
// suspend, which no CI machine can stage, so the policy is asserted against a
// signal the test sends. It stamps each signal with the test's clock, which is
// the moment a real source gives it.
type WakeSource struct {
	clock  hostconn.Clock
	mu     sync.Mutex
	ch     chan monoclock.Nanos
	sleeps chan monoclock.Nanos
}

// NewWakeSource builds a source on the clock the supervisors are running on.
func NewWakeSource(clock hostconn.Clock) *WakeSource {
	return &WakeSource{
		clock:  clock,
		ch:     make(chan monoclock.Nanos, 8),
		sleeps: make(chan monoclock.Nanos, 8),
	}
}

// Wakes answers the channel the supervisor reads.
func (w *WakeSource) Wakes() <-chan monoclock.Nanos { return w.ch }

// Sleeps answers the channel the supervisor reads when the machine is about to
// sleep: the test source offers the pre-sleep edge too, so the pause it causes
// can be asserted.
func (w *WakeSource) Sleeps() <-chan monoclock.Nanos { return w.sleeps }

// Sleep reports that the machine is about to sleep, at the clock's current
// reading.
func (w *WakeSource) Sleep() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sleeps <- w.clock.Now()
}

// Wake reports one wake from sleep at the clock's current reading.
func (w *WakeSource) Wake() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ch <- w.clock.Now()
}
