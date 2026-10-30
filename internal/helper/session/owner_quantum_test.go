package session

import (
	"bytes"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/sessionruntime"
)

// continuousRawProcess never reports EAGAIN while open. Every quantum is
// observable, and its bytes pass through the real owner/runtime ingest path.
// Unlike a finite queued script, it cannot accidentally let a drain-to-EAGAIN
// implementation escape when the test happens to stop producing.
type continuousRawProcess struct {
	*rawReaderFakeProcess
	quanta chan struct{}
	onRead func()
}

func newContinuousRawProcess() *continuousRawProcess {
	return &continuousRawProcess{rawReaderFakeProcess: newRawReaderFakeProcess(), quanta: make(chan struct{}, 1)}
}

func (p *continuousRawProcess) RawReadChunk(buf []byte) (int, bool, error) {
	p.mu.Lock()
	ended := p.eofSet
	p.mu.Unlock()
	if ended {
		return 0, false, io.EOF
	}
	if p.onRead != nil {
		p.onRead()
	}
	// Keep the screen workload small; readability, not ingest cost, is the
	// adversarial property. There is always another byte on the next read.
	buf[0] = 'A'
	select {
	case p.quanta <- struct{}{}:
	default:
	}
	return 1, false, nil
}

func (p *continuousRawProcess) awaitQuantum(t *testing.T) {
	t.Helper()
	select {
	case <-p.quanta:
	case <-time.After(hangLimit):
		t.Fatal("the owner never ingested continuous output")
	}
}

