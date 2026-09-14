package session

// Task 4's acceptance criteria for tokenBook (the-session-surface plan,
// nocx-6q1uh.4). Every test drives a FAKE clock — never a real wait — for
// tokenLifetime (60s) and resultRetention (5m), per AGENTS.md's "no test
// depends on timing".

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// fakeClock is a manually-advanced clock: Now is safe for concurrent use,
// which tokenBook's own contract requires of whatever it is handed.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func testIncarnation() sessionruntime.Incarnation {
	return sessionruntime.Incarnation{Session: "sess-tok", Generation: 1}
}

func testTokenSnapshot() retainedSnapshot {
	rows := []emulator.Row{
		{Cells: []emulator.Cell{{Grapheme: "h", Width: emulator.WidthNarrow, HasText: true}}},
	}
	return retainedSnapshot{
		ID:       1,
		Identity: sessionruntime.ScreenIdentity{At: testIncarnation(), Cols: 1, Rows: 1},
		Rows:     rows,
		Cursor:   emulator.Cursor{},
	}
}

func mintOne(t *testing.T, book *tokenBook) Token {
	t.Helper()
	tok, err := book.Mint(testTokenSnapshot(), sessionruntime.TargetRegion, sessionruntime.RowRange{First: 0, Last: 0})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return tok
}

