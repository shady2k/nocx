package session

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/sessionruntime"
)

// reviewDeadlineTimer models a scheduled callback without advancing real time.
// Fire deliberately still works after Stop: time.AfterFunc.Stop cannot recall
// a callback which has already started, so stale callbacks must be harmless.
type reviewDeadlineTimer struct {
	delay   time.Duration
	fire    func()
	stopped atomic.Bool
	once    sync.Once
}

func (t *reviewDeadlineTimer) Stop() bool { return !t.stopped.Swap(true) }

func (t *reviewDeadlineTimer) Fire() { t.once.Do(t.fire) }

func awaitReviewDeadline(t *testing.T, scheduled <-chan *reviewDeadlineTimer) *reviewDeadlineTimer {
	t.Helper()
	select {
	case timer := <-scheduled:
		return timer
	case <-time.After(hangLimit):
		t.Fatal("queued intent has no scheduled deadline wakeup")
		return nil
	}
}

// Unlike newIntentTestSession, this fixture does not start run. Clocks and
// timer scheduling must be installed before any owner goroutine can use them.
func newReviewDeadlineSession(t *testing.T, proc *rawReaderFakeProcess) (*hostSession, sessionruntime.Control) {
	t.Helper()
	owner, rt := newTestOwner(t, proc)
	hs := &hostSession{proc: proc, owner: owner, runtime: rt, win: owner.win, now: time.Now}
	hs.tokens = newTokenBook(rt.Incarnation(), time.Now)
	owner.SetTokens(hs.tokens)
	return hs, grantAgent(t, rt)
}

func TestReviewQuietPendingIntentExpiresBehindHeldWriter(t *testing.T) {
	for _, cause := range []string{"commit_deadline", "expired"} {
		t.Run(cause, func(t *testing.T) {
			proc := newRawReaderFakeProcess()
			hs, ctrl := newReviewDeadlineSession(t, proc)
			var mono, wall atomic.Int64
			mono.Store(100)
			wall.Store(time.Date(2026, time.October, 29, 12, 0, 0, 0, time.UTC).UnixNano())
			hs.owner.nowMono = mono.Load
			hs.tokens.now = func() time.Time { return time.Unix(0, wall.Load()) }
			scheduled := make(chan *reviewDeadlineTimer, 32)
			hs.owner.deadlineAfterFunc = func(delay time.Duration, fire func()) lifecycleTimer {
				timer := &reviewDeadlineTimer{delay: delay, fire: fire}
				scheduled <- timer
				return timer
			}
			entered, release := proc.blockNextWrite()
			go hs.owner.run()
			t.Cleanup(func() { release(); hs.owner.stop(false, time.Time{}) })

			blocked, err := hs.owner.submit(ownerItem{kind: itemClientFrame, payload: []byte("held")})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(hangLimit):
				t.Fatal("writer did not enter the held write")
			}
			tok := mintRegionToken(t, hs)
			commitBy := int64(200)
			wantErr := errCommitDeadline
			if cause == "expired" {
				commitBy = 1 << 62
				wantErr = ErrExpired
			}
			done := submitIntent(t, hs, ctrl, tok, "must-not-write", commitBy)
			timer := awaitReviewDeadline(t, scheduled)
			if timer.delay <= 0 {
				t.Fatalf("scheduled delay = %s, want a future deadline", timer.delay)
			}
			if state, _ := hs.tokens.Status(tok.ID); state != "in_progress" {
				t.Fatalf("intent before deadline = %q, want in_progress", state)
			}
			if cause == "commit_deadline" {
				mono.Store(300)
			} else {
				wall.Store(tok.ExpiresAt.Add(time.Nanosecond).UnixNano())
			}
			// Only the timer wakes the quiet owner. No output, extra submit,
			// writer release or caller cancellation may drive this expiry.
			timer.Fire()
			res := awaitResult(t, done)
			if res.State != sessionruntime.IntentStateRefused || !errors.Is(res.Err, wantErr) || res.BytesWritten != 0 {
				t.Fatalf("quiet queued intent = %+v, want refused/%v without bytes", res, wantErr)
			}
			if state, _ := hs.tokens.Status(tok.ID); state != "recorded" {
				t.Fatalf("expired intent token = %q, want recorded", state)
			}
			if got := proc.writtenPayloads(); len(got) != 0 {
				t.Fatalf("writer was released or expired intent wrote: %q", got)
			}
			select {
			case res := <-blocked:
				t.Fatalf("deadline expiry unexpectedly settled the held writer: %+v", res)
			default:
			}
			release()
			awaitResult(t, blocked)
		})
	}
}

