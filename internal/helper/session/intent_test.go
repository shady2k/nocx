package session

// The owner-side acceptance criteria of the one-shot write path
// (nocx-6q1uh.6, spec §6.2, §6.5, §7.2): a commit deadline enforced twice,
// an access-epoch bump that waits for what it revokes, and a retry that
// answers in_progress while genuinely blocked. Each submits ownerItems
// directly, the same seam commit_point_test.go (nocx-6q1uh.4) already
// exercises — the wire encoding these RPCs actually carry is covered
// separately, in internal/helper/client's contract tests.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// blockingProcess is scriptedProcess with a Write that parks until the test
// releases it (once). It is what makes a write OBSERVABLY still in flight:
// a retry answering in_progress while genuinely blocked (tokenGate runs at
// RECEIPT, tokens.go — never at an item's turn in the write queue), an
// access-bump proving it waits for a queued sibling to become terminal
// before it applies, and a commit-point commitBy check that only fires once
// something else has occupied the writer long enough for a clock to move
// past it.
type blockingProcess struct {
	*scriptedProcess
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func newBlockingProcess() *blockingProcess {
	return &blockingProcess{
		scriptedProcess: newScriptedProcess(""),
		gate:            make(chan struct{}),
		entered:         make(chan struct{}, 1),
	}
}

func (p *blockingProcess) Write(b []byte) (int, error) {
	select {
	case p.entered <- struct{}{}:
	default:
	}
	<-p.gate
	return p.scriptedProcess.Write(b)
}

// awaitEntered waits until a write has reached this process and is parked —
// an observable state change, never a duration (AGENTS.md).
func (p *blockingProcess) awaitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-p.entered:
	case <-time.After(hangLimit):
		t.Fatal("the write never reached the blocking process")
	}
}

// release lets every write through from now on — the first one still
// blocked, and every one after it, since the gate stays closed only once.
func (p *blockingProcess) release() { p.once.Do(func() { close(p.gate) }) }

// WaitReadable and RawReadUntilAgain give blockingProcess a read barrier
// (nocx-6q1uh.15): the [rawReader] shape hasReadBarrier (owner_ssh.go)
// requires, over the same "serve the script, then park" read behaviour
// scriptedProcess.Read already has. Every intent-bearing test in this file
// needs this to be true — without it, commitIntent refuses every intent
// ErrNoReadBarrier before Admit is ever asked, regardless of what the test
// itself is exercising (a commit deadline, an access bump, a cancelled
// caller). blockingProcess, not scriptedProcess, is where this lives: the
// bare *scriptedProcess ("") owner_adversarial_test.go's
// TestNoReadBarrierRefusesATokenAtReceiptButNeverRecordsItsOutcome uses is
// deliberately left without a barrier (that test's whole point), and giving
// scriptedProcess itself these methods would give that fixture one too.
// Explicit p.scriptedProcess.* field access is required throughout: this
// type's own release method (above) shadows the embedded release CHANNEL.
func (p *blockingProcess) WaitReadable(ctx context.Context) error {
	p.scriptedProcess.mu.Lock()
	hasScript := len(p.scriptedProcess.script) > 0
	p.scriptedProcess.mu.Unlock()
	if hasScript {
		return nil
	}
	select {
	case <-p.scriptedProcess.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *blockingProcess) RawReadUntilAgain(_ []byte, deliver func([]byte)) (eof bool, err error) {
	p.scriptedProcess.mu.Lock()
	chunk := p.scriptedProcess.script
	p.scriptedProcess.script = nil
	p.scriptedProcess.mu.Unlock()
	if len(chunk) > 0 {
		deliver(chunk)
	}
	select {
	case <-p.scriptedProcess.release:
		return true, io.EOF
	default:
		return false, nil
	}
}

var _ rawReader = (*blockingProcess)(nil)

// newIntentTestSession builds a real owner and token book over proc, wired
// the way finishSpawn wires them (service.go) minus the parts a bare owner
// test does not need: no Service, no inventory row. Control is granted up
// front — these tests submit ownerItems directly, under the owner's own
// vocabulary, so none of them ever reaches hostSession.ensureControl
// (intent_ops.go), which is what grants it lazily on the wire path instead.
func newIntentTestSession(t *testing.T, proc Process) (*hostSession, sessionruntime.Control) {
	t.Helper()
	rt := pumpRuntime(t, proc)
	hs := &hostSession{
		proc:    proc,
		win:     newWindow(2 * creditLimit),
		runtime: rt,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:     time.Now,
	}
	owner := newSessionOwner(proc, rt, hs.win, hs.log)
	rt.SetReplies(owner)
	hs.owner = owner
	hs.tokens = newTokenBook(rt.Incarnation(), time.Now)
	owner.SetTokens(hs.tokens)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })

	control, err := rt.GrantControl(sessionruntime.Principal{Kind: sessionruntime.PrincipalAgent, ID: "coordinator"})
	if err != nil {
		t.Fatalf("grant control: %v", err)
	}
	return hs, control
}

