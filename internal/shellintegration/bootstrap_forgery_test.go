package shellintegration

import (
	"context"
	"testing"
)

// §6.1's two rules against a forged readiness token, and §11 assertion 42.
//
// The party these tests model is the one §5.5 names: anyone who can write the
// session's PTY. It cannot force step 4 or step 5 — those are facts of the
// backend's own — but step 3, "frame 1 received and verified", is known to us
// only because stage-1 says so ON THE TERMINAL, and that token is exactly the
// one a forger controls.

// Rule 2, and the direction that needs no race at all.
//
// The loader refuses before stage-1 ever runs — an absent hasher, a digest
// mismatch, an unreachable /dev/fd/N are each already a named outcome — and
// emits its refusal. A forged STAGE_READY written AFTERWARDS must mint
// nothing and must cause no frame to be written: in the honest refusal case
// the session mints nothing at all, so a mint here would produce a live
// per-epoch bearer where there would otherwise be none.
func TestDeliverBootstrap_AForgedReadyAfterARefusalMintsNothing(t *testing.T) {
	opts := stageOpts()
	minted := 0
	plan := testPlan(t, opts, &minted)
	far := &scriptedFarSide{steps: []farSideStep{
		{line: LoaderReadyToken},
		// The honest refusal: the loader could not verify stage-1.
		{line: OutcomePrefix + OutcomeToken(OutcomeStageDigestMismatch)},
		// The forgery, arriving after it.
		{line: StageReadyToken},
		{line: StageReadyToken},
	}}
	lg, _ := captureLog()

	out := DeliverBootstrap(context.Background(), lg, far, plan)
	if out != OutcomeStageDigestMismatch {
		t.Fatalf("outcome = %q, want %q — the honest refusal is the outcome", out, OutcomeStageDigestMismatch)
	}
	if minted != 0 {
		t.Errorf("the pair was minted %d times after a terminal outcome; want 0", minted)
	}
	if n := len(far.written()); n != 1 {
		t.Errorf("%d writes, want exactly one (frame 1): no frame may be written after an observed outcome", n)
	}
}

// The same rule with the outcome arriving before READY: the window is closed
// and the loader frame itself is never written.
func TestDeliverBootstrap_NoFrameIsWrittenAfterAnOutcomeSeenBeforeReady(t *testing.T) {
	opts := stageOpts()
	minted := 0
	plan := testPlan(t, opts, &minted)
	far := &scriptedFarSide{steps: []farSideStep{
		{line: OutcomePrefix + OutcomeToken(OutcomeNoSecureTemp)},
		{line: LoaderReadyToken},
		{line: StageReadyToken},
	}}
	lg, _ := captureLog()

	if out := DeliverBootstrap(context.Background(), lg, far, plan); out != OutcomeNoSecureTemp {
		t.Fatalf("outcome = %q, want %q", out, OutcomeNoSecureTemp)
	}
	if minted != 0 {
		t.Errorf("minted %d times after a terminal outcome; want 0", minted)
	}
	if n := len(far.written()); n != 0 {
		t.Errorf("%d writes after a terminal outcome, want 0", n)
	}
}

// The other direction of assertion 42: injected BEFORE the honest refusal,
// the race is genuine and cannot be closed by framing — winning it requires
// writing the session's terminal, which is also enough to read the frame. What
// the design promises instead is that the outcome is still a REFUSAL, which is
// what the backend hangs its hard invalidation on: the winner holds a bearer
// that dies with the outcome it outran.
func TestDeliverBootstrap_AForgedReadyBeforeTheRefusalStillEndsInARefusal(t *testing.T) {
	opts := stageOpts()
	minted := 0
	plan := testPlan(t, opts, &minted)
	far := &scriptedFarSide{steps: []farSideStep{
		{line: LoaderReadyToken},
		{line: StageReadyToken}, // forged: stage-1 never ran
		{line: OutcomePrefix + OutcomeToken(OutcomeStageDigestMismatch)},
	}}
	lg, _ := captureLog()

	out := DeliverBootstrap(context.Background(), lg, far, plan)
	if out == OutcomeBootstrapAccepted {
		t.Fatal("a forged STAGE_READY produced an ACCEPTED outcome; the refusal must win the report")
	}
	if out != OutcomeStageDigestMismatch {
		t.Errorf("outcome = %q, want %q", out, OutcomeStageDigestMismatch)
	}
	// The race did produce a frame — this is the exposure the design names
	// and declines to claim away. It is recorded here so a later change that
	// silently widened or narrowed it fails this test rather than a review.
	if minted != 1 {
		t.Errorf("minted %d times, want 1 — the race is real and is bounded by invalidation, not by framing", minted)
	}
}

