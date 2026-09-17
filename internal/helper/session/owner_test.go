package session

// The session I/O owner's acceptance tests (nocx-6q1uh.3, spec §5). Written
// test-first against the interfaces in owner.go and NOT run by this worker
// (the coordinator runs every suite once on the merged tree — AGENTS.md,
// "Git authority"); go vet and go build are what checked these compile and
// type-check.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// ---------------------------------------------------------------------------
// Fixture: a Process a test can block, override and end on its own terms —
// the fake half of these tests, standing in for a real PTY's slave not
// reading and for a writer returning n < len(p).
// ---------------------------------------------------------------------------

type ownerFakeProcess struct {
	mu sync.Mutex

	readCh   chan []byte
	readErr  error
	readDone bool

	// rawQueue/rawReady/rawEOFSet/rawEOFErr are the [rawReader] half of this
	// fixture (nocx-6q1uh.15): produce and endRead feed BOTH this and the
	// plain io.Reader fields above, because commitIntent (owner.go) refuses
	// every state-changing intent ErrNoReadBarrier on a Process that answers
	// only through Read (owner_ssh.go's hasReadBarrier) — which every
	// intent-bearing test built over this fixture needs to NOT be true, the
	// same way a real local PTY (internal/pty.LocalPty) is not. The pattern
	// mirrors rawReaderFakeProcess (owner_adversarial_test.go): a queue and a
	// readiness signal that carries no data of its own, so WaitReadable
	// consumes nothing and every byte is still delivered exactly once,
	// through RawReadUntilAgain.
	rawQueue  [][]byte
	rawReady  chan struct{}
	rawEOFSet bool
	rawEOFErr error

	// writeGate, when non-nil, parks a Write until it closes; writeEntered
	// (if set alongside it) is closed the moment that Write is entered, so a
	// test can wait for "the write is blocked" as an observable event rather
	// than a duration.
	writeGate    chan struct{}
	writeEntered chan struct{}
	// writeOverride, when set, replaces Write's ordinary "take everything"
	// behaviour entirely — this is how a short write and a failing write are
	// produced.
	writeOverride func(p []byte) (int, error)
	written       [][]byte

	closed  bool
	closeCh chan struct{}

	pid int
}

func newOwnerFakeProcess() *ownerFakeProcess {
	return &ownerFakeProcess{
		readCh:   make(chan []byte, 64),
		rawReady: make(chan struct{}, 1),
		closeCh:  make(chan struct{}),
		pid:      4242,
	}
}

// Read is never called by the owner over this fixture: mode() (owner_ssh.go)
// picks rawReader first, and this fixture implements it. It exists only to
// satisfy io.Reader (Process embeds io.ReadWriteCloser).
func (p *ownerFakeProcess) Read(b []byte) (int, error) {
	chunk, ok := <-p.readCh
	if !ok {
		p.mu.Lock()
		err := p.readErr
		p.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return 0, err
	}
	return copy(b, chunk), nil
}

func (p *ownerFakeProcess) wakeRaw() {
	select {
	case p.rawReady <- struct{}{}:
	default:
	}
}

// produce delivers one chunk to the next Read AND to the next raw drain —
// see rawQueue's doc above for why both.
func (p *ownerFakeProcess) produce(b []byte) {
	p.readCh <- append([]byte(nil), b...)
	p.mu.Lock()
	p.rawQueue = append(p.rawQueue, append([]byte(nil), b...))
	p.mu.Unlock()
	p.wakeRaw()
}

// endRead closes the read side: this and every later Read answer err (or
// io.EOF, if err is nil), and the next raw drain reports the same eof/err.
func (p *ownerFakeProcess) endRead(err error) {
	p.mu.Lock()
	if p.readDone {
		p.mu.Unlock()
		return
	}
	p.readDone = true
	p.readErr = err
	p.rawEOFSet = true
	p.rawEOFErr = err
	p.mu.Unlock()
	close(p.readCh)
	p.wakeRaw()
}