func TestMintRefusesCapacityWithoutEvictingALiveSlot(t *testing.T) {
	clock := newFakeClock()
	book := newTokenBook(testIncarnation(), clock.Now)
	snap := testTokenSnapshot()
	rows := sessionruntime.RowRange{First: 0, Last: 0}

	for i := 0; i < maxLiveTokens; i++ {
		if _, err := book.Mint(snap, sessionruntime.TargetRegion, rows); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
	if got := len(book.slots); got != maxLiveTokens {
		t.Fatalf("book holds %d slots after %d mints, want %d", got, maxLiveTokens, maxLiveTokens)
	}

	if _, err := book.Mint(snap, sessionruntime.TargetRegion, rows); !errors.Is(err, ErrCapacity) {
		t.Fatalf("mint at capacity: got %v, want ErrCapacity", err)
	}
	if got := len(book.slots); got != maxLiveTokens {
		t.Fatalf("mint at capacity changed slot count to %d, want unchanged %d (nothing evicted)", got, maxLiveTokens)
	}
}

func TestASlotReleasesOnlyAfterExpiryAndRetentionPastTerminal(t *testing.T) {
	clock := newFakeClock()
	book := newTokenBook(testIncarnation(), clock.Now)
	tok := mintOne(t, book)

	canon := canonicalIntent{Kind: sessionruntime.IntentKindKey, Payload: []byte("x")}
	if _, _, err := book.Consume(tok.ID, canon); err != nil {
		t.Fatalf("consume: %v", err)
	}
	book.Record(tok.ID, storedResult{State: "executed", BytesWritten: 1})

	if state, _ := book.Status(tok.ID); state != "recorded" {
		t.Fatalf("status right after recording: got %q, want recorded", state)
	}

	// Past the token's own 60s lifetime, but short of resultRetention (5m)
	// since the result became terminal: still answers.
	clock.Advance(resultRetention - time.Second)
	if state, r := book.Status(tok.ID); state != "recorded" || r == nil {
		t.Fatalf("status just before retention elapses: got (%q, %v), want (recorded, non-nil)", state, r)
	}

	// Now both conditions hold: expired, and resultRetention has passed
	// since the result became terminal.
	clock.Advance(2 * time.Second)
	state, r := book.Status(tok.ID)
	if state != "unknown" || r != nil {
		t.Fatalf("status after release: got (%q, %v), want (unknown, nil)", state, r)
	}
}

func TestASlotConsumedButNeverRecordedIsNotReleased(t *testing.T) {
	// Defensive case (tokens.go's own doc on releasable): a slot bound to an
	// intent whose write never settles must not be discarded, or a genuine
	// replay of it would see token_spent instead of its own eventual result.
	clock := newFakeClock()
	book := newTokenBook(testIncarnation(), clock.Now)
	tok := mintOne(t, book)

	canon := canonicalIntent{Kind: sessionruntime.IntentKindKey, Payload: []byte("x")}
	if _, _, err := book.Consume(tok.ID, canon); err != nil {
		t.Fatalf("consume: %v", err)
	}

	clock.Advance(tokenLifetime + resultRetention + time.Hour)
	if state, _ := book.Status(tok.ID); state != "in_progress" {
		t.Fatalf("status for a never-recorded consumed slot: got %q, want in_progress", state)
	}
}

func TestVerifyRefusesATamperedOrExpiredToken(t *testing.T) {
	clock := newFakeClock()
	book := newTokenBook(testIncarnation(), clock.Now)
	tok := mintOne(t, book)

	if err := book.Verify(tok); err != nil {
		t.Fatalf("verify a freshly minted token: %v", err)
	}

	t.Run("one MAC byte flipped", func(t *testing.T) {
		bad := tok
		bad.MAC[0] ^= 0xFF
		if err := book.Verify(bad); !errors.Is(err, ErrForged) {
			t.Fatalf("got %v, want ErrForged", err)
		}
	})

	t.Run("rows altered", func(t *testing.T) {
		bad := tok
		bad.Rows.Last++
		if err := book.Verify(bad); !errors.Is(err, ErrForged) {
			t.Fatalf("got %v, want ErrForged", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		clock.Advance(tokenLifetime + time.Second)
		if err := book.Verify(tok); !errors.Is(err, ErrExpired) {
			t.Fatalf("got %v, want ErrExpired", err)
		}
	})
}

func TestVerifyRefusesATokenFromAnotherIncarnationsKey(t *testing.T) {
	clock := newFakeClock()
	own := newTokenBook(testIncarnation(), clock.Now)
	other := newTokenBook(sessionruntime.Incarnation{Session: "sess-tok", Generation: 2}, clock.Now)
	foreign := mintOne(t, other)

	if err := own.Verify(foreign); !errors.Is(err, ErrForged) {
		t.Fatalf("got %v, want ErrForged", err)
	}
}

// TestConcurrentConsumeOfTheSameIntentLetsExactlyOneProceed is the plan's
// own acceptance criterion: "Concurrent Consume of the same token with the
// same intent from 16 goroutines: exactly one proceeds, the others get
// inProgress or the recorded result."
func TestConcurrentConsumeOfTheSameIntentLetsExactlyOneProceed(t *testing.T) {
	clock := newFakeClock()
	book := newTokenBook(testIncarnation(), clock.Now)
	tok := mintOne(t, book)
	canon := canonicalIntent{Kind: sessionruntime.IntentKindKey, Payload: []byte("same")}

	const n = 16
	var wg sync.WaitGroup
	var proceeded, other, unexpected int32
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorded, inProgress, err := book.Consume(tok.ID, canon)
			switch {
			case err != nil:
				atomic.AddInt32(&unexpected, 1)
			case recorded == nil && !inProgress:
				atomic.AddInt32(&proceeded, 1)
			default: // recorded != nil, or inProgress
				atomic.AddInt32(&other, 1)
			}
		}()
	}
	wg.Wait()

	if proceeded != 1 {
		t.Fatalf("proceeded = %d, want exactly 1 (unexpected errors = %d, other = %d)", proceeded, unexpected, other)
	}
	if unexpected != 0 {
		t.Fatalf("a same-intent Consume returned an error %d times, want 0", unexpected)
	}
	if other != n-1 {
		t.Fatalf("inProgress-or-recorded = %d, want %d", other, n-1)
	}
}

// TestConcurrentConsumeOfADifferentIntentIsTokenSpent is the plan's other
// half of the same criterion: "with a different intent, ErrTokenSpent."
func TestConcurrentConsumeOfADifferentIntentIsTokenSpent(t *testing.T) {
	clock := newFakeClock()
	book := newTokenBook(testIncarnation(), clock.Now)
	tok := mintOne(t, book)

	const n = 16
	var wg sync.WaitGroup
	var proceeded, spent, unexpected int32
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			canon := canonicalIntent{Kind: sessionruntime.IntentKindKey, Payload: []byte{byte(i)}}
			_, _, err := book.Consume(tok.ID, canon)
			switch {
			case err == nil:
				atomic.AddInt32(&proceeded, 1)
			case errors.Is(err, ErrTokenSpent):
				atomic.AddInt32(&spent, 1)
			default:
				atomic.AddInt32(&unexpected, 1)
			}
		}(i)
	}
	wg.Wait()

	if proceeded != 1 {
		t.Fatalf("proceeded = %d, want exactly 1", proceeded)
	}
	if spent != n-1 {
		t.Fatalf("token_spent = %d, want %d (unexpected = %d)", spent, n-1, unexpected)
	}
}

// TestStoredResultEncodesUnderTheBound is the plan's own bound: "The stored
// result encodes in ≤128 bytes for every cause."
func TestStoredResultEncodesUnderTheBound(t *testing.T) {
	causes := []string{
		"", "stale_target", "incomparable", "expired", "forged", "token_spent",
		"snapshot_gone", "completeness_unknown", "cannot_encode", "would_submit",
		"access_revoked", "no_read_barrier", "commit_deadline", "capacity",
		"busy", "closing", "refused",
	}
	for _, cause := range causes {
		r := storedResult{State: "refused", BytesWritten: 1 << 20, FenceAfter: ^uint64(0), Cause: cause}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("cause %q: marshal: %v", cause, err)
		}
		if len(b) > 128 {
			t.Fatalf("cause %q encodes to %d bytes (%s), want <= 128", cause, len(b), b)
		}
	}
}
