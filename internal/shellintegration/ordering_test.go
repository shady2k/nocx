package shellintegration

import (
	"context"
	"errors"
	"testing"
	"time"
)

// §6.1's ordering, as a thing that can be asserted rather than a paragraph.
//
// Nothing here waits on a duration. The gate's two facts are SCRIPTED events —
// the test says "the receiver is ready" and "the publish settled" — and the
// schedule is arithmetic over a declared graph, never a stopwatch (§11
// assertion 32, and its opening sentence).

func TestMintGate_NothingIsMintedBeforeTheReceiverIsReady(t *testing.T) {
	g := NewMintGate()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var out PublishOutcome
	var err error
	go func() {
		out, err = g.Await(ctx)
		close(done)
	}()

	// Only the publish has settled. The mint may not proceed on it alone:
	// step 4 is a fact of its own.
	g.PublishSettled(PublishCommitted)
	select {
	case <-done:
		t.Fatalf("Await returned on the publish alone (out=%q err=%v); §6.1 step 4 is a separate fact", out, err)
	case <-time.After(20 * time.Millisecond):
	}

	g.ReceiverReady()
	<-done
	if err != nil {
		t.Fatalf("Await after both facts = %v, want nil", err)
	}
	if out != PublishCommitted {
		t.Errorf("outcome = %q, want %q", out, PublishCommitted)
	}
}

func TestMintGate_NothingIsMintedBeforeThePublishSettles(t *testing.T) {
	g := NewMintGate()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_, _ = g.Await(ctx)
		close(done)
	}()
	g.ReceiverReady()
	select {
	case <-done:
		t.Fatal("Await returned before the publish reached a terminal outcome (§6.1 step 5)")
	case <-time.After(20 * time.Millisecond):
	}
	g.PublishSettled(PublishFailed)
	<-done
}

// "After a failed publish the far side may still accept a generation
// installed earlier, so a failed publish is not a refusal" (§6.1).
func TestMintGate_AFailedOrContendedPublishIsNotARefusal(t *testing.T) {
	for _, o := range []PublishOutcome{PublishCommitted, PublishUnchanged, PublishFailed, PublishContended} {
		g := NewMintGate()
		g.ReceiverReady()
		g.PublishSettled(o)
		got, err := g.Await(context.Background())
		if err != nil {
			t.Errorf("publish %q: Await = %v, want nil — a failed publish is not a refusal", o, err)
		}
		if got != o {
			t.Errorf("publish %q: outcome = %q", o, got)
		}
	}
}

// Assertion 12: with the lifecycle channel refused, no capability and no
// fence are ever generated.
func TestMintGate_ARefusedReceiverMintsNothing(t *testing.T) {
	g := NewMintGate()
	g.PublishSettled(PublishCommitted)
	g.ReceiverUnavailable(errors.New("forwarding refused"))
	if _, err := g.Await(context.Background()); err == nil {
		t.Fatal("Await = nil after the receiver was refused; nothing may be minted")
	}
}

// And it waits for the publish EVEN WHEN THE RECEIVER WAS REFUSED. Step 5
// orders frame 2, not the mint: step 8 — the far side re-proving the
// generation as it now stands — follows frame 2 whether it carries a bearer or
// the non-secret refusal. Await returned on the receiver's error alone, which
// re-opened for the refusal path exactly the mutation race step 5 exists to
// close: stage-1 read its frame while the publish was still in flight and
// degraded a session whose publish committed a moment later.
func TestMintGate_ARefusedReceiverStillWaitsForThePublish(t *testing.T) {
	g := NewMintGate()
	g.ReceiverUnavailable(errors.New("forwarding refused"))

	done := make(chan error, 1)
	go func() {
		_, err := g.Await(context.Background())
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("Await returned before the publish reached a terminal outcome; " +
			"frame 2 would race the publish stage-1 verifies against (§6.1 steps 5 and 8)")
	case <-time.After(20 * time.Millisecond):
	}

	g.PublishSettled(PublishCommitted)
	if err := <-done; err == nil {
		t.Fatal("Await = nil after the receiver was refused; nothing may be minted")
	}
}