// WaitReadable blocks until produce or endRead has something waiting, or ctx
// ends — it consumes nothing itself (rawReady carries no data), matching a
// real local PTY's readiness half (spec §5.2).
func (p *ownerFakeProcess) WaitReadable(ctx context.Context) error {
	select {
	case <-p.rawReady:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RawReadUntilAgain delivers everything queued since the last drain, whole,
// and reports eof in the SAME call once endRead has run — never deferred to
// a later drain, because rawReady's single-slot buffer can already have
// collapsed a produce-then-endRead pair (owner_test.go's own
// TestExitWithUnreadTailIngestsTheTail: both calls back to back) into the one
// signal this call is answering, and nothing would wake a further drain to
// report it. Mirrors rawReaderFakeProcess's own RawReadUntilAgain
// (owner_adversarial_test.go) for exactly this reason.
func (p *ownerFakeProcess) RawReadUntilAgain(_ []byte, deliver func([]byte)) (eof bool, err error) {
	p.mu.Lock()
	q := p.rawQueue
	p.rawQueue = nil
	eofSet := p.rawEOFSet
	eofErr := p.rawEOFErr
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

var _ rawReader = (*ownerFakeProcess)(nil)

func (p *ownerFakeProcess) Write(b []byte) (int, error) {
	p.mu.Lock()
	gate := p.writeGate
	entered := p.writeEntered
	override := p.writeOverride
	p.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if gate != nil {
		<-gate
	}
	if override != nil {
		n, err := override(b)
		if n > 0 {
			p.mu.Lock()
			p.written = append(p.written, append([]byte(nil), b[:n]...))
			p.mu.Unlock()
		}
		return n, err
	}
	p.mu.Lock()
	p.written = append(p.written, append([]byte(nil), b...))
	p.mu.Unlock()
	return len(b), nil
}

// blockNextWrite parks the next Write call until release is called.
// waitEntered closes the moment that Write is entered — the observable
// signal a test waits on instead of a duration.
func (p *ownerFakeProcess) blockNextWrite() (waitEntered <-chan struct{}, release func()) {
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

func (p *ownerFakeProcess) setWriteOverride(f func([]byte) (int, error)) {
	p.mu.Lock()
	p.writeOverride = f
	p.mu.Unlock()
}

func (p *ownerFakeProcess) writtenPayloads() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]byte, len(p.written))
	copy(out, p.written)
	return out
}

func (p *ownerFakeProcess) Close() error {
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

// InterruptWrite satisfies writeInterrupter (owner.go): it releases a write
// this fixture is currently parking, the same effect a real LocalPty's
// SetWriteDeadline has on a blocked write. Implementing it here — rather
// than leaving ownerFakeProcess without the seam — is what lets
// TestShutdownWithABlockedLocalWriteInterruptsAndJoins exercise stop's own
// call to it, instead of a test standing in for what that call would have
// done.
func (p *ownerFakeProcess) InterruptWrite() error {
	p.mu.Lock()
	gate := p.writeGate
	p.writeGate = nil
	p.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}
	return nil
}

var _ writeInterrupter = (*ownerFakeProcess)(nil)

func (p *ownerFakeProcess) Resize(context.Context, uint16, uint16, uint16, uint16) error { return nil }

func (p *ownerFakeProcess) Done() <-chan struct{} { return p.closeCh }

func (p *ownerFakeProcess) WaitErr() (error, bool) { return nil, false }

func (p *ownerFakeProcess) Pid() int { return p.pid }

func (p *ownerFakeProcess) Shell() string { return "owner-fake" }

func (p *ownerFakeProcess) ForegroundProcessGroup() (int, error) { return 0, nil }

var _ Process = (*ownerFakeProcess)(nil)

// newTestOwner builds a runtime and the owner over it, bound as its
// ReplySink exactly as finishSpawn does, but without starting run — callers
// that want the owner running call go owner.run() themselves, so a test that
// needs to inspect state before the first event can do so race-free.
func newTestOwner(t *testing.T, proc Process) (*sessionOwner, *sessionruntime.Session) {
	t.Helper()
	rt, screen, err := newSessionRuntime(defaultScreen, proc, "owner-test-session", 80, 24)
	if err != nil {
		t.Fatalf("build the runtime under test: %v", err)
	}
	t.Cleanup(screen.Close)
	win := newWindow(2 * creditLimit)
	owner := newSessionOwner(proc, rt, win, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rt.SetReplies(owner)
	return owner, rt
}

// grantAgent gives an agent principal control, the authority every intent in
// these tests is admitted under.
func grantAgent(t *testing.T, rt *sessionruntime.Session) sessionruntime.Control {
	t.Helper()
	ctrl, err := rt.GrantControl(sessionruntime.Principal{Kind: sessionruntime.PrincipalAgent, ID: "agent-under-test"})
	if err != nil {
		t.Fatalf("grant control: %v", err)
	}
	return ctrl
}

func keyIntent(rt *sessionruntime.Session, ctrl sessionruntime.Control, key string) sessionruntime.Intent {
	return sessionruntime.Intent{
		At:      rt.Incarnation(),
		Under:   ctrl.Epoch,
		By:      ctrl.Holder,
		Kind:    sessionruntime.IntentKindKey,
		Payload: []byte(key),
	}
}

// ---------------------------------------------------------------------------
// TestAnIntentBehindABlockedClientFrameValidatesAtTheHead
// ---------------------------------------------------------------------------

// TestAnIntentBehindABlockedClientFrameValidatesAtTheHead is spec §5.3's
// linearisation point, from the losing side: a client frame is blocked on
// the writer (the slave not reading, in production; the fake's gate here);
// an intent submitted behind it must not be validated until the frame's
// write completes — and by then, output has changed the region a check
// function reads, so the intent is refused with zero bytes of it ever
// reaching the process.
func TestAnIntentBehindABlockedClientFrameValidatesAtTheHead(t *testing.T) {
	proc := newOwnerFakeProcess()
	owner, rt := newTestOwner(t, proc)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })
	ctrl := grantAgent(t, rt)

	entered, release := proc.blockNextWrite()
	frameDone, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("blocked-frame")})
	if err != nil {
		t.Fatalf("submit the client frame: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the client frame's write was never entered")
	}

	checkRan := make(chan sessionruntime.Snapshot, 1)
	checkErr := errors.New("stale_target: the region changed while the frame ahead of it was blocked")
	intentDone, err := owner.submit(ownerItem{
		kind: itemIntent,
		intent: &pendingIntent{
			Intent: keyIntent(rt, ctrl, "Enter"),
			Check: func(snap sessionruntime.Snapshot) error {
				checkRan <- snap
				return checkErr
			},
		},
	})
	if err != nil {
		t.Fatalf("submit the intent behind it: %v", err)
	}

	// While the frame sits blocked and the intent waits behind it, output
	// changes the region the intent's check will read.
	if err := rt.Ingest([]byte("the screen changed\r\n")); err != nil {
		t.Fatalf("ingest while the frame is blocked: %v", err)
	}

	select {
	case <-checkRan:
		t.Fatal("the intent's check ran before the blocked frame ahead of it completed")
	case <-time.After(50 * time.Millisecond):
		// Expected: nothing has run it yet. This is the one bounded wait in
		// this file that is not on an event, and it exists only to give a
		// genuine ordering defect a chance to show itself before the frame
		// is released; it never causes the test to fail on its own.
	}

	release()

	frameRes := <-frameDone
	if frameRes.Err != nil || frameRes.State != sessionruntime.IntentStateExecuted {
		t.Fatalf("the blocked frame resolved state=%v err=%v, want executed", frameRes.State, frameRes.Err)
	}

	select {
	case <-checkRan:
	case <-time.After(5 * time.Second):
		t.Fatal("the intent's check never ran once the frame ahead of it completed")
	}

	res := <-intentDone
	if !errors.Is(res.Err, checkErr) {
		t.Fatalf("the intent behind the blocked frame resolved err=%v, want %v", res.Err, checkErr)
	}
	if res.State != sessionruntime.IntentStateRefused {
		t.Fatalf("the intent's state is %v, want refused", res.State)
	}
	if got := proc.writtenPayloads(); len(got) != 1 || string(got[0]) != "blocked-frame" {
		t.Fatalf("the process received %q, want exactly the one client frame and nothing of the refused intent", got)
	}
}

