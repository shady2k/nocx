package filesystem_test

import (
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/filesystem"
)

// TestWatchPollWaitIsTheLadder pins the one ladder the transport and the far
// helper both poll on. The two parties are asserted against this function from
// their own tests; this is the function's own statement, so a change to a rung
// is a change somebody had to make here, on purpose.
func TestWatchPollWaitIsTheLadder(t *testing.T) {
	base := filesystem.WatchPollBase
	if base != 500*time.Millisecond {
		t.Fatalf("WatchPollBase = %v, want 500ms", base)
	}
	quiet := []time.Duration{2, 4, 10, 20}
	for step, multiplier := range quiet {
		want := multiplier * base
		if got := filesystem.WatchPollWait(base, step+1, 0); got != want {
			t.Fatalf("quiet step %d = %v, want %v", step+1, got, want)
		}
	}
	// A step past the cap is the cap, never a longer wait: a path that has been
	// quiet for a minute stays at the slowest legitimate rung.
	if got, want := filesystem.WatchPollWait(base, 99, 0), 20*base; got != want {
		t.Fatalf("quiet step past the cap = %v, want %v", got, want)
	}
	failing := []time.Duration{20, 40, 80, 120}
	for step, multiplier := range failing {
		want := multiplier * base
		if got := filesystem.WatchPollWait(base, 0, step+1); got != want {
			t.Fatalf("failure step %d = %v, want %v", step+1, got, want)
		}
	}
	if got, want := filesystem.WatchPollWait(base, 0, 99), 120*base; got != want {
		t.Fatalf("failure step past the cap = %v, want %v", got, want)
	}
	// The failure ladder outranks the quiet one: a path that is both noisy and
	// unreachable waits like an unreachable one.
	if got, want := filesystem.WatchPollWait(base, 0, 1), 20*base; got != want {
		t.Fatalf("failing rung = %v, want %v", got, want)
	}
	// A base of zero is the default rather than a zero wait: a caller that
	// forgot to pass one must not turn the ladder into a spin.
	if got, want := filesystem.WatchPollWait(0, 0, 0), 2*filesystem.WatchPollBase; got != want {
		t.Fatalf("zero base = %v, want %v", got, want)
	}
}
