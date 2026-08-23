package shellintegration

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// The writer, driven against a scripted far side.
//
// Nothing here waits on a duration, and nothing here MEASURES one: a deadline
// is a scripted event the test places in the far side's answers, so "the
// deadline fires" is stated rather than waited for. That is the only way the
// two three-second budgets of design §7 can be asserted at all — a test that
// slept them would take six seconds and still prove nothing about a slow
// machine.

// farSideStep is one thing the far side does when the writer reads.
type farSideStep struct {
	line     string // a line of output
	deadline bool   // the read deadline passes instead
	eof      bool   // the stream ends
}

// scriptedFarSide plays a fixed script and records every byte written to it.
type scriptedFarSide struct {
	mu     sync.Mutex
	steps  []farSideStep
	writes [][]byte
	// writesAt records how many writes had happened when each step was
	// consumed, which is how the ordering assertions are made: "the secret
	// was written only after STAGE_READY" is a statement about this.
	stepWrites []int
}

func (f *scriptedFarSide) ReadLine(ctx context.Context, timeout time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.steps) == 0 {
		return "", ErrBootstrapDeadline
	}
	step := f.steps[0]
	f.steps = f.steps[1:]
	f.stepWrites = append(f.stepWrites, len(f.writes))
	switch {
	case step.deadline:
		return "", ErrBootstrapDeadline
	case step.eof:
		return "", context.Canceled
	default:
		return step.line, nil
	}
}

func (f *scriptedFarSide) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := append([]byte(nil), p...)
	f.writes = append(f.writes, cp)
	return len(p), nil
}

// remaining is how many scripted lines the driver never asked for. Zero means
// the far side reached the end of its script, which for a script ending in a
// terminal outcome means the driver read that outcome.
func (f *scriptedFarSide) remaining() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.steps)
}

func (f *scriptedFarSide) written() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.writes...)
}

func (f *scriptedFarSide) allBytes() []byte {
	var b []byte
	for _, w := range f.written() {
		b = append(b, w...)
	}
	return b
}

// testPlan is one session's plan with a mint counter, so "nothing is minted
// before it can be used" is a count and not a hope.
func testPlan(t *testing.T, opts LaunchOptions, minted *int) BootstrapPlan {
	t.Helper()
	stage, err := Stage1Frame(ShellAuto, opts)
	if err != nil {
		t.Fatalf("Stage1Frame: %v", err)
	}
	return BootstrapPlan{
		Stage1: stage,
		Secret: SecretFunc(func(context.Context) ([]byte, error) {
			*minted++
			return SecretFrame(opts)
		}),
	}
}

// captureLog returns a logger and the buffer it writes to — the "product
// logs" surface of design §11 assertion 7.
func captureLog() (log.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return log.NewSlogAdapter(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))), buf
}

func TestDeliverBootstrap_HappyPathSendsTwoFramesInOrderAndNothingElse(t *testing.T) {
	opts := stageOpts()
	minted := 0
	plan := testPlan(t, opts, &minted)
	far := &scriptedFarSide{steps: []farSideStep{
		{line: LoaderReadyToken},
		{line: StageReadyToken},
		{line: OutcomePrefix + OutcomeToken(OutcomeBootstrapAccepted)},
	}}
	lg, logs := captureLog()

	out := DeliverBootstrap(context.Background(), lg, far, plan)
	if out != OutcomeBootstrapAccepted {
		t.Fatalf("outcome %q, want %q", out, OutcomeBootstrapAccepted)
	}

	writes := far.written()
	if len(writes) != 2 {
		t.Fatalf("%d writes, want exactly two frames and nothing else: %q", len(writes), writes)
	}
	// Each frame is ONE write: a header and the body it describes may not
	// be separated, or a far side reading exactly the declared length can
	// be left holding half of one.
	stageHdr, err := FrameHeader(FrameStageSeq, len(plan.Stage1))
	if err != nil {
		t.Fatalf("FrameHeader: %v", err)
	}
	if got, want := string(writes[0]), stageHdr+string(plan.Stage1); got != want {
		t.Errorf("frame 1 is not the header plus the payload in one write")
	}
	secret, err := SecretFrame(opts)
	if err != nil {
		t.Fatalf("SecretFrame: %v", err)
	}
	secretHdr, err := FrameHeader(FrameSecretSeq, len(secret))
	if err != nil {
		t.Fatalf("FrameHeader: %v", err)
	}
	if got, want := string(writes[1]), secretHdr+string(secret); got != want {
		t.Errorf("frame 2 is not the header plus the payload in one write")
	}

	// Design §6.1: nothing is minted before it can be used. The secret was
	// minted once, and only after frame 1 had been verified — which is what
	// STAGE_READY reports.
	if minted != 1 {
		t.Errorf("the secret was minted %d times, want exactly one", minted)
	}
	if far.stepWrites[1] != 1 {
		t.Errorf("frame 2 was written before STAGE_READY was read (writes at that point: %d)", far.stepWrites[1])
	}

	// The product-log surface carries no bearer.
	if strings.Contains(logs.String(), canaryCap) || strings.Contains(logs.String(), canaryFence) {
		t.Errorf("a secret reached the product log:\n%s", logs.String())
	}
}