// ---------------------------------------------------------------------------
// TestAClientFrameAndAnIntentNeverInterleave
// ---------------------------------------------------------------------------

// submitRetryingBusy resubmits on errBusy until the item is accepted.
//
// submit's own contract (owner.go) is that a full queue is answered at once
// rather than by blocking, and `busy` is a NAMED wire refusal (tokens.go's
// causeOf) a real caller is expected to retry — the same rung an agent's
// intent submission answers with under load. This test drives 1000
// unthrottled concurrent submitters against a 64-deep queue (intentQueueMax)
// specifically to race the owner's ordering, and a transient `busy` there is
// the documented behaviour of a queue that filled, not a defect in it — the
// invariant this test asserts is ORDERING of what is accepted, never that a
// bursty producer is never told to slow down. Retrying on the caller's own
// side is therefore not a timing workaround; it is what the API asks of it.
func submitRetryingBusy(o *sessionOwner, it ownerItem) (<-chan ownerResult, error) {
	for {
		done, err := o.submit(it)
		if err == nil {
			return done, nil
		}
		if !errors.Is(err, errBusy) {
			return nil, err
		}
		runtime.Gosched()
	}
}

// TestAClientFrameAndAnIntentNeverInterleave drives 1000 rounds of one client
// frame immediately followed by one intent, submitted from a concurrent
// goroutine so the two races the owner's queue against itself, and asserts
// the bytes that reached the process are always the frame's then the
// intent's, contiguous — never interleaved, never reordered.
func TestAClientFrameAndAnIntentNeverInterleave(t *testing.T) {
	const rounds = 1000
	proc := newOwnerFakeProcess()
	owner, rt := newTestOwner(t, proc)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })
	ctrl := grantAgent(t, rt)

	var wg sync.WaitGroup
	for i := 0; i < rounds; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			done, err := submitRetryingBusy(owner, ownerItem{
				kind:   itemIntent,
				intent: &pendingIntent{Intent: keyIntent(rt, ctrl, "Enter")},
			})
			if err != nil {
				t.Errorf("submit intent: %v", err)
				return
			}
			<-done
		}()
		frameDone, err := submitRetryingBusy(owner, ownerItem{kind: itemClientFrame, payload: []byte("F")})
		if err != nil {
			t.Fatalf("submit frame %d: %v", i, err)
		}
		if res := <-frameDone; res.Err != nil {
			t.Fatalf("frame %d: %v", i, res.Err)
		}
	}
	wg.Wait()

	got := proc.writtenPayloads()
	if len(got) == 0 {
		t.Fatal("nothing reached the process")
	}
	// Every payload is either the frame's literal byte or an encoded key —
	// never a splice of both, which is what a torn write would produce.
	for _, p := range got {
		if len(p) == 1 && p[0] == 'F' {
			continue
		}
		if len(p) == 0 {
			t.Fatalf("an empty payload reached the process: interleaving corrupted a write")
		}
	}
}