func TestReviewPendingDeadlineIsStoppedWhenRevocationSettlesToken(t *testing.T) {
	proc := newRawReaderFakeProcess()
	defer func() { _ = proc.Close() }()
	hs, ctrl := newReviewDeadlineSession(t, proc)
	hs.owner.nowMono = func() int64 { return 100 }
	scheduled := make(chan *reviewDeadlineTimer, 1)
	hs.owner.deadlineAfterFunc = func(delay time.Duration, fire func()) lifecycleTimer {
		timer := &reviewDeadlineTimer{delay: delay, fire: fire}
		scheduled <- timer
		return timer
	}
	tok := mintRegionToken(t, hs)
	done := submitIntent(t, hs, ctrl, tok, "must-not-write", 200)
	hs.owner.dispatchIncoming(<-hs.owner.incoming)
	hs.owner.armPendingDeadline()
	timer := awaitReviewDeadline(t, scheduled)
	bump := submitBump(t, hs, tok.AccessEpoch)
	hs.owner.dispatchIncoming(<-hs.owner.incoming)
	hs.owner.armPendingDeadline()
	if res := awaitResult(t, done); !errors.Is(res.Err, errAccessRevoked) {
		t.Fatalf("revoked intent = %+v", res)
	}
	awaitResult(t, bump)
	if !timer.stopped.Load() {
		t.Fatal("terminal pending intent left its deadline timer armed")
	}
	// A callback already running when Stop won must not resurrect the
	// resolved intent or overwrite the access_revoked record.
	timer.Fire()
	hs.owner.expirePending()
	state, recorded := hs.tokens.Status(tok.ID)
	if state != "recorded" || recorded == nil || recorded.Cause != "access_revoked" {
		t.Fatalf("stale deadline changed token outcome: %q, %+v", state, recorded)
	}
	select {
	case timer := <-scheduled:
		t.Fatalf("empty pending queue scheduled another deadline after %s", timer.delay)
	default:
	}
}

func TestReviewCheckedIntentNeedsActualEAGAINBeforeCommit(t *testing.T) {
	proc := newRawReaderFakeProcess()
	defer func() { _ = proc.Close() }()
	owner, rt := newTestOwner(t, proc)
	ctrl := grantAgent(t, rt)
	proc.produce(bytes.Repeat([]byte("x"), pageSize))
	proc.produce([]byte("\r\nTAIL-AT-BARRIER"))
	checked := 0
	refusal := errors.New("test refuses after observing the true barrier")
	done, err := owner.submit(ownerItem{kind: itemIntent, intent: &pendingIntent{
		Intent: keyIntent(rt, ctrl, "Enter"), Interactive: true,
		Canonical: canonicalIntent{AccessEpoch: owner.currentAccessEpoch()},
		Check: func(s sessionruntime.Snapshot) error {
			checked++
			if !bytes.Contains(s.Screen, []byte("TAIL-AT-BARRIER")) {
				t.Error("conditional check omitted the final readable quantum")
			}
			return refusal
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	owner.dispatchIncoming(<-owner.incoming)
	for turn := 1; turn <= 2; turn++ {
		owner.advance()
		if checked != 0 || len(owner.pending) != 1 {
			t.Fatalf("turn %d committed on data rather than EAGAIN: checks=%d, pending=%d", turn, checked, len(owner.pending))
		}
		select {
		case res := <-done:
			t.Fatalf("turn %d settled before the read barrier: %+v", turn, res)
		default:
		}
	}
	owner.advance()
	if checked != 1 || len(owner.pending) != 0 {
		t.Fatalf("actual EAGAIN did not commit exactly once: checks=%d, pending=%d", checked, len(owner.pending))
	}
	if res := awaitResult(t, done); !errors.Is(res.Err, refusal) || res.BytesWritten != 0 {
		t.Fatalf("barrier-gated intent = %+v", res)
	}
	if got := proc.writtenPayloads(); len(got) != 0 {
		t.Fatalf("refused conditional intent wrote %q", got)
	}
}
