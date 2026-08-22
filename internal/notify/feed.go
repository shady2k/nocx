// Package-local: the feed is the only holder of "what was raised". It never
// decides delivery, and the router never decides membership (AD-8).
//
// In memory, deliberately: ADR-0033 forbids the renderer holding facts, and
// this design holds none on disk either — the feed dies with the process and
// the spec says so with both ends named (§7).

package notify

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// OccurrenceID is the stable identity of one raised event. Monotonic within
// the process, which the feed also relies on for ordering: the feed never
// outlives the process, so there is nothing for a random id to protect and
// an ordinal costs no lookup.
type OccurrenceID string

// Occurrence is one raised event as the feed remembers it. Count and LastAt
// are the collapse fields (Task 2); a lone occurrence has Count 1 and LastAt
// equal to its event's At.
type Occurrence struct {
	ID     OccurrenceID
	Event  Event
	ReadAt *time.Time
	Count  int
	LastAt time.Time
}

// DroppedRecord is the visible half of eviction. It lives OUTSIDE the
// occurrence budget and is never evicted: a soft degrade must be visible in
// the product, not only in a log.
type DroppedRecord struct {
	Count  int
	Oldest time.Time
	Newest time.Time
}

// FeedLimits bounds the feed in both currencies. Rows alone are not a usable
// unit of account: notify.raise admits 4096 runes of title and 4096 of body
// (internal/transport/ws_notify.go:82).
type FeedLimits struct {
	MaxOccurrences int

	// MaxRetainedBytes bounds the title and body bytes held, with ONE
	// exception, and both ends of it are named here: the budget binds
	// whenever more than one occurrence is held, and the feed never evicts
	// its last UNREAD occurrence — so a single unread occurrence larger
	// than the whole budget is kept, and the feed sits over budget until a
	// second one arrives (a single READ one is evicted like any other).
	//
	// Keeping it is the intended behaviour: a feed holding nothing is worse
	// than a feed slightly over budget. In production the case cannot arise
	// — notify.raise admits 4096 runes of title and 4096 of body
	// (internal/transport/ws_notify.go) against a 1 MiB budget — so the
	// exception is a floor under a pathological configuration, not a hole
	// in the bound. Pinned by TestFeedKeepsOneOccurrenceOverBudget.
	MaxRetainedBytes int64

	// CollapseWindow is how long a run of repeats stays one row. An arrival
	// sharing a CollapseKey with the newest matching occurrence, within this
	// long of that occurrence's LastAt, compacts into it.
	CollapseWindow time.Duration
}

// CollapseKey identifies one run of repeats. It reads no Title and no Body:
// a program printing a fresh title each time would otherwise restore
// unbounded growth, and the same reasoning already governs DebounceKey.
//
// Level is here and NOT in DebounceKey, deliberately. The two keys answer
// different questions: this one asks "is this row the same run", the debounce
// one asks "is this the same delivery stream". Occurrences carry their own
// identity (OccurrenceID), so neither key is being used as one.
type CollapseKey struct {
	Backend string
	Session string
	Kind    Kind
	Level   Level
}

func collapseKeyOf(ev Event) CollapseKey {
	return CollapseKey{Backend: ev.Attribution.Backend, Session: ev.SessionID, Kind: ev.Kind, Level: ev.Level}
}

// FeedSnapshot is the authoritative read. The renderer reconciles against
// Revision; a gap of more than one means a missed change notification and the
// renderer refetches (design §8).
type FeedSnapshot struct {
	Revision    uint64
	UnreadCount int
	Occurrences []Occurrence
	Dropped     DroppedRecord
}

// Feed is the bounded in-memory record. Safe for concurrent use.
type Feed struct {
	limits FeedLimits
	clock  Clock

	// pubMu serialises a mutation together with its publication, so the
	// observer is handed revisions in mutation order. Without it two
	// mutators could capture revisions 1 and 2, both release mu, and then
	// race — publishing 2 before 1, while OnChange's contract says the
	// observer sees the state the revision names and the renderer treats a
	// gap as a missed change.
	//
	// It is taken BEFORE mu and never inside it. Taking it while holding mu
	// would deadlock the very reentrancy OnChange promises: a second
	// mutator would sit on mu waiting for pubMu, while the observer holding
	// pubMu called Snapshot and waited for mu. Ordering publication already
	// serialises the mutators against each other, so nothing is lost by
	// covering the mutation with the same lock — and the callback still
	// runs with mu released, which is what makes Snapshot legal inside it.
	pubMu    sync.Mutex
	mu       sync.Mutex
	seq      uint64
	revision uint64
	items    []Occurrence // oldest first
	bytes    int64
	dropped  DroppedRecord
	onChange func(uint64)
}

// NewFeed validates the limits. A feed that cannot hold one occurrence is a
// configuration error, not a feed that silently holds nothing.
func NewFeed(limits FeedLimits, clock Clock) (*Feed, error) {
	if limits.MaxOccurrences < 1 {
		return nil, errors.New("notify: MaxOccurrences must be at least 1")
	}
	if limits.MaxRetainedBytes < 1 {
		return nil, errors.New("notify: MaxRetainedBytes must be at least 1")
	}
	if limits.CollapseWindow <= 0 {
		return nil, errors.New("notify: CollapseWindow must be positive")
	}
	if clock == nil {
		return nil, errors.New("notify: feed needs a clock")
	}
	return &Feed{limits: limits, clock: clock}, nil
}