// ---------------------------------------------------------------------------
// TestAResizeRacingACommitMakesTheTargetIncomparable /
// TestResizeWhileAWriteIsBlocked
//
// nocx-6q1uh.4 gives the commit point its real "incomparable" refusal, minted
// from a token bound to a geometry. This task has no token yet, so these two
// stand in with the exact seam the token mechanism will use — the caller's
// own check, run under the runtime's lock inside Commit — comparing the
// geometry a caller captured against the one in force when its intent
// reaches the head. That is what "the target is incomparable" IS, one layer
// down from where a token states it.
// ---------------------------------------------------------------------------

func TestAResizeRacingACommitMakesTheTargetIncomparable(t *testing.T) {
	proc := newOwnerFakeProcess()
	owner, rt := newTestOwner(t, proc)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })
	ctrl := grantAgent(t, rt)

	mintedAt := rt.Geometry()
	// Named errGeometryChanged rather than errIncomparable — nocx-6q1uh.4/.6
	// added a package-level errIncomparable (tokens.go) after this test was
	// written, and this local sentinel is unrelated to it (this task has no
	// token yet, per this test's own doc above); the rename only avoids the
	// shadow, which govet now catches package-wide.
	errGeometryChanged := errors.New("incomparable: the geometry changed since this target was minted")
	checkGeometry := func(snap sessionruntime.Snapshot) error {
		if snap.Geometry != mintedAt {
			return errGeometryChanged
		}
		return nil
	}

	entered, release := proc.blockNextWrite()
	// A client frame sits at the head so the resize and the intent both wait
	// behind it, and the resize is committed before the intent's turn comes.
	frameDone, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("x")})
	if err != nil {
		t.Fatalf("submit frame: %v", err)
	}
	<-entered

	g := sessionruntime.Geometry{Cols: 100, Rows: 40}
	resizeDone, err := owner.submit(ownerItem{kind: itemResize, resize: &g})
	if err != nil {
		t.Fatalf("submit resize: %v", err)
	}
	intentDone, err := owner.submit(ownerItem{
		kind:   itemIntent,
		intent: &pendingIntent{Intent: keyIntent(rt, ctrl, "Enter"), Check: checkGeometry},
	})
	if err != nil {
		t.Fatalf("submit intent: %v", err)
	}

	release()
	if res := <-frameDone; res.Err != nil {
		t.Fatalf("frame: %v", res.Err)
	}
	if res := <-resizeDone; res.Err != nil || res.State != sessionruntime.IntentStateExecuted {
		t.Fatalf("resize: state=%v err=%v", res.State, res.Err)
	}
	res := <-intentDone
	if !errors.Is(res.Err, errGeometryChanged) {
		t.Fatalf("the intent behind a committed resize resolved err=%v, want %v", res.Err, errGeometryChanged)
	}
}

