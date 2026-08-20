package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
)

// §6.1's ordering where it is actually assembled: the composition root, which
// is the only thing that sees both the ssh side producing the two facts and
// the shellintegration side consuming them.
//
// Nothing here waits on a duration. The two facts are scripted events and the
// bootstrap's far side is a scripted stream, so "the receiver was refused" and
// "the publish settled" are statements the test makes rather than durations it
// sits through (design §11's opening sentence).

// The canary values: if either appears in a frame, the mint happened.
const (
	mintCanaryCapability = "ab7f0c1d2e3f40516273849506a7b8c9dabbccddeeff00112233445566778899"
	mintCanaryFence      = "0011223344556677889900aabbccddeeff112233445566778899aabbccddeeff"
)

// scriptedStream is a far side that answers from a fixed script and records
// every byte written to it — the same shape internal/shellintegration uses,
// declared here because the ssh.BootstrapStream interface is what the adapter's
// run is handed.
type scriptedStream struct {
	mu     sync.Mutex
	lines  []string
	writes [][]byte
}

func (s *scriptedStream) ReadLine(_ context.Context, _ time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.lines) == 0 {
		return "", ssh.ErrBootstrapDeadline
	}
	line := s.lines[0]
	s.lines = s.lines[1:]
	return line, nil
}

func (s *scriptedStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (s *scriptedStream) allBytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b []byte
	for _, w := range s.writes {
		b = append(b, w...)
	}
	return b
}

func mintOpts() ssh.LaunchOptions {
	return ssh.LaunchOptions{
		SessionID:     "0123456789abcdef0123456789abcdef",
		Enhanced:      true,
		Capability:    mintCanaryCapability,
		Recovery:      mintCanaryFence,
		Lane:          "lane-1",
		Domain:        "dom-1",
		Epoch:         7,
		LifecyclePort: 40000,
	}
}

func prepareMint(t *testing.T) (*remoteLauncherAdapter, ssh.BootstrapRun, ssh.BootstrapGate, *bytes.Buffer) {
	t.Helper()
	lg, buf := captureAdapterLogs(t)
	a := &remoteLauncherAdapter{inner: shellintegration.NewRemoteLauncher(), logger: lg}
	_, run, gate, ok := a.Prepare(ssh.ShellAuto, mintOpts())
	if !ok {
		t.Fatal("Prepare declined")
	}
	return a, run, gate, buf
}

// happy is the far side of a bootstrap that reaches the frame and then
// accepts.
func happyScript() *scriptedStream {
	return &scriptedStream{lines: []string{
		shellintegration.LoaderReadyToken,
		shellintegration.StageReadyToken,
		shellintegration.OutcomePrefix + shellintegration.OutcomeToken(shellintegration.OutcomeBootstrapAccepted),
	}}
}

// Assertion 12: nothing is minted before the lifecycle receiver is ready —
// with the lifecycle channel REFUSED, no capability and no fence are ever
// generated. Not "minted and then discarded", which is what the earlier draft
// did and which hands a bearer across a boundary before establishing that it
// has any use.
func TestMintOrdering_ARefusedReceiverMintsNothing(t *testing.T) {
	_, run, gate, _ := prepareMint(t)
	far := happyScript()

	gate.PublishSettled(nil)
	gate.ReceiverUnavailable(errors.New("forwarding refused"))

	reason := run(context.Background(), far)

	wire := string(far.allBytes())
	if strings.Contains(wire, mintCanaryCapability) {
		t.Error("the capability was written after the receiver was refused")
	}
	if strings.Contains(wire, mintCanaryFence) {
		t.Error("the recovery fence was written after the receiver was refused")
	}
	// The far side is told, rather than left to time out: a non-secret
	// refusal reaches it in the same slot the secret would have used.
	if !strings.Contains(wire, shellintegration.OutcomeToken(shellintegration.OutcomeChannelUnavailable)) {
		t.Error("stage-1 was not told the channel is unavailable; it would sit on its bounded timeout")
	}
	// And the bootstrap still reaches a terminal outcome: the shell comes
	// up, it simply has no authenticated channel.
	if reason != ssh.ReasonNone {
		t.Logf("outcome reason = %q", reason)
	}
}

