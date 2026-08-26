package transport

import (
	"testing"
	"time"
)

// ── control.saturated notification rate limit ─────────────────────────────
//
// A refused NOTIFICATION (no id) has no response to carry the -32004 error,
// so the server emits the control.saturated notification instead. The
// emission is rate-limited per (methodClass, scope): a burst of refused
// notifications must not flood the wire, even though the renderer also
// deduplicates its toast. These tests pin the limiter's contract.

func TestSaturatedNotifyLimiter_AllowsFirstAndRateLimitsBurst(t *testing.T) {
	l := newSaturatedNotifyLimiter(time.Minute)
	if !l.allow("ssh", "probe") {
		t.Fatal("first notification for a fresh key must be allowed")
	}
	if l.allow("ssh", "probe") {
		t.Fatal("a second notification inside the interval must be refused (rate limited)")
	}
	if l.allow("ssh", "probe") {
		t.Fatal("a third inside the interval must be refused")
	}
}

func TestSaturatedNotifyLimiter_RefreshesAfterInterval(t *testing.T) {
	l := newSaturatedNotifyLimiter(30 * time.Millisecond)
	if !l.allow("session", "control") {
		t.Fatal("first must be allowed")
	}
	if l.allow("session", "control") {
		t.Fatal("inside the interval must be refused")
	}
	// The limiter exposes no clock-control or expiry event; this duration is
	// the behavior under test, so wait beyond its configured 30 ms interval.
	time.Sleep(150 * time.Millisecond)
	if !l.allow("session", "control") {
		t.Fatal("after the interval a new notification must be allowed")
	}
}

func TestSaturatedNotifyLimiter_KeysAreIndependent(t *testing.T) {
	l := newSaturatedNotifyLimiter(time.Minute)
	if !l.allow("ssh", "probe") {
		t.Fatal("first key must be allowed")
	}
	// A different class or scope is a different episode: not limited by the
	// first key's emission.
	if !l.allow("config", "control") {
		t.Fatal("a different (class, scope) key must not inherit the first key's limit")
	}
	if !l.allow("ssh", "control") {
		t.Fatal("a different scope under the same class must not inherit the limit")
	}
	if l.allow("ssh", "probe") {
		t.Fatal("the original key is still inside its interval")
	}
}