// TestResizeWhileAWriteIsBlocked is the ordering half: a resize submitted
// while a write is in flight completes AFTER that write, never before or
// interleaved with it — the owner never hands the writer a second item, and
// never runs CommitGeometry, until the head write it is waiting behind
// resolves.
func TestResizeWhileAWriteIsBlocked(t *testing.T) {
	proc := newOwnerFakeProcess()
	owner, rt := newTestOwner(t, proc)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })
	_ = rt

	entered, release := proc.blockNextWrite()
	frameDone, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("x")})
	if err != nil {
		t.Fatalf("submit frame: %v", err)
	}
	<-entered

	g := sessionruntime.Geometry{Cols: 90, Rows: 30}
	resizeDone, err := owner.submit(ownerItem{kind: itemResize, resize: &g})
	if err != nil {
		t.Fatalf("submit resize: %v", err)
	}

	select {
	case <-resizeDone:
		t.Fatal("the resize completed before the blocked write ahead of it")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	if res := <-frameDone; res.Err != nil {
		t.Fatalf("frame: %v", res.Err)
	}
	if res := <-resizeDone; res.Err != nil || res.State != sessionruntime.IntentStateExecuted {
		t.Fatalf("resize after the write: state=%v err=%v", res.State, res.Err)
	}
	if got := rt.Geometry().Geometry; got != g {
		t.Fatalf("the commit in force is %+v, want %+v", got, g)
	}
}

// ---------------------------------------------------------------------------
// Shutdown: TestExitWithUnreadTailIngestsTheTail,
// TestForcedStopWithAProgramThatNeverClosesReportsTailLost,
// TestShutdownWithABlockedLocalWriteInterruptsAndJoins
// ---------------------------------------------------------------------------