// mintRegionToken mints a whole-region target from a fresh snapshot: none of
// these tests care which rows a token names, only that it names a live one.
func mintRegionToken(t *testing.T, hs *hostSession) Token {
	t.Helper()
	snap, err := hs.takeSnapshot()
	if err != nil {
		t.Fatalf("takeSnapshot: %v", err)
	}
	tok, err := hs.mintTarget(snap.ID, sessionruntime.TargetRegion, sessionruntime.RowRange{First: 0, Last: 0})
	if err != nil {
		t.Fatalf("mintTarget: %v", err)
	}
	return tok
}

// submitIntent builds a token-bearing pendingIntent and submits it, without
// waiting — commit_point_test.go's submitTokenIntent does both in one call,
// which these tests cannot use whenever the ORDER two submissions reach
// o.incoming in matters (a victim queued before the bump that revokes it):
// sequential calls from ONE goroutine preserve that order on a single
// channel; two independent goroutines racing their own submits would not.
func submitIntent(t *testing.T, hs *hostSession, control sessionruntime.Control, tok Token, payload string, commitBy int64) <-chan ownerResult {
	t.Helper()
	canon := canonicalIntent{Kind: sessionruntime.IntentKindText, Payload: []byte(payload), AccessEpoch: tok.AccessEpoch}
	done, err := hs.owner.submit(ownerItem{
		kind: itemIntent,
		intent: &pendingIntent{
			Intent: sessionruntime.Intent{
				At: tok.At, Under: control.Epoch, By: control.Holder,
				Kind: canon.Kind, Payload: canon.Payload,
			},
			Check:     checkToken(tok),
			Token:     tok,
			Canonical: canon,
			CommitBy:  commitBy,
		},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return done
}

func submitBump(t *testing.T, hs *hostSession, above uint64) <-chan ownerResult {
	t.Helper()
	done, err := hs.owner.submit(ownerItem{kind: itemAccessBump, above: above})
	if err != nil {
		t.Fatalf("submit bump: %v", err)
	}
	return done
}

func awaitResult(t *testing.T, done <-chan ownerResult) ownerResult {
	t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(hangLimit):
		t.Fatal("the owner never resolved the submitted item")
		return ownerResult{}
	}
}

// TestAnIntentPastCommitByIsRefused is nocx-6q1uh.6's own acceptance
// criterion: spec §7.2's "refuses at receipt one whose commitBy has already
// passed" and "refuses it again at the commit point" are two SEPARATE
// checks in this tree — tokenGate (tokens.go) gates at receipt, the moment
// an itemIntent comes off o.incoming (owner.go's run()), before it is ever
// queued; commitIntent (owner.go) re-checks alone, right before Admit, for
// an intent that was fine when it arrived and stopped being so while queued
// behind other input. Both are exercised here, separately, each with its
// own fake clock.
func TestAnIntentPastCommitByIsRefused(t *testing.T) {
	t.Run("at receipt", func(t *testing.T) {
		// rawReaderFakeProcess (owner_adversarial_test.go), not
		// newScriptedProcess: this test needs a session WITH a read barrier
		// so tokenGate's own commitBy check (the thing under test) is what
		// refuses it, never the read-barrier check ahead of it in the same
		// function (nocx-6q1uh.15).
		proc := newRawReaderFakeProcess()
		hs, control := newIntentTestSession(t, proc)
		hs.owner.nowMono = func() int64 { return 1000 }
		tok := mintRegionToken(t, hs)

		res := awaitResult(t, submitIntent(t, hs, control, tok, "x", 999))
		if res.State != sessionruntime.IntentStateRefused || !errors.Is(res.Err, errCommitDeadline) {
			t.Fatalf("at receipt: got state=%v err=%v, want Refused/errCommitDeadline", res.State, res.Err)
		}
		if res.BytesWritten != 0 {
			t.Fatalf("a commit_deadline refusal reported %d bytes written, want 0", res.BytesWritten)
		}
		if got := proc.writtenPayloads(); len(got) != 0 {
			t.Fatalf("a commit_deadline refusal wrote to the program: %q", got)
		}
	})

	t.Run("at the commit point", func(t *testing.T) {
		proc := newBlockingProcess()
		hs, control := newIntentTestSession(t, proc)
		var clock int64 = 1000
		hs.owner.nowMono = func() int64 { return atomic.LoadInt64(&clock) }

		blockerTok := mintRegionToken(t, hs)
		blockerDone := submitIntent(t, hs, control, blockerTok, "q", math.MaxInt64)
		proc.awaitEntered(t)

		// Within its deadline the instant it arrives: tokenGate's own
		// receipt check must let it through and queue it behind the block.
		victimTok := mintRegionToken(t, hs)
		victimDone := submitIntent(t, hs, control, victimTok, "v", atomic.LoadInt64(&clock)+500)

		// The deadline is crossed WHILE the victim waits behind the blocked
		// write — never merely by having been submitted late.
		atomic.StoreInt64(&clock, clock+1000)
		proc.release()

		blocker := awaitResult(t, blockerDone)
		if blocker.State != sessionruntime.IntentStateExecuted {
			t.Fatalf("blocker: got %v, err=%v, want Executed", blocker.State, blocker.Err)
		}
		victim := awaitResult(t, victimDone)
		if victim.State != sessionruntime.IntentStateRefused || !errors.Is(victim.Err, errCommitDeadline) {
			t.Fatalf("at the commit point: got state=%v err=%v, want Refused/errCommitDeadline", victim.State, victim.Err)
		}
	})
}

// TestABumpAcknowledgesOnlyAfterOlderIntentsAreTerminal is spec §7.2's own
// acceptance criterion: session.access-bump raises this session's access
// epoch and refuses every intent still queued from before it
// (access.go's applyAccessBump) before it ever answers, and it is
// idempotent on the epoch it names.
func TestABumpAcknowledgesOnlyAfterOlderIntentsAreTerminal(t *testing.T) {
	proc := newBlockingProcess()
	hs, control := newIntentTestSession(t, proc)

	blockerTok := mintRegionToken(t, hs)
	blockerDone := submitIntent(t, hs, control, blockerTok, "q", math.MaxInt64)
	proc.awaitEntered(t)

	// Queued behind the block, at the CURRENT epoch — submitted before the
	// bump below, in this same goroutine, which is what fixes their order
	// on o.incoming regardless of how the owner's own select interleaves
	// its other cases.
	victimTok := mintRegionToken(t, hs)
	victimDone := submitIntent(t, hs, control, victimTok, "v", math.MaxInt64)

	bumpDone := submitBump(t, hs, 1)

	// The bump cannot have resolved yet: applyAccessBump runs only on the
	// owner's own goroutine, and that goroutine cannot reach it (or the
	// victim behind it) while the writer is still busy with the blocked
	// write — a structural guarantee (advance()'s own writerBusy guard),
	// not a race against wall-clock time.
	select {
	case res := <-bumpDone:
		t.Fatalf("the bump resolved (%+v) before the blocking write completed", res)
	default:
	}

	proc.release()

	victim := awaitResult(t, victimDone)
	if victim.State != sessionruntime.IntentStateRefused || !errors.Is(victim.Err, errAccessRevoked) {
		t.Fatalf("victim: got state=%v err=%v, want Refused/errAccessRevoked", victim.State, victim.Err)
	}
	bump := awaitResult(t, bumpDone)
	if bump.Epoch != 2 {
		t.Fatalf("bump epoch = %d, want 2", bump.Epoch)
	}
	blocker := awaitResult(t, blockerDone)
	if blocker.State != sessionruntime.IntentStateExecuted {
		t.Fatalf("blocker: got %v, err=%v, want Executed", blocker.State, blocker.Err)
	}
	if got := hs.owner.currentAccessEpoch(); got != 2 {
		t.Fatalf("session access epoch = %d, want 2", got)
	}

	// Idempotent on the epoch it names: a repeat naming the SAME above
	// changes nothing and answers the epoch already in force, never a
	// second sweep or a second raise.
	repeat := awaitResult(t, submitBump(t, hs, 1))
	if repeat.Epoch != 2 {
		t.Fatalf("repeat bump epoch = %d, want 2 (idempotent)", repeat.Epoch)
	}
}

// TestARetryWhileTheFirstWriteIsBlockedGetsInProgress is spec §6.2's own
// replay answer: tokenGate runs AT RECEIPT (tokens.go), never at an item's
// turn in the write-ordering queue, so a second submission of the SAME
// token and the SAME canonical intent answers in_progress the instant it
// arrives — even while the first attempt's write is still parked at the
// PTY, never only once the writer frees up. Once the write settles, a
// status poll answers the terminal, recorded outcome.
func TestARetryWhileTheFirstWriteIsBlockedGetsInProgress(t *testing.T) {
	proc := newBlockingProcess()
	hs, control := newIntentTestSession(t, proc)
	tok := mintRegionToken(t, hs)

	firstDone := submitIntent(t, hs, control, tok, "hi", math.MaxInt64)
	proc.awaitEntered(t)

	retry := awaitResult(t, submitIntent(t, hs, control, tok, "hi", math.MaxInt64))
	if retry.State != sessionruntime.IntentStateAdmitted {
		t.Fatalf("retry while blocked: got %v, want Admitted (the wire's in_progress)", retry.State)
	}

	proc.release()
	first := awaitResult(t, firstDone)
	if first.State != sessionruntime.IntentStateExecuted {
		t.Fatalf("first: got %v, err=%v, want Executed", first.State, first.Err)
	}

	state, r := hs.tokens.Status(tok.ID)
	if state != "recorded" || r == nil || r.State != "executed" {
		t.Fatalf("status after settling: got (%q, %+v), want recorded/executed", state, r)
	}
}

// TestCancellingTheCallDoesNotCancelTheIntent is D11 at the wire's own RPC
// method (hs.intent, intent_ops.go): the caller's context ending stops the
// CALL from waiting, never the intent the owner already has — session.intent
// is in RefusesCancel (service.go) for exactly this reason, spec §6.5's own
// framing ("transport cancellation never implies not executed"). The helper
// still settles the write and records a terminal result, retrievable
// afterward through session.intent-status — never as "cancelled": bytes on a
// PTY cannot be recalled, and a write the owner already committed to is not
// something a departed caller can un-ask for.
func TestCancellingTheCallDoesNotCancelTheIntent(t *testing.T) {
	proc := newBlockingProcess()
	hs, _ := newIntentTestSession(t, proc)
	tok := mintRegionToken(t, hs)
	wireTok, err := marshalToken(tok)
	if err != nil {
		t.Fatalf("marshalToken: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	type callOutcome struct {
		res proto.IntentResult
		err error
	}
	outcome := make(chan callOutcome, 1)
	go func() {
		res, callErr := hs.intent(ctx, proto.IntentParams{
			Token:       wireTok,
			AccessEpoch: tok.AccessEpoch,
			CommitBy:    math.MaxInt64,
			Kind:        "text",
			Payload:     []byte("hi"),
		})
		outcome <- callOutcome{res, callErr}
	}()

	proc.awaitEntered(t)
	cancel()

	select {
	case got := <-outcome:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("hs.intent returned err=%v, want context.Canceled", got.err)
		}
	case <-time.After(hangLimit):
		t.Fatal("hs.intent never returned once its context was cancelled")
	}

	// The write the cancelled call was waiting on is still parked: nothing
	// released it yet, so it must not have reached the process.
	select {
	case w := <-proc.written:
		t.Fatalf("the write landed before it was released: %q", w)
	default:
	}

	proc.release()

	// The owner settles it exactly as it would for a caller that stayed.
	// Polled because the cancelled call's own done channel is not reachable
	// from outside hs.intent: the ceiling below is a failure bound, never
	// the condition this test succeeds on (AGENTS.md, "no test depends on
	// timing").
	deadline := time.After(hangLimit)
	for {
		state, rec := hs.tokens.Status(tok.ID)
		if state == "recorded" {
			if rec.State != "executed" {
				t.Fatalf("recorded state = %q, want executed — a cancelled CALLER must never look like a cancelled INTENT", rec.State)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("the intent's outcome was never recorded after its caller's context ended")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestASessionIntentRefusalShowsTheRegionOnceThenOmitsIt is spec §6.2's own
// rule, exercised through the real wire op (renderIntentResult and
// hostSession.intentStatus, intent_ops.go), not through a hand-built DTO:
// "regionNow ... is returned only on the response that produced the result;
// a replay or session.intent-status answers the same refusal with
// RegionOmitted instead, never a stale echo of what the screen looked like
// when this was first decided." A stale_target refusal — the screen changing
// between mint and commit — is what this test uses to reach a refusal at
// all: TestARetryWhileTheFirstWriteIsBlockedGetsInProgress's own executed
// outcome never carries a Refusal object to omit anything from.
func TestASessionIntentRefusalShowsTheRegionOnceThenOmitsIt(t *testing.T) {
	// rawReaderFakeProcess, not newScriptedProcess (nocx-6q1uh.15): the
	// stale_target refusal under test is decided by checkToken inside
	// sessionruntime.Session.Commit, which an intent only reaches once it has
	// cleared tokenGate's own read-barrier check — a session with no barrier
	// refuses ErrNoReadBarrier before Commit is ever called.
	proc := newRawReaderFakeProcess()
	svc, hs := spawnScripted(t, proc, 0)

	snap := callOp[proto.SnapshotResult](t, svc, proto.OpSnapshot, proto.SnapshotParams{Session: hs.id})
	target := callOp[proto.TargetResult](t, svc, proto.OpTarget, proto.TargetParams{
		Session: hs.id, SnapshotID: snap.SnapshotID, Kind: "region",
		First: 0, Last: snap.Frame.Rows - 1,
	})

	// The program prints between the snapshot session.target minted from and
	// the commit session.intent will attempt: the row range's digest no
	// longer matches what the token named, unchanged identity — exactly
	// checkToken's (tokens.go) stale_target case, never incomparable.
	if err := hs.runtime.Ingest([]byte("changed\r\n")); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	commitBy := int64(monoclock.Now()) + int64(5*time.Second)
	first := callOp[proto.IntentResult](t, svc, proto.OpIntent, proto.IntentParams{
		Session: hs.id, Token: target.Token, AccessEpoch: snap.AccessEpoch,
		CommitBy: commitBy, Kind: "text", Payload: []byte("hi"),
	})
	if first.State != "refused" || first.Refusal == nil || first.Refusal.Cause != "stale_target" {
		t.Fatalf("the first attempt = %+v, want refused/stale_target", first)
	}
	if first.Refusal.RegionOmitted {
		t.Fatal("the FIRST refusal must show the region, not omit it")
	}

	// A replay of the SAME token+intent answers the recorded refusal —
	// still the response that produced it, per tokenGate's own "recorded"
	// short-circuit, so RegionOmitted follows the SAME rule a moment later.
	replay := callOp[proto.IntentResult](t, svc, proto.OpIntent, proto.IntentParams{
		Session: hs.id, Token: target.Token, AccessEpoch: snap.AccessEpoch,
		CommitBy: commitBy, Kind: "text", Payload: []byte("hi"),
	})
	if replay.State != "refused" || replay.Refusal == nil || replay.Refusal.Cause != "stale_target" {
		t.Fatalf("the replay = %+v, want the same refused/stale_target", replay)
	}
	if !replay.Refusal.RegionOmitted {
		t.Fatal("a REPLAY of the same token+intent must omit the region, not re-show it")
	}
	if replay.Refusal.RegionNow != "" {
		t.Fatalf("a replay carried regionNow %q, want none", replay.Refusal.RegionNow)
	}

	// session.intent-status answers the same compact record, also omitted.
	status := callOp[proto.IntentStatusResult](t, svc, proto.OpIntentStatus, proto.IntentStatusParams{
		Session: hs.id, TokenID: target.TokenID,
	})
	if status.Result == nil || status.Result.Refusal == nil || !status.Result.Refusal.RegionOmitted {
		t.Fatalf("intent-status = %+v, want the recorded refusal with regionOmitted", status)
	}
	if status.Result.Refusal.RegionNow != "" {
		t.Fatalf("intent-status carried regionNow %q, want none", status.Result.Refusal.RegionNow)
	}
}