// refusedFar is the far side of a session whose lifecycle forward was refused,
// instrumented so that the ORDER of two events is a RECORDED FACT and not a
// duration the test sits through (AGENTS.md: no test may depend on timing).
//
// Two instruments. STAGE_READY is withheld until the test releases it, so
// "only frame 1 has been written" is true by CONSTRUCTION rather than by
// having looked quickly enough — the writer cannot reach frame 2 before
// stage-1 has announced itself. And every frame is STAMPED, under the same
// mutex the publish is settled under, with whether the publish had settled
// when it arrived, so the ordering the test reports is one the far side
// observed rather than one the test inferred from a duration.
//
// What the stamp cannot do is prove the writer BLOCKED: a writer that never
// waited would still be stamped late if the test won the mutex first. That
// half is proven where it can be — shellintegration's
// TestMintGate_ARefusedReceiverStillWaitsForThePublish, on the gate itself —
// and end to end by the live-sshd rows, which is where the defect was found.
// Stated here rather than left for a reader to assume.
type refusedFar struct {
	mu      sync.Mutex
	settled bool
	stamps  []bool
	wire    []byte

	step    int
	frame1  chan struct{}
	release chan struct{}
}

func newRefusedFar() *refusedFar {
	return &refusedFar{frame1: make(chan struct{}), release: make(chan struct{})}
}

func (f *refusedFar) ReadLine(ctx context.Context, _ time.Duration) (string, error) {
	f.mu.Lock()
	step := f.step
	f.step++
	f.mu.Unlock()
	switch step {
	case 0:
		return shellintegration.LoaderReadyToken, nil
	case 1:
		// The loader has the terminal and stage-1 is verified — but only
		// when the test says so, which is what makes the frame count
		// above it deterministic.
		select {
		case <-f.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return shellintegration.StageReadyToken, nil
	case 2:
		return shellintegration.OutcomePrefix +
			shellintegration.OutcomeToken(shellintegration.OutcomeBootstrapAccepted), nil
	}
	return "", ssh.ErrBootstrapDeadline
}

func (f *refusedFar) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stamps = append(f.stamps, f.settled)
	f.wire = append(f.wire, p...)
	if len(f.stamps) == 1 {
		close(f.frame1)
	}
	return len(p), nil
}

// settle answers §6.1 step 5 and records that it has been answered, both under
// the mutex Write takes. A frame therefore lands wholly on one side of this
// call or the other, and its stamp says which.
func (f *refusedFar) settle(gate ssh.BootstrapGate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	gate.PublishSettled(nil)
	f.settled = true
}

func (f *refusedFar) snapshot() ([]bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.stamps...), string(f.wire)
}

