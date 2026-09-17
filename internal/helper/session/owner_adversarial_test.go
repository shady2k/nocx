package session

// Independent adversarial tests for the session I/O owner and the one-shot
// token path (nocx-6q1uh.13, "the owner and token" part; the authority part
// is a different worker's). Written from the SPEC (.internal/specs/
// 2026-09-14-the-session-surface-design.md §5, §6.2, §6.5, §7.2) by a worker
// who did not implement owner.go, owner_ssh.go, tokens.go, access.go or
// intent_ops.go — AGENTS.md's testing rule 4 — against the merged tree at
// nocx-6q1uh.6, reusing owner_test.go's and intent_test.go's own fixtures
// and helpers (newTestOwner, grantAgent, keyIntent, newOwnerFakeProcess,
// newIntentTestSession, mintRegionToken, submitIntent, submitBump,
// awaitResult) rather than re-deriving them.
//
// One fixture is new here: rawReaderFakeProcess. When this file was first
// written, EVERY fixture in this package (ownerFakeProcess, scriptedProcess,
// blockingProcess) omitted the rawReader interface (WaitReadable/
// RawReadUntilAgain) that owner.go's mode()/hasReadBarrier() use to decide
// "local PTY versus SSH channel" (spec §5.2), so commitIntent (owner.go)
// refused ErrNoReadBarrier for EVERY itemIntent — not only a token-bearing
// one — before Admit was ever asked, contradicting nearly every assertion
// owner_test.go, intent_test.go and commit_point_test.go make about intents
// reaching Executed, Refused-for-a-different-reason, or a check function
// ever running at all. nocx-6q1uh.15 fixed the FIXTURES rather than the
// production gate: ownerFakeProcess (owner_test.go) and blockingProcess
// (intent_test.go) now implement rawReader faithfully, and the bare
// *scriptedProcess call sites that needed a barrier were switched to
// rawReaderFakeProcess instead — never scriptedProcess itself, which stays
// without one on purpose (see the next paragraph). This file's own
// TestNoReadBarrierRefusesATokenAtReceiptButNeverRecordsItsOutcome, directly
// below, is the one test that still needs a session with NO barrier, and it
// keeps using bare *scriptedProcess for exactly that reason — every OTHER
// test below that needs an intent to actually commit is built over
// rawReaderFakeProcess so it is not itself standing on the same gap.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/sessionruntime"
)

// ---------------------------------------------------------------------------
// rawReaderFakeProcess: a Process with a REAL local-PTY-shaped read barrier.
// ---------------------------------------------------------------------------

// rawReaderFakeProcess implements [rawReader] (owner.go) properly: a
// readiness signal a test raises with produce/endRead, and a
// RawReadUntilAgain that hands the owner everything queued since the last
// drain — the shape a real local PTY's non-blocking read(2) loop has,
// without a real fd. Its write side mirrors ownerFakeProcess's own
// blockNextWrite/release, because that half of "a local PTY" is not what is
// missing from the existing fixtures — a short-write override is not needed
// here since TestEveryShortWritePrefixLengthReportsExactBytesWritten (below)
// uses ownerFakeProcess's own setWriteOverride directly (itemClientFrame
// never touches hasReadBarrier, so no barrier is needed for it).
type rawReaderFakeProcess struct {
	mu     sync.Mutex
	queue  [][]byte
	ready  chan struct{}
	eofSet bool
	eofErr error

	writeGate    chan struct{}
	writeEntered chan struct{}
	written      [][]byte
	// writeNotify publishes every Write's payload for [awaitWrite]
	// (nocx-6q1uh.15: commit_point_test.go's own TestACommitPointSpendsATokenExactlyOnce
	// needs to wait for a write it does not otherwise control the timing of,
	// which written's slice alone cannot do without a duration-based poll —
	// AGENTS.md, "no test depends on timing"). It mirrors scriptedProcess's
	// own written channel (runtime_test.go) rather than inventing a second
	// shape for the same idea.
	writeNotify chan []byte

	closed  bool
	closeCh chan struct{}
	pid     int
}