// TestDeliverBootstrap_ReceiverDeadlineUnblocksTheFarSideAndMintsNothing:
// the loader never announced itself, so the far side is blocked on a header
// read. The writer stops waiting, sends the abort header — which the far side
// must refuse, giving it a prompt — and nothing is minted, because nothing
// could have used it.
func TestDeliverBootstrap_ReceiverDeadlineUnblocksTheFarSideAndMintsNothing(t *testing.T) {
	opts := stageOpts()
	minted := 0
	plan := testPlan(t, opts, &minted)
	far := &scriptedFarSide{steps: []farSideStep{{deadline: true}}}
	lg, _ := captureLog()

	out := DeliverBootstrap(context.Background(), lg, far, plan)
	if out != OutcomeReceiverUnready {
		t.Fatalf("outcome %q, want %q", out, OutcomeReceiverUnready)
	}
	if minted != 0 {
		t.Errorf("a capability was minted for a session that never answered")
	}
	writes := far.written()
	if len(writes) != 1 || string(writes[0]) != string(abortHeader) {
		t.Fatalf("writes = %q, want exactly the abort header", writes)
	}
	// The abort header must be safe wherever it lands: FrameHeaderLen bytes
	// so it satisfies one pending header read, not a header of this
	// protocol so the reader refuses it, and a COMMENT if the far side
	// turns out to be an ordinary shell.
	if len(abortHeader) != FrameHeaderLen {
		t.Errorf("the abort header is %d bytes, want %d", len(abortHeader), FrameHeaderLen)
	}
	if abortHeader[0] != '#' || abortHeader[len(abortHeader)-1] != '\n' {
		t.Errorf("the abort header is not a shell comment: %q", abortHeader)
	}
	if strings.HasPrefix(string(abortHeader), FrameMagic) {
		t.Errorf("the abort header parses as a frame header: %q", abortHeader)
	}
}

func TestDeliverBootstrap_StageDeadlineMintsNothing(t *testing.T) {
	opts := stageOpts()
	minted := 0
	plan := testPlan(t, opts, &minted)
	far := &scriptedFarSide{steps: []farSideStep{
		{line: LoaderReadyToken},
		{deadline: true},
	}}
	lg, _ := captureLog()

	if out := DeliverBootstrap(context.Background(), lg, far, plan); out != OutcomeBootstrapTimeout {
		t.Fatalf("outcome %q, want %q", out, OutcomeBootstrapTimeout)
	}
	if minted != 0 {
		t.Errorf("a capability was minted for a stage-1 that never answered")
	}
	writes := far.written()
	if len(writes) != 2 || string(writes[1]) != string(abortHeader) {
		t.Fatalf("writes = %d, want frame 1 then the abort header", len(writes))
	}
}

// TestDeliverBootstrap_NoByteIsWrittenAfterTheLastFrame: the far side is not
// blocked on us while it verifies its generation, so a byte now would be a
// byte after a complete frame — which the protocol forbids precisely because
// the far side is about to become a shell that would read it as input.
func TestDeliverBootstrap_NoByteIsWrittenAfterTheLastFrame(t *testing.T) {
	opts := stageOpts()
	minted := 0
	plan := testPlan(t, opts, &minted)
	far := &scriptedFarSide{steps: []farSideStep{
		{line: LoaderReadyToken},
		{line: StageReadyToken},
		{deadline: true},
	}}
	lg, _ := captureLog()

	if out := DeliverBootstrap(context.Background(), lg, far, plan); out != OutcomeBootstrapTimeout {
		t.Fatalf("outcome %q, want %q", out, OutcomeBootstrapTimeout)
	}
	if n := len(far.written()); n != 2 {
		t.Fatalf("%d writes, want only the two frames — nothing may follow the last one", n)
	}
}