func TestContinuousReadableOutputAllowsInteractiveInputAndAccessBump(t *testing.T) {
	proc := newContinuousRawProcess()
	owner, rt := newTestOwner(t, proc)
	ctrl := grantAgent(t, rt)
	go owner.run()
	t.Cleanup(func() { owner.stop(false, time.Time{}) })
	proc.wake()
	proc.awaitQuantum(t)

	done, err := owner.submit(ownerItem{kind: itemIntent, intent: &pendingIntent{
		Intent: keyIntent(rt, ctrl, "Enter"), Interactive: true,
		Canonical: canonicalIntent{AccessEpoch: owner.currentAccessEpoch()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res := awaitResult(t, done); res.Err != nil || res.State != sessionruntime.IntentStateExecuted {
		t.Fatalf("interactive input under continuous output = %+v", res)
	}
	bump, err := owner.submit(ownerItem{kind: itemAccessBump, above: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res := awaitResult(t, bump); res.Epoch != 2 {
		t.Fatalf("access bump under continuous output = %+v", res)
	}
}

func TestContinuousReadableOutputExpiresCheckedIntentWithoutCheckingOrWriting(t *testing.T) {
	proc := newContinuousRawProcess()
	hs, ctrl := newIntentTestSession(t, proc)
	var clock atomic.Int64
	clock.Store(100)
	hs.owner.nowMono = clock.Load
	tok := mintRegionToken(t, hs)
	// The first quantum crosses the deadline after token admission, not at
	// receipt. A second owner turn must resolve it without producer exit.
	proc.onRead = func() { clock.Store(300) }
	checked := make(chan struct{}, 1)
	canon := canonicalIntent{Kind: sessionruntime.IntentKindText, Payload: []byte("v"), AccessEpoch: tok.AccessEpoch}
	done, err := hs.owner.submit(ownerItem{kind: itemIntent, intent: &pendingIntent{
		Intent: sessionruntime.Intent{
			At: hs.runtime.Incarnation(), Under: ctrl.Epoch, By: ctrl.Holder,
			Kind: sessionruntime.IntentKindText, Payload: []byte("v"),
		},
		Token: tok, Canonical: canon, CommitBy: 200,
		Check: func(sessionruntime.Snapshot) error { checked <- struct{}{}; return nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res := awaitResult(t, done); !errors.Is(res.Err, errCommitDeadline) || res.BytesWritten != 0 {
		t.Fatalf("checked intent under continuous output = %+v", res)
	}
	select {
	case <-checked:
		t.Fatal("a screen check ran without a read barrier")
	default:
	}
	if got := proc.writtenPayloads(); len(got) != 0 {
		t.Fatalf("expired intent wrote %q", got)
	}
	if state, _ := hs.tokens.Status(tok.ID); state != "recorded" {
		t.Fatalf("expired token state = %q, want recorded", state)
	}
}

func TestCheckedHeadWaitsForEveryReadableChunkAndEAGAIN(t *testing.T) {
	proc := newRawReaderFakeProcess()
	owner, rt := newTestOwner(t, proc)
	ctrl := grantAgent(t, rt)
	defer func() { _ = proc.Close() }()
	proc.produce(bytes.Repeat([]byte("x"), pageSize))
	proc.produce([]byte("\r\nLAST-READABLE-CHUNK"))
	checked := false
	done := make(chan ownerResult, 1)
	owner.pending = append(owner.pending, ownerItem{kind: itemIntent, done: done, intent: &pendingIntent{
		Intent: keyIntent(rt, ctrl, "Enter"), Interactive: true,
		Canonical: canonicalIntent{AccessEpoch: owner.currentAccessEpoch()},
		Check: func(s sessionruntime.Snapshot) error {
			checked = true
			if !bytes.Contains(s.Screen, []byte("LAST-READABLE-CHUNK")) {
				t.Error("check did not see the last readable chunk")
			}
			return errors.New("test refuses the write after checking")
		},
	}})
	owner.advance()
	if checked || len(owner.pending) != 1 {
		t.Fatal("checked head did not remain queued after the first quantum")
	}
	owner.advance()
	if checked || len(owner.pending) != 1 {
		t.Fatal("positive data was mistaken for an EAGAIN barrier")
	}
	owner.advance()
	if !checked || len(owner.pending) != 0 {
		t.Fatal("checked head did not resolve after the actual read barrier")
	}
	if res := <-done; res.State != sessionruntime.IntentStateRefused {
		t.Fatalf("checked head = %+v", res)
	}
}

func TestContinuousReadableOutputDoesNotPreventStop(t *testing.T) {
	proc := newContinuousRawProcess()
	owner, _ := newTestOwner(t, proc)
	go owner.run()
	proc.wake()
	proc.awaitQuantum(t)
	stopped := make(chan struct{})
	go func() { owner.stop(false, time.Time{}); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(hangLimit):
		t.Fatal("continuous readability prevented owner stop/detach")
	}
}

func TestContinuousReadableOutputRevokesQueuedCheckedIntent(t *testing.T) {
	proc := newContinuousRawProcess()
	hs, ctrl := newIntentTestSession(t, proc)
	proc.wake()
	proc.awaitQuantum(t)
	tok := mintRegionToken(t, hs)
	done := submitIntent(t, hs, ctrl, tok, "must-not-write", 1<<62)
	bump, err := hs.owner.submit(ownerItem{kind: itemAccessBump, above: tok.AccessEpoch})
	if err != nil {
		t.Fatal(err)
	}
	if res := awaitResult(t, bump); res.Epoch != tok.AccessEpoch+1 {
		t.Fatalf("bump = %+v", res)
	}
	if res := awaitResult(t, done); !errors.Is(res.Err, errAccessRevoked) {
		t.Fatalf("queued intent = %+v, want access_revoked", res)
	}
	if got := proc.writtenPayloads(); len(got) != 0 {
		t.Fatalf("revoked checked intent wrote %q", got)
	}
	if state, _ := hs.tokens.Status(tok.ID); state != "recorded" {
		t.Fatalf("revoked token state = %q", state)
	}
}

func TestContinuousReadableOutputAllowsWriterDetach(t *testing.T) {
	proc := newContinuousRawProcess()
	owner, _ := newTestOwner(t, proc)
	entered, release := proc.blockNextWrite()
	defer release()
	go owner.run()
	t.Cleanup(func() { owner.stop(false, time.Time{}) })
	proc.wake()
	proc.awaitQuantum(t)
	done, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("held")})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(hangLimit):
		t.Fatal("the writer never entered the held write")
	}
	owner.triggerDetach()
	if res := awaitResult(t, done); !errors.Is(res.Err, errDeliveryUnknown) {
		t.Fatalf("detached write = %+v", res)
	}
	if !owner.writerDetached() {
		t.Fatal("continuous readability prevented writer detach")
	}
}

func TestOwnerAdmissionRemainsBoundedAfterIncomingMovesToPending(t *testing.T) {
	proc := newRawReaderFakeProcess()
	owner, _ := newTestOwner(t, proc)
	defer func() { _ = proc.Close() }()
	var results []<-chan ownerResult
	for i := range intentQueueMax {
		done, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("queued")})
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		results = append(results, done)
		owner.dispatchIncoming(<-owner.incoming)
	}
	if _, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("overflow")}); !errors.Is(err, errBusy) {
		t.Fatalf("admission beyond the pending bound = %v, want busy", err)
	}
	owner.beginClosing()
	for _, done := range results {
		if res := <-done; res.State != sessionruntime.IntentStateCancelled {
			t.Fatalf("queued result = %+v", res)
		}
	}
}

func TestContinuousReadableOutputExpiresCheckedIntentBehindHeldWriter(t *testing.T) {
	proc := newContinuousRawProcess()
	hs, ctrl := newIntentTestSession(t, proc)
	var clock atomic.Int64
	clock.Store(100)
	hs.owner.nowMono = clock.Load
	entered, release := proc.blockNextWrite()
	defer release()
	blocked, err := hs.owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("held")})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(hangLimit):
		t.Fatal("the writer never entered the held write")
	}
	tok := mintRegionToken(t, hs)
	done := submitIntent(t, hs, ctrl, tok, "expired", 200)
	// This no-op bump is an ordered receipt witness, not a revocation:
	// the token was admitted while clock was still within commitBy.
	witness, err := hs.owner.submit(ownerItem{kind: itemAccessBump, above: 0})
	if err != nil {
		t.Fatal(err)
	}
	awaitResult(t, witness)
	clock.Store(300)
	if res := awaitResult(t, done); !errors.Is(res.Err, errCommitDeadline) {
		t.Fatalf("intent behind a held writer = %+v", res)
	}
	release()
	awaitResult(t, blocked)
}

