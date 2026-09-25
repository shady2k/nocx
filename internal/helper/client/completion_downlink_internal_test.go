package client

// The downlink's own bookkeeping, tested against a send spy: the
// spawn-answer race (an accepted completion can predate the helper session's
// identity), acceptance order, and what a failed send settles into are THIS
// type's behaviour, and no wire is needed to judge them.
//
// WHAT PRODUCTION CAN REACH, stated because the review asked. The pre-bind
// window the buffering tests exercise — a completion accepted before the
// bind names the session — is one production CANNOT CURRENTLY REACH: on a
// fresh open the bind runs inside hostedSpawn.run the moment the spawn
// answers, while the bridge that can carry an Accept starts only after the
// open returns (the transport's StartLifecycle); on a re-adoption the
// identity is known before the downlink is built, so it binds at
// construction. The tests stay because the queue is the carrier's own
// contract — bounded, ordered, exactly-once — and the wedge-detector for the
// window the type was built for.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
)

// spySend records every completion that LANDED, in order, and answers each
// attempt with what fail says for it (nil lands it).
type spySend struct {
	mu       sync.Mutex
	attempts int
	sessions []proto.HostSessionID
	fence    [][32]byte
	exits    []*int
	fail     func(attempt int, fence [32]byte) error
	// landed is signalled once per completion that landed, so a test waits
	// on the delivery itself rather than on a clock.
	landed chan struct{}
}

func newSpy() *spySend { return &spySend{landed: make(chan struct{}, 256)} }

func (s *spySend) send(_ context.Context, params proto.LifecycleCompleteParams) error {
	var fence [32]byte
	raw, err := hex.DecodeString(params.Nonce)
	if err != nil || len(raw) != 32 {
		panic("spy: a downlink sent a nonce that is not 64 hex characters: " + params.Nonce)
	}
	copy(fence[:], raw)
	s.mu.Lock()
	s.attempts++
	attempt := s.attempts
	fail := s.fail
	s.mu.Unlock()
	if fail != nil {
		if err := fail(attempt, fence); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.sessions = append(s.sessions, params.Session)
	s.fence = append(s.fence, fence)
	s.exits = append(s.exits, params.ExitCode)
	s.mu.Unlock()
	s.landed <- struct{}{}
	return nil
}

func (s *spySend) delivered() [][32]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][32]byte(nil), s.fence...)
}

// newTestDownlink builds the downlink over the spy with a logger private to
// the test, and retries without a clock: the pause before a retry is the
// session's context alone.
func newTestDownlink(t *testing.T, spy *spySend) (*CompletionDownlink, *slog.Logger) {
	t.Helper()
	sl := logtest.Slog(t)
	ctx := log.WithLogger(context.Background(), log.NewSlogAdapter(sl))
	return newTestDownlinkCtx(ctx, spy), sl
}

func newTestDownlinkCtx(ctx context.Context, spy *spySend) *CompletionDownlink {
	d := newCompletionDownlink(ctx, spy.send, func(context.Context, proto.LifecycleEnteredParams) error { return nil })
	d.retryWait = func(ctx context.Context, _ int) error { return ctx.Err() }
	return d
}

// accept is one completion the kernel accepted, through the downlink's own
// acceptance seam.
func accept(t *testing.T, d *CompletionDownlink, fence [32]byte, exit *int) {
	t.Helper()
	if err := d.Accept(func() error { return nil }, &lifecycle.Complete{Fence: lifecycle.FenceNonce(fence), ExitCode: exit}); err != nil {
		t.Fatalf("accept: %v", err)
	}
}

func awaitLanded(t *testing.T, spy *spySend, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-spy.landed:
		case <-time.After(5 * time.Second):
			t.Fatalf("%d of %d completions landed", i, n)
		}
	}
}

// lostRecords are the lines that say an accepted fact was lost.
func lostRecords(sl *slog.Logger) []logtest.Record {
	var out []logtest.Record
	for _, r := range logtest.RecordsSlog(sl) {
		if strings.Contains(r.Message, "did not reach the helper session") {
			out = append(out, r)
		}
	}
	return out
}