// Rule 1, out of order: STAGE_READY before LOADER_READY is a NAMED bootstrap
// failure, not a token that is quietly dropped and not a second trigger.
func TestDeliverBootstrap_AnOutOfOrderTokenIsANamedFailure(t *testing.T) {
	opts := stageOpts()
	minted := 0
	plan := testPlan(t, opts, &minted)
	far := &scriptedFarSide{steps: []farSideStep{
		{line: StageReadyToken},
		{line: LoaderReadyToken},
	}}
	lg, _ := captureLog()

	if out := DeliverBootstrap(context.Background(), lg, far, plan); out != OutcomeBootstrapOutOfOrder {
		t.Fatalf("outcome = %q, want %q", out, OutcomeBootstrapOutOfOrder)
	}
	if minted != 0 {
		t.Errorf("minted %d times on an out-of-order token; want 0", minted)
	}
	if n := len(far.written()); n != 0 {
		t.Errorf("%d writes on an out-of-order token, want 0", n)
	}
}

// Rule 1, repeated: a second LOADER_READY is not a second trigger.
func TestDeliverBootstrap_ARepeatedTokenIsANamedFailure(t *testing.T) {
	opts := stageOpts()
	minted := 0
	plan := testPlan(t, opts, &minted)
	far := &scriptedFarSide{steps: []farSideStep{
		{line: LoaderReadyToken},
		{line: LoaderReadyToken},
		{line: StageReadyToken},
	}}
	lg, _ := captureLog()

	if out := DeliverBootstrap(context.Background(), lg, far, plan); out != OutcomeBootstrapOutOfOrder {
		t.Fatalf("outcome = %q, want %q", out, OutcomeBootstrapOutOfOrder)
	}
	if minted != 0 {
		t.Errorf("minted %d times on a repeated token; want 0", minted)
	}
	// Frame 1 went out on the FIRST, honest READY. The failure is what stops
	// a second one from producing a second frame.
	if n := len(far.written()); n != 1 {
		t.Errorf("%d writes, want exactly one (frame 1 on the first READY)", n)
	}
}

// A repeated STAGE_READY after the pair has been delivered is the same rule
// one step along: the window is sealed by then, so it can neither mint nor
// write.
func TestDeliverBootstrap_ARepeatedStageReadyAfterTheSecretIsRefused(t *testing.T) {
	opts := stageOpts()
	minted := 0
	plan := testPlan(t, opts, &minted)
	far := &scriptedFarSide{steps: []farSideStep{
		{line: LoaderReadyToken},
		{line: StageReadyToken},
		{line: StageReadyToken},
	}}
	lg, _ := captureLog()

	if out := DeliverBootstrap(context.Background(), lg, far, plan); out != OutcomeBootstrapOutOfOrder {
		t.Fatalf("outcome = %q, want %q", out, OutcomeBootstrapOutOfOrder)
	}
	if minted != 1 {
		t.Errorf("minted %d times, want exactly 1 — the repeat must not mint a second pair", minted)
	}
	if n := len(far.written()); n != 2 {
		t.Errorf("%d writes, want exactly two frames", n)
	}
}

// Every backend-named outcome carries a token, including the one this package
// adds: OutcomeForToken is the single table both sides read, and an outcome
// with no token cannot be reported at all.
func TestOutcomeOutOfOrder_HasATokenAndRoundTrips(t *testing.T) {
	tok := OutcomeToken(OutcomeBootstrapOutOfOrder)
	if tok == "" {
		t.Fatal("OutcomeBootstrapOutOfOrder has no wire token")
	}
	got, ok := OutcomeForToken(tok)
	if !ok || got != OutcomeBootstrapOutOfOrder {
		t.Errorf("OutcomeForToken(%q) = %q,%v", tok, got, ok)
	}
}
