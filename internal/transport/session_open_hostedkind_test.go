package transport

import (
	"testing"

	"github.com/shady2k/nocx/internal/session"
)

// TestHostedKindNameMatchesRegOpensOwnWords is the drift guard
// hostedKindName's own comment promises: the "session opened" line this
// package logs for a helper-hosted open and the one session.Reg.Open logs
// for a local PTY must spell each Kind the SAME way, because a poll for
// "kind=ssh profile_id=…" (connection-password.spec.ts:202, :307) cannot
// tell which of the two lines it is reading.
func TestHostedKindNameMatchesRegOpensOwnWords(t *testing.T) {
	cases := []struct {
		kind session.Kind
		want string
	}{
		{session.KindLocal, "local"},
		{session.KindRemote, "ssh"},
	}
	for _, c := range cases {
		if got := hostedKindName(c.kind); got != c.want {
			t.Fatalf("hostedKindName(%v) = %q, want %q (session.Reg.Open's own word)", c.kind, got, c.want)
		}
	}
}