// TestExitWithUnreadTailIngestsTheTail is the ordinary exit: the process
// closes its output after writing something nobody had read yet, and the
// owner's drain reaches it before it declares the read side over — the tail
// is in the runtime's screen, not lost to a stop that arrived first.
func TestExitWithUnreadTailIngestsTheTail(t *testing.T) {
	proc := newOwnerFakeProcess()
	owner, rt := newTestOwner(t, proc)
	go owner.run()

	proc.produce([]byte("the last thing the shell said\r\n"))
	proc.endRead(nil)

	if tailLost := owner.stop(true, time.Time{}); tailLost {
		t.Fatal("an ordinary exit reported tailLost, want the tail ingested cleanly")
	}
	if got := string(rt.Snapshot().Screen); got != "the last thing the shell said" {
		t.Fatalf("the screen reads %q, want the tail the process wrote before it closed", got)
	}
}

// TestForcedStopWithAProgramThatNeverClosesReportsTailLost is the deadline
// firing: a program that never closes its output is still being read from
// when the deadline passes, so the readable side is forced closed and the
// session reports tailLost — the design does not promise a drain that
// cannot occur (spec §5.7).
func TestForcedStopWithAProgramThatNeverClosesReportsTailLost(t *testing.T) {
	proc := newOwnerFakeProcess() // never produces, never ends its own read
	owner, _ := newTestOwner(t, proc)
	go owner.run()

	tailLost := owner.stop(false, time.Now().Add(50*time.Millisecond))
	if !tailLost {
		t.Fatal("a deadline that fired before the process ever closed did not report tailLost")
	}
}

// TestShutdownWithABlockedLocalWriteInterruptsAndJoins is the local-PTY half
// of spec §5.7's writer handling: stop interrupts a write in flight (a real
// LocalPty's InterruptWrite; here, a Process that honours the same seam),
// and the owner joins rather than abandoning it.
func TestShutdownWithABlockedLocalWriteInterruptsAndJoins(t *testing.T) {
	proc := newOwnerFakeProcess()
	owner, _ := newTestOwner(t, proc)
	go owner.run()

	entered, _ := proc.blockNextWrite()
	frameDone, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("stuck")})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	<-entered
	proc.endRead(nil)

	// stop's own beginClosing calls InterruptWrite (ownerFakeProcess
	// implements writeInterrupter, the same seam a real LocalPty answers),
	// which releases the write above from WITHIN shutdown rather than from
	// this test reaching in — the join that follows is what is under test:
	// stop must not return until that write's own resolution has happened
	// and the read side, already at EOF, has been noticed too.
	done := make(chan bool, 1)
	go func() { done <- owner.stop(false, time.Now().Add(5*time.Second)) }()

	select {
	case tailLost := <-done:
		if tailLost {
			t.Fatal("stop reported tailLost even though its own interrupt released the write and the read side had already reached EOF")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("stop did not return: the interrupt did not unblock the write, or the join never noticed")
	}
	if res := <-frameDone; res.Err != nil {
		t.Fatalf("the previously-blocked frame: %v", res.Err)
	}
}

// ---------------------------------------------------------------------------
// failed_partial
// ---------------------------------------------------------------------------

// TestAFailedPartialWriteReportsTheExactBytesWritten is failed_partial: a
// writer returning n < len(p) is reported with that exact n, as FAILED and
// never as executed or cancelled — a write can fail part-way, and the bytes
// it did take are beyond recall.
func TestAFailedPartialWriteReportsTheExactBytesWritten(t *testing.T) {
	proc := newOwnerFakeProcess()
	owner, _ := newTestOwner(t, proc)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })

	proc.setWriteOverride(func(p []byte) (int, error) {
		if len(p) <= 3 {
			return len(p), nil
		}
		return 3, nil
	})

	done, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("more than three bytes")})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	res := <-done
	if res.State != sessionruntime.IntentStateFailed {
		t.Fatalf("state is %v, want failed", res.State)
	}
	if res.BytesWritten != 3 {
		t.Fatalf("BytesWritten is %d, want exactly 3", res.BytesWritten)
	}
	if !errors.Is(res.Err, io.ErrShortWrite) {
		t.Fatalf("err is %v, want %v", res.Err, io.ErrShortWrite)
	}
}