// §6.1 steps 5 and 8, on the path with NOTHING TO MINT — which is the path
// that was broken. A session whose lifecycle forward was refused carries no
// capability, so there is no bearer to withhold; there is still a generation
// the far side re-proves AFTER frame 2, and frame 2 is what releases it to go
// and look. Sending the refusal early let stage-1 probe a far host whose
// publish was still in flight — and find nothing, on a host that had the
// bundle moments later.
func TestMintOrdering_ARefusedForwardStillWaitsForThePublish(t *testing.T) {
	lg, _ := captureAdapterLogs(t)
	a := &remoteLauncherAdapter{inner: shellintegration.NewRemoteLauncher(), logger: lg}
	opts := mintOpts()
	opts.Capability = ""
	opts.Recovery = ""
	_, run, gate, ok := a.Prepare(ssh.ShellAuto, opts)
	if !ok {
		t.Fatal("Prepare declined")
	}
	gate.ReceiverUnavailable(errors.New("tcpip-forward request denied by peer"))

	far := newRefusedFar()
	done := make(chan ssh.RefusalReason, 1)
	go func() { done <- run(context.Background(), far) }()

	// Frame 1 is out and STAGE_READY is still withheld, so the writer cannot
	// have reached frame 2: one frame, by construction.
	<-far.frame1
	if stamps, _ := far.snapshot(); len(stamps) != 1 {
		t.Fatalf("%d frames were written before stage-1 announced itself, want 1", len(stamps))
	}

	// Now let it reach the barrier, and answer the publish. Whichever order
	// the two goroutines actually run in, the stamp on frame 2 records which
	// side of the answer it landed on.
	close(far.release)
	far.settle(gate)

	var reason ssh.RefusalReason
	select {
	case reason = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the bootstrap never finished after the publish settled")
	}

	stamps, wire := far.snapshot()
	if len(stamps) != 2 {
		t.Fatalf("%d frames were written, want 2 — stage-1 must be told, not left to time out", len(stamps))
	}
	if !stamps[1] {
		t.Error("frame 2 reached stage-1 before the publish settled: the refusal released the " +
			"far side to re-prove a generation that was still being published (§6.1 steps 5 and 8)")
	}
	if !strings.Contains(wire, shellintegration.OutcomeToken(shellintegration.OutcomeChannelUnavailable)) {
		t.Error("stage-1 was not told the channel is unavailable; it would sit on its bounded timeout")
	}
	// §6.4's row for a refused lifecycle forward, named. The far side reported
	// that its shell came up, which is true and is not this question: only
	// this side knows there was no channel to offer, and a session reported
	// integrated with no domain behind it never leaves `starting`.
	if reason != ssh.ReasonChannelUnavailable {
		t.Errorf("reason = %q, want %q — §6.4 names this row and the backend knew it",
			reason, ssh.ReasonChannelUnavailable)
	}
}

// A far side that names a failure of its OWN keeps that name after a refusal
// frame. It is the more specific answer and a second thing the user can act
// on: "there is no generation on this host" is not made less true by our also
// having had no channel to offer.
func TestMintOrdering_ARefusedForwardDoesNotOverwriteTheFarSidesOwnFailure(t *testing.T) {
	lg, _ := captureAdapterLogs(t)
	a := &remoteLauncherAdapter{inner: shellintegration.NewRemoteLauncher(), logger: lg}
	opts := mintOpts()
	opts.Capability = ""
	opts.Recovery = ""
	_, run, gate, ok := a.Prepare(ssh.ShellAuto, opts)
	if !ok {
		t.Fatal("Prepare declined")
	}
	gate.ReceiverUnavailable(errors.New("tcpip-forward request denied by peer"))
	gate.PublishSettled(nil)

	far := &scriptedStream{lines: []string{
		shellintegration.LoaderReadyToken,
		shellintegration.StageReadyToken,
		shellintegration.OutcomePrefix + shellintegration.OutcomeToken(shellintegration.OutcomeGenerationUnavailable),
	}}
	if reason := run(context.Background(), far); reason != ssh.ReasonGenerationUnavailable {
		t.Errorf("reason = %q, want %q", reason, ssh.ReasonGenerationUnavailable)
	}
}

// §6.1 step 5: the mint waits for the publish to reach a terminal outcome. The
// wait is a wait and not a formality — the frame is not written while the
// publish is still in flight.
func TestMintOrdering_NothingIsWrittenWhileThePublishIsInFlight(t *testing.T) {
	_, run, gate, _ := prepareMint(t)
	far := happyScript()
	gate.ReceiverReady()

	done := make(chan ssh.RefusalReason, 1)
	go func() { done <- run(context.Background(), far) }()

	// Frame 1 goes out on READY. Frame 2 must not, because the publish has
	// not settled.
	deadline := time.After(2 * time.Second)
	for {
		far.mu.Lock()
		n := len(far.writes)
		far.mu.Unlock()
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("frame 1 was never written")
		default:
		}
	}
	if strings.Contains(string(far.allBytes()), mintCanaryCapability) {
		t.Fatal("the capability was written before the publish settled (§6.1 step 5)")
	}

	gate.PublishSettled(nil)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the bootstrap never finished after the publish settled")
	}
	if !strings.Contains(string(far.allBytes()), mintCanaryCapability) {
		t.Error("the capability was never written once both facts were in")
	}
}