// OnChange registers the single change observer. It fires AFTER the mutation
// is visible to Snapshot, so an observer that immediately reads gets the
// state the revision names — and it fires in mutation order, one call per
// revision with no gaps and no reordering, whatever the concurrency of the
// mutators (pubMu). The observer may call Snapshot; it must not call a
// mutator, which would deadlock on the publication lock its own call holds.
func (f *Feed) OnChange(fn func(revision uint64)) {
	f.mu.Lock()
	f.onChange = fn
	f.mu.Unlock()
}

// Add records one occurrence and returns its id. An arrival that joins a run
// already held collapses into it and returns THAT occurrence's id, so a
// caller cannot tell a fresh row from a joined one by the id alone — which is
// the point: the run is one thing the user reads once.
func (f *Feed) Add(ev Event) OccurrenceID {
	f.pubMu.Lock()
	defer f.pubMu.Unlock()
	f.mu.Lock()
	if i := f.collapsibleLocked(ev); i >= 0 {
		f.bytes -= eventBytes(f.items[i].Event)
		f.items[i].Count++
		f.items[i].LastAt = ev.At
		f.items[i].Event = ev
		f.items[i].ReadAt = nil // the count changed: there is something new to see
		f.bytes += eventBytes(ev)
		id := f.items[i].ID
		f.enforceLocked()
		f.revision++
		rev, fn := f.revision, f.onChange
		f.mu.Unlock()
		if fn != nil {
			fn(rev)
		}
		return id
	}
	f.seq++
	id := OccurrenceID(fmt.Sprintf("occ-%d", f.seq))
	f.items = append(f.items, Occurrence{ID: id, Event: ev, Count: 1, LastAt: ev.At})
	f.bytes += eventBytes(ev)
	f.enforceLocked()
	f.revision++
	rev := f.revision
	fn := f.onChange
	f.mu.Unlock()
	if fn != nil {
		fn(rev)
	}
	return id
}

// MarkAllRead marks every unread occurrence read and returns the new revision.
func (f *Feed) MarkAllRead() uint64 {
	now := f.clock.Now()
	f.pubMu.Lock()
	defer f.pubMu.Unlock()
	f.mu.Lock()
	for i := range f.items {
		if f.items[i].ReadAt == nil {
			t := now
			f.items[i].ReadAt = &t
		}
	}
	f.revision++
	rev := f.revision
	fn := f.onChange
	f.mu.Unlock()
	if fn != nil {
		fn(rev)
	}
	return rev
}

// Snapshot returns the feed newest first.
func (f *Feed) Snapshot() FeedSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Occurrence, 0, len(f.items))
	unread := 0
	for i := len(f.items) - 1; i >= 0; i-- {
		out = append(out, f.items[i])
		if f.items[i].ReadAt == nil {
			unread++
		}
	}
	return FeedSnapshot{Revision: f.revision, UnreadCount: unread, Occurrences: out, Dropped: f.dropped}
}

// collapsibleLocked returns the index of the newest occurrence this event
// joins, or -1. Only the NEWEST occurrence of the key is a candidate: a run
// is consecutive by definition, and reaching backwards past a different event
// would merge two runs separated by something the user needs between them.
// Caller holds mu.
func (f *Feed) collapsibleLocked(ev Event) int {
	want := collapseKeyOf(ev)
	for i := len(f.items) - 1; i >= 0; i-- {
		if collapseKeyOf(f.items[i].Event) != want {
			continue
		}
		if ev.At.Sub(f.items[i].LastAt) > f.limits.CollapseWindow {
			return -1
		}
		return i
	}
	return -1
}

// enforceLocked evicts until both budgets hold, with the single exception
// FeedLimits.MaxRetainedBytes states: it returns with f.bytes still over
// budget when the only thing held is one unread occurrence, because
// victimLocked refuses to name it. Both ends: eviction runs while more than
// one occurrence is held, and stops at the last unread one. Read occurrences
// go first; an unread one is evicted only when no read one is left. Caller
// holds mu.
//
// Refusing the new occurrence instead would freeze the feed at the moment a
// flood began, which is the failure the design's flood argument rejects
// (spec §7) — so eviction is the policy and it is always recorded.
func (f *Feed) enforceLocked() {
	for len(f.items) > f.limits.MaxOccurrences || f.bytes > f.limits.MaxRetainedBytes {
		idx := f.victimLocked()
		if idx < 0 {
			return
		}
		victim := f.items[idx]
		f.bytes -= eventBytes(victim.Event)
		f.items = append(f.items[:idx], f.items[idx+1:]...)
		f.recordDroppedLocked(victim)
	}
}

// victimLocked picks the oldest read occurrence, or the oldest occurrence of
// any kind when none is read. Never the one just added when another exists.
func (f *Feed) victimLocked() int {
	if len(f.items) == 0 {
		return -1
	}
	for i := range f.items {
		if f.items[i].ReadAt != nil {
			return i
		}
	}
	if len(f.items) == 1 {
		return -1
	}
	return 0
}

func (f *Feed) recordDroppedLocked(o Occurrence) {
	at := o.Event.At
	if f.dropped.Count == 0 || at.Before(f.dropped.Oldest) {
		f.dropped.Oldest = at
	}
	if at.After(f.dropped.Newest) {
		f.dropped.Newest = at
	}
	f.dropped.Count += o.Count
}

func eventBytes(ev Event) int64 { return int64(len(ev.Title) + len(ev.Body)) }
