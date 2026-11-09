//go:build nocx_dev_bind

package transport

import (
	"testing"
	"time"
)

// The stand's session-scoped startup bound (nocx-evj4b.1.6, review round 3):
// a spec asks for a long interval by opening under the marker, and every other
// session keeps the product's default. The hook is only in a `nocx_dev_bind`
// build, so this test is too.
func TestDevIntegrationStartupBoundIsTheMarkedUsersOnly(t *testing.T) {
	t.Setenv(devStartupBoundEnv, "120s")
	t.Setenv(devStartupBoundUserEnv, "nocx-e2e-long-bound")

	if got := devIntegrationStartupBound("nocx-e2e-long-bound"); got != 120*time.Second {
		t.Fatalf("marked user bound = %v, want 120s", got)
	}
	if got := devIntegrationStartupBound("someone-else"); got != 0 {
		t.Fatalf("an unmarked user got %v, want the product default (0)", got)
	}
}

func TestDevIntegrationStartupBoundIgnoresUnusableValues(t *testing.T) {
	for _, tc := range []struct{ bound, user string }{
		{bound: "not-a-duration", user: "nocx-e2e-long-bound"},
		{bound: "-5s", user: "nocx-e2e-long-bound"},
		{bound: "", user: "nocx-e2e-long-bound"},
		{bound: "120s", user: ""},
	} {
		t.Setenv(devStartupBoundEnv, tc.bound)
		t.Setenv(devStartupBoundUserEnv, tc.user)
		if got := devIntegrationStartupBound(tc.user); got != 0 {
			t.Fatalf("bound=%q user=%q produced %v, want 0", tc.bound, tc.user, got)
		}
	}
}