// TestDeliverBootstrap_AFarSideRefusalIsReportedAsItself: the loader named an
// outcome instead of announcing itself. The writer reports that outcome
// rather than inventing a timeout, and sends nothing.
func TestDeliverBootstrap_AFarSideRefusalIsReportedAsItself(t *testing.T) {
	opts := stageOpts()
	minted := 0
	plan := testPlan(t, opts, &minted)
	far := &scriptedFarSide{steps: []farSideStep{
		{line: OutcomePrefix + OutcomeToken(OutcomeStageDigestUnavailable)},
	}}
	lg, _ := captureLog()

	if out := DeliverBootstrap(context.Background(), lg, far, plan); out != OutcomeStageDigestUnavailable {
		t.Fatalf("outcome %q, want %q", out, OutcomeStageDigestUnavailable)
	}
	if n := len(far.written()); n != 0 {
		t.Errorf("%d writes to a far side that had already refused", n)
	}
	if minted != 0 {
		t.Errorf("a capability was minted for a refused bootstrap")
	}
}

// TestDeliverBootstrap_WithNoLifecycleChannelTheFrameCarriesARefusal is
// design §6.1's last paragraph: the earlier draft delivered the secret and
// then discarded it when the channel turned out to be refused, which hands a
// bearer across a boundary before establishing that it has any use.
//
// It also pins §6.4's row for that refusal — "nothing minted; native shell;
// channel-unavailable" — and the second half of it is the half that was
// wrong. This asserted OutcomeBootstrapAccepted, on the reading that the far
// side had the last word. The far side is answering a different question:
// stage-1 execs the launcher on a refusal exactly as it does on a secret, so
// `accepted` is the truth about the SHELL and says nothing about the channel,
// which only this side knows there was none of. Reported as accepted, the
// session claimed integration with no domain behind it and never left
// `starting`, because the hard invalidation that moves it out runs only on a
// named refusal.
func TestDeliverBootstrap_WithNoLifecycleChannelTheFrameCarriesARefusal(t *testing.T) {
	opts := stageOpts()
	stage, err := Stage1Frame(ShellAuto, opts)
	if err != nil {
		t.Fatalf("Stage1Frame: %v", err)
	}
	far := &scriptedFarSide{steps: []farSideStep{
		{line: LoaderReadyToken},
		{line: StageReadyToken},
		{line: OutcomePrefix + OutcomeToken(OutcomeBootstrapAccepted)},
	}}
	lg, logs := captureLog()

	out := DeliverBootstrap(context.Background(), lg, far,
		BootstrapPlan{Stage1: stage, Secret: nil})
	if out != OutcomeChannelUnavailable {
		t.Fatalf("outcome %q, want %q — §6.4's row for a refused lifecycle channel",
			out, OutcomeChannelUnavailable)
	}
	// And the shell did come up: the far side was asked for its terminal
	// outcome and answered `accepted`, which is what makes this a NAMED
	// conventional session rather than one that failed to start.
	if far.remaining() != 0 {
		t.Errorf("%d scripted lines were never read; the far side did not reach its "+
			"terminal outcome, so this is not the row under test", far.remaining())
	}
	sent := far.allBytes()
	if !bytes.Contains(sent, RefusalFrame(OutcomeChannelUnavailable)) {
		t.Errorf("frame 2 was not the non-secret refusal: %q", sent)
	}
	if bytes.Contains(sent, []byte(canaryCap)) || bytes.Contains(sent, []byte(canaryFence)) {
		t.Errorf("a bearer travelled on a session that had no channel to use it")
	}
	if strings.Contains(logs.String(), canaryCap) {
		t.Errorf("a secret reached the product log")
	}
}

// TestDeliverBootstrap_DoesNotEndTheIntervalAtReady (design §11 assertion
// 14, the writer's half): READY says the far side is listening, not that it
// is finished with the terminal. The interval closes at a TERMINAL OUTCOME
// and at nothing else — here the far side says READY and then goes quiet, and
// the writer reports a timeout rather than an acceptance.
func TestDeliverBootstrap_DoesNotEndTheIntervalAtReady(t *testing.T) {
	opts := stageOpts()
	minted := 0
	plan := testPlan(t, opts, &minted)
	far := &scriptedFarSide{steps: []farSideStep{
		{line: LoaderReadyToken},
		{line: StageReadyToken},
		// Ordinary far-side output is not an outcome either. It was a
		// second LOADER_READY here until §6.1's rule 1 landed: a repeated
		// token is now a NAMED failure rather than a line to drop, and
		// asserting it here would be asserting rule 1 twice under the
		// wrong name (bootstrap_forgery_test.go owns it).
		{line: "Last login: Tue Aug 19 09:12:44 2026"},
		{deadline: true},
	}}
	lg, _ := captureLog()

	out := DeliverBootstrap(context.Background(), lg, far, plan)
	if out == OutcomeBootstrapAccepted {
		t.Fatal("the bootstrap was accepted on a READY, with no terminal outcome")
	}
	if out != OutcomeBootstrapTimeout {
		t.Fatalf("outcome %q, want %q", out, OutcomeBootstrapTimeout)
	}
}