func TestContinuousReadableOutputExpiresTokenWithoutReadBarrier(t *testing.T) {
	proc := newContinuousRawProcess()
	hs, ctrl := newIntentTestSession(t, proc)
	tok := mintRegionToken(t, hs)
	var wall atomic.Int64
	wall.Store(tok.MintedAt.UnixNano())
	hs.tokens.now = func() time.Time { return time.Unix(0, wall.Load()) }
	proc.onRead = func() { wall.Store(tok.ExpiresAt.Add(time.Second).UnixNano()) }
	done := submitIntent(t, hs, ctrl, tok, "expired", 1<<62)
	if res := awaitResult(t, done); !errors.Is(res.Err, ErrExpired) {
		t.Fatalf("token awaiting read barrier = %+v, want expired", res)
	}
	if got := proc.writtenPayloads(); len(got) != 0 {
		t.Fatalf("expired token wrote %q", got)
	}
	if state, _ := hs.tokens.Status(tok.ID); state != "recorded" {
		t.Fatalf("expired token state = %q", state)
	}
}

func TestSaturatedOwnerStillAdmitsRevocationAndRefusesQueuedTokens(t *testing.T) {
	proc := newRawReaderFakeProcess()
	owner, rt := newTestOwner(t, proc)
	defer func() { _ = proc.Close() }()
	ctrl := grantAgent(t, rt)
	hs := &hostSession{owner: owner, runtime: rt, now: time.Now}
	hs.tokens = newTokenBook(rt.Incarnation(), time.Now)
	owner.SetTokens(hs.tokens)
	var results []<-chan ownerResult
	var tokens []Token
	for range intentQueueMax {
		tok := mintRegionToken(t, hs)
		results = append(results, submitIntent(t, hs, ctrl, tok, "must-not-write", 1<<62))
		tokens = append(tokens, tok)
		owner.dispatchIncoming(<-owner.incoming)
	}
	bump, err := owner.submit(ownerItem{kind: itemAccessBump, above: 1})
	if err != nil {
		t.Fatalf("revocation rejected behind saturated pending input: %v", err)
	}
	owner.dispatchIncoming(<-owner.incoming)
	if res := awaitResult(t, bump); res.Epoch != 2 {
		t.Fatalf("revocation result = %+v", res)
	}
	for i, done := range results {
		if res := awaitResult(t, done); !errors.Is(res.Err, errAccessRevoked) {
			t.Fatalf("queued intent %d = %+v", i, res)
		}
		if state, _ := hs.tokens.Status(tokens[i].ID); state != "recorded" {
			t.Fatalf("queued token %d state = %q", i, state)
		}
	}
	if got := proc.writtenPayloads(); len(got) != 0 {
		t.Fatalf("revoked queued intents wrote %q", got)
	}
}

