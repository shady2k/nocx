package vault

import (
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/waittest"
)

// Close returns on a Vault whose auto-seal goroutine was never started.
//
// Close owns both ends of the goroutine's lifetime: it signals the exit and
// then waits for it. The wait is unconditional, so it has to be well defined
// on a Vault that has no goroutine to wait for — a literal in a test today, a
// constructor that fails after assembling the struct tomorrow. If it is not,
// the caller blocks with nothing to read afterwards but a ten-minute panic
// whose stack names the parked receive and not the assembly that caused it.
//
// The vault here is everything New builds except the `go autoSealLoop()`,
// which is precisely the state such a caller is in.
func TestClose_ReturnsWhenTheAutoSealGoroutineWasNeverStarted(t *testing.T) {
	v := &Vault{
		autoSealWake: make(chan struct{}, 1),
		autoSealQuit: make(chan struct{}),
	}

	var returned atomic.Bool
	go func() {
		v.Close()
		returned.Store(true)
	}()

	waittest.WaitFor(t, "Close to return on a vault with no auto-seal goroutine", returned.Load)
}

// Close does not return until the auto-seal goroutine has exited.
//
// This is the other end of the interval, and the one the goroutine-count test
// cannot see: that test polls, so a Close that returned early while its
// goroutine was a microsecond from exiting reads exactly like one that
// waited. Here the loop is held inside the timer's Stop — a point it has
// provably reached and provably not passed — and Close is asked, from another
// goroutine, whether it has returned. It must not have.
func TestClose_WaitsForTheAutoSealGoroutineToExit(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderFile))
	clock, release := useHeldFakeAutoSealTimer(t, v)
	setupAndDrainAutoSeal(t, v, clock, "pass")
	timer := clock.armed(t, 1)

	var returned atomic.Bool
	go func() {
		v.Close()
		returned.Store(true)
	}()

	waittest.WaitFor(t, "the auto-seal goroutine to reach the timer Stop on its way out",
		timer.stopEntered.Load)
	if returned.Load() {
		t.Fatal("Close returned while its auto-seal goroutine was still inside Stop")
	}

	release()
	waittest.WaitFor(t, "Close to return once the goroutine is released", returned.Load)
}