// errOf is the error a record carries, AS the error it was logged as — the
// cause chain is what is judged, not its text.
func errOf(t *testing.T, r logtest.Record) error {
	t.Helper()
	for _, a := range r.Attrs {
		if a.Key == "err" {
			if err, ok := a.Value.Any().(error); ok {
				return err
			}
			t.Fatalf("the record's err is %T, want the error itself so its cause chain survives", a.Value.Any())
		}
	}
	t.Fatalf("the record %q carries no err", r.Message)
	return nil
}

// TestCompletionsAcceptedBeforeBindWaitForTheBindThenDeliverInOrder is the
// spawn-answer race: the adapter exists before the spawn RPC answers, so a
// completion the kernel accepts in that window arrives with no addressee.
// It waits in the queue, and the bind delivers what waited in the order the
// kernel accepted it — exactly once, never re-delivered by a second bind.
func TestCompletionsAcceptedBeforeBindWaitForTheBindThenDeliverInOrder(t *testing.T) {
	spy := newSpy()
	dl, sl := newTestDownlink(t, spy)

	first, second, third := [32]byte{1}, [32]byte{2}, [32]byte{3}
	accept(t, dl, first, nil)
	accept(t, dl, second, nil)
	if got := len(spy.delivered()); got != 0 {
		t.Fatalf("%d completions sent before the bind named a session, want 0 queued until then", got)
	}

	entry := HostSessionID{Generation: "gen-under-test", Session: "0123456789abcdef0123456789abcdef"}
	dl.Bind(entry)
	awaitLanded(t, spy, 2)
	// A second Bind is the open path's idempotence: nothing re-delivered,
	// and what comes after it is delivered once, behind what came before.
	dl.Bind(entry)
	accept(t, dl, third, nil)
	awaitLanded(t, spy, 1)

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.fence) != 3 || spy.fence[0] != first || spy.fence[1] != second || spy.fence[2] != third {
		t.Fatalf("delivered %v, want the two queued completions and then the third, each once, in acceptance order", spy.fence)
	}
	for i, got := range spy.sessions {
		if got.Generation != "gen-under-test" || got.Session != entry.Session {
			t.Fatalf("completion %d delivered to %+v, want the bound session", i, got)
		}
	}
	if lost := lostRecords(sl); len(lost) != 0 {
		t.Fatalf("the happy path logged a loss: %v", lost)
	}
}

// TestAnUnboundDownlinkBuffersOnlyWhatIsBounded keeps the pre-bind queue
// from becoming the coordinator's unbounded memory: a completion that
// arrives past the bound is dropped and LOGGED with its cause, because a
// kernel-accepted completion disappearing silently is the exact degrade this
// carrier exists to refuse.
func TestAnUnboundDownlinkBuffersOnlyWhatIsBounded(t *testing.T) {
	spy := newSpy()
	dl, sl := newTestDownlink(t, spy)

	for i := 0; i < maxPendingCompletions; i++ {
		accept(t, dl, [32]byte{byte(i)}, nil)
	}
	if lost := lostRecords(sl); len(lost) != 0 {
		t.Fatalf("the bound logged %v before the bound was exceeded", lost)
	}
	accept(t, dl, [32]byte{0xFF}, nil)
	lost := lostRecords(sl)
	if len(lost) != 1 || !errors.Is(errOf(t, lost[0]), errNeverBound) {
		t.Fatalf("the completion past the bound was not logged with its cause: %v", lost)
	}
	dl.Bind(HostSessionID{Session: "0123456789abcdef0123456789abcdef"})
	awaitLanded(t, spy, maxPendingCompletions)
}

// TestAnOrdinarySendLandsOnceAndLogsNothing is the paired ordinary half of
// the failure tests below: one attempt, one landing, and no line about a
// retry or a loss.
func TestAnOrdinarySendLandsOnceAndLogsNothing(t *testing.T) {
	spy := newSpy()
	dl, sl := newTestDownlink(t, spy)
	dl.Bind(HostSessionID{Session: "0123456789abcdef0123456789abcdef"})

	code := 3
	accept(t, dl, [32]byte{7}, &code)
	awaitLanded(t, spy, 1)

	spy.mu.Lock()
	attempts, exit := spy.attempts, spy.exits[0]
	spy.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("an ordinary send took %d attempts, want 1", attempts)
	}
	if exit == nil || *exit != 3 {
		t.Fatalf("the completion carried exit %v, want 3", exit)
	}
	for _, r := range logtest.RecordsSlog(sl) {
		if r.Level >= slog.LevelWarn {
			t.Fatalf("an ordinary send logged %q at %v", r.Message, r.Level)
		}
	}
}