func newRawReaderFakeProcess() *rawReaderFakeProcess {
	return &rawReaderFakeProcess{
		ready:       make(chan struct{}, 1),
		closeCh:     make(chan struct{}),
		writeNotify: make(chan []byte, 64),
		pid:         6060,
	}
}

func (p *rawReaderFakeProcess) wake() {
	select {
	case p.ready <- struct{}{}:
	default:
	}
}

// produce makes b available to the NEXT drain, delivered whole — never
// truncated to a scratch buffer's size the way a fixture built only around
// io.Reader (ownerFakeProcess, scriptedProcess) would be by a single Read
// call, which is why the reply-storm test below needs this fixture rather
// than either of those: a single produce of tens of thousands of bytes must
// reach the emulator in one piece for the reply it provokes to be measured
// honestly.
func (p *rawReaderFakeProcess) produce(b []byte) {
	p.mu.Lock()
	p.queue = append(p.queue, append([]byte(nil), b...))
	p.mu.Unlock()
	p.wake()
}

func (p *rawReaderFakeProcess) endRead(err error) {
	p.mu.Lock()
	if p.eofSet {
		p.mu.Unlock()
		return
	}
	p.eofSet = true
	p.eofErr = err
	p.mu.Unlock()
	p.wake()
}

func (p *rawReaderFakeProcess) WaitReadable(ctx context.Context) error {
	select {
	case <-p.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *rawReaderFakeProcess) RawReadUntilAgain(_ []byte, deliver func([]byte)) (eof bool, err error) {
	p.mu.Lock()
	q := p.queue
	p.queue = nil
	eofSet := p.eofSet
	eofErr := p.eofErr
	p.mu.Unlock()
	for _, chunk := range q {
		deliver(chunk)
	}
	if eofSet {
		if eofErr == nil {
			eofErr = io.EOF
		}
		return true, eofErr
	}
	return false, nil
}

var _ rawReader = (*rawReaderFakeProcess)(nil)

// Read is never called by the owner over this fixture (mode() picks
// rawReader first), so it only has to exist to satisfy io.Reader.
func (p *rawReaderFakeProcess) Read(_ []byte) (int, error) {
	<-p.closeCh
	return 0, io.EOF
}

func (p *rawReaderFakeProcess) Write(b []byte) (int, error) {
	p.mu.Lock()
	gate := p.writeGate
	entered := p.writeEntered
	p.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if gate != nil {
		<-gate
	}
	payload := append([]byte(nil), b...)
	p.mu.Lock()
	p.written = append(p.written, payload)
	p.mu.Unlock()
	select {
	case p.writeNotify <- payload:
	default:
	}
	return len(b), nil
}

// awaitWrite waits for the next thing this process was written, the same
// observable-event shape scriptedProcess.awaitWrite (runtime_test.go) gives.
func (p *rawReaderFakeProcess) awaitWrite(t *testing.T) []byte {
	t.Helper()
	select {
	case w := <-p.writeNotify:
		return w
	case <-time.After(hangLimit):
		t.Fatal("the session never wrote an answer back to the program")
		return nil
	}
}

func (p *rawReaderFakeProcess) blockNextWrite() (waitEntered <-chan struct{}, release func()) {
	entered := make(chan struct{})
	gate := make(chan struct{})
	p.mu.Lock()
	p.writeEntered = entered
	p.writeGate = gate
	p.mu.Unlock()
	var once sync.Once
	return entered, func() {
		once.Do(func() {
			p.mu.Lock()
			p.writeGate = nil
			p.writeEntered = nil
			p.mu.Unlock()
			close(gate)
		})
	}
}

func (p *rawReaderFakeProcess) writtenPayloads() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]byte, len(p.written))
	copy(out, p.written)
	return out
}

func (p *rawReaderFakeProcess) Resize(context.Context, uint16, uint16, uint16, uint16) error {
	return nil
}

func (p *rawReaderFakeProcess) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	close(p.closeCh)
	p.endRead(nil)
	return nil
}

func (p *rawReaderFakeProcess) Done() <-chan struct{} { return p.closeCh }