// "A failed publish is not a refusal": after a failed publish the far side may
// still accept a generation installed earlier, so the gate opens and the pair
// is delivered. The far side stays the owner of "is this installation valid".
func TestMintOrdering_AFailedPublishStillDeliversThePair(t *testing.T) {
	_, run, gate, _ := prepareMint(t)
	far := happyScript()
	gate.ReceiverReady()
	gate.PublishSettled(errors.New("sftp: permission denied"))

	run(context.Background(), far)

	if !strings.Contains(string(far.allBytes()), mintCanaryCapability) {
		t.Error("a failed publish suppressed the pair; §6.1 says it is not a refusal")
	}
}

// Assertion 11, the fence's confidentiality half on the copy this side owns:
// the attempt's own buffer closes at the TERMINAL BOOTSTRAP OUTCOME — on a
// refusal, on a timeout, AND AFTER A SUCCESSFUL BOOTSTRAP alike.
//
// The last case is the one that is easy to get wrong. Closing the fence's
// AUTHORITY interval is a different event on a different clock (it opens when
// the bootstrap succeeds), and a reader who conflates the two concludes that a
// successful bootstrap is the moment to keep the copy rather than drop it.
func TestFenceConfidentiality_TheAttemptsCopyClosesOnEveryTerminalOutcome(t *testing.T) {
	cases := map[string][]string{
		"a successful bootstrap": {
			shellintegration.LoaderReadyToken,
			shellintegration.StageReadyToken,
			shellintegration.OutcomePrefix + shellintegration.OutcomeToken(shellintegration.OutcomeBootstrapAccepted),
		},
		"a refusal": {
			shellintegration.LoaderReadyToken,
			shellintegration.OutcomePrefix + shellintegration.OutcomeToken(shellintegration.OutcomeStageDigestMismatch),
		},
		"a timeout": {
			shellintegration.LoaderReadyToken,
			shellintegration.StageReadyToken,
		},
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			_, run, gate, _ := prepareMint(t)
			gate.ReceiverReady()
			gate.PublishSettled(nil)
			far := &scriptedStream{lines: script}

			run(context.Background(), far)

			// A second run of the same attempt can mint nothing, because
			// the attempt's copy of both bearers is gone. That is the
			// closing event, observed through the only seam that can see
			// it: what the next frame would carry.
			second := happyScript()
			run(context.Background(), second)
			wire := string(second.allBytes())
			if strings.Contains(wire, mintCanaryFence) {
				t.Errorf("the fence survived %s: the attempt's copy is still mintable", name)
			}
			if strings.Contains(wire, mintCanaryCapability) {
				t.Errorf("the capability survived %s: the attempt's copy is still mintable", name)
			}
		})
	}
}

// The refusal reaches the product as its own name, through the sink the
// composition root wires. Before P5 this was ssh.ReasonUnknown for every one
// of twenty-one outcomes.
func TestMintOrdering_TheOutcomeIsReportedAsItsOwnReason(t *testing.T) {
	lg, _ := captureAdapterLogs(t)
	var gotLane string
	var gotReason ssh.RefusalReason
	a := &remoteLauncherAdapter{
		inner:  shellintegration.NewRemoteLauncher(),
		logger: lg,
		reportBootstrapOutcome: func(lane string, reason ssh.RefusalReason) {
			gotLane, gotReason = lane, reason
		},
	}
	_, run, gate, ok := a.Prepare(ssh.ShellAuto, mintOpts())
	if !ok {
		t.Fatal("Prepare declined")
	}
	gate.ReceiverReady()
	gate.PublishSettled(nil)
	far := &scriptedStream{lines: []string{
		shellintegration.LoaderReadyToken,
		shellintegration.OutcomePrefix + shellintegration.OutcomeToken(shellintegration.OutcomeGenerationUnavailable),
	}}

	if reason := run(context.Background(), far); reason != ssh.ReasonGenerationUnavailable {
		t.Errorf("run reported %q, want %q", reason, ssh.ReasonGenerationUnavailable)
	}
	if gotLane != "lane-1" {
		t.Errorf("reported lane = %q, want lane-1", gotLane)
	}
	if gotReason != ssh.ReasonGenerationUnavailable {
		t.Errorf("reported reason = %q, want %q — the backend knew which it was",
			gotReason, ssh.ReasonGenerationUnavailable)
	}
}