func TestDeliverBootstrap_EOFBeforeAnAnswerIsInterrupted(t *testing.T) {
	opts := stageOpts()
	minted := 0
	plan := testPlan(t, opts, &minted)
	far := &scriptedFarSide{steps: []farSideStep{{line: LoaderReadyToken}, {eof: true}}}
	lg, _ := captureLog()

	if out := DeliverBootstrap(context.Background(), lg, far, plan); out != OutcomeBootstrapInterrupted {
		t.Fatalf("outcome %q, want %q", out, OutcomeBootstrapInterrupted)
	}
}

// TestDeliverBootstrap_ANonProtocolLineIsNotAnOutcome: the far side may be a
// shell that never ran our loader. Its output is dropped, never read as a
// refusal this side invented.
func TestDeliverBootstrap_ANonProtocolLineIsNotAnOutcome(t *testing.T) {
	opts := stageOpts()
	minted := 0
	plan := testPlan(t, opts, &minted)
	far := &scriptedFarSide{steps: []farSideStep{
		{line: "bash: NOCX1: command not found"},
		{line: OutcomePrefix + "not-ours"},
		{line: LoaderReadyToken},
		{line: StageReadyToken},
		{line: OutcomePrefix + OutcomeToken(OutcomeBootstrapAccepted)},
	}}
	lg, _ := captureLog()

	if out := DeliverBootstrap(context.Background(), lg, far, plan); out != OutcomeBootstrapAccepted {
		t.Fatalf("outcome %q, want the bootstrap to continue past unknown lines", out)
	}
}

// TestSecretFrame_RefusesWhatCannotTravel: the header is compared by stage-1
// as ONE string, so a field carrying a space would let two different tuples
// produce one header.
func TestSecretFrame_RefusesWhatCannotTravel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		twist func(*LaunchOptions)
	}{
		{"a domain with a space", func(o *LaunchOptions) { o.Domain = "dom with space" }},
		{"a session id with a newline", func(o *LaunchOptions) { o.SessionID = "sid\nmore" }},
		{"a capability that is not hex", func(o *LaunchOptions) { o.Capability = "NOT-HEX" }},
		{"an empty capability", func(o *LaunchOptions) { o.Capability = "" }},
		{"a fence that is not hex", func(o *LaunchOptions) { o.Recovery = "NOT-HEX" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := stageOpts()
			tc.twist(&opts)
			if _, err := SecretFrame(opts); err == nil {
				t.Fatal("the frame was built anyway")
			}
		})
	}
}

// TestSecretFrame_EndsInExactlyOneNewline: stage-1 detects a truncated body
// by comparing the surviving length against the declared one, and a command
// substitution strips every trailing newline — so one is the contract.
func TestSecretFrame_EndsInExactlyOneNewline(t *testing.T) {
	opts := stageOpts()
	frame, err := SecretFrame(opts)
	if err != nil {
		t.Fatalf("SecretFrame: %v", err)
	}
	if !bytes.HasSuffix(frame, []byte("\n")) || bytes.HasSuffix(frame, []byte("\n\n")) {
		t.Errorf("the frame does not end in exactly one newline: %q", frame)
	}
	if lines := bytes.Count(frame, []byte("\n")); lines != 3 {
		t.Errorf("the frame has %d lines, want the header, the capability and the fence", lines)
	}
}

// TestStage1Frame_FitsItsCapAndCarriesNoSecret.
func TestStage1Frame_FitsItsCapAndCarriesNoSecret(t *testing.T) {
	opts := stageOpts()
	for _, kind := range []ShellKind{ShellBash, ShellZsh, ShellUnknown, ShellAuto} {
		frame, err := Stage1Frame(kind, opts)
		if err != nil {
			t.Fatalf("Stage1Frame(%s): %v", kind, err)
		}
		if len(frame) > MaxStageFrameLen {
			t.Errorf("stage-1 for %s is %d bytes, over the %d cap", kind, len(frame), MaxStageFrameLen)
		}
		if bytes.Contains(frame, []byte(canaryCap)) || bytes.Contains(frame, []byte(canaryFence)) {
			t.Errorf("stage-1 for %s carries a secret", kind)
		}
		t.Logf("stage-1 for %-8s is %d bytes of the %d cap", kind, len(frame), MaxStageFrameLen)
	}
	if _, err := Stage1Frame(ShellKind("smoothie"), opts); err == nil {
		t.Error("an unmapped shell kind produced a stage-1 instead of a refusal")
	}
}