// The gate is a gate, not a decision: it is answered exactly once per fact
// and a second answer never reopens it.
func TestMintGate_EachFactIsAnsweredOnce(t *testing.T) {
	g := NewMintGate()
	g.ReceiverReady()
	g.ReceiverUnavailable(errors.New("too late"))
	g.PublishSettled(PublishUnchanged)
	g.PublishSettled(PublishFailed)
	out, err := g.Await(context.Background())
	if err != nil {
		t.Fatalf("a second receiver answer reopened the gate: %v", err)
	}
	if out != PublishUnchanged {
		t.Errorf("outcome = %q, want %q — the first answer wins", out, PublishUnchanged)
	}
}

func TestMintGate_ACancelledAttemptMintsNothing(t *testing.T) {
	g := NewMintGate()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Await(ctx); err == nil {
		t.Fatal("Await = nil on a cancelled attempt; nothing may be minted")
	}
}

// ── the schedule (§7's arithmetic, §11 assertion 32) ──────────────────────

// The publish and the receiver are SCHEDULED CONCURRENTLY. Asserted on the
// declared graph: neither step waits for the other.
func TestBootstrapSchedule_PublishAndReceiverAreConcurrent(t *testing.T) {
	if waitsFor(t, stepPublish, stepReceiverReady) {
		t.Error("the publish waits for the receiver; §7 requires them concurrent")
	}
	if waitsFor(t, stepReceiverReady, stepPublish) {
		t.Error("the receiver waits for the publish; §7 requires them concurrent")
	}
}

// The user's session channel is claimed before nocx opens an auxiliary one,
// and claiming it does not serialize the publish behind the loader.
//
// Both halves matter and they pull in opposite directions. Without the first
// edge the publish can spend the server's last session slot on nocx's own
// work and the user gets no terminal at all — ADR-0004's one absolute,
// broken by nocx itself. With an edge to the RECEIVER instead, the publish
// would be sequential and §7's arithmetic would stop closing. So: a common
// ancestor, and still no edge between the two concurrent branches.
func TestBootstrapSchedule_ThePublishWaitsForTheUsersSessionChannel(t *testing.T) {
	if !waitsFor(t, stepPublish, stepSessionEstablished) {
		t.Error("the publish does not wait for the user's session channel; with one session slot " +
			"it can take the slot the interactive shell needs and Connect returns no terminal")
	}
	if waitsFor(t, stepSessionEstablished, stepPublish) {
		t.Error("the user's session channel waits for the publish; the whole point is that it does not")
	}
	if waitsFor(t, stepPublish, stepReceiverReady) || waitsFor(t, stepReceiverReady, stepPublish) {
		t.Error("claiming the session channel serialized the publish against the receiver; " +
			"§7's arithmetic closes only while those two are concurrent")
	}
}

// The mint waits for BOTH, and for nothing less (§6.1 step 6).
func TestBootstrapSchedule_TheMintWaitsForBothFacts(t *testing.T) {
	if !waitsFor(t, stepMint, stepPublish) {
		t.Error("the mint does not wait for the publish outcome (§6.1 step 5)")
	}
	if !waitsFor(t, stepMint, stepReceiverReady) {
		t.Error("the mint does not wait for the receiver (§6.1 step 4)")
	}
	if !waitsFor(t, stepMint, stepStageVerified) {
		t.Error("the mint does not wait for frame 1 to be verified (§6.1 step 3)")
	}
}

// The deadline holds against the SCHEDULE. 3 + 3 + 10 = 16 exceeds 15, which
// is exactly why the sum is not the question: the longest path through the
// graph is, and it is 13.
func TestBootstrapSchedule_TheParallelScheduleClosesTheIntegrationDeadline(t *testing.T) {
	sum := ReceiverReadyDeadline + FrameCompletionDeadline + PublishDeadline
	if sum <= integrationDeadline {
		t.Fatalf("the sum %v no longer exceeds %v; this test's premise is gone", sum, integrationDeadline)
	}
	worst := bootstrapWorstCase()
	if worst > integrationDeadline {
		t.Errorf("worst path = %v, over the %v integration deadline", worst, integrationDeadline)
	}
	if worst != PublishDeadline+FrameCompletionDeadline {
		t.Errorf("worst path = %v, want %v (publish, then the outcome after frame 2)",
			worst, PublishDeadline+FrameCompletionDeadline)
	}
}

// waitsFor reports whether step waits — directly or through another step —
// for other.
func waitsFor(t *testing.T, step, other string) bool {
	t.Helper()
	deps, ok := bootstrapStepDependencies(step)
	if !ok {
		t.Fatalf("no schedule step named %q", step)
	}
	for _, d := range deps {
		if d == other || waitsFor(t, d, other) {
			return true
		}
	}
	return false
}