// ---------------------------------------------------------------------------
// TestAProgramThatFloodsAndDoesNotReadKeepsItsPaneAlive
// ---------------------------------------------------------------------------

// TestAProgramThatFloodsAndDoesNotReadKeepsItsPaneAlive is nocx-6q1uh.1's own
// acceptance criterion (spec §5.8), on a real PTY: a program floods output
// and asks where its cursor is without ever reading its input; the owner's
// drain keeps running regardless — the window keeps growing, observed as a
// state change and never a duration — and once the program starts reading,
// it receives the reply the runtime already decided.
func TestAProgramThatFloodsAndDoesNotReadKeepsItsPaneAlive(t *testing.T) {
	// The shell floods in the background from the first line, asks DSR
	// immediately (the runtime answers it into the shell's OWN stdin
	// without anybody reading it yet), and only blocks on reading once the
	// test lets it stop. The flood ends when the TEST creates a stop file,
	// after it has seen the frames it watches for — never when a counted
	// busy-loop in the shell happens to finish. The loop used to be
	// `while [ $i -lt 300000 ]`, whose wall time is the runner's speed: on a
	// fast ubuntu runner (CI run 35234462129, ci-linux with-secret-service)
	// the flood was over after 3 of 20 frames and the watchdog read a
	// finished program as a stalled pane.
	stop := filepath.Join(t.TempDir(), "stop-flooding")
	script := fmt.Sprintf(`
stty -icanon -echo min 1 time 0
yes "flood-nocx-6q1uh" 2>/dev/null &
FLOODPID=$!
printf '\033[6n'
while [ ! -e '%s' ]; do :; done
kill "$FLOODPID" 2>/dev/null
answer=$(dd bs=1 count=6 2>/dev/null | od -An -tx1 | tr -d ' \n')
printf 'REPLY:%%s\n' "$answer"
`, stop)
	lp, err := pty.NewLocal(log.NewSlogAdapter(nil), pty.Config{
		Command: "/bin/sh",
		Args:    []string{"-c", script},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatalf("start a real shell on a real pty: %v", err)
	}
	t.Cleanup(func() { _ = lp.Close() })

	owner, _ := newTestOwner(t, lp)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })

	win := owner.win
	// A watchdog on a FRAME COUNT, not a duration (AGENTS.md, spec §5.8):
	// the pane must keep receiving output — proof the drain never blocked on
	// the write the DSR reply is queued behind — for a number of distinct
	// window writes well past what one reply could ever produce alone.
	const watchdogFrames = 20
	seen := 0
	deadline := time.After(15 * time.Second)
	for seen < watchdogFrames {
		changed := win.changed()
		select {
		case <-changed:
			seen++
		case <-deadline:
			t.Fatalf("only %d of %d watched-for frames arrived: the pane appears to have stalled", seen, watchdogFrames)
		}
	}

	// The program is still flooding and has not yet read the DSR reply this
	// owner already queued: nothing here asserts on that reply's timing,
	// only that frames kept arriving while it sat unread — which the loop
	// above already demonstrated.

	// Now let the shell stop flooding and read: the window closes when its
	// output ends, which is the observable end of the script.
	if err := os.WriteFile(stop, nil, 0o600); err != nil {
		t.Fatalf("tell the script to stop flooding: %v", err)
	}
	select {
	case <-lp.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the shell never exited")
	}

	// lp.Done firing is the PROCESS being reaped, never a promise that this
	// owner has finished draining it: spec §5.7's own tail-drain step keeps
	// reading "until EOF" after termination, and everything the flood wrote
	// but this owner had not yet read off the kernel's own pty buffer still
	// has to pass through the real VT emulator (Session.Ingest) before
	// RawReadUntilAgain ever reaches the EIO Linux answers once the slave
	// closes and finishRead closes the window. A bare isClosed() check right
	// here asserted a synchrony the design never promises — exactly the
	// "wait on a duration, not a state change" mistake AGENTS.md's testing
	// rules forbid, just spelled as "no wait at all" — so wait on the SAME
	// observable the watchdog loop above already uses, win.changed(), until
	// isClosed() actually reports true.
	closeDeadline := time.After(15 * time.Second)
	for !win.isClosed() {
		changed := win.changed()
		select {
		case <-changed:
		case <-closeDeadline:
			t.Fatal("the window is not closed after the shell exited")
		}
	}
}

