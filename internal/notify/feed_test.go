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
	f, err := NewFeed(FeedLimits{MaxOccurrences: 2, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, RealClock{})
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
	f, err := NewFeed(FeedLimits{MaxOccurrences: 2, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, RealClock{})
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
	f, err := NewFeed(FeedLimits{MaxOccurrences: 10, MaxRetainedBytes: 12, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, RealClock{})
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
	f, err := NewFeed(FeedLimits{MaxOccurrences: 2, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, RealClock{})
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
	f, err := NewFeed(FeedLimits{MaxOccurrences: 4, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, RealClock{})
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
		{"no occurrences", FeedLimits{MaxOccurrences: 0, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, RealClock{}},
		{"no bytes", FeedLimits{MaxOccurrences: 16, MaxRetainedBytes: 0, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, RealClock{}},
		{"no collapse window", FeedLimits{MaxOccurrences: 16, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 0}, RealClock{}},
		{"no clock", FeedLimits{MaxOccurrences: 16, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, nil},
		{"no run retained", FeedLimits{MaxOccurrences: 16, MaxRetainedBytes: 1 << 20, MaxRunRetained: 0, CollapseWindow: 30 * time.Second}, RealClock{}},
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
	f, err := NewFeed(FeedLimits{MaxOccurrences: 50, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, clk)
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
	f, err := NewFeed(FeedLimits{MaxOccurrences: 50, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, clk)
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
	f, err := NewFeed(FeedLimits{MaxOccurrences: 50, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, clk)
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
	f, err := NewFeed(FeedLimits{MaxOccurrences: 50, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, clk)
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
	there.Attribution.Backend = "helper-7"
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
	f, err := NewFeed(FeedLimits{MaxOccurrences: 50, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, clk)
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
	const runRetained = 20
	f, err := NewFeed(FeedLimits{MaxOccurrences: 8, MaxRetainedBytes: 4096, MaxRunRetained: runRetained, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	danger := evEvent("s-important", "deploy failed")
	danger.Level = LevelDanger
	danger.At = clk.Now()
	kept := f.Add(danger)
	assertRunAccounting(t, f, "after the danger row")

	for i := 0; i < 10000; i++ {
		clk.Advance(time.Millisecond)
		e := evEvent("s-runaway", "ding")
		e.At = clk.Now()
		f.Add(e)
		// The invariant is asserted after EVERY mutation, not once at the
		// end: a tail that lost a member without recording it would be
		// invisible to a single check at the finish, because the next
		// thousand joins would keep the arithmetic looking plausible.
		assertRunAccounting(t, f, fmt.Sprintf("after runaway add %d", i+1))
	}

	snap := f.Snapshot()
	if len(snap.Occurrences) > 8 {
		t.Fatalf("feed grew to %d occurrences past its bound", len(snap.Occurrences))
	}
	f.mu.Lock()
	held := f.bytes
	f.mu.Unlock()
	if held > 4096 {
		t.Fatalf("feed held %d bytes past its 4096 budget — the retained members are outside the bound", held)
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
		if o.Event.SessionID != "s-runaway" {
			continue
		}
		if o.Count != 10000 {
			t.Fatalf("the runaway row has Count %d, want 10000", o.Count)
		}
		// Retaining the tail must not restore the unbounded growth that
		// collapse exists to prevent: 10 000 joins leave 20 members held
		// and the rest counted.
		if len(o.Run) != runRetained {
			t.Fatalf("the runaway row retains %d members, want %d", len(o.Run), runRetained)
		}
		if o.RunDropped != 10000-runRetained {
			t.Fatalf("RunDropped = %d, want %d", o.RunDropped, 10000-runRetained)
		}
	}
}

// assertRunAccounting is the invariant D2 hangs on, checked over the whole
// feed: a row's Count is exactly what it still holds plus what it admits it
// no longer holds. The flood test asserts it after every mutation, because a
// tail that drops a member without recording it is arithmetic that stays
// plausible for the next thousand joins.
func assertRunAccounting(t *testing.T, f *Feed, when string) {
	t.Helper()
	for _, o := range f.Snapshot().Occurrences {
		if o.Count != len(o.Run)+o.RunDropped {
			t.Fatalf("%s: row %q has Count %d but holds %d members and admits %d dropped",
				when, o.ID, o.Count, len(o.Run), o.RunDropped)
		}
		if o.RunDropped < 0 {
			t.Fatalf("%s: row %q has RunDropped %d", when, o.ID, o.RunDropped)
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
	f, err := NewFeed(FeedLimits{MaxOccurrences: 8, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, RealClock{})
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
	limits := FeedLimits{MaxOccurrences: 8, MaxRetainedBytes: 16, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}
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

// ── the run tail (D2) ───────────────────────────────────────────────────

// runLimits is the shape every run-tail test starts from: the tail bound is
// what varies, so it is the only argument.
func runLimits(retained int) FeedLimits {
	return FeedLimits{MaxOccurrences: 50, MaxRetainedBytes: 1 << 20, MaxRunRetained: retained, CollapseWindow: 30 * time.Second}
}

// A fresh occurrence holds ITSELF, so an expansion never has to special-case
// a run of one — the row of one and the row of forty are read by the same
// code, and `Count == len(Run) + RunDropped` holds from the first add.
func TestFeedFreshOccurrenceIsItsOwnRun(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(runLimits(20), clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	ev := evEvent("s1", "build finished")
	ev.At = clk.Now()
	id := f.Add(ev)

	o := f.Snapshot().Occurrences[0]
	if len(o.Run) != 1 {
		t.Fatalf("a fresh occurrence holds %d members, want 1 — itself", len(o.Run))
	}
	if o.RunDropped != 0 {
		t.Fatalf("RunDropped = %d on a fresh occurrence, want 0", o.RunDropped)
	}
	if o.Count != len(o.Run)+o.RunDropped {
		t.Fatalf("Count %d != len(Run) %d + RunDropped %d", o.Count, len(o.Run), o.RunDropped)
	}
	m := o.Run[0]
	if m.ID != id {
		t.Errorf("the sole member's id = %q, want the row's own %q", m.ID, id)
	}
	if !m.At.Equal(ev.At) {
		t.Errorf("member At = %v, want the event's %v", m.At, ev.At)
	}
	if m.Title != "build finished" {
		t.Errorf("member Title = %q, want the event's", m.Title)
	}
	if m.ReadAt != nil {
		t.Errorf("a fresh member is read at %v; nobody has seen it", *m.ReadAt)
	}
}

// A join appends a member carrying its OWN id, instant and title. The row
// keeps the newest title (epic 1's rule) and the member keeps the one it
// arrived with — which is the whole reason an expansion is worth opening.
func TestFeedJoinAppendsAMemberWithItsOwnIdentity(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(runLimits(20), clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	titles := []string{"step 1 of 3", "step 2 of 3", "step 3 of 3"}
	ats := make([]time.Time, 0, len(titles))
	for _, title := range titles {
		ev := evEvent("s1", title)
		ev.At = clk.Now()
		ats = append(ats, ev.At)
		f.Add(ev)
		clk.Advance(time.Second)
	}

	o := f.Snapshot().Occurrences[0]
	if o.Count != 3 {
		t.Fatalf("Count = %d, want 3", o.Count)
	}
	if len(o.Run) != 3 {
		t.Fatalf("held %d members, want 3", len(o.Run))
	}
	if o.RunDropped != 0 {
		t.Fatalf("RunDropped = %d with a tail of 20, want 0", o.RunDropped)
	}
	seen := map[OccurrenceID]bool{}
	for i, m := range o.Run {
		if m.Title != titles[i] {
			t.Errorf("member %d Title = %q, want %q — members are oldest first, each with the title it arrived with", i, m.Title, titles[i])
		}
		if !m.At.Equal(ats[i]) {
			t.Errorf("member %d At = %v, want its own arrival %v", i, m.At, ats[i])
		}
		if seen[m.ID] {
			t.Errorf("member %d reuses id %q; a join mints its own", i, m.ID)
		}
		seen[m.ID] = true
	}
	if o.Event.Title != titles[len(titles)-1] {
		t.Errorf("the ROW kept %q, want the newest title", o.Event.Title)
	}
}

// Past MaxRunRetained the OLDEST member goes and RunDropped rises by one, so
// the expansion can say "3 of 5" rather than presenting a truncation as the
// whole.
func TestFeedRunTailDropsTheOldest(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(runLimits(3), clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	for i := 1; i <= 5; i++ {
		ev := evEvent("s1", fmt.Sprintf("tick %d", i))
		ev.At = clk.Now()
		f.Add(ev)
		clk.Advance(time.Second)

		o := f.Snapshot().Occurrences[0]
		if o.Count != len(o.Run)+o.RunDropped {
			t.Fatalf("after add %d: Count %d != len(Run) %d + RunDropped %d", i, o.Count, len(o.Run), o.RunDropped)
		}
		if len(o.Run) > 3 {
			t.Fatalf("after add %d: held %d members past the bound of 3", i, len(o.Run))
		}
	}

	o := f.Snapshot().Occurrences[0]
	if o.Count != 5 {
		t.Fatalf("Count = %d, want 5 — the row counts every join including the dropped ones", o.Count)
	}
	if o.RunDropped != 2 {
		t.Fatalf("RunDropped = %d, want 2", o.RunDropped)
	}
	want := []string{"tick 3", "tick 4", "tick 5"}
	got := make([]string, 0, len(o.Run))
	for _, m := range o.Run {
		got = append(got, m.Title)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("retained %v, want the NEWEST three %v", got, want)
	}
}

// The asymmetry that makes an expansion worth showing marks in: MarkAllRead
// marks the row and every member it still holds; a later join clears the
// ROW's mark and leaves the members' alone. They were seen, the new one was
// not.
func TestMarkAllReadMarksMembersAndAJoinClearsOnlyTheRow(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(runLimits(20), clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	for _, title := range []string{"one", "two"} {
		ev := evEvent("s1", title)
		ev.At = clk.Now()
		f.Add(ev)
		clk.Advance(time.Second)
	}
	f.MarkAllRead()

	o := f.Snapshot().Occurrences[0]
	if o.ReadAt == nil {
		t.Fatal("MarkAllRead left the row unread")
	}
	for i, m := range o.Run {
		if m.ReadAt == nil {
			t.Fatalf("MarkAllRead left member %d (%q) unread", i, m.Title)
		}
	}

	ev := evEvent("s1", "three")
	ev.At = clk.Now()
	f.Add(ev)

	o = f.Snapshot().Occurrences[0]
	if o.ReadAt != nil {
		t.Errorf("the join left the row read at %v; the count rose, so there is something new to see", *o.ReadAt)
	}
	if len(o.Run) != 3 {
		t.Fatalf("held %d members, want 3", len(o.Run))
	}
	for i, m := range o.Run[:2] {
		if m.ReadAt == nil {
			t.Errorf("the join cleared member %d (%q)'s mark; it was seen and the new arrival was not", i, m.Title)
		}
	}
	if o.Run[2].ReadAt != nil {
		t.Errorf("the newly joined member arrived read at %v", *o.Run[2].ReadAt)
	}
}

// A byte budget dies of drift, not of arithmetic: the members' titles count
// against MaxRetainedBytes, and evicting a row must release every byte its
// members held. The assertion is the exact one — f.bytes returns to its
// prior value across an add-then-evict cycle, not merely to a smaller one.
//
// The eviction is driven through the real path with NO add alongside it:
// every add-triggered eviction trades one row for another, and the
// arithmetic of the replacement is exactly what would hide a leak.
func TestFeedEvictionReleasesEveryMemberByte(t *testing.T) {
	clk := NewManualClock()
	limits := runLimits(20)
	limits.MaxOccurrences = 4
	f, err := NewFeed(limits, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	held := func() int64 {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.bytes
	}

	keeper := evEvent("s-keep", "keeper")
	keeper.At = clk.Now()
	f.Add(keeper)
	clk.Advance(time.Second)
	before := held()

	// A run of eight, each member with a title of its own. The row keeps
	// the newest title; the members keep all eight.
	titles := []string{"m0", "m1", "m2", "m3", "m4", "m5", "m6", "m7"}
	var wantRun int64
	for _, title := range titles {
		ev := evEvent("s-run", title)
		ev.At = clk.Now()
		f.Add(ev)
		clk.Advance(time.Second)
		wantRun += int64(len(title)) // every member's title is on the books
	}
	wantRun += int64(len(titles[len(titles)-1])) // plus the row's own event

	if grown := held() - before; grown != wantRun {
		t.Fatalf("a run of %d grew the budget by %d bytes, want exactly %d — member titles are not counted the way MaxRetainedBytes says",
			len(titles), grown, wantRun)
	}

	// Evict exactly that row: mark it read so victimLocked names it, then
	// squeeze the row bound and run the real eviction.
	f.mu.Lock()
	at := time.Unix(1, 0)
	f.items[1].ReadAt = &at
	f.limits.MaxOccurrences = 1
	f.enforceLocked()
	f.limits.MaxOccurrences = 4
	f.mu.Unlock()

	snap := f.Snapshot()
	if len(snap.Occurrences) != 1 || snap.Occurrences[0].Event.SessionID != "s-keep" {
		t.Fatalf("held %d rows after the eviction, want the keeper alone", len(snap.Occurrences))
	}
	if after := held(); after != before {
		t.Fatalf("f.bytes = %d after the run row was evicted, want the prior %d — the eviction leaked %d member bytes",
			after, before, after-before)
	}
}

// A snapshot is a copy, not a window: the feed goes on mutating members in
// place (the tail slides, MarkAllRead stamps them), and a caller holding an
// earlier snapshot must not see any of it. Without the copy this is also a
// data race between the transport reading a snapshot and the feed admitting
// the next occurrence.
func TestFeedSnapshotCopiesTheRun(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(runLimits(2), clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	ev := evEvent("s1", "first")
	ev.At = clk.Now()
	f.Add(ev)
	clk.Advance(time.Second)

	snap := f.Snapshot()
	before := snap.Occurrences[0].Run
	if len(before) != 1 || before[0].Title != "first" {
		t.Fatalf("snapshot run = %+v", before)
	}

	for _, title := range []string{"second", "third"} {
		next := evEvent("s1", title)
		next.At = clk.Now()
		f.Add(next)
		clk.Advance(time.Second)
	}
	f.MarkAllRead()

	if len(before) != 1 || before[0].Title != "first" {
		t.Fatalf("the earlier snapshot's run changed under the caller: %+v", before)
	}
	if before[0].ReadAt != nil {
		t.Fatalf("MarkAllRead reached into an already-returned snapshot")
	}
}

func newFailureFeed(t *testing.T) *Feed {
	t.Helper()
	f, err := NewFeed(FeedLimits{MaxOccurrences: 20, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, RealClock{})
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	return f
}

// failedEvent is a notification as it reached the router: attributed, from a
// session, and about to be lost.
func failedEvent() Event {
	ev := evEvent("s1", "build finished")
	ev.Attribution = Attribution{Backend: "local", Tab: "7", Host: "build-01", Session: "s1"}
	return ev
}

// TestFeedRecordsADeliveryFailureAsItsOwnRow: a delivery that failed after
// the raise was answered becomes ONE occurrence naming what failed, which
// channel and why, attributed to the original event so it lands beside the
// notification it is about (nocx-r6pxp, D3).
func TestFeedRecordsADeliveryFailureAsItsOwnRow(t *testing.T) {
	f := newFailureFeed(t)
	ev := failedEvent()
	f.Add(ev)

	f.RecordDeliveryFailure(ev, "banner", ErrUnavailable)

	snap := f.Snapshot()
	if len(snap.Occurrences) != 2 {
		t.Fatalf("feed holds %d occurrences, want 2 (the notification and the failure)", len(snap.Occurrences))
	}
	row := snap.Occurrences[0].Event // newest first
	// What failed: the notification the user did not get.
	if !strings.Contains(row.Title, "build finished") {
		t.Errorf("failure row title %q does not name the notification that failed", row.Title)
	}
	// Which channel, and why.
	if !strings.Contains(row.Body, "banner") {
		t.Errorf("failure row body %q does not name the channel", row.Body)
	}
	if !strings.Contains(row.Body, ErrUnavailable.Error()) {
		t.Errorf("failure row body %q does not name the reason", row.Body)
	}
	// Beside the notification it is about: the same session, host and tab,
	// or the renderer cannot resolve it to the pane it belongs to.
	if row.Attribution != ev.Attribution || row.SessionID != ev.SessionID {
		t.Errorf("failure row attribution %+v/%q, want the original %+v/%q", row.Attribution, row.SessionID, ev.Attribution, ev.SessionID)
	}
	// A fact nocx observed about its own delivery, not something the
	// program asked for — and warning, not danger (see
	// RecordDeliveryFailure).
	if row.Trust != TrustAttested {
		t.Errorf("failure row trust = %q, want %q", row.Trust, TrustAttested)
	}
	if row.Level != LevelWarning {
		t.Errorf("failure row level = %q, want %q", row.Level, LevelWarning)
	}
	// A row of its own: the level differs, so it never joins the run of the
	// notification it reports on.
	if snap.Occurrences[0].ID == snap.Occurrences[1].ID {
		t.Error("the failure joined the notification's row instead of standing beside it")
	}
	if snap.Occurrences[1].Event.Title != "build finished" {
		t.Errorf("the original row now reads %q", snap.Occurrences[1].Event.Title)
	}
}

// TestFeedCollapsesARunOfDeliveryFailures: a sink that is broken rather than
// unlucky fails every delivery, and the failures compact into one counted row
// like any other run. Collapse is what keeps a broken channel from being a
// second flood beside the first — it is not a substitute for the recursion
// bound (nothing here is raised), it is the bound on the honest repeats.
func TestFeedCollapsesARunOfDeliveryFailures(t *testing.T) {
	f := newFailureFeed(t)
	ev := failedEvent()
	for i := 0; i < 3; i++ {
		f.RecordDeliveryFailure(ev, "banner", ErrUnavailable)
	}

	snap := f.Snapshot()
	if len(snap.Occurrences) != 1 {
		t.Fatalf("three failures of one channel held %d rows, want 1", len(snap.Occurrences))
	}
	if snap.Occurrences[0].Count != 3 {
		t.Fatalf("collapsed row Count = %d, want 3", snap.Occurrences[0].Count)
	}
}

// TestFeedDeliveryFailureWithNoNamedChannelStillSaysSomething: a table row
// may leave Destination.Target empty, and the row still has to read as a
// sentence rather than opening with a colon.
func TestFeedDeliveryFailureWithNoNamedChannelStillSaysSomething(t *testing.T) {
	f := newFailureFeed(t)
	ev := failedEvent()
	ev.Title = ""
	f.RecordDeliveryFailure(ev, "", ErrUnavailable)

	row := f.Snapshot().Occurrences[0].Event
	if row.Title == "" || strings.HasPrefix(row.Body, ":") || strings.HasPrefix(row.Body, " ") {
		t.Fatalf("unnamed channel produced title %q body %q", row.Title, row.Body)
	}
	if !strings.Contains(row.Body, ErrUnavailable.Error()) {
		t.Fatalf("failure row body %q does not name the reason", row.Body)
	}
}

// ── retention: what the drawer gives up first (nocx-9brr5) ──────────────

// mustAckEvent is a row only the PERSON may decide to forget: an attested
// danger, the shape of "the build failed".
func mustAckEvent(session, title string) Event {
	return Event{SessionID: session, Title: title, Kind: KindBlockFinished, Trust: TrustAttested, Level: LevelDanger}
}

// bellEvent is the cheap row that measured the defect: readline hitting the
// start of a line, or Tab finding no candidate.
func bellEvent(session, title string) Event {
	return Event{SessionID: session, Title: title, Kind: KindBell, Trust: TrustProgramRequest, Level: LevelInfo}
}

// The predicate is one rule in one place, so it is worth stating the whole
// matrix rather than the two corners a caller happens to reach. Twelve cases:
// four levels against three trust classes.
func TestMustAcknowledgeReadsLevelAndExcludesHeuristic(t *testing.T) {
	levels := []Level{LevelInfo, LevelSuccess, LevelWarning, LevelDanger}
	for _, trust := range []Trust{TrustAttested, TrustProgramRequest, TrustHeuristic} {
		for _, level := range levels {
			alarming := level == LevelWarning || level == LevelDanger
			want := alarming && trust != TrustHeuristic
			if got := MustAcknowledge(Event{Trust: trust, Level: level}); got != want {
				t.Errorf("MustAcknowledge(trust=%s level=%s) = %v, want %v", trust, level, got, want)
			}
		}
	}
}

// The defect, in the numbers that measured it.
//
// A bell fires from readline hitting the start of a line or Tab finding no
// candidate. Bells more than 30s apart never collapse (CollapseWindow is
// 30s), so one every 31s is ~116 rows an hour; two hours of that is 232 rows
// against a 200-row feed, which is 33 evictions more than enough to reach the
// bottom. Before this change the bottom is where the unread "the build
// failed" sat, and the person never learned it happened.
//
// Both ends of the interval: the unread must-ack row is held from the Add
// that created it until the person reads it, and no volume of informational
// arrivals in between may take it.
func TestFeedFloodOfInformationalKeepsAnUnreadMustAck(t *testing.T) {
	clk := NewManualClock()
	const capacity = 200
	f, err := NewFeed(FeedLimits{MaxOccurrences: capacity, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	build := mustAckEvent("s-build", "the build failed")
	build.At = clk.Now()
	kept := f.Add(build)

	const bells = 232
	for i := 0; i < bells; i++ {
		clk.Advance(31 * time.Second) // past the window: 232 rows, not one run
		e := bellEvent("s-noise", "ding")
		e.At = clk.Now()
		f.Add(e)
	}

	snap := f.Snapshot()
	if len(snap.Occurrences) != capacity {
		t.Fatalf("held %d occurrences, want %d", len(snap.Occurrences), capacity)
	}
	// The bells must genuinely have arrived as separate rows, or the flood
	// never happened and the test proves nothing.
	if want := bells + 1 - capacity; snap.Dropped.Count != want {
		t.Fatalf("Dropped.Count = %d, want %d — %d bells did not arrive as separate rows",
			snap.Dropped.Count, want, bells)
	}
	var found bool
	for _, o := range snap.Occurrences {
		if o.ID == kept {
			found = true
		}
	}
	if !found {
		t.Fatalf("a drip of %d bells evicted the unread %q; a cheap row must never evict an expensive one",
			bells, build.Title)
	}
	// And what went instead: the oldest bells, which is the whole trade.
	if got := snap.Occurrences[len(snap.Occurrences)-1].Event.Title; got != build.Title {
		t.Fatalf("oldest held row is %q, want %q", got, build.Title)
	}
}

// The last resort, and the promise broken only out loud. When everything held
// is an unread must-ack row there is nothing cheap left to give up, so the
// oldest goes — and DroppedRecord, which lives outside the occurrence budget
// and is never itself evicted, is where the product says so.
func TestFeedFullOfUnreadMustAckEvictsTheOldestAndRecordsIt(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(FeedLimits{MaxOccurrences: 3, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	ids := make([]OccurrenceID, 0, 3)
	firstAt := clk.Now()
	for i := 0; i < 3; i++ {
		e := mustAckEvent(fmt.Sprintf("s%d", i), fmt.Sprintf("failure %d", i))
		e.At = clk.Now()
		ids = append(ids, f.Add(e))
		clk.Advance(time.Minute)
	}
	if got := f.Snapshot().Dropped.Count; got != 0 {
		t.Fatalf("Dropped.Count = %d before the feed was full, want 0", got)
	}

	overflow := mustAckEvent("s3", "failure 3")
	overflow.At = clk.Now()
	f.Add(overflow)

	snap := f.Snapshot()
	if len(snap.Occurrences) != 3 {
		t.Fatalf("held %d occurrences, want 3", len(snap.Occurrences))
	}
	for _, o := range snap.Occurrences {
		if o.ID == ids[0] {
			t.Fatalf("kept %q and evicted something newer; the last resort is oldest first", o.Event.Title)
		}
	}
	if snap.Dropped.Count != 1 {
		t.Fatalf("Dropped.Count = %d, want 1 — an unread must-ack was dropped silently", snap.Dropped.Count)
	}
	if !snap.Dropped.Oldest.Equal(firstAt) || !snap.Dropped.Newest.Equal(firstAt) {
		t.Fatalf("Dropped span is [%s, %s], want both %s", snap.Dropped.Oldest, snap.Dropped.Newest, firstAt)
	}
}

// The exclusion, and the one a plausible implementation gets wrong by reading
// level alone. A heuristic warning is a GUESS at a warning; it is
// informational for retention, so it is given up before an attested one and
// before a plain informational row that arrived after it.
func TestFeedHeuristicWarningIsInformational(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(FeedLimits{MaxOccurrences: 2, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	guess := Event{SessionID: "s-guess", Title: "pane looks idle", Kind: KindPaneWorkFinished, Trust: TrustHeuristic, Level: LevelWarning, At: clk.Now()}
	guessID := f.Add(guess)
	clk.Advance(time.Minute)

	attested := mustAckEvent("s-build", "the build failed")
	attested.At = clk.Now()
	attestedID := f.Add(attested)
	clk.Advance(time.Minute)

	later := bellEvent("s-noise", "ding")
	later.At = clk.Now()
	laterID := f.Add(later)

	snap := f.Snapshot()
	if len(snap.Occurrences) != 2 {
		t.Fatalf("held %d occurrences, want 2", len(snap.Occurrences))
	}
	held := map[OccurrenceID]bool{}
	for _, o := range snap.Occurrences {
		held[o.ID] = true
	}
	if held[guessID] {
		t.Fatalf("kept the heuristic warning %q: reading level alone made a guess unforgettable", guess.Title)
	}
	if !held[attestedID] {
		t.Fatalf("evicted the attested danger %q", attested.Title)
	}
	if !held[laterID] {
		t.Fatalf("evicted the later informational row %q while an OLDER informational one was held", later.Title)
	}
}

// Inside the must-ack tier the read axis still decides: a row the person has
// already seen goes before one they have not.
func TestFeedEvictsReadMustAckBeforeUnreadMustAck(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(FeedLimits{MaxOccurrences: 2, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	older := mustAckEvent("s0", "older unread failure")
	older.At = clk.Now()
	olderID := f.Add(older)
	clk.Advance(time.Minute)

	newer := mustAckEvent("s1", "newer read failure")
	newer.At = clk.Now()
	newerID := f.Add(newer)

	// MarkAllRead marks every row, so the one state that separates the two
	// tiers — a READ row newer than an unread one — is only reachable from
	// inside the package, exactly as TestFeedEvictsNewerReadBeforeOlderUnread
	// reaches for it.
	at := clk.Now()
	f.mu.Lock()
	f.items[1].ReadAt = &at
	f.mu.Unlock()

	clk.Advance(time.Minute)
	third := mustAckEvent("s2", "newest unread failure")
	third.At = clk.Now()
	thirdID := f.Add(third)

	snap := f.Snapshot()
	if len(snap.Occurrences) != 2 {
		t.Fatalf("held %d occurrences, want 2", len(snap.Occurrences))
	}
	for _, o := range snap.Occurrences {
		if o.ID == newerID {
			t.Fatalf("kept the READ must-ack %q; it is evicted before any unread one", o.Event.Title)
		}
	}
	held := map[OccurrenceID]bool{}
	for _, o := range snap.Occurrences {
		held[o.ID] = true
	}
	if !held[olderID] || !held[thirdID] {
		t.Fatalf("evicted an UNREAD must-ack while a read one was held; held %v", held)
	}
	if snap.Dropped.Count != 1 {
		t.Fatalf("Dropped.Count = %d, want 1", snap.Dropped.Count)
	}
}

// The ordinary case, both ends: under capacity the new axis changes nothing —
// every row is held, in arrival order, and nothing is recorded as dropped.
func TestFeedUnderCapacityGivesUpNothing(t *testing.T) {
	clk := NewManualClock()
	f, err := NewFeed(FeedLimits{MaxOccurrences: 10, MaxRetainedBytes: 1 << 20, MaxRunRetained: 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}

	want := []string{"ding", "the build failed", "pane looks idle", "note"}
	events := []Event{
		bellEvent("s0", want[0]),
		mustAckEvent("s1", want[1]),
		{SessionID: "s2", Title: want[2], Kind: KindPaneWorkFinished, Trust: TrustHeuristic, Level: LevelWarning},
		evEvent("s3", want[3]),
	}
	for _, e := range events {
		e.At = clk.Now()
		f.Add(e)
		clk.Advance(time.Minute)
	}

	snap := f.Snapshot()
	if len(snap.Occurrences) != len(want) {
		t.Fatalf("held %d occurrences, want %d", len(snap.Occurrences), len(want))
	}
	// Snapshot is newest first; the feed keeps arrival order underneath it.
	for i, title := range want {
		if got := snap.Occurrences[len(want)-1-i].Event.Title; got != title {
			t.Fatalf("row %d is %q, want %q", i, got, title)
		}
	}
	if snap.Dropped.Count != 0 {
		t.Fatalf("Dropped.Count = %d under capacity, want 0", snap.Dropped.Count)
	}
	if snap.UnreadCount != len(want) {
		t.Fatalf("UnreadCount = %d, want %d", snap.UnreadCount, len(want))
	}
}