// ── assertion 26: `starting` can never be permanent ───────────────────────

// Every path out of the driver names a terminal outcome from the closed set.
//
// This is assertion 26's other half, and it is the half a deadline cannot
// give you: the schedule proves the bootstrap FINISHES inside 15 s, and this
// proves that when it finishes it SAYS SO. A path that returned an empty
// outcome would leave the session's axis at `starting` with no further event
// coming, which §7 forbids outright — and it would do it silently, because
// nothing downstream can tell "no answer yet" from "answered with nothing".
//
// Nothing here waits: every branch is a scripted far side.
func TestDeliverBootstrap_EveryPathNamesATerminalOutcome(t *testing.T) {
	opts := stageOpts()
	accepted := OutcomePrefix + OutcomeToken(OutcomeBootstrapAccepted)
	refused := OutcomePrefix + OutcomeToken(OutcomeStageDigestMismatch)

	paths := map[string][]farSideStep{
		"the far side never announces itself": {{deadline: true}},
		"the stream ends before READY":        {{eof: true}},
		"a refusal before READY":              {{line: OutcomePrefix + OutcomeToken(OutcomeNoSecureTemp)}},
		"the far side goes quiet after READY": {{line: LoaderReadyToken}, {deadline: true}},
		"the stream ends after READY":         {{line: LoaderReadyToken}, {eof: true}},
		"a refusal after frame 1":             {{line: LoaderReadyToken}, {line: refused}},
		"a token out of order":                {{line: StageReadyToken}},
		"a repeated token":                    {{line: LoaderReadyToken}, {line: LoaderReadyToken}},
		"quiet after the pair was delivered":  {{line: LoaderReadyToken}, {line: StageReadyToken}, {deadline: true}},
		"the stream ends after the pair":      {{line: LoaderReadyToken}, {line: StageReadyToken}, {eof: true}},
		"noise, then a refusal":               {{line: LoaderReadyToken}, {line: "Last login: Tue"}, {line: refused}},
		"the happy path":                      {{line: LoaderReadyToken}, {line: StageReadyToken}, {line: accepted}},
	}
	closed := map[Outcome]bool{}
	for _, o := range AllOutcomes() {
		closed[o] = true
	}
	for name, steps := range paths {
		t.Run(name, func(t *testing.T) {
			minted := 0
			plan := testPlan(t, opts, &minted)
			lg, _ := captureLog()
			out := DeliverBootstrap(context.Background(), lg, &scriptedFarSide{steps: steps}, plan)
			if out == "" {
				t.Fatal("the driver returned no outcome; the session's axis would stay at `starting` " +
					"with nothing else coming")
			}
			if !closed[out] {
				t.Fatalf("outcome %q is not in the closed set, so nothing downstream can name it", out)
			}
		})
	}
}

// The gate's wait is inside the schedule's budget, not beside it. If the mint
// could wait longer than the publish is allowed to take, the bootstrap's own
// 3 + 3 would stop bounding anything and assertion 26 would hold only on
// paths where the publish behaves.
func TestBootstrapSchedule_TheMintWaitIsInsideThePublishBudget(t *testing.T) {
	deps, _ := bootstrapStepDependencies(stepMint)
	var worstDep time.Duration
	for _, d := range deps {
		if f := bootstrapFinish(d, map[string]time.Duration{}); f > worstDep {
			worstDep = f
		}
	}
	if worstDep > PublishDeadline {
		t.Errorf("the mint can be reached at %v, past the publish budget of %v; the wait would be "+
			"bounded by nothing the schedule accounts for", worstDep, PublishDeadline)
	}
	if worstDep+FrameCompletionDeadline > integrationDeadline {
		t.Errorf("the mint at %v plus the terminal outcome at %v exceeds the %v deadline",
			worstDep, FrameCompletionDeadline, integrationDeadline)
	}
}

// ---------------------------------------------------------------------------
// The schedule (§7's arithmetic, §11 assertion 32)
// ---------------------------------------------------------------------------