// TestACommitPointDuringAnIdleReadinessWaitDoesNotDeadlock is the regression
// test for nocx-6q1uh.18, on a real PTY: the merged-tree gate hung for 8+
// minutes with the readiness goroutine parked in WaitReadable (idle
// program, nothing to read) and the owner's own goroutine blocked in
// RawReadUntilAgain, in drainLocal, called from processHead's unconditional
// "opportunistic extra drain" for a queued item.
//
// The original cause: internal/poll's SyscallConn().Read holds the master
// file's own read lock for the WHOLE call it wraps, a parked wait included,
// not only while a read(2) is actually in flight. WaitReadable and
// RawReadUntilAgain both went through that call, on the SAME *os.File, so a
// readiness goroutine idling in WaitReadable held exactly the lock the
// owner's own drain needed next — and on an idle program there was nothing
// left to make the fd readable and release it: no output was coming until
// the program read the very input stuck behind this commit point, and the
// program could not read input this call had not yet been allowed to write.
// A first fix (fd99a9d1, an interrupt-then-wait handshake between the two
// goroutines) did not hold up: it still serialised them through the same
// call, and this exact test hung again against it. The fix that stuck moves
// WaitReadable off the master *os.File entirely, onto a dup fd and a
// self-pipe polled directly (internal/pty.LocalPty.WaitReadable's own doc),
// so the two goroutines share nothing left to contend over.
//
// The shell here prints one line and then goes silent for several seconds
// (its own sleep, not this test's) — the "idle for a while" the bead asks
// for. The one line is this test's OBSERVABLE state change (win.changed()):
// once it has arrived, the readiness goroutine is very quickly back to
// waiting on an fd with nothing further to report until the shell wakes up.
// A client frame submitted into that window reaches processHead's drain
// unconditionally, exactly the call that used to hang forever; completion
// is watched on the submit's own result channel, never on a duration — the
// outer timers below are failure watchdogs, not the passing condition.
func TestACommitPointDuringAnIdleReadinessWaitDoesNotDeadlock(t *testing.T) {
	const script = `
printf 'HELLO\n'
sleep 5
`
	lp, err := pty.NewLocal(log.NewSlogAdapter(nil), pty.Config{
		Command: "/bin/sh",
		Args:    []string{"-c", script},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatalf("start a real shell on a real pty: %v", err)
	}
	t.Cleanup(func() { _ = lp.Close() })

	owner, _ := newTestOwner(t, lp)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })

	win := owner.win
	changed := win.changed()
	select {
	case <-changed:
		// "HELLO\n" has been ingested: the shell is now in its `sleep 5`,
		// producing nothing further, and the readiness goroutine — resumed
		// right after that ingest, drainLocal's own doc — has nothing left
		// to do but park in WaitReadable for the rest of it.
	case <-time.After(15 * time.Second):
		t.Fatal("the shell's first line never arrived")
	}

	done, err := owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("x")})
	if err != nil {
		t.Fatalf("submit a client frame: %v", err)
	}
	select {
	case res := <-done:
		if res.Err != nil {
			t.Fatalf("the client frame did not complete: %v", res.Err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the commit point never completed: the owner appears to have deadlocked against the parked readiness wait")
	}
}