// An accepted bootstrap reports no refusal at all, so the axis is left to the
// kernel: "a domain is live" is its word and this is not it.
func TestMintOrdering_AnAcceptedBootstrapReportsNoRefusal(t *testing.T) {
	lg, _ := captureAdapterLogs(t)
	reported := 0
	var gotReason ssh.RefusalReason
	a := &remoteLauncherAdapter{
		inner:  shellintegration.NewRemoteLauncher(),
		logger: lg,
		reportBootstrapOutcome: func(_ string, reason ssh.RefusalReason) {
			reported++
			gotReason = reason
		},
	}
	_, run, gate, _ := a.Prepare(ssh.ShellAuto, mintOpts())
	gate.ReceiverReady()
	gate.PublishSettled(nil)
	if reason := run(context.Background(), happyScript()); reason != ssh.ReasonNone {
		t.Errorf("run reported %q for an accepted bootstrap, want no refusal", reason)
	}
	if reported != 1 || gotReason != ssh.ReasonNone {
		t.Errorf("sink saw %d report(s), last %q; want exactly one carrying no refusal", reported, gotReason)
	}
}

// The product log is a surface like any other, and no bearer reaches it.
func TestMintOrdering_NoBearerReachesTheProductLog(t *testing.T) {
	_, run, gate, logs := prepareMint(t)
	gate.ReceiverReady()
	gate.PublishSettled(errors.New("sftp: permission denied"))
	run(context.Background(), happyScript())
	if s := logs.String(); strings.Contains(s, mintCanaryCapability) || strings.Contains(s, mintCanaryFence) {
		t.Errorf("a bearer reached the product log:\n%s", s)
	}
}

// §6.4's `subsystem` row, reported as its cause. The far side answers
// generation-unavailable — true, and not the half a user can act on. When the
// publish also failed, the reason there is no generation is that nocx could
// not write one, and that is what the product says.
func TestMintOrdering_AFailedPublishRenamesAMissingGeneration(t *testing.T) {
	_, run, gate, _ := prepareMint(t)
	gate.ReceiverReady()
	gate.PublishSettled(errors.New("sftp: subsystem request failed"))
	far := &scriptedStream{lines: []string{
		shellintegration.LoaderReadyToken,
		shellintegration.StageReadyToken,
		shellintegration.OutcomePrefix + shellintegration.OutcomeToken(shellintegration.OutcomeGenerationUnavailable),
	}}
	if reason := run(context.Background(), far); reason != ssh.ReasonPublishUnavailable {
		t.Errorf("reason = %q, want %q — the symptom is that nothing is installed, the cause is "+
			"that nocx could not install it", reason, ssh.ReasonPublishUnavailable)
	}
}

// And the substitution is honest only when BOTH are true. A far side that
// finds no generation after a SUCCESSFUL publish is a different fault, and
// renaming it would send the user to look at a publish that worked.
func TestMintOrdering_ASuccessfulPublishKeepsTheMissingGenerationsOwnName(t *testing.T) {
	_, run, gate, _ := prepareMint(t)
	gate.ReceiverReady()
	gate.PublishSettled(nil)
	far := &scriptedStream{lines: []string{
		shellintegration.LoaderReadyToken,
		shellintegration.StageReadyToken,
		shellintegration.OutcomePrefix + shellintegration.OutcomeToken(shellintegration.OutcomeGenerationUnavailable),
	}}
	if reason := run(context.Background(), far); reason != ssh.ReasonGenerationUnavailable {
		t.Errorf("reason = %q, want %q", reason, ssh.ReasonGenerationUnavailable)
	}
}