// The deadline arithmetic must close, and it does not close by addition:
// 3 + 3 + 10 is 16 and the integration deadline is 15. §7 resolves it by
// making the publish and the receiver CONCURRENT — the publish runs on its
// auxiliary channel while stage-1 quarantines input and waits for its frame —
// and requires the deadline to be proven against that schedule, "not on a sum
// and not on a stopwatch".
//
// So the schedule is declared as a graph and the proof is the longest path
// through it. That is the only form in which the claim is falsifiable by
// anything other than a slow machine: add an edge between the publish and the
// receiver and the longest path becomes 16, and the assertion fails on a fast
// machine as loudly as on a slow one.
//
// It lives HERE, beside the assertions, and not in ordering.go, because it is
// a claim about the package rather than behaviour of it: no path the product
// runs reads an edge or a bound. Declaring it in production would be a second
// copy of numbers whose only reader is this file, and the dead-code ratchet
// says so — it compiles without -test precisely so that a production function
// whose only callers are its own tests is reported rather than excused.
const (
	// stepSessionEstablished is the saved path's spelling of §6.1 step 1 —
	// the interactive session channel is open and its pty granted. It bounds
	// nothing (one round trip on a connection that is already authenticated),
	// and it is declared because the ABSENCE of its edges was the defect: the
	// publish used to be scheduled with no dependency on it at all, so on a
	// server with one session slot the two competed and the user's own shell
	// lost six times in ten. It is a common ANCESTOR of the publish and the
	// frames, never an edge between them.
	stepSessionEstablished = "session-established"
	// stepPublish is §6.1 step 2's publish half: bounded by T.
	stepPublish = "publish"
	// stepReceiverReady is step 4.
	stepReceiverReady = "receiver-ready"
	// stepStageVerified is step 3 — frame 1 received and verified, which
	// the backend learns from STAGE_READY.
	stepStageVerified = "stage-verified"
	// stepMint is step 6. It costs nothing: it is two random reads and a
	// format, and it waits only because §6.1 says it must.
	stepMint = "mint"
	// stepOutcome is steps 7 to 9 — frame 2 delivered, the far side
	// re-proving its generation, and the terminal outcome.
	stepOutcome = "terminal-outcome"
)

// integrationDeadline is §7's outer bound: `starting` can never be permanent,
// and a session reaches a terminal state inside it on every path.
const integrationDeadline = 15 * time.Second

// bootstrapStep is one node of the schedule: its own bound, and the steps
// that must finish before it may start.
type bootstrapStep struct {
	bound time.Duration
	after []string
}

// bootstrapSchedule is §6.1 as a directed acyclic graph. The absence of an
// edge is as load-bearing as the presence of one: there is no edge between
// stepPublish and stepReceiverReady, and that absence IS §7's "not
// sequential".
var bootstrapSchedule = map[string]bootstrapStep{
	stepSessionEstablished: {bound: 0},
	stepPublish:            {bound: PublishDeadline, after: []string{stepSessionEstablished}},
	stepReceiverReady:      {bound: ReceiverReadyDeadline},
	stepStageVerified:      {bound: FrameCompletionDeadline, after: []string{stepReceiverReady, stepSessionEstablished}},
	stepMint:               {bound: 0, after: []string{stepStageVerified, stepReceiverReady, stepPublish}},
	stepOutcome:            {bound: FrameCompletionDeadline, after: []string{stepMint}},
}

// bootstrapStepDependencies reports which steps a step waits for. ok is false
// for a name that is not a step, so a test cannot silently assert about one
// that does not exist.
func bootstrapStepDependencies(step string) ([]string, bool) {
	s, ok := bootstrapSchedule[step]
	if !ok {
		return nil, false
	}
	return append([]string(nil), s.after...), true
}

// bootstrapWorstCase is the longest path through the schedule: what the whole
// bootstrap costs when every bound is spent in full and every concurrent
// branch runs to its own limit.
func bootstrapWorstCase() time.Duration {
	var worst time.Duration
	for name := range bootstrapSchedule {
		if d := bootstrapFinish(name, map[string]time.Duration{}); d > worst {
			worst = d
		}
	}
	return worst
}

// bootstrapFinish is when a step can be finished at the latest: the latest
// finish of everything it waits for, plus its own bound. Memoised, so a
// diamond in the graph is not counted twice.
func bootstrapFinish(name string, memo map[string]time.Duration) time.Duration {
	if d, ok := memo[name]; ok {
		return d
	}
	s := bootstrapSchedule[name]
	var start time.Duration
	for _, dep := range s.after {
		if d := bootstrapFinish(dep, memo); d > start {
			start = d
		}
	}
	memo[name] = start + s.bound
	return memo[name]
}
