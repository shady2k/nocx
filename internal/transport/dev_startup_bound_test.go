//go:build nocx_dev_bind

package transport

import (
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/session"
)

// The stand's session-scoped startup bound (nocx-evj4b.1.6, review round 3):
// a spec asks for a long interval by opening under the marker, and every other
// session keeps the product's default. The hook is only in a `nocx_dev_bind`
// build, so this test is too.
func TestDevStartupBoundIsTheMarkedSessionsOnly(t *testing.T) {
	t.Setenv(devStartupBoundEnv, "120s")
	t.Setenv(devStartupBoundUserEnv, "nocx-e2e-long-bound")

	marked := session.ID("sid-marked")
	devNoteStartupBound(marked, "nocx-e2e-long-bound")
	if bound, ok := devTakeStartupBound(marked); !ok || bound != 120*time.Second {
		t.Fatalf("marked session bound = %v, %v; want 120s, true", bound, ok)
	}
	// Taken ONCE: the timer that arms from it is the only reader, and a second
	// session must not inherit the number.
	if _, ok := devTakeStartupBound(marked); ok {
		t.Fatal("the bound was answered twice for one session")
	}

	other := session.ID("sid-other")
	devNoteStartupBound(other, "someone-else")
	if _, ok := devTakeStartupBound(other); ok {
		t.Fatal("a session that does not name the marker got the stand's bound")
	}
}

func TestDevStartupBoundIgnoresUnusableValues(t *testing.T) {
	for _, tc := range []struct{ bound, user string }{
		{bound: "not-a-duration", user: "nocx-e2e-long-bound"},
		{bound: "-5s", user: "nocx-e2e-long-bound"},
		{bound: "", user: "nocx-e2e-long-bound"},
		{bound: "120s", user: ""},
	} {
		t.Setenv(devStartupBoundEnv, tc.bound)
		t.Setenv(devStartupBoundUserEnv, tc.user)
		sid := session.ID("sid-unusable")
		devNoteStartupBound(sid, tc.user)
		if _, ok := devTakeStartupBound(sid); ok {
			t.Fatalf("bound=%q user=%q produced a bound", tc.bound, tc.user)
		}
	}
}