func (p *rawReaderFakeProcess) WaitErr() (error, bool) { return nil, false }

func (p *rawReaderFakeProcess) Pid() int { return p.pid }

func (p *rawReaderFakeProcess) Shell() string { return "raw-reader-fake" }

func (p *rawReaderFakeProcess) ForegroundProcessGroup() (int, error) { return 0, nil }

var _ Process = (*rawReaderFakeProcess)(nil)

// ---------------------------------------------------------------------------
// TestDrainBeforeValidateSeesOutputAlreadyReadableAtTheHead (spec §5.3 step 1)
// ---------------------------------------------------------------------------

// TestDrainBeforeValidateSeesOutputAlreadyReadableAtTheHead is the winning
// side of TestAnIntentBehindABlockedClientFrameValidatesAtTheHead
// (owner_test.go): output produced while an earlier write sits blocked, and
// therefore already readable by the time the intent behind it reaches the
// head, must be drained and ingested before that intent's own check runs —
// over the REAL raw-reader drain path (drainLocal), not through a direct
// rt.Ingest call from the test's own goroutine the way the existing test
// does.
func TestDrainBeforeValidateSeesOutputAlreadyReadableAtTheHead(t *testing.T) {
	proc := newRawReaderFakeProcess()
	owner, rt := newTestOwner(t, proc)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })
	ctrl := grantAgent(t, rt)

	entered, release := proc.blockNextWrite()
	frameDone, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("x")})
	if err != nil {
		t.Fatalf("submit frame: %v", err)
	}
	<-entered

	proc.produce([]byte("fresh-output\r\n"))

	checkRan := make(chan sessionruntime.Snapshot, 1)
	intentDone, err := owner.submit(ownerItem{
		kind: itemIntent,
		intent: &pendingIntent{
			Intent: keyIntent(rt, ctrl, "Enter"),
			Check: func(snap sessionruntime.Snapshot) error {
				checkRan <- snap
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("submit intent: %v", err)
	}

	release()
	if res := <-frameDone; res.Err != nil {
		t.Fatalf("frame: %v", res.Err)
	}

	var snap sessionruntime.Snapshot
	select {
	case snap = <-checkRan:
	case <-time.After(5 * time.Second):
		t.Fatal("the intent's check never ran")
	}
	if got := string(snap.Screen); !strings.Contains(got, "fresh-output") {
		t.Fatalf("the check's own snapshot read %q, want it to contain the output produced before the intent reached the head", got)
	}
	if res := <-intentDone; res.Err != nil || res.State != sessionruntime.IntentStateExecuted {
		t.Fatalf("intent: state=%v err=%v, want executed", res.State, res.Err)
	}
}

// ---------------------------------------------------------------------------
// TestReplyStormWithBlockedWriterOverflowsReserveAndDrainContinues (spec §5.5, §5.8)
// ---------------------------------------------------------------------------

// TestReplyStormWithBlockedWriterOverflowsReserveAndDrainContinues is spec
// §5.5's own scenario: a program keeps asking questions (DSR, "where is my
// cursor") while a write ahead of it sits blocked (nothing is reading the
// program's input), the concatenated answer exceeds the 64 KiB reserve, and
// output arriving AFTER that overflow still reaches the screen — the drain
// never stalls just because a reply was dropped. The completeness→refusal
// half of the same promise ("every later intent is refused
// completeness_unknown") is checked in isolation, at the sessionruntime
// level, by internal/sessionruntime/digest_adversarial_test.go's
// TestCommitRefusesOnceCompletenessBecomesLostIngestNotOnlyWhenUnknown — see
// this task's report for why it is expected to fail against the merged
// runtime.go.
func TestReplyStormWithBlockedWriterOverflowsReserveAndDrainContinues(t *testing.T) {
	proc := newRawReaderFakeProcess()
	owner, rt := newTestOwner(t, proc)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })

	entered, release := proc.blockNextWrite()
	frameDone, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("x")})
	if err != nil {
		t.Fatalf("submit frame: %v", err)
	}
	<-entered

	// 12,000 cursor-position queries answered from a cursor that never
	// moves (nothing here reads them) produce at least 12,000*7 = 84,000
	// bytes of reply — comfortably past the 64 KiB reserve in one Ingest
	// call.
	storm := bytes.Repeat([]byte("\x1b[6n"), 12000)
	proc.produce(storm)

	deadline := time.After(10 * time.Second)
	for rt.Completeness() != sessionruntime.CompletenessLostIngest {
		select {
		case <-deadline:
			t.Fatal("completeness never became lost-ingest after the reply storm overflowed the reserve")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// The drain keeps running after the overflow: ordinary output produced
	// afterward still reaches the screen.
	proc.produce([]byte("post-storm\r\n"))
	deadline = time.After(10 * time.Second)
	for !strings.Contains(string(rt.Snapshot().Screen), "post-storm") {
		select {
		case <-deadline:
			t.Fatal("output after the reply storm never reached the screen: the drain appears to have stalled")
		case <-time.After(5 * time.Millisecond):
		}
	}

	release()
	if res := <-frameDone; res.Err != nil {
		t.Fatalf("the previously-blocked frame: %v", res.Err)
	}
}

// ---------------------------------------------------------------------------
// TestResizeRacingARealTokenCommitIsIncomparable (spec §5.6, §6.2)
// ---------------------------------------------------------------------------

// TestResizeRacingARealTokenCommitIsIncomparable is a stronger variant of
// owner_test.go's TestAResizeRacingACommitMakesTheTargetIncomparable: that
// test stands in for a token with a hand-rolled check comparing geometry
// directly (its own doc says so, because nocx-6q1uh.3 had no token yet).
// This drives the REAL token path — a token minted from a real snapshot,
// checked by tokens.go's own checkToken through commitIntent's own
// tokenGate/Commit sequence — against a resize that lands between mint and
// commit.
func TestResizeRacingARealTokenCommitIsIncomparable(t *testing.T) {
	proc := newRawReaderFakeProcess()
	hs, control := newIntentTestSession(t, proc)

	entered, release := proc.blockNextWrite()
	blockerTok := mintRegionToken(t, hs)
	blockerDone := submitIntent(t, hs, control, blockerTok, "q", math.MaxInt64)
	<-entered

	// Minted against the geometry in force before the resize below commits.
	victimTok := mintRegionToken(t, hs)

	g := sessionruntime.Geometry{Cols: 100, Rows: 40}
	resizeDone, err := hs.owner.submit(ownerItem{kind: itemResize, resize: &g})
	if err != nil {
		t.Fatalf("submit resize: %v", err)
	}
	victimDone := submitIntent(t, hs, control, victimTok, "v", math.MaxInt64)

	release()
	blocker := awaitResult(t, blockerDone)
	if blocker.State != sessionruntime.IntentStateExecuted {
		t.Fatalf("blocker: state=%v err=%v, want executed", blocker.State, blocker.Err)
	}
	resize := awaitResult(t, resizeDone)
	if resize.State != sessionruntime.IntentStateExecuted {
		t.Fatalf("resize: state=%v err=%v, want executed", resize.State, resize.Err)
	}
	victim := awaitResult(t, victimDone)
	if victim.State != sessionruntime.IntentStateRefused || !errors.Is(victim.Err, errIncomparable) {
		t.Fatalf("victim after the resize: state=%v err=%v, want refused/errIncomparable", victim.State, victim.Err)
	}
}

// ---------------------------------------------------------------------------
// TestSixteenConcurrentSubmittersNeverInterleaveAFrameAndAnIntent (spec §5.3)
// ---------------------------------------------------------------------------

// TestSixteenConcurrentSubmittersNeverInterleaveAFrameAndAnIntent is the
// plan's own schedule for Task 13: "under 16 concurrent submitters" — unlike
// owner_test.go's TestAClientFrameAndAnIntentNeverInterleave, which drives
// 1000 rounds from ONE controlling goroutine (a frame then an intent, in
// strict alternation), this races 16 independent goroutines each submitting
// frames and intents on their own schedule, and checks the byte-level
// promise the linearisation point (spec §5.3 step 3) makes: every payload
// that reaches the process is exactly one item's own bytes, never a splice
// of two.
func TestSixteenConcurrentSubmittersNeverInterleaveAFrameAndAnIntent(t *testing.T) {
	const submitters = 16
	const roundsPerSubmitter = 60
	proc := newRawReaderFakeProcess()
	owner, rt := newTestOwner(t, proc)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })
	ctrl := grantAgent(t, rt)

	var wg sync.WaitGroup
	for s := 0; s < submitters; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			for r := 0; r < roundsPerSubmitter; r++ {
				if (s+r)%2 == 0 {
					done, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("F")})
					if err != nil {
						continue // errBusy under 16-way contention on a bounded queue: acceptable, not a defect this test is about.
					}
					<-done
				} else {
					done, err := owner.submit(ownerItem{
						kind:   itemIntent,
						intent: &pendingIntent{Intent: keyIntent(rt, ctrl, "Enter")},
					})
					if err != nil {
						continue
					}
					<-done
				}
			}
		}(s)
	}
	wg.Wait()

	for _, p := range proc.writtenPayloads() {
		if len(p) == 0 {
			t.Fatal("an empty payload reached the process: interleaving corrupted a write")
		}
		if len(p) == 1 && p[0] == 'F' {
			continue
		}
		for _, b := range p {
			if b == 'F' {
				t.Fatalf("a payload mixed the client frame's own byte into an intent's write: %q", p)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TestEveryShortWritePrefixLengthReportsExactBytesWritten (spec §6.5)
// ---------------------------------------------------------------------------

// TestEveryShortWritePrefixLengthReportsExactBytesWritten is a stronger
// variant of owner_test.go's TestAFailedPartialWriteReportsTheExactBytesWritten,
// which exercises exactly one prefix length. This exercises every prefix
// length from 1 to len(payload)-1, since finishItem's own "n != len(payload)"
// comparison is a boundary a single case cannot fully trust.
func TestEveryShortWritePrefixLengthReportsExactBytesWritten(t *testing.T) {
	payload := []byte("more than three bytes long")
	for prefixLen := 1; prefixLen < len(payload); prefixLen++ {
		prefixLen := prefixLen
		t.Run(fmt.Sprintf("prefix=%d", prefixLen), func(t *testing.T) {
			proc := newOwnerFakeProcess()
			owner, _ := newTestOwner(t, proc)
			go owner.run()
			t.Cleanup(func() { owner.stop(true, time.Time{}) })

			proc.setWriteOverride(func(p []byte) (int, error) {
				return prefixLen, nil
			})

			done, err := owner.submit(ownerItem{kind: itemClientFrame, payload: append([]byte(nil), payload...)})
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			res := <-done
			if res.State != sessionruntime.IntentStateFailed {
				t.Fatalf("state=%v, want failed", res.State)
			}
			if res.BytesWritten != prefixLen {
				t.Fatalf("BytesWritten=%d, want exactly %d", res.BytesWritten, prefixLen)
			}
			if !errors.Is(res.Err, io.ErrShortWrite) {
				t.Fatalf("err=%v, want io.ErrShortWrite", res.Err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestNoReadBarrierRefusesATokenAtReceiptButNeverRecordsItsOutcome (spec §5.2, §6.2)
// ---------------------------------------------------------------------------

// TestNoReadBarrierRefusesATokenAtReceiptButNeverRecordsItsOutcome is this
// task's flagship finding, UPDATED for nocx-6q1uh.15's fix: it pinned down a
// wedge (below) and now pins down its absence. scriptedProcess
// (runtime_test.go) is the same barrier-less fixture ssh_owner_test.go's
// real-SSH tests independently confirm hasReadBarrier() answers false for
// (owner_ssh.go's mode()); using it here is deliberate, not an oversight —
// see this file's own package doc.
//
// The original defect: tokenGate (tokens.go) ran Consume — binding the token
// to its canonical intent — before commitIntent (owner.go) ever got a chance
// to check hasReadBarrier(), so a no_read_barrier refusal at commitIntent
// left the slot bound but recorded nothing (commitIntent's own refusal
// RETURNED without calling recordTokenOutcome). session.intent.status could
// then never answer anything but "in_progress" for that token, forever, and
// a same-token retry got stuck the same way: tokenGate's own Consume saw a
// bound-but-unrecorded slot and answered in_progress without ever reaching
// commitIntent's refusal a second time.
//
// The fix moves the read-barrier check to the FRONT of tokenGate itself,
// before Verify or Consume ever run (spec §5.2 is a fact about the SESSION,
// fixed for its whole life, never about this attempt), and chooses "never
// consume" over "consume and record a terminal refused result" (spec §6.2's
// two readings of "a refusal is recorded"): a session with no barrier can
// never honour ANY future attempt on this token either, so binding it would
// only spend a one-shot resource on a write that never happened. The
// observable consequence, asserted below: the token is never bound at all
// (Status answers "unknown", the same as a token nothing has ever touched),
// and a retry of the same token+intent gets the SAME refusal again — not
// wedged, and not admitted into a queue nothing will ever service.
func TestNoReadBarrierRefusesATokenAtReceiptButNeverRecordsItsOutcome(t *testing.T) {
	proc := newScriptedProcess("")
	hs, control := newIntentTestSession(t, proc)
	tok := mintRegionToken(t, hs)
	canon := canonicalIntent{Kind: sessionruntime.IntentKindText, Payload: []byte("hi"), AccessEpoch: tok.AccessEpoch}

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
			CommitBy:  math.MaxInt64,
		},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	res := awaitResult(t, done)
	if res.State != sessionruntime.IntentStateRefused || !errors.Is(res.Err, ErrNoReadBarrier) {
		t.Fatalf("first attempt: state=%v err=%v, want refused/ErrNoReadBarrier", res.State, res.Err)
	}
	if res.BytesWritten != 0 {
		t.Fatalf("a no_read_barrier refusal reported %d bytes written, want 0", res.BytesWritten)
	}

	// Fixed: the read-barrier check runs before Consume ever binds the
	// token, so the slot is never touched at all — Status answers exactly
	// what it would for a token nothing has ever attempted to spend.
	if state, r := hs.tokens.Status(tok.ID); state != "unknown" || r != nil {
		t.Fatalf("status after a no_read_barrier refusal: got (%q, %v), want (\"unknown\", nil) — "+
			"a refusal decided before Consume ever ran must leave the token entirely untouched",
			state, r)
	}

	// A retry of the SAME token+intent gets the SAME refusal again, never
	// stuck behind a bound-but-unrecorded slot and never admitted into a
	// queue nothing will ever service: the read-barrier check runs first
	// every time, and this session's lack of one never changes.
	retry := awaitResult(t, submitIntent(t, hs, control, tok, "hi", math.MaxInt64))
	if retry.State != sessionruntime.IntentStateRefused || !errors.Is(retry.Err, ErrNoReadBarrier) {
		t.Fatalf("retry of the same token+intent: got state=%v err=%v, want refused/ErrNoReadBarrier again",
			retry.State, retry.Err)
	}
}

// ---------------------------------------------------------------------------
// TestALostResponseIsStillAnsweredThroughStatus (spec §6.2)
// ---------------------------------------------------------------------------

// TestALostResponseIsStillAnsweredThroughStatus is spec §6.2's "the helper
// cannot observe receipt" from the caller's own side: a caller submits an
// intent and never reads the channel submit handed back — the same
// observable shape a crashed caller or a dropped connection would leave
// behind — and the outcome must still be discoverable afterward through
// session.intent-status. Unlike intent_test.go's
// TestCancellingTheCallDoesNotCancelTheIntent, nothing here ever creates a
// context to cancel: the response is simply never read at all.
func TestALostResponseIsStillAnsweredThroughStatus(t *testing.T) {
	proc := newRawReaderFakeProcess()
	hs, control := newIntentTestSession(t, proc)
	tok := mintRegionToken(t, hs)

	canon := canonicalIntent{Kind: sessionruntime.IntentKindText, Payload: []byte("gone"), AccessEpoch: tok.AccessEpoch}
	if _, err := hs.owner.submit(ownerItem{
		kind: itemIntent,
		intent: &pendingIntent{
			Intent: sessionruntime.Intent{
				At: tok.At, Under: control.Epoch, By: control.Holder,
				Kind: canon.Kind, Payload: canon.Payload,
			},
			Check:     checkToken(tok),
			Token:     tok,
			Canonical: canon,
			CommitBy:  math.MaxInt64,
		},
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	deadline := time.After(hangLimit)
	for {
		state, r := hs.tokens.Status(tok.ID)
		if state == "recorded" {
			if r.State != "executed" {
				t.Fatalf("recorded state = %q, want executed", r.State)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("the intent's outcome was never recorded even though its own caller never read the response")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// TestAccessBumpIsIdempotentOnAnAlreadySupersededEpochToo (spec §7.2)
// ---------------------------------------------------------------------------

// TestAccessBumpIsIdempotentOnAnAlreadySupersededEpochToo is a boundary
// variant of intent_test.go's TestABumpAcknowledgesOnlyAfterOlderIntentsAreTerminal,
// whose own idempotence check repeats the exact SAME above just sent. Spec
// §7.2 says "above names the epoch it supersedes", which is a broader claim:
// a bump naming an epoch superseded TWO bumps ago must be just as much a
// no-op as one repeating the last bump exactly.
func TestAccessBumpIsIdempotentOnAnAlreadySupersededEpochToo(t *testing.T) {
	proc := newRawReaderFakeProcess()
	hs, _ := newIntentTestSession(t, proc)

	first := awaitResult(t, submitBump(t, hs, 1))
	if first.Epoch != 2 {
		t.Fatalf("first bump epoch = %d, want 2", first.Epoch)
	}
	second := awaitResult(t, submitBump(t, hs, 3))
	if second.Epoch != 4 {
		t.Fatalf("second bump epoch = %d, want 4", second.Epoch)
	}

	stale := awaitResult(t, submitBump(t, hs, 1))
	if stale.Epoch != 4 {
		t.Fatalf("a bump naming a long-superseded epoch answered %d, want the epoch already in force, 4", stale.Epoch)
	}
	if got := hs.owner.currentAccessEpoch(); got != 4 {
		t.Fatalf("session access epoch = %d after a stale bump, want unchanged at 4", got)
	}
}

// ---------------------------------------------------------------------------
// TestCommitByExactlyAtTheDeadlineIsNotRefused (spec §7.2, boundary)
// ---------------------------------------------------------------------------

// TestCommitByExactlyAtTheDeadlineIsNotRefused is the boundary
// intent_test.go's TestAnIntentPastCommitByIsRefused does not check: both
// tokenGate (tokens.go) and commitIntent (owner.go) refuse only when
// "now > commitBy" — strictly greater — so a commitBy exactly equal to now
// must still be admitted, not refused as though it had already passed.
func TestCommitByExactlyAtTheDeadlineIsNotRefused(t *testing.T) {
	proc := newRawReaderFakeProcess()
	hs, control := newIntentTestSession(t, proc)
	hs.owner.nowMono = func() int64 { return 1000 }
	tok := mintRegionToken(t, hs)

	res := awaitResult(t, submitIntent(t, hs, control, tok, "x", 1000))
	if res.State != sessionruntime.IntentStateExecuted {
		t.Fatalf("commitBy exactly at the deadline: state=%v err=%v, want executed", res.State, res.Err)
	}
}

// SignalProcessGroup stands in for the program answering the SIGHUP a
// graceful stop sends (sessionOwner.requestTermination): a real program on a
// PTY exits and its output reaches EOF, which is the one event a graceful stop
// waits for. Without it this fixture never ends its read, so every test that
// stops its owner gracefully in cleanup hangs there instead of finishing.
func (p *rawReaderFakeProcess) SignalProcessGroup(_ int, _ syscall.Signal) error {
	p.endRead(nil)
	return nil
}
