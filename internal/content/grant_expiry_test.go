package content_test

import (
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
)

// permitObserveAt is the smallest policy that mints a usable grant: one
// permitted row, so the mint has something to stamp a deadline on.
func permitObservePolicy() content.EffectPolicy {
	return content.EffectPolicy{
		Observe:           content.EffectRow{Decision: content.DecisionPermit},
		MutateReversible:  content.EffectRow{Decision: content.DecisionRefuse},
		MutateDestructive: content.EffectRow{Decision: content.DecisionRefuse},
		PrivilegeChange:   content.EffectRow{Decision: content.DecisionRefuse},
		Disclose:          content.EffectRow{Decision: content.DecisionRefuse},
		CrossBoundary:     content.EffectRow{Decision: content.DecisionRefuse},
		Delegate:          content.EffectRow{Decision: content.DecisionRefuse},
	}
}

// The mint states the deadline. Before this, EffectPolicy.AsGrant returned
// Grant{Version: 1, Policy: effective} and left ExpiresAt at zero, so every
// row in authority_grants recorded expires_at = 0 and ADR-0020 §5's
// "expiring" was a word in a document (nocx-1z1r1).
func TestAsGrantStampsTheDeadline(t *testing.T) {
	before := time.Now()
	g := permitObservePolicy().AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
	})
	after := time.Now()

	if g.ExpiresAt == 0 {
		t.Fatal("AsGrant left ExpiresAt at zero: the mint states the deadline")
	}
	lo := before.Add(content.GrantLifetime).UnixMilli()
	hi := after.Add(content.GrantLifetime).UnixMilli()
	if g.ExpiresAt < lo || g.ExpiresAt > hi {
		t.Fatalf("ExpiresAt = %d, want within [%d, %d] (mint + GrantLifetime)", g.ExpiresAt, lo, hi)
	}
}

// And on an ordinary machine the freshly minted grant is live — the paired
// half of every "refuses when expired" assertion below (AGENTS.md testing
// rule 3).
func TestAFreshlyMintedGrantIsNotExpired(t *testing.T) {
	g := permitObservePolicy().AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
	})
	if g.Expired(time.Now()) {
		t.Fatal("a grant minted a moment ago reports expired")
	}
}

// The interval has both ends: authority exists from the mint UNTIL the
// deadline, and the deadline itself is past it.
func TestGrantExpiredAtAndAfterTheDeadline(t *testing.T) {
	deadline := time.UnixMilli(1_750_000_000_000)
	g := content.Grant{Version: 1, ExpiresAt: deadline.UnixMilli()}

	if g.Expired(deadline.Add(-time.Millisecond)) {
		t.Fatal("a grant one millisecond before its deadline reports expired")
	}
	if !g.Expired(deadline) {
		t.Fatal("a grant AT its deadline is not expired: the interval is closed at the mint and open at the deadline")
	}
	if !g.Expired(deadline.Add(time.Millisecond)) {
		t.Fatal("a grant past its deadline is not expired")
	}
}

// A grant with no stated deadline is a grant nobody minted — a test literal,
// or a record written before the mint stamped one. It carries no deadline to
// compare, so it is not expired; the mint is what guarantees production
// grants always carry one (TestAsGrantStampsTheDeadline above, and the
// transport's runGrantFor test).
func TestGrantWithNoStatedDeadlineIsNotExpired(t *testing.T) {
	var g content.Grant
	if g.Expired(time.Now()) {
		t.Fatal("a grant with no stated deadline reports expired")
	}
}
