// Package waittest provides polling helpers for asynchronous tests.
package waittest

import (
	"testing"
	"time"
)

const (
	// DefaultTimeout is the ceiling used by WaitFor. Tests with an unusually
	// slow external fixture can use WaitForTimeout with an explicit ceiling.
	DefaultTimeout = 5 * time.Second
	pollInterval   = 10 * time.Millisecond
)

// WaitFor polls cond until it returns true. It fails the test if the condition
// is not observed within DefaultTimeout; what is included in that failure so
// the missing state is actionable.
func WaitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	waitFor(t, what, DefaultTimeout, cond)
}

// WaitForTimeout is WaitFor with an explicit timeout for genuinely slow test
// fixtures. Polling remains internal so callers wait on state, not a duration.
func WaitForTimeout(t testing.TB, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	waitFor(t, what, timeout, cond)
}

// WaitForDetail is WaitFor with state attached to the failure. detail is
// called only when the wait times out, so the failure can say what the system
// was actually doing and not merely what it was waiting for — the accepted
// events, the output so far, the stages that did arrive. A timeout that names
// only its own description cannot be told apart from a starved machine, which
// is the judgement this suite has to make on every red container run.
func WaitForDetail(t testing.TB, what string, detail func() string, cond func() bool) {
	t.Helper()
	waitForDetail(t, what, DefaultTimeout, detail, cond)
}

// WaitForTimeoutDetail is WaitForDetail with an explicit ceiling.
func WaitForTimeoutDetail(t testing.TB, what string, timeout time.Duration, detail func() string, cond func() bool) {
	t.Helper()
	waitForDetail(t, what, timeout, detail, cond)
}

func waitFor(t testing.TB, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	waitForDetail(t, what, timeout, nil, cond)
}

func waitForDetail(t testing.TB, what string, timeout time.Duration, detail func() string, cond func() bool) {
	// Without this the timeout is reported against this file rather than the
	// caller's line, which would defeat the whole point of naming `what`.
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if timeout <= 0 || !time.Now().Before(deadline) {
			if detail != nil {
				t.Fatalf("timed out waiting for %s; %s", what, detail())
			}
			t.Fatalf("timed out waiting for %s", what)
			return
		}
		delay := pollInterval
		if remaining := time.Until(deadline); remaining < delay {
			delay = remaining
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
}