// TestAFailedSendIsRetriedAndTheBoundaryStillLands is the failure half: the
// first attempts fail the way a helper that does not answer inside one
// delivery's bound fails them, and the boundary still reaches the runtime —
// the head keeps its place, so what was accepted after it lands after it —
// with every failure logged with its cause intact.
func TestAFailedSendIsRetriedAndTheBoundaryStillLands(t *testing.T) {
	spy := newSpy()
	spy.fail = func(attempt int, _ [32]byte) error {
		if attempt <= 2 {
			return fmt.Errorf("one attempt's bound passed: %w", context.DeadlineExceeded)
		}
		return nil
	}
	dl, sl := newTestDownlink(t, spy)
	dl.Bind(HostSessionID{Session: "0123456789abcdef0123456789abcdef"})

	first, second := [32]byte{1}, [32]byte{2}
	accept(t, dl, first, nil)
	accept(t, dl, second, nil)
	awaitLanded(t, spy, 2)

	if got := spy.delivered(); got[0] != first || got[1] != second {
		t.Fatalf("delivered %v, want the retried head first and the one behind it second", got)
	}
	if lost := lostRecords(sl); len(lost) != 0 {
		t.Fatalf("a boundary that landed on a retry was logged as lost: %v", lost)
	}
	var retries int
	for _, r := range logtest.RecordsSlog(sl) {
		if strings.Contains(r.Message, "retrying") {
			retries++
			if !errors.Is(errOf(t, r), context.DeadlineExceeded) {
				t.Fatalf("the retry line lost the send's cause: %v", errOf(t, r))
			}
		}
	}
	if retries != 2 {
		t.Fatalf("%d failed attempts logged, want the 2 that failed", retries)
	}
}

// TestARefusedSendIsFinalAndTheQueueMovesOn: the helper's own refusal (it
// has no such session) cannot change by asking again, so it is logged as
// lost with the refusal as its cause, and what was accepted behind it is
// still delivered. A lost connection is final the same way: this carrier
// never comes back from one.
func TestARefusedSendIsFinalAndTheQueueMovesOn(t *testing.T) {
	for name, final := range map[string]error{
		"refused": &RefusalError{Code: "no_such_session", Message: "no such session"},
		"lost":    fmt.Errorf("%w: eof", ErrLost),
	} {
		t.Run(name, func(t *testing.T) {
			refused, next := [32]byte{1}, [32]byte{2}
			spy := newSpy()
			spy.fail = func(_ int, fence [32]byte) error {
				if fence == refused {
					return final
				}
				return nil
			}
			dl, sl := newTestDownlink(t, spy)
			dl.Bind(HostSessionID{Session: "0123456789abcdef0123456789abcdef"})

			accept(t, dl, refused, nil)
			accept(t, dl, next, nil)
			awaitLanded(t, spy, 1)

			if got := spy.delivered(); len(got) != 1 || got[0] != next {
				t.Fatalf("delivered %v, want only the completion behind the final failure", got)
			}
			spy.mu.Lock()
			attempts := spy.attempts
			spy.mu.Unlock()
			if attempts != 2 {
				t.Fatalf("%d attempts, want one for each: a final answer is not asked again", attempts)
			}
			lost := lostRecords(sl)
			if len(lost) != 1 || !errors.Is(errOf(t, lost[0]), final) {
				t.Fatalf("the final failure was not logged with its cause: %v", lost)
			}
		})
	}
}

