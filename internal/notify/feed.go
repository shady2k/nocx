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

// RunMember is one constituent of a collapsed row, retained so an expansion
// can show what the row compacted (D2). It carries four fields and no more:
// At, because an expansion whose rows share one timestamp is not worth
// opening; Title, because a run's titles differ, which is why collapse keeps
// the newest one; and ReadAt, because each constituent keeps its own unread
// mark.
//
// It deliberately carries no Level, no Trust and no Body. The ROW owns
// severity and detail — a member that could disagree with its row would be a
// second answer to one question, which is what AD-8 is about.
type RunMember struct {
	ID     OccurrenceID
	At     time.Time
	Title  string
	ReadAt *time.Time
}

// Occurrence is one raised event as the feed remembers it. Count and LastAt
// are the collapse fields; a lone occurrence has Count 1 and LastAt equal to
// its event's At.
//
// Run and RunDropped are the bounded tail (D2), and the invariant binding
// them to Count holds for EVERY occurrence at ALL times, from the add that
// creates it onwards:
//
//	Count == len(Run) + RunDropped
//
// A fresh occurrence therefore holds itself, so an expansion never has to
// special-case a run of one. RunDropped is what makes the expansion able to
// say "20 of 4310" rather than presenting a truncation as the whole.
type Occurrence struct {
	ID     OccurrenceID
	Event  Event
	ReadAt *time.Time
	Count  int
	LastAt time.Time

	// Run holds the newest MaxRunRetained constituents, OLDEST FIRST —
	// the same direction as items, and the renderer reverses it the same
	// way Snapshot does. Never nil.
	Run []RunMember

	// RunDropped counts the constituents this row no longer holds.
	RunDropped int
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

	// MaxRunRetained bounds the tail a collapsed row keeps (D2). Both ends
	// of the retention interval: a constituent is held from the Add that
	// created it until either the tail overflows or the row is evicted.
	//
	// Retaining ALL of them is not available — the flood case admits
	// 10 000 occurrences from one session, and keeping every one restores
	// exactly the unbounded growth collapse exists to prevent. Their
	// titles count against MaxRetainedBytes like every other byte: a bound
	// that excludes the thing that grows is not a bound.
	MaxRunRetained int
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
	// A row always holds itself, so a tail of zero is not "keep no tail" —
	// it is a feed whose Count invariant cannot hold on the first add.
	if limits.MaxRunRetained < 1 {
		return nil, errors.New("notify: MaxRunRetained must be at least 1")
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
		// The join gets an identity of its own even though Add returns the
		// ROW's id: the row is the thing the user reads once, the member is
		// the thing an expansion addresses.
		f.seq++
		f.appendRunMemberLocked(i, OccurrenceID(fmt.Sprintf("occ-%d", f.seq)), ev)
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
	// Run holds the row ITSELF, so Count == len(Run) + RunDropped from the
	// first add and an expansion never special-cases a run of one.
	f.items = append(f.items, Occurrence{
		ID: id, Event: ev, Count: 1, LastAt: ev.At,
		Run: []RunMember{{ID: id, At: ev.At, Title: ev.Title}},
	})
	f.bytes += eventBytes(ev) + int64(len(ev.Title))
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

// ChannelPipeline names the "channel" of a delivery that never reached a
// sink at all: admission refused the event under the router's global limits,
// so what failed is the pipeline itself rather than any one surface. Route
// failures name their route's resolved Target instead — the router is the
// only holder of "where", and its own word for the destination is the one
// the row repeats.
const ChannelPipeline = "the notification pipeline"

// RecordDeliveryFailure records ONE occurrence naming a delivery that failed
// after the raise was already answered, and returns its id (D3, nocx-r6pxp).
//
// notify.raise answers {} the moment the event is ACCEPTED. Under the policy
// the delivery may then happen when a debounce window closes, seconds later,
// with no caller left to fail to — so a failure past that point used to reach
// a logger.Warn and nothing else, which is the soft degrade visible only in a
// log that AGENTS.md condemns. This is where it becomes visible in the
// product: the centre already exists to remember what happened while you were
// not looking, and a notification that was accepted and never arrived is
// exactly that.
//
// It is admitted DIRECTLY, through Add, and never raised back through the
// pipeline that just failed. That is the constraint that bites: a failure row
// carried by the sink that is broken would fail the same way and produce a
// second failure row, and one broken sink would become an unbounded feed of
// complaints about being broken. Going through Add makes the recursion
// impossible by construction rather than by a caller remembering not to —
// nothing here resolves a route, so nothing here can fail a delivery.
//
// The row keeps the FAILED event's attribution and kind, so it lands beside
// the notification it is about and the renderer's kind-shaped presentation
// stays true. Kind is not a new value: it is a closed enum on the wire
// (contracts/notify.feed.read.schema.json), and a "delivery failed" kind is a
// wire-contract change (AGENTS.md rule 5) rather than something to smuggle
// past the schema from here.
//
// WHICH failures earn a row is the caller's decision, not this method's: the
// composition root binds the hosts, so it is where the one exemption lives
// (an ErrUnavailable channel does not exist on this host — internal/app/app.go,
// the result handler). This method builds and admits the row for whatever it
// is handed.
//
// Level is WARNING, not danger. Nothing was lost: the occurrence this row is
// about is in the feed one row below it, which is the whole point of the
// centre, and what degraded is a channel of nocx's own rather than the user's
// work. Danger is for a fact the user must act on now; a banner that did not
// appear while its message survived is a fact they should see and can act on
// at leisure — turning the permission back on, say, which is the commonest
// reason this row exists at all. And a run of them collapses into one counted
// row like any other run, so a channel that is broken rather than unlucky
// costs one row per burst.
//
// Trust is ATTESTED for the same reason the level is nocx's to set: this is a
// fact nocx observed about its own delivery, not something the program asked
// for.
func (f *Feed) RecordDeliveryFailure(ev Event, channel string, cause error) OccurrenceID {
	if channel == "" {
		// A table row may leave Destination.Target empty; the row still has
		// to say something rather than open with ": ".
		channel = "an unnamed channel"
	}
	title := "Not delivered"
	if ev.Title != "" {
		title += ": " + ev.Title
	}
	return f.Add(Event{
		SessionID: ev.SessionID,
		Title:     title,
		Body:      fmt.Sprintf("%s could not deliver it: %v", channel, cause),
		Kind:      ev.Kind,
		Trust:     TrustAttested,
		Level:     LevelWarning,
		// The instant is the FAILURE's, not the event's: the delivery it
		// reports on may have been accepted a whole debounce window earlier.
		At:          f.clock.Now(),
		Attribution: ev.Attribution,
	})
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
		// Every RETAINED member too, and this is the asymmetry the design
		// turns on: a later join clears the ROW's mark and leaves these
		// alone. They were seen; the new one was not, and that difference
		// is the whole reason an expansion shows individual marks.
		for j := range f.items[i].Run {
			if f.items[i].Run[j].ReadAt == nil {
				t := now
				f.items[i].Run[j].ReadAt = &t
			}
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
		// The run is COPIED, not shared: the feed goes on mutating members
		// in place — the tail slides on every join, MarkAllRead stamps
		// them — and a returned snapshot must be a value, not a window
		// onto state that keeps moving. Without this it is also a data
		// race between a transport reading a snapshot and the next Add.
		o := f.items[i]
		o.Run = make([]RunMember, len(f.items[i].Run))
		copy(o.Run, f.items[i].Run)
		out = append(out, o)
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
		f.bytes -= eventBytes(victim.Event) + runBytes(victim.Run)
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

// runBytes is what a row's retained tail costs the byte budget. Titles only:
// a member carries no body, and its id and instant are fixed-width.
func runBytes(run []RunMember) int64 {
	var n int64
	for _, m := range run {
		n += int64(len(m.Title))
	}
	return n
}

// appendRunMemberLocked adds one constituent to the row at idx and keeps the
// tail inside MaxRunRetained, dropping the OLDEST and recording that it did.
// Both halves of the accounting happen here so they cannot drift apart:
// every byte admitted is admitted on the line that admits the member, and
// every byte released is released on the line that drops it. Caller holds mu.
func (f *Feed) appendRunMemberLocked(idx int, id OccurrenceID, ev Event) {
	o := &f.items[idx]
	o.Run = append(o.Run, RunMember{ID: id, At: ev.At, Title: ev.Title})
	f.bytes += int64(len(ev.Title))
	for len(o.Run) > f.limits.MaxRunRetained {
		f.bytes -= int64(len(o.Run[0].Title))
		// Slide rather than reslice from the front: the backing array
		// stays put and its capacity stays usable, so a run of ten
		// thousand joins reallocates a bounded number of times. Safe
		// because Snapshot hands out copies.
		copy(o.Run, o.Run[1:])
		o.Run = o.Run[:len(o.Run)-1]
		o.RunDropped++
	}
}
