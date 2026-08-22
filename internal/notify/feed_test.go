package notify

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func evEvent(session, title string) Event {
	return Event{SessionID: session, Title: title, Kind: KindProgramNotify, Trust: TrustProgramRequest, Level: LevelInfo}
}

func TestFeedEvictsReadBeforeUnread(t *testing.T) {
	f, err := NewFeed(FeedLimits{MaxOccurrences: 2, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, RealClock{})
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	f.Add(evEvent("s1", "will-be-read"))
	f.MarkAllRead()
	unread := f.Add(evEvent("s2", "unread"))
	f.Add(evEvent("s3", "new"))

	snap := f.Snapshot()
	if len(snap.Occurrences) != 2 {
		t.Fatalf("held %d occurrences, want 2", len(snap.Occurrences))
	}
	// The READ one goes, not the unread one: an unread row is the only
	// thing the feed exists to protect.
	if snap.Occurrences[len(snap.Occurrences)-1].ID != unread {
		t.Fatalf("evicted the unread occurrence; oldest held is %q", snap.Occurrences[len(snap.Occurrences)-1].Event.Title)
	}
	if snap.Dropped.Count != 1 {
		t.Fatalf("Dropped.Count = %d, want 1", snap.Dropped.Count)
	}
}

// TestFeedEvictsNewerReadBeforeOlderUnread is the case that tells the stated
// policy apart from plain FIFO, and it reaches past the public API to get it.
//
// MarkAllRead is the only read-marking API this task has, and it marks every
// held occurrence — so through the API the read set is always a PREFIX of
// insertion order, "oldest read" and "oldest" are always the same row, and
// read-first and FIFO agree on every reachable state. The one state that
// separates them is a read occurrence NEWER than an unread one, which arrives
// for real in Task 2 (collapse clears ReadAt on an older row). Until then the
// criterion is only checkable from inside the package.
func TestFeedEvictsNewerReadBeforeOlderUnread(t *testing.T) {
	f, err := NewFeed(FeedLimits{MaxOccurrences: 2, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, RealClock{})
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	older := f.Add(evEvent("s1", "older-unread"))
	newer := f.Add(evEvent("s2", "newer-read"))

	at := time.Unix(1, 0)
	f.mu.Lock()
	f.items[1].ReadAt = &at
	f.mu.Unlock()

	f.Add(evEvent("s3", "new"))

	snap := f.Snapshot()
	if len(snap.Occurrences) != 2 {
		t.Fatalf("held %d occurrences, want 2", len(snap.Occurrences))
	}
	for _, o := range snap.Occurrences {
		if o.ID == newer {
			t.Fatalf("kept the read occurrence %q; a read row is evicted before any unread one", o.Event.Title)
		}
	}
	if snap.Occurrences[len(snap.Occurrences)-1].ID != older {
		t.Fatalf("evicted the older UNREAD occurrence while a read one was held; oldest held is %q",
			snap.Occurrences[len(snap.Occurrences)-1].Event.Title)
	}
	if snap.Dropped.Count != 1 {
		t.Fatalf("Dropped.Count = %d, want 1", snap.Dropped.Count)
	}
}

func TestFeedEvictsOnByteBudget(t *testing.T) {
	// Rows are not the binding budget here: MaxOccurrences is 10 and only
	// two occurrences are added, so anything evicted was evicted for bytes.
	f, err := NewFeed(FeedLimits{MaxOccurrences: 10, MaxRetainedBytes: 12, CollapseWindow: 30 * time.Second}, RealClock{})
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	f.Add(evEvent("s1", "aaaaaaaa")) // 8 bytes: inside the budget
	if got := len(f.Snapshot().Occurrences); got != 1 {
		t.Fatalf("held %d occurrences after the first add, want 1", got)
	}
	f.Add(evEvent("s2", "bbbbbbbb")) // 16 bytes held: over it

	snap := f.Snapshot()
	if len(snap.Occurrences) != 1 {
		t.Fatalf("held %d occurrences, want 1 — the byte budget did not evict", len(snap.Occurrences))
	}
	if snap.Occurrences[0].Event.Title != "bbbbbbbb" {
		t.Fatalf("held %q, want the newest occurrence", snap.Occurrences[0].Event.Title)
	}
	if snap.Dropped.Count != 1 {
		t.Fatalf("Dropped.Count = %d, want 1", snap.Dropped.Count)
	}
}

func TestFeedRevisionIsMonotonic(t *testing.T) {
	f, err := NewFeed(FeedLimits{MaxOccurrences: 2, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, RealClock{})
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	prev := f.Snapshot().Revision

	rise := func(what string) uint64 {
		t.Helper()
		rev := f.Snapshot().Revision
		if rev <= prev {
			t.Fatalf("%s: revision %d did not rise above %d", what, rev, prev)
		}
		prev = rev
		return rev
	}
	steady := func(what string) {
		t.Helper()
		if rev := f.Snapshot().Revision; rev != prev {
			t.Fatalf("%s: revision moved from %d to %d without a mutation", what, prev, rev)
		}
	}

	f.Add(evEvent("s1", "one"))
	rise("Add")
	steady("Snapshot")

	returned := f.MarkAllRead()
	got := rise("MarkAllRead")
	if returned != got {
		t.Fatalf("MarkAllRead returned %d, snapshot says %d", returned, got)
	}

	f.Add(evEvent("s2", "two"))
	rise("Add")
	f.Add(evEvent("s3", "three")) // third occurrence in a feed of two: evicts
	rise("Add that evicts")
	if dropped := f.Snapshot().Dropped.Count; dropped != 1 {
		t.Fatalf("Dropped.Count = %d, want 1 — no eviction happened, so the eviction case is untested", dropped)
	}
	steady("Snapshot after eviction")
}

func TestFeedOnChangeSeesTheMutation(t *testing.T) {
	f, err := NewFeed(FeedLimits{MaxOccurrences: 4, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, RealClock{})
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	var handed []uint64
	f.OnChange(func(revision uint64) {
		snap := f.Snapshot() // reentrant by contract: the mutex is released first
		if snap.Revision != revision {
			t.Errorf("handler handed revision %d, Snapshot says %d", revision, snap.Revision)
		}
		handed = append(handed, revision)
		switch len(handed) {
		case 1: // the Add
			if len(snap.Occurrences) != 1 {
				t.Errorf("handler saw %d occurrences, want the added one", len(snap.Occurrences))
			}
			if snap.UnreadCount != 1 {
				t.Errorf("handler saw UnreadCount %d, want 1", snap.UnreadCount)
			}
		case 2: // the MarkAllRead
			if snap.UnreadCount != 0 {
				t.Errorf("handler saw UnreadCount %d after MarkAllRead, want 0", snap.UnreadCount)
			}
		}
	})

	f.Add(evEvent("s1", "one"))
	f.MarkAllRead()

	if len(handed) != 2 {
		t.Fatalf("OnChange fired %d times, want 2", len(handed))
	}
	if handed[1] <= handed[0] {
		t.Fatalf("OnChange handed %v, which is not increasing", handed)
	}
}

func TestFeedRejectsUselessLimits(t *testing.T) {
	cases := []struct {
		name   string
		limits FeedLimits
		clock  Clock
	}{
		{"no occurrences", FeedLimits{MaxOccurrences: 0, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, RealClock{}},
		{"no bytes", FeedLimits{MaxOccurrences: 16, MaxRetainedBytes: 0, CollapseWindow: 30 * time.Second}, RealClock{}},
		{"no collapse window", FeedLimits{MaxOccurrences: 16, MaxRetainedBytes: 1 << 20, CollapseWindow: 0}, RealClock{}},
		{"no clock", FeedLimits{MaxOccurrences: 16, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := NewFeed(tc.limits, tc.clock)
			if err == nil {
				t.Fatalf("NewFeed(%+v) returned no error", tc.limits)
			}
			if f != nil {
				t.Fatalf("NewFeed returned a feed alongside its error")
			}
		})
	}
}

func TestFeedCollapsesConsecutiveRun(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(FeedLimits{MaxOccurrences: 50, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	base := evEvent("s1", "ding")
	base.At = clk.Now()
	first := f.Add(base)

	clk.Advance(5 * time.Second)
	next := evEvent("s1", "ding again")
	next.At = clk.Now()
	second := f.Add(next)

	if first != second {
		t.Fatalf("a repeat inside the window minted a new id (%q vs %q)", first, second)
	}
	snap := f.Snapshot()
	if len(snap.Occurrences) != 1 {
		t.Fatalf("held %d occurrences, want 1", len(snap.Occurrences))
	}
	if snap.Occurrences[0].Count != 2 {
		t.Fatalf("Count = %d, want 2", snap.Occurrences[0].Count)
	}
	if snap.Occurrences[0].Event.Title != "ding again" {
		t.Fatalf("collapsed row kept the OLD title %q", snap.Occurrences[0].Event.Title)
	}
	if !snap.Occurrences[0].LastAt.Equal(next.At) {
		t.Fatalf("LastAt = %v, want the newest arrival %v", snap.Occurrences[0].LastAt, next.At)
	}
}

func TestFeedNeverCollapsesAcrossLevel(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(FeedLimits{MaxOccurrences: 50, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	ok := evEvent("s1", "step passed")
	ok.Level = LevelSuccess
	ok.At = clk.Now()
	f.Add(ok)

	clk.Advance(time.Second)
	bad := evEvent("s1", "step FAILED")
	bad.Level = LevelDanger
	bad.At = clk.Now()
	f.Add(bad)

	// The whole reason Level is in the key: a failure must never be
	// compacted into a run of successes.
	if got := len(f.Snapshot().Occurrences); got != 2 {
		t.Fatalf("held %d occurrences, want 2 — a danger collapsed into a success run", got)
	}
}

func TestFeedOpensANewRowPastTheWindow(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(FeedLimits{MaxOccurrences: 50, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	a := evEvent("s1", "deploy staging succeeded")
	a.At = clk.Now()
	f.Add(a)

	clk.Advance(31 * time.Second)
	b := evEvent("s1", "deploy production succeeded")
	b.At = clk.Now()
	f.Add(b)

	// The counterexample the review found: same session, kind and level, but
	// two separate acts. The window, not the read state, is what separates them.
	if got := len(f.Snapshot().Occurrences); got != 2 {
		t.Fatalf("held %d occurrences, want 2 — two separate deploys merged", got)
	}
}

// TestFeedNeverCollapsesAcrossBackend is the assertion that Attribution.Backend
// actually participates in the key. It is the first code to read the field, and
// without this the field could be dropped from CollapseKey with every other
// feed test still green.
func TestFeedNeverCollapsesAcrossBackend(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(FeedLimits{MaxOccurrences: 50, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	here := evEvent("s1", "build finished")
	here.Attribution.Backend = "local"
	here.At = clk.Now()
	f.Add(here)

	clk.Advance(time.Second)
	// Same session id, same kind, same level — but a different machine said
	// it. Session ids are only unique per backend, so collapsing these would
	// merge two machines' runs into one row.
	there := evEvent("s1", "build finished")
	there.Attribution.Backend = "relay-7"
	there.At = clk.Now()
	f.Add(there)

	if got := len(f.Snapshot().Occurrences); got != 2 {
		t.Fatalf("held %d occurrences, want 2 — two backends collapsed into one row", got)
	}
}

// TestFeedCollapseIntoAReadRowReopensIt: the count changed, so there is
// something new to see. This is also the state that makes the read-before-
// unread eviction rule reachable through the public API — a read row can now
// be NEWER than an unread one, which MarkAllRead alone can never produce.
func TestFeedCollapseIntoAReadRowReopensIt(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(FeedLimits{MaxOccurrences: 50, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	first := evEvent("s1", "ding")
	first.At = clk.Now()
	id := f.Add(first)
	f.MarkAllRead()
	if got := f.Snapshot().UnreadCount; got != 0 {
		t.Fatalf("UnreadCount = %d after MarkAllRead, want 0", got)
	}

	clk.Advance(time.Second)
	again := evEvent("s1", "ding")
	again.At = clk.Now()
	if got := f.Add(again); got != id {
		t.Fatalf("the repeat minted a new id %q, want the held %q", got, id)
	}

	snap := f.Snapshot()
	if len(snap.Occurrences) != 1 {
		t.Fatalf("held %d occurrences, want 1", len(snap.Occurrences))
	}
	if snap.Occurrences[0].ReadAt != nil {
		t.Fatalf("the collapsed row stayed read at %v; the count rose, so it is unread again", *snap.Occurrences[0].ReadAt)
	}
	if snap.UnreadCount != 1 {
		t.Fatalf("UnreadCount = %d, want 1", snap.UnreadCount)
	}
}

func TestFloodDoesNotEvictAnUnreadDangerFromAnotherSession(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(FeedLimits{MaxOccurrences: 8, MaxRetainedBytes: 4096, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	danger := evEvent("s-important", "deploy failed")
	danger.Level = LevelDanger
	danger.At = clk.Now()
	kept := f.Add(danger)

	for i := 0; i < 10000; i++ {
		clk.Advance(time.Millisecond)
		e := evEvent("s-runaway", "ding")
		e.At = clk.Now()
		f.Add(e)
	}

	snap := f.Snapshot()
	if len(snap.Occurrences) > 8 {
		t.Fatalf("feed grew to %d occurrences past its bound", len(snap.Occurrences))
	}
	var found bool
	for _, o := range snap.Occurrences {
		if o.ID == kept {
			found = true
		}
	}
	// This is the property the whole flood argument exists to buy.
	if !found {
		t.Fatal("a runaway session evicted the unread danger row, which is what the feed exists to protect")
	}
	// And it is one row with a count, not ten thousand rows: nothing was
	// evicted at all, so Dropped stayed empty.
	if snap.Dropped.Count != 0 {
		t.Fatalf("Dropped.Count = %d — the flood evicted instead of collapsing", snap.Dropped.Count)
	}
	for _, o := range snap.Occurrences {
		if o.Event.SessionID == "s-runaway" && o.Count != 10000 {
			t.Fatalf("the runaway row has Count %d, want 10000", o.Count)
		}
	}
}

// The observer is handed revisions in MUTATION order, not in whatever order
// the mutators happen to be scheduled after they let go of the lock.
//
// OnChange's contract says the observer sees the state the revision names —
// which the renderer relies on, since it reconciles by revision and refetches
// on a gap. Two mutators that captured revisions 1 and 2 and then raced to
// publish could hand 2 before 1, and the renderer would take 1 for a rewind
// it has no rule for. The publication order is the property; the revision
// numbers are only how it is observed.
func TestFeedPublishesInRevisionOrder(t *testing.T) {
	f, err := NewFeed(FeedLimits{MaxOccurrences: 8, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, RealClock{})
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	var mu sync.Mutex
	var handed []uint64
	f.OnChange(func(revision uint64) {
		// Reentrant by contract, and exercised here on purpose: the fix
		// must not publish while holding the feed's own mutex.
		_ = f.Snapshot()
		mu.Lock()
		handed = append(handed, revision)
		mu.Unlock()
	})

	const goroutines, each = 8, 25
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if (g+i)%4 == 3 {
					f.MarkAllRead()
					continue
				}
				f.Add(evEvent(fmt.Sprintf("s%d", g), fmt.Sprintf("title-%d-%d", g, i)))
			}
		}(g)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(handed) != goroutines*each {
		t.Fatalf("observer was handed %d revisions, want one per mutation (%d)", len(handed), goroutines*each)
	}
	for i := 1; i < len(handed); i++ {
		if handed[i] <= handed[i-1] {
			t.Fatalf("revision %d was published after %d (index %d): the observer saw the feed go backwards", handed[i], handed[i-1], i)
		}
	}
	if last := handed[len(handed)-1]; last != f.Snapshot().Revision {
		t.Fatalf("last published revision %d, feed says %d", last, f.Snapshot().Revision)
	}
}

// The byte budget is a bound on a feed that holds MORE THAN ONE occurrence,
// and this is the case at the other end of that interval: a single unread
// occurrence larger than the whole budget is KEPT, and the feed sits over
// budget until a second one arrives.
//
// That is deliberate — a feed holding nothing is worse than a feed slightly
// over budget, and in production the case cannot arise anyway: notify.raise
// admits 4096 runes of title and 4096 of body against a 1 MiB budget. It is
// pinned here so the next reader of enforceLocked finds a test rather than a
// surprise.
func TestFeedKeepsOneOccurrenceOverBudget(t *testing.T) {
	limits := FeedLimits{MaxOccurrences: 8, MaxRetainedBytes: 16, CollapseWindow: 30 * time.Second}
	f, err := NewFeed(limits, RealClock{})
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	id := f.Add(evEvent("s1", strings.Repeat("x", 100)))

	snap := f.Snapshot()
	if len(snap.Occurrences) != 1 {
		t.Fatalf("held %d occurrences, want the one that does not fit", len(snap.Occurrences))
	}
	if snap.Occurrences[0].ID != id {
		t.Fatalf("held %q, want the added occurrence %q", snap.Occurrences[0].ID, id)
	}
	if snap.Dropped.Count != 0 {
		t.Fatalf("Dropped.Count = %d — nothing was evicted, so nothing may be recorded", snap.Dropped.Count)
	}
	f.mu.Lock()
	held := f.bytes
	f.mu.Unlock()
	if held <= limits.MaxRetainedBytes {
		t.Fatalf("held %d bytes against a budget of %d: the over-budget case this test names did not happen",
			held, limits.MaxRetainedBytes)
	}

	// And the other end: with a second occurrence the budget binds again,
	// evicting down to — but never past — one.
	f.Add(evEvent("s2", strings.Repeat("y", 100)))

	snap = f.Snapshot()
	if len(snap.Occurrences) != 1 {
		t.Fatalf("held %d occurrences after a second arrival, want 1", len(snap.Occurrences))
	}
	if snap.Occurrences[0].ID == id {
		t.Fatalf("evicted the newer occurrence and kept %q", id)
	}
	if snap.Dropped.Count != 1 {
		t.Fatalf("Dropped.Count = %d, want 1 — the eviction must be recorded", snap.Dropped.Count)
	}
}