// TestTheSessionEndingWhileRetryingSettlesTheBoundaryAsLost is the interval's
// other end: a delivery that keeps failing is retried until the pane's
// session ends, and then — not before — it is logged lost, with both the
// send's failure and the session's end in its cause.
func TestTheSessionEndingWhileRetryingSettlesTheBoundaryAsLost(t *testing.T) {
	sl := logtest.Slog(t)
	ctx, stop := context.WithCancel(log.WithLogger(context.Background(), log.NewSlogAdapter(sl)))
	defer stop()
	sendErr := errors.New("helper did not answer")
	spy := newSpy()
	spy.fail = func(int, [32]byte) error { return sendErr }
	dl := newTestDownlinkCtx(ctx, spy)
	waiting := make(chan struct{})
	var once sync.Once
	dl.retryWait = func(ctx context.Context, _ int) error {
		once.Do(func() { close(waiting) })
		<-ctx.Done()
		return ctx.Err()
	}
	dl.Bind(HostSessionID{Session: "0123456789abcdef0123456789abcdef"})
	accept(t, dl, [32]byte{1}, nil)

	<-waiting
	if lost := lostRecords(sl); len(lost) != 0 {
		t.Fatalf("a boundary was given up on while the session still lived: %v", lost)
	}
	stop()

	if !logtest.WaitForSlog(sl, 5*time.Second, func(r logtest.Record) bool {
		return strings.Contains(r.Message, "did not reach the helper session")
	}) {
		t.Fatal("the session ended with a delivery still failing, and nothing logged it lost")
	}
	err := errOf(t, lostRecords(sl)[0])
	if !errors.Is(err, sendErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("the loss's cause is %v, want both the send's failure and the session's end", err)
	}
}

// TestTheAcceptanceLockCoversTheKernelsAcceptance pins where the order is
// fixed, the exact way the defect failed it: while the kernel is accepting
// an envelope, no other source may be between its own acceptance and its
// enqueue. TryLock is the deterministic probe; under a wrapper that ran the
// kernel outside the lock it succeeds.
func TestTheAcceptanceLockCoversTheKernelsAcceptance(t *testing.T) {
	spy := newSpy()
	dl, _ := newTestDownlink(t, spy)
	err := dl.Accept(func() error {
		if dl.accept.TryLock() {
			dl.accept.Unlock()
			t.Error("the kernel accepted with the acceptance lock free: a second source's completion could be queued ahead of this one")
		}
		return nil
	}, &lifecycle.Complete{Fence: lifecycle.FenceNonce{1}})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
}

// TestARefusedIngestQueuesNothing: the kernel's refusal is returned as it
// was, and nothing is queued — the observer is not a second gate, and it is
// not a way around the first one either.
func TestARefusedIngestQueuesNothing(t *testing.T) {
	spy := newSpy()
	dl, _ := newTestDownlink(t, spy)
	refusal := errors.New("kernel: wrong capability")
	if err := dl.Accept(func() error { return refusal }, &lifecycle.Complete{Fence: lifecycle.FenceNonce{1}}); !errors.Is(err, refusal) {
		t.Fatalf("Accept answered %v, want the kernel's own refusal", err)
	}
	dl.mu.Lock()
	queued := len(dl.pending)
	dl.mu.Unlock()
	if queued != 0 {
		t.Fatalf("%d queued for a refused ingest, want none", queued)
	}
}

// TestTheDownlinkDeliversUnderItsOwnContext pins the delivery SEAM the
// lifetime fix hangs the downlink off: every attempt runs under a child of
// the context the downlink was built with — the hosted session's lifetime —
// bounded by its own deadline, so the session's end reaches the context the
// send is holding. A deliver that built a fresh or detached context fails
// here, and no clock is involved: cancel propagates to children before it
// returns.
func TestTheDownlinkDeliversUnderItsOwnContext(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	// The context is inspected while the send still HOLDS it: a delivery's
	// context ends with the delivery, so one read after the call has
	// returned says nothing about what the send was given.
	seen := make(chan context.Context, 1)
	release := make(chan struct{})
	dl := newCompletionDownlink(ctx,
		func(ctx context.Context, _ proto.LifecycleCompleteParams) error {
			seen <- ctx
			<-release
			return nil
		},
		func(context.Context, proto.LifecycleEnteredParams) error { return nil })
	dl.Bind(HostSessionID{Generation: "gen-under-test", Session: "0123456789abcdef0123456789abcdef"})
	accept(t, dl, [32]byte{1}, nil)

	select {
	case got := <-seen:
		if got.Err() != nil {
			t.Fatalf("the send's context was already done on arrival: %v", got.Err())
		}
		if _, ok := got.Deadline(); !ok {
			t.Fatal("the send's context carries no deadline: a helper that never answers would hold the queue for the session's life")
		}
		// THE SESSION ENDS. The context the send is holding must end with it.
		stop()
		if got.Err() == nil {
			t.Fatal("deliver ran under a context the downlink's own cancellation does not reach: the composition would cancel a context nobody reads")
		}
		close(release)
	case <-time.After(5 * time.Second):
		t.Fatal("deliver never reached the send")
	}
}