func TestRefilledIncomingRevocationPrecedesPendingTokenCommit(t *testing.T) {
	proc := newRawReaderFakeProcess()
	owner, rt := newTestOwner(t, proc)
	ctrl := grantAgent(t, rt)
	hs := &hostSession{owner: owner, runtime: rt, now: time.Now}
	hs.tokens = newTokenBook(rt.Incarnation(), time.Now)
	owner.SetTokens(hs.tokens)
	tok := mintRegionToken(t, hs)
	victim := submitIntent(t, hs, ctrl, tok, "must-not-write", 1<<62)
	owner.dispatchIncoming(<-owner.incoming)

	denied := ownerItem{kind: itemIntent, intent: &pendingIntent{
		Interactive: true, Canonical: canonicalIntent{AccessEpoch: 1}, CommitBy: 1,
	}}
	for range intentQueueMax - 1 {
		if _, err := owner.submit(denied); err != nil {
			t.Fatal(err)
		}
	}
	var bump <-chan ownerResult
	receipts := 0
	owner.nowMono = func() int64 {
		receipts++
		if receipts == 2 {
			// Receipt one refunded a slot; refill without exceeding the
			// ordinary admission bound or the incoming channel capacity.
			if _, err := owner.submit(denied); err != nil {
				t.Errorf("refilling incoming: %v", err)
			}
		}
		if receipts == intentQueueMax {
			var err error
			bump, err = owner.submit(ownerItem{kind: itemAccessBump, above: 1})
			if err != nil {
				t.Errorf("queueing revocation at the dispatch boundary: %v", err)
			}
		}
		return 100
	}
	go owner.run()
	t.Cleanup(func() { owner.stop(false, time.Time{}) })
	if res := awaitResult(t, victim); !errors.Is(res.Err, errAccessRevoked) {
		t.Fatalf("pending token advanced ahead of queued revocation: %+v", res)
	}
	if bump == nil {
		t.Fatal("the receipt-boundary revocation was never queued")
	}
	if res := awaitResult(t, bump); res.Epoch != 2 {
		t.Fatalf("receipt-boundary revocation = %+v", res)
	}
	if got := proc.writtenPayloads(); len(got) != 0 {
		t.Fatalf("pending token wrote before queued revocation: %q", got)
	}
}

func TestRefillingReceiptRefusalsStillIngestsLocalOutput(t *testing.T) {
	proc := newContinuousRawProcess()
	owner, rt := newTestOwner(t, proc)
	denied := ownerItem{kind: itemIntent, intent: &pendingIntent{
		Interactive: true, Canonical: canonicalIntent{AccessEpoch: 1}, CommitBy: 1,
	}}
	for range intentQueueMax {
		if _, err := owner.submit(denied); err != nil {
			t.Fatal(err)
		}
	}
	receipts := 0
	owner.nowMono = func() int64 {
		receipts++
		if receipts > 1 {
			// Refill from the previous receipt's refunded slot. Incoming
			// stays nonempty without ever exceeding admission capacity.
			if _, err := owner.submit(denied); err != nil && !errors.Is(err, errOwnerClosing) {
				t.Errorf("refilling denied input: %v", err)
			}
		}
		return 100
	}
	changed := owner.win.changed()
	proc.wake()
	go owner.run()
	t.Cleanup(func() { owner.stop(false, time.Time{}) })
	select {
	case <-changed:
	case <-time.After(hangLimit):
		t.Fatal("refilled receipt refusals starved local output ingest")
	}
	if !bytes.Contains(rt.Snapshot().Screen, []byte("A")) {
		t.Fatal("local output was not ingested into the screen")
	}
}
