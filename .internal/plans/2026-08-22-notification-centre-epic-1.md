# The bell shows what you missed — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent nocx-p0xhg`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** A user opens the bell in the activity bar and sees what happened while they were not looking — including events no banner showed them — and marks them read.

**Architecture:** An `Ingress` stage stamps what nocx owns (`At`, `Level`, `Attribution`, a minted `OccurrenceID`) exactly once, then fans out to two independent consumers: a bounded in-memory `Feed` that remembers, and the existing `Policy` → `Router` → sinks path that delivers. Membership and delivery stop being one decision. The renderer reads the feed as a revisioned snapshot plus a revision-only change hint, the same shape `internal/settings` already uses.

**Tech Stack:** Go (`internal/notify`, `internal/transport`, `internal/app`), TypeScript + Solid (`frontend/src`), JSON Schema contracts with generated renderer types, Playwright for the acceptance check.

**Spec:** `.internal/specs/2026-08-22-notification-centre-design.md` (commit `81a13469`). **Epic:** `nocx-p0xhg`.

## Global Constraints

- **AD-1:** the feed is control plane only. No PTY byte enters it, ever.
- **AD-6:** the backend never parses the byte stream. OSC parsing stays in `frontend/src/renderers/xterm.ts`.
- **AD-8:** the `Router` remains the only holder of "where"; the `Feed` is the only holder of "what was raised". Neither answers the other's question.
- **ADR-0047 §3, non-negotiable:** trust classes are hard capability bounds. `TrustHeuristic` may never reach a sink whose `LeavesMachine()` is true, and `NewRouter` refuses such a table at construction (`ErrTrustCapability`). No task in this plan touches that check, and no task makes it configurable.
- **ADR-0047 §2.2:** protected fields are stamped by nocx and are absent from the wire, never validated on it.
- **AGENTS.md rule 5:** every JSON-RPC result shape gets a schema in `contracts/` with `additionalProperties: false` and an explicit `required`, a generated renderer type, a `_DTOConformsToContract` case and a `_OverTheWireConformsToContract` case.
- **UI kit:** surfaces import from `frontend/src/ui/`. A surface may **place** a kit component and may never **repaint** it. New kit components get a module, a CSS file in `styles/components/`, a stable identity class, a test and a row in `frontend/src/ui/README.md`.
- **Commit format:** `<type>(<scope>): <imperative subject> (<bead-id>)`, body as prose explaining what was wrong and why this way.
- **Feed is in memory only.** Nothing is written to disk; nothing survives the process. Do not add a `DocumentStore` document.

---

### Task 1: The occurrence record and the bounded feed

**Files:**

- Create: `internal/notify/feed.go`
- Test: `internal/notify/feed_test.go`

**Interfaces:**

- Consumes: `notify.Event`, `notify.Kind`, `notify.Level`, `notify.Attribution` from `internal/notify/notify.go`.
- Produces:
  - `type OccurrenceID string`
  - `type Occurrence struct { ID OccurrenceID; Event Event; ReadAt *time.Time; Count int; LastAt time.Time }`
  - `type FeedLimits struct { MaxOccurrences int; MaxRetainedBytes int64 }`
  - `func NewFeed(limits FeedLimits, clock Clock) (*Feed, error)`
  - `func (f *Feed) Add(ev Event) OccurrenceID`
  - `func (f *Feed) Snapshot() FeedSnapshot`
  - `func (f *Feed) MarkAllRead() uint64`
  - `type FeedSnapshot struct { Revision uint64; UnreadCount int; Occurrences []Occurrence; Dropped DroppedRecord }`
  - `type DroppedRecord struct { Count int; Oldest, Newest time.Time }`
  - `func (f *Feed) OnChange(fn func(revision uint64))`

**Acceptance Criteria:**

- `Add` returns a distinct id per call and the snapshot lists occurrences newest first.
- Exceeding `MaxOccurrences` evicts the oldest **read** occurrence first, and only evicts an unread one when no read one exists.
- Exceeding `MaxRetainedBytes` (UTF-8 `Title`+`Body` of every held occurrence) evicts by the same rule until the feed is inside the budget.
- Every eviction increments `Dropped.Count` and widens `[Oldest, Newest]`; the dropped record is **outside** the occurrence budget and is never itself evicted.
- `Revision` is monotonic and bumps on `Add`, on `MarkAllRead` and on an eviction.
- `MarkAllRead` sets `ReadAt` on every unread occurrence and returns the new revision.
- `OnChange` fires after the mutation is visible to `Snapshot`, never before.

- [ ] **Step 1: Write the failing test for eviction order**

```go
// internal/notify/feed_test.go
package notify

import (
	"testing"
	"time"
)

func evEvent(session, title string) Event {
	return Event{SessionID: session, Title: title, Kind: KindProgramNotify, Trust: TrustProgramRequest, Level: LevelInfo}
}

func TestFeedEvictsReadBeforeUnread(t *testing.T) {
	f, err := NewFeed(FeedLimits{MaxOccurrences: 2, MaxRetainedBytes: 1 << 20}, RealClock{})
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	first := f.Add(evEvent("s1", "old-unread"))
	f.Add(evEvent("s2", "will-be-read"))
	f.MarkAllRead()
	f.Add(evEvent("s3", "new"))

	snap := f.Snapshot()
	if len(snap.Occurrences) != 2 {
		t.Fatalf("held %d occurrences, want 2", len(snap.Occurrences))
	}
	// The READ one goes, not the older unread one: an unread row is the
	// only thing the feed exists to protect.
	if snap.Occurrences[len(snap.Occurrences)-1].ID != first {
		t.Fatalf("evicted the unread occurrence; oldest held is %q", snap.Occurrences[len(snap.Occurrences)-1].Event.Title)
	}
	if snap.Dropped.Count != 1 {
		t.Fatalf("Dropped.Count = %d, want 1", snap.Dropped.Count)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/notify -run TestFeedEvictsReadBeforeUnread -v`
Expected: FAIL — `undefined: NewFeed`.

- [ ] **Step 3: Write `internal/notify/feed.go`**

```go
// Package-local: the feed is the only holder of "what was raised". It never
// decides delivery, and the router never decides membership (AD-8).
//
// In memory, deliberately: ADR-0048 forbids the renderer holding facts, and
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
	MaxOccurrences   int
	MaxRetainedBytes int64
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
	if clock == nil {
		return nil, errors.New("notify: feed needs a clock")
	}
	return &Feed{limits: limits, clock: clock}, nil
}

// OnChange registers the single change observer. It fires AFTER the mutation
// is visible to Snapshot, so an observer that immediately reads gets the
// state the revision names.
func (f *Feed) OnChange(fn func(revision uint64)) {
	f.mu.Lock()
	f.onChange = fn
	f.mu.Unlock()
}

// Add records one occurrence and returns its id.
func (f *Feed) Add(ev Event) OccurrenceID {
	f.mu.Lock()
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

// enforceLocked evicts until both budgets hold. Read occurrences go first;
// an unread one is evicted only when no read one is left. Caller holds mu.
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
```

- [ ] **Step 4: Run the test and watch it pass**

Run: `go test ./internal/notify -run TestFeedEvictsReadBeforeUnread -v`
Expected: PASS.

- [ ] **Step 5: Add the remaining acceptance tests**

Write, in the same file: `TestFeedEvictsOnByteBudget` (two occurrences whose titles exceed `MaxRetainedBytes`, assert the feed shrinks and `Dropped.Count` rises); `TestFeedRevisionIsMonotonic` (assert `Add`, `MarkAllRead` and an eviction each raise it and none lowers it); `TestFeedOnChangeSeesTheMutation` (an `OnChange` handler that calls `Snapshot` observes the revision it was handed); `TestFeedRejectsUselessLimits` (`MaxOccurrences: 0` and `MaxRetainedBytes: 0` each return an error).

- [ ] **Step 6: Run the package and commit**

```bash
go test ./internal/notify/... -race
git add internal/notify/feed.go internal/notify/feed_test.go
git commit -m "feat(notify): the feed remembers what was raised, bounded in both currencies (nocx-p0xhg.1)"
```

---

### Task 2: Flood collapse

**Files:**

- Modify: `internal/notify/feed.go`
- Test: `internal/notify/feed_test.go`

**Interfaces:**

- Consumes: `Feed`, `Occurrence` from Task 1.
- Produces: `type CollapseKey struct { Backend, Session string; Kind Kind; Level Level }`, and `FeedLimits` gains `CollapseWindow time.Duration`.

**Acceptance Criteria:**

- Consecutive `Add` calls sharing a `CollapseKey`, inside `CollapseWindow` of the previous occurrence's `LastAt`, compact into the existing occurrence: `Count` rises, `LastAt` and `Event.Title` become the newest, and no new id is minted.
- An occurrence whose key matches but arrives **after** the window opens a new occurrence with a new id.
- An occurrence whose key differs in **any** component — including `Level` — never collapses into the other.
- Collapsing into a **read** occurrence marks it unread again: the count changed, so there is something new to see.
- The collapse window is strictly shorter than any read lifetime by construction: it is a duration, and read state is cleared only by `MarkAllRead`.

- [ ] **Step 1: Write the failing tests**

```go
func TestFeedCollapsesConsecutiveRun(t *testing.T) {
	clk := NewManualClock()
	f, _ := NewFeed(FeedLimits{MaxOccurrences: 50, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)

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
}

func TestFeedNeverCollapsesAcrossLevel(t *testing.T) {
	clk := NewManualClock()
	f, _ := NewFeed(FeedLimits{MaxOccurrences: 50, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)

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
	f, _ := NewFeed(FeedLimits{MaxOccurrences: 50, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)

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
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/notify -run 'TestFeedCollapses|TestFeedNeverCollapses|TestFeedOpensANew' -v`
Expected: FAIL — `unknown field CollapseWindow in struct literal`.

- [ ] **Step 3: Add the key and the collapse branch**

Add to `internal/notify/feed.go`:

```go
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
```

Add `CollapseWindow time.Duration` to `FeedLimits`, reject a non-positive value in `NewFeed`, and replace the append in `Add` with:

```go
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
```

```go
// collapsibleLocked returns the index of the newest occurrence this event
// joins, or -1. Only the NEWEST occurrence of the key is a candidate: a run
// is consecutive by definition, and reaching backwards past a different event
// would merge two runs separated by something the user needs between them.
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
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/notify -run TestFeed -v -race`
Expected: PASS, all feed tests including Task 1's.

- [ ] **Step 5: Add the flood property test**

```go
func TestFloodDoesNotEvictAnUnreadDangerFromAnotherSession(t *testing.T) {
	clk := NewManualClock()
	f, _ := NewFeed(FeedLimits{MaxOccurrences: 8, MaxRetainedBytes: 4096, CollapseWindow: 30 * time.Second}, clk)

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
}
```

- [ ] **Step 6: Run and commit**

```bash
go test ./internal/notify/... -race
git add internal/notify/feed.go internal/notify/feed_test.go
git commit -m "feat(notify): a flood is one row with a count, not ten thousand (nocx-p0xhg.2)"
```

---

### Task 3: Ingress — stamping moves, once

**Files:**

- Create: `internal/notify/ingress.go`
- Test: `internal/notify/ingress_test.go`
- Modify: `internal/notify/notify.go` (remove `ev.At = time.Now()` from `Router.Raise`)
- Modify: `internal/notify/notify.go` (add `Backend` to `Attribution`)

**Interfaces:**

- Consumes: `Feed` (Tasks 1–2), `Policy` from `internal/notify/policy.go`.
- Produces:
  - `type Submitter interface { Submit(ev Event) Disposition }` — satisfied by `*Policy` as it stands.
  - `func NewIngress(feed *Feed, next Submitter, clock Clock) (*Ingress, error)`
  - `func (i *Ingress) Admit(ctx context.Context, ev Event) (OccurrenceID, error)`
  - `func (i *Ingress) Raise(ctx context.Context, ev Event) Outcome` — so `*Ingress` satisfies `transport.NotifyRaiser` and replaces `*Policy` at that seam.

**Acceptance Criteria:**

- `Admit` stamps `ev.At` from the injected clock when it is zero and leaves a non-zero `At` alone (a relay replaying a batch already carries its own instants — helper design D15 reserves that path).
- `Router.Raise` no longer stamps `At`; a `Router` used directly with a zero `At` is a programming error the ingress prevents, and `notify.go`'s comment says so.
- `Admit` records into the feed **before** submitting for delivery, so a panic in delivery cannot lose the record.
- An event whose policy disposition is `DispositionSuppressed` is still in the feed.
- `Attribution.Backend` is carried through unchanged and reaches the feed.

- [ ] **Step 1: Write the failing test**

```go
// internal/notify/ingress_test.go
package notify

import (
	"context"
	"testing"
	"time"
)

type recordingSubmitter struct{ got []Event; disp Disposition }

func (r *recordingSubmitter) Submit(ev Event) Disposition {
	r.got = append(r.got, ev)
	return r.disp
}

func TestIngressRecordsASuppressedEvent(t *testing.T) {
	clk := NewManualClock()
	feed, _ := NewFeed(FeedLimits{MaxOccurrences: 10, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)
	sub := &recordingSubmitter{disp: DispositionSuppressed}
	in, err := NewIngress(feed, sub, clk)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}

	id, err := in.Admit(context.Background(), Event{
		SessionID: "s1", Title: "quiet", Kind: KindProgramNotify, Trust: TrustProgramRequest, Level: LevelInfo,
	})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}

	snap := feed.Snapshot()
	if len(snap.Occurrences) != 1 || snap.Occurrences[0].ID != id {
		t.Fatal("a suppressed event is missing from the feed — which is exactly the event the feed exists for")
	}
	if snap.Occurrences[0].Event.At.IsZero() {
		t.Fatal("the occurrence was filed with a zero timestamp; ingress must stamp At")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/notify -run TestIngressRecordsASuppressedEvent -v`
Expected: FAIL — `undefined: NewIngress`.

- [ ] **Step 3: Write `internal/notify/ingress.go`**

```go
package notify

import (
	"context"
	"errors"
)

// Submitter is the delivery half ingress hands an event to. *Policy satisfies
// it; the narrow interface is what keeps ingress testable without a router.
type Submitter interface {
	Submit(ev Event) Disposition
}

// Ingress is the one entry point of the notification pipeline: it stamps the
// fields nocx owns, records the occurrence, and only then submits it for
// delivery. Membership and delivery become two decisions here, which is the
// whole inversion — before this, the policy sat in front of the router and a
// suppressed event was destroyed, so the events most worth seeing were
// exactly the ones nothing remembered.
//
// It is also the local interface a remote relay's notify service may later be
// the remote half of: the remote helper design's D17 forbids a helper service
// that has no local counterpart, so this type existing is the precondition,
// not a speculative hook.
type Ingress struct {
	feed  *Feed
	next  Submitter
	clock Clock
}

func NewIngress(feed *Feed, next Submitter, clock Clock) (*Ingress, error) {
	if feed == nil {
		return nil, errors.New("notify: ingress needs a feed")
	}
	if next == nil {
		return nil, errors.New("notify: ingress needs a submitter")
	}
	if clock == nil {
		return nil, errors.New("notify: ingress needs a clock")
	}
	return &Ingress{feed: feed, next: next, clock: clock}, nil
}

// Admit stamps, records, then submits — in that order. Recording first is
// deliberate: a delivery path that panics or blocks must not be able to lose
// the record, because the record is the only thing that survives the moment.
//
// A non-zero At is left alone. A relay replaying a batch it buffered while
// nothing was attached carries its own instants, and restamping them "now"
// would file yesterday's session end as having happened at reconnect.
func (i *Ingress) Admit(ctx context.Context, ev Event) (OccurrenceID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if ev.At.IsZero() {
		ev.At = i.clock.Now()
	}
	id := i.feed.Add(ev)
	i.next.Submit(ev)
	return id, nil
}

// Raise satisfies the transport's NotifyRaiser so ingress replaces the policy
// at that seam. The outcome stays empty for the same reason Policy.Raise's
// does: delivery is asynchronous past the debounce window, and a failure
// surfaces through the policy's result handler rather than at this caller.
func (i *Ingress) Raise(ctx context.Context, ev Event) Outcome {
	if _, err := i.Admit(ctx, ev); err != nil {
		return Outcome{Err: err}
	}
	return Outcome{}
}
```

- [ ] **Step 4: Move the stamping out of the router**

In `internal/notify/notify.go`, delete `ev.At = time.Now()` from `Router.Raise` and replace the comment that announced it:

```go
// At is NOT stamped here any more. Ingress is the first nocx-owned stage and
// stamps it once (ingress.go); a second stamp here would overwrite the instant
// a replayed batch carries. A zero At reaching this point means something
// bypassed ingress, which is a wiring defect rather than a value to repair.
func (r *Router) Raise(ctx context.Context, ev Event) Outcome {
	routes := r.Resolve(ev.Kind, ev.Trust, RouteRaise)
```

- [ ] **Step 5: Add `Backend` to `Attribution`**

In `internal/notify/notify.go`:

```go
type Attribution struct {
	// Backend names which backend raised this — "local" for this machine,
	// the same vocabulary internal/commandnames.LocalRoute already uses for
	// the same idea. nocx-if6 phase A makes session identity
	// (backendId, sessionId); carrying it from the first commit is what stops
	// every feed row needing a retrofit when the relay lands.
	Backend string

	// Tab keeps the old word on purpose (nocx-ehkvy) ...
	Tab     string
	Host    string
	Session string
}
```

- [ ] **Step 6: Run the whole package and fix the fallout**

Run: `go test ./internal/notify/... -race`
Expected: PASS. Router tests that asserted `At` was stamped by `Raise` now stamp it themselves; change those tests rather than restoring the line.

- [ ] **Step 7: Add the failure-path tests the spec names (§10)**

Each of these has a paired "and on an ordinary machine it succeeds" already
present above — a failure-path test without its pair is how `contentkey`
shipped a key that was obtainable nowhere.

```go
func TestIngressRecordsWhenTheSinkRefuses(t *testing.T) {
	// The banner refuses (no authorisation, or no host bound at all) and the
	// occurrence is STILL in the feed. If a refused delivery could remove the
	// record, the one case the centre exists for — nothing reached you —
	// would be the one case it forgets.
}

func TestIngressUnderAFullFeed(t *testing.T) {
	// MaxOccurrences: 1. Admit twice. Assert: the second Admit returns a valid
	// id, the feed holds one occurrence, Dropped.Count is 1, and the delivery
	// path was still invoked for BOTH. A full feed must not become a silent
	// filter on delivery — those are two decisions and this is the whole point.
}

func TestMarkAllReadRacingAdmit(t *testing.T) {
	// -race, with N goroutines calling Admit and M calling MarkAllRead.
	// Assert no data race and that the final revision is at least N+M: every
	// mutation must have bumped it, or a renderer can miss a change and never
	// learn it did.
}

func TestOccurrenceIntervalHasBothEnds(t *testing.T) {
	// AGENTS.md rule 3 wants an interval, not a moment. The invariant: an
	// occurrence is present in Snapshot() from the return of Admit until it
	// is evicted. Assert the START (present immediately after Admit returns,
	// before any delivery has completed) and the END (absent after the
	// eviction that Dropped.Count records, and not before it).
}
```

Run: `go test ./internal/notify -run 'TestIngress|TestMarkAllReadRacing|TestOccurrenceInterval' -race -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/notify/ingress.go internal/notify/ingress_test.go internal/notify/notify.go internal/notify/notify_test.go
git commit -m "feat(notify): ingress stamps once, and records before it delivers (nocx-p0xhg.3)"
```

---

### Task 4: `session.ended` — the feed's first honest source

**Files:**

- Modify: `internal/transport/ws.go` (`monitorExit`, around line 2317)
- Test: `internal/transport/ws_notify_session_ended_test.go`

**Interfaces:**

- Consumes: `notify.KindSessionEnded`, `notify.TrustAttested`, `Ingress.Admit` (Task 3).
- Produces: nothing new; a raise at an existing site.

**Acceptance Criteria:**

- When a session ends, exactly one `session.ended` event is admitted, with `Trust: notify.TrustAttested` — it comes from the session registry, not from a parsed byte.
- `Level` maps from the cause: `ExitExited` with status 0 → `LevelSuccess`; `ExitExited` with a non-zero status → `LevelWarning`; `ExitInterrupted` → `LevelWarning`.
- The title names the host and the cause; the body carries the exit status when there is one. Neither is built from any byte of the stream (AD-6).
- `Attribution` carries `Backend: "local"`, the session id and the host.
- A server with no ingress wired (devharness, e2e without the feed) still emits the `exit` notification and does not panic.

- [ ] **Step 1: Write the failing test**

```go
// internal/transport/ws_notify_session_ended_test.go
package transport

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/notify"
)

type capturingIngress struct{ events []notify.Event }

func (c *capturingIngress) Raise(ctx context.Context, ev notify.Event) notify.Outcome {
	c.events = append(c.events, ev)
	return notify.Outcome{}
}

func TestSessionEndRaisesAnAttestedEvent(t *testing.T) {
	cap := &capturingIngress{}
	srv := newTestServerWithNotifyRaiser(t, cap) // helper: see step 3
	sess := srv.openTestSession(t)
	srv.endTestSession(t, sess, "exited", 1)

	if len(cap.events) != 1 {
		t.Fatalf("raised %d events, want 1", len(cap.events))
	}
	got := cap.events[0]
	if got.Kind != notify.KindSessionEnded {
		t.Fatalf("Kind = %q, want %q", got.Kind, notify.KindSessionEnded)
	}
	if got.Trust != notify.TrustAttested {
		t.Fatalf("Trust = %q — a registry fact must be attested, not a program request", got.Trust)
	}
	if got.Level != notify.LevelWarning {
		t.Fatalf("Level = %q, want warning for a non-zero exit status", got.Level)
	}
	if got.Attribution.Backend != "local" {
		t.Fatalf("Attribution.Backend = %q, want %q", got.Attribution.Backend, "local")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/transport -run TestSessionEndRaisesAnAttestedEvent -v`
Expected: FAIL — no raise happens; `raised 0 events, want 1`.

- [ ] **Step 3: Raise it in `monitorExit`**

In `internal/transport/ws.go`, immediately after `cause, status := sess.ExitOutcome()` and before the `wconn.TryNotify("exit", ...)` call:

```go
	// The feed's first attested source (spec §9). This is a registry fact,
	// not a parsed byte: AD-6 is untouched because nothing here reads the
	// stream — the cause comes from the session layer, which is its single
	// owner (nocx-ictcq).
	//
	// It is raised whether or not a renderer is attached. That is the point:
	// the exit notification below needs a connection and this does not, so a
	// session that ended while the renderer was detached is still remembered.
	if s.notifyRaiser != nil {
		level := notify.LevelWarning
		title := fmt.Sprintf("Session on %s was interrupted", sess.Host())
		body := ""
		if cause == session.ExitExited {
			title = fmt.Sprintf("Session on %s ended", sess.Host())
			if status == 0 {
				level = notify.LevelSuccess
			} else {
				body = fmt.Sprintf("exit status %d", status)
			}
		}
		s.notifyRaiser.Raise(context.Background(), notify.Event{
			SessionID: string(sess.ID()),
			Title:     title,
			Body:      body,
			Kind:      notify.KindSessionEnded,
			Trust:     notify.TrustAttested,
			Level:     level,
			Attribution: notify.Attribution{
				Backend: commandnames.LocalRoute,
				Host:    sess.Host(),
				Session: string(sess.ID()),
			},
		})
	}
```

- [ ] **Step 4: Add the route so the event can reach a sink**

This event is `attested`, so it may reach every sink. In `internal/app/app.go`, add to the `notify.Table` literal beside the existing `program.notify` row:

```go
		{Kind: notify.KindSessionEnded, Trust: notify.TrustAttested}: {
			{Sink: notify.HostSink{Host: attentionHost}},
		},
```

- [ ] **Step 5: Run the tests and watch them pass**

Run: `go test ./internal/transport -run TestSessionEnd -v && go test ./internal/app/... -race`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/transport/ws.go internal/transport/ws_notify_session_ended_test.go internal/app/app.go
git commit -m "feat(transport,notify): a session that ends while you are away is remembered (nocx-p0xhg.4)"
```

---

### Task 5: The wire — three methods, three schemas, both conformance tests

**Files:**

- Create: `contracts/notify.feed.read.schema.json`
- Create: `contracts/notify.feed.markRead.schema.json`
- Create: `contracts/notify.feed.changed.schema.json`
- Create: `internal/transport/ws_notify_feed.go`
- Test: `internal/transport/ws_notify_feed_test.go`
- Modify: `internal/transport/ws_contract_test.go`
- Generated (do not hand-edit): `frontend/src/generated/notify.feed.read.ts`, `notify.feed.markRead.ts`, `notify.feed.changed.ts`

**Interfaces:**

- Consumes: `notify.FeedSnapshot`, `notify.Occurrence`, `notify.DroppedRecord` (Tasks 1–2).
- Produces:
  - `func WithNotifyFeed(f NotifyFeed) WSServerOption`
  - `type NotifyFeed interface { Snapshot() notify.FeedSnapshot; MarkAllRead() uint64 }`
  - wire methods `notify.feed.read`, `notify.feed.markRead`; notification `notify.feed.changed`.

**Acceptance Criteria:**

- `notify.feed.read` returns `{revision, unreadCount, occurrences[], dropped}` and nothing else; `additionalProperties: false` holds on every object in the schema.
- Each occurrence carries `id`, `at`, `title`, `body`, `kind`, `level`, `count`, `read`, `backendId`, `sessionId`, `host` — and **no** `trust`: trust is a routing capability, not something a surface renders, and putting it on the wire invites a renderer to act on it.
- `notify.feed.markRead` returns `{revision}`.
- `notify.feed.changed` carries `{revision}` and nothing else.
- With no feed wired, all three answer `-32601`, matching `WithNotifyRaiser`'s existing behaviour.
- `npm run contracts:check` passes; a `_DTOConformsToContract` case and an `_OverTheWireConformsToContract` case exist for each method.

- [ ] **Step 1: Write the schemas**

`contracts/notify.feed.read.schema.json`:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://nocx.local/contracts/notify.feed.read.schema.json",
  "title": "NotifyFeedRead",
  "description": "Result of notify.feed.read: the authoritative snapshot of the notification centre's in-memory feed. The renderer reconciles against revision — it applies a notify.feed.changed hint only when the hint is exactly its own revision plus one, and refetches on any gap, so a change notification dropped by the refreshable outbound queue costs one refetch rather than a lost row (nocx-sb3f). Occurrences are newest first. trust is deliberately absent: it is a routing capability bound (ADR-0047 §3) and not something a surface renders, and carrying it would invite a renderer to act on a decision the router already made.",
  "type": "object",
  "additionalProperties": false,
  "required": ["revision", "unreadCount", "occurrences", "dropped"],
  "properties": {
    "revision": {
      "description": "Monotonic, in-memory, bumped on every feed mutation — an add, a mark-read, or an eviction. Never persisted and never reset except by process restart, which the renderer sees as a reconnect.",
      "type": "integer",
      "minimum": 0
    },
    "unreadCount": {
      "description": "Occurrences whose read flag is false. This is the ONE number the bell badge and the dock badge both read (design §6); the tab activity dot answers a different question and keeps reading hasActivity.",
      "type": "integer",
      "minimum": 0
    },
    "occurrences": {
      "description": "The feed, newest first.",
      "type": "array",
      "items": { "$ref": "#/$defs/occurrence" }
    },
    "dropped": {
      "description": "The visible half of eviction. It lives outside the occurrence budget and is never itself evicted: a soft degrade must be visible in the product, not only in a log.",
      "$ref": "#/$defs/dropped"
    }
  },
  "$defs": {
    "occurrence": {
      "type": "object",
      "additionalProperties": false,
      "required": [
        "id",
        "at",
        "title",
        "body",
        "kind",
        "level",
        "count",
        "read",
        "backendId",
        "sessionId",
        "host"
      ],
      "properties": {
        "id": {
          "description": "Stable identity of one occurrence, minted by ingress. Opaque to the renderer and monotonic within the backend process.",
          "type": "string"
        },
        "at": { "description": "RFC 3339 instant, stamped once by ingress.", "type": "string" },
        "title": {
          "description": "Untrusted presentation data (ADR-0047 §2.3). Rendered as text, never as markup.",
          "type": "string"
        },
        "body": {
          "description": "Untrusted presentation data, same guarantees as title. Empty when the source had none.",
          "type": "string"
        },
        "kind": {
          "description": "The event kind, stamped by the source adapter from the method invoked — never carried on the wire inbound.",
          "type": "string",
          "enum": ["block.finished", "session.ended", "program.notify", "bell", "pane.workFinished"]
        },
        "level": {
          "description": "Severity, stamped by nocx: a program cannot forge danger.",
          "type": "string",
          "enum": ["info", "success", "warning", "danger"]
        },
        "count": {
          "description": "How many occurrences this row collapsed. 1 for a lone occurrence. A row's count rises only while consecutive repeats of its collapse key arrive inside the collapse window.",
          "type": "integer",
          "minimum": 1
        },
        "read": {
          "description": "True once the user marked the feed read. A row whose count rises becomes unread again — the count changed, so there is something new to see.",
          "type": "boolean"
        },
        "backendId": {
          "description": "Which backend raised this — \"local\" for this machine, the same vocabulary internal/commandnames.LocalRoute uses. Present from the first commit because nocx-if6 phase A makes session identity (backendId, sessionId).",
          "type": "string"
        },
        "sessionId": {
          "description": "Addressing: which session this came from. The renderer resolves it to a tab; the backend cannot, and does not try.",
          "type": "string"
        },
        "host": {
          "description": "The host the session speaks for, stamped from the registry.",
          "type": "string"
        }
      }
    },
    "dropped": {
      "type": "object",
      "additionalProperties": false,
      "required": ["count", "oldest", "newest"],
      "properties": {
        "count": {
          "description": "Occurrences evicted since the process started. Zero means nothing has been lost.",
          "type": "integer",
          "minimum": 0
        },
        "oldest": {
          "description": "RFC 3339 instant of the oldest evicted occurrence, or the empty string when count is 0.",
          "type": "string"
        },
        "newest": {
          "description": "RFC 3339 instant of the newest evicted occurrence, or the empty string when count is 0.",
          "type": "string"
        }
      }
    }
  }
}
```

`contracts/notify.feed.markRead.schema.json` — result only:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://nocx.local/contracts/notify.feed.markRead.schema.json",
  "title": "NotifyFeedMarkRead",
  "description": "Result of notify.feed.markRead: every unread occurrence becomes read and the feed's revision advances. The renderer applies the returned revision directly rather than waiting for the change notification it will also receive — the notification is a hint and the result is authoritative.",
  "type": "object",
  "additionalProperties": false,
  "required": ["revision"],
  "properties": {
    "revision": {
      "description": "The feed revision after the mark. Monotonic.",
      "type": "integer",
      "minimum": 0
    }
  }
}
```

`contracts/notify.feed.changed.schema.json` — params of the notification:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://nocx.local/contracts/notify.feed.changed.schema.json",
  "title": "NotifyFeedChanged",
  "description": "Params of the notify.feed.changed notification: the feed's new revision and nothing else. Carrying only the revision is what makes the notification droppable without loss — it rides the refreshable outbound queue, and a dropped one costs the renderer one refetch rather than a row it never learns about (nocx-sb3f). A renderer applies it only when it is exactly its own revision plus one; any gap means it missed one and must refetch.",
  "type": "object",
  "additionalProperties": false,
  "required": ["revision"],
  "properties": {
    "revision": {
      "description": "The feed revision after the mutation that prompted this notification.",
      "type": "integer",
      "minimum": 0
    }
  }
}
```

- [ ] **Step 2: Generate the renderer types**

```bash
cd frontend && npm run contracts && cd ..
git status --short frontend/src/generated/
```

Expected: three new files under `frontend/src/generated/`.

- [ ] **Step 3: Write the failing over-the-socket test**

```go
// internal/transport/ws_notify_feed_test.go — the shape that matters is the
// REAL result off the REAL socket. A test validating a payload the test built
// proves the struct is well-formed, not that the server sends it.
func TestNotifyFeedRead_OverTheWireConformsToContract(t *testing.T) {
	feed, _ := notify.NewFeed(notify.FeedLimits{MaxOccurrences: 10, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, notify.RealClock{})
	feed.Add(notify.Event{
		SessionID: "s1", Title: "deploy failed", Body: "exit status 1",
		Kind: notify.KindSessionEnded, Trust: notify.TrustAttested, Level: notify.LevelWarning,
		At:   time.Now(),
		Attribution: notify.Attribution{Backend: "local", Host: "prod-1", Session: "s1"},
	})

	srv := newTestServer(t, WithNotifyFeed(feed))
	raw := srv.call(t, "notify.feed.read", map[string]any{})
	assertConformsToContract(t, "notify.feed.read", raw)
}
```

- [ ] **Step 4: Run it and watch it fail**

Run: `go test ./internal/transport -run TestNotifyFeedRead_OverTheWire -v`
Expected: FAIL — `undefined: WithNotifyFeed`.

- [ ] **Step 5: Write `internal/transport/ws_notify_feed.go`**

```go
package transport

// notify.feed.read / notify.feed.markRead -- the renderer's window onto the
// notification centre. The DTOs mirror contracts/notify.feed.read.schema.json
// field for field, and `trust` is absent from BOTH: it is a routing capability
// bound (ADR-0047 3), not something a surface renders, and carrying it would
// invite a renderer to act on a decision the router already made.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/shady2k/nocx/internal/notify"
)

// NotifyFeed is the narrow seam onto the centre's feed. *notify.Feed satisfies
// it without an adapter -- the same signature-identical shape NotifyRaiser uses.
type NotifyFeed interface {
	Snapshot() notify.FeedSnapshot
	MarkAllRead() uint64
}

// WithNotifyFeed enables notify.feed.read and notify.feed.markRead. When
// absent both answer -32601, exactly as notify.raise does without a raiser.
func WithNotifyFeed(f NotifyFeed) WSServerOption {
	return func(s *WSServer) { s.notifyFeed = f }
}

type feedOccurrenceDTO struct {
	ID        string `json:"id"`
	At        string `json:"at"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Kind      string `json:"kind"`
	Level     string `json:"level"`
	Count     int    `json:"count"`
	Read      bool   `json:"read"`
	BackendID string `json:"backendId"`
	SessionID string `json:"sessionId"`
	Host      string `json:"host"`
}

type feedDroppedDTO struct {
	Count  int    `json:"count"`
	Oldest string `json:"oldest"`
	Newest string `json:"newest"`
}

type feedReadResult struct {
	Revision    uint64              `json:"revision"`
	UnreadCount int                 `json:"unreadCount"`
	Occurrences []feedOccurrenceDTO `json:"occurrences"`
	Dropped     feedDroppedDTO      `json:"dropped"`
}

type feedMarkReadResult struct {
	Revision uint64 `json:"revision"`
}

// feedChangedParams is the notification payload: the revision and nothing
// else. That is what makes it droppable without loss.
type feedChangedParams struct {
	Revision uint64 `json:"revision"`
}

func stampOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func snapshotToResult(snap notify.FeedSnapshot) feedReadResult {
	// Built with make, never left nil: the schema says type array, and a nil
	// slice marshals to null. That exact defect is what the contracts' first
	// run caught on vault.status.
	occ := make([]feedOccurrenceDTO, 0, len(snap.Occurrences))
	for _, o := range snap.Occurrences {
		occ = append(occ, feedOccurrenceDTO{
			ID:        string(o.ID),
			At:        stampOrEmpty(o.Event.At),
			Title:     o.Event.Title,
			Body:      o.Event.Body,
			Kind:      string(o.Event.Kind),
			Level:     string(o.Event.Level),
			Count:     o.Count,
			Read:      o.ReadAt != nil,
			BackendID: o.Event.Attribution.Backend,
			SessionID: o.Event.SessionID,
			Host:      o.Event.Attribution.Host,
		})
	}
	return feedReadResult{
		Revision:    snap.Revision,
		UnreadCount: snap.UnreadCount,
		Occurrences: occ,
		Dropped: feedDroppedDTO{
			Count:  snap.Dropped.Count,
			Oldest: stampOrEmpty(snap.Dropped.Oldest),
			Newest: stampOrEmpty(snap.Dropped.Newest),
		},
	}
}

func (s *WSServer) handleNotifyFeedRead(_ context.Context, _ json.RawMessage) (any, *rpcError) {
	if s.notifyFeed == nil {
		return nil, errMethodNotFound
	}
	return snapshotToResult(s.notifyFeed.Snapshot()), nil
}

func (s *WSServer) handleNotifyFeedMarkRead(_ context.Context, _ json.RawMessage) (any, *rpcError) {
	if s.notifyFeed == nil {
		return nil, errMethodNotFound
	}
	return feedMarkReadResult{Revision: s.notifyFeed.MarkAllRead()}, nil
}
```

Add the `notifyFeed NotifyFeed` field to `WSServer` and register both handlers
in the server's method table beside `notify.raise`. Use the package's existing
`errMethodNotFound` / `rpcError` vocabulary rather than minting a second one --
match the surrounding handlers exactly.

- [ ] **Step 6: Wire the change notification**

```go
// BroadcastFeedChanged tells every attached renderer the feed moved. It
// carries the revision only, so it is safe on the refreshable outbound queue:
// a dropped one costs the renderer one refetch, never a row it never learns
// about. That is precisely why this is NOT the terminal class nocx-sb3f
// describes -- this notification has a successor, namely the snapshot.
//
// TryNotify, not Notify: a client whose queue is full must not block the
// feed's mutation path. Same broadcast shape as unlock_requester.go:184.
func (s *WSServer) BroadcastFeedChanged(revision uint64) {
	params := mustMarshal(feedChangedParams{Revision: revision})
	for _, wc := range s.clients() {
		_ = wc.TryNotify("notify.feed.changed", params)
	}
}
```

Add `TestBroadcastFeedChangedReachesEveryClient`, asserting two attached
clients each receive it, and `TestBroadcastFeedChangedSurvivesAFullQueue`,
asserting a client whose queue refuses the frame does not stop the other from
receiving it.

- [ ] **Step 7: Add the DTO cases and run the whole contract table**

Add one `_DTOConformsToContract` case per method to `internal/transport/ws_contract_test.go`: for `notify.feed.read`, a populated snapshot, an empty one (`occurrences: []`, not `null` — the schema's `type: array` rejects null, which is exactly the defect the first schema run caught on `vault.status`), and one with `dropped.count > 0`.

```bash
go test ./internal/transport -run Contract -v
cd frontend && npm run contracts:check && cd ..
```

Expected: PASS both.

- [ ] **Step 8: Commit**

```bash
git add contracts/notify.feed.*.json frontend/src/generated/notify.feed.*.ts internal/transport/ws_notify_feed.go internal/transport/ws_notify_feed_test.go internal/transport/ws_contract_test.go
git commit -m "feat(transport): the feed is readable over the wire, snapshot plus revision (nocx-p0xhg.5)"
```

---

### Task 6: Wire it at the composition root

**Files:**

- Modify: `internal/app/app.go` (the notify block, lines ~1019–1075)
- Test: `internal/app/app_notify_feed_test.go`

**Interfaces:**

- Consumes: everything from Tasks 1–5.
- Produces: a running feed reachable from `main()`.

**Acceptance Criteria:**

- `notifyFeed` is constructed with explicit limits and its `OnChange` is bound to the transport's broadcast.
- `transport.WithNotifyRaiser` receives the **ingress**, not the policy; the policy is now behind ingress.
- `transport.WithNotifyFeed` receives the feed.
- `deadcode -tags gtk3 -whylive '…/internal/notify.(*Feed).Add' ./...` prints a path from `main`, not "reachable only through reflection".

- [ ] **Step 1: Write the failing reachability test**

```go
// internal/app/app_notify_feed_test.go
func TestAppWiresTheFeedIntoTheRaiseSeam(t *testing.T) {
	a := newTestApp(t)
	// The seam the renderer reaches: raising through the transport must land
	// in the feed, or the whole pipeline is test-reachable only — the shape
	// nocx-rtg0 shipped and nobody noticed.
	if a.notifyFeed == nil {
		t.Fatal("no feed constructed at the composition root")
	}
	before := a.notifyFeed.Snapshot().Revision
	a.notifyIngress.Raise(context.Background(), notify.Event{
		SessionID: "s1", Title: "hello", Kind: notify.KindProgramNotify,
		Trust: notify.TrustProgramRequest, Level: notify.LevelInfo,
	})
	if a.notifyFeed.Snapshot().Revision == before {
		t.Fatal("a raise through the wired seam did not reach the feed")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/app -run TestAppWiresTheFeed -v`
Expected: FAIL — `a.notifyFeed undefined`.

- [ ] **Step 3: Add the constants and construct the feed**

Beside `notifyDebounceWindow` in `internal/app/app.go`:

```go
// The feed's budgets. Rows alone are not a usable unit of account — a single
// occurrence may carry 8 KiB of title and body — so both bind, and eviction is
// always recorded in the dropped row rather than being silent.
//
// The collapse window is deliberately much shorter than any read lifetime. If
// it were not, two separate acts sharing a session, kind and level (two
// deploys an hour apart) would merge into one row simply because nobody had
// cleared the inbox.
const (
	notifyFeedMaxOccurrences   = 200
	notifyFeedMaxRetainedBytes = 1 << 20
	notifyFeedCollapseWindow   = 30 * time.Second
)
```

Then, after `notifyPolicy` is constructed:

```go
	notifyFeed, feedErr := notify.NewFeed(notify.FeedLimits{
		MaxOccurrences:   notifyFeedMaxOccurrences,
		MaxRetainedBytes: notifyFeedMaxRetainedBytes,
		CollapseWindow:   notifyFeedCollapseWindow,
	}, notify.RealClock{})
	if feedErr != nil {
		return nil, fmt.Errorf("notify feed: %w", feedErr)
	}
	notifyIngress, ingressErr := notify.NewIngress(notifyFeed, notifyPolicy, notify.RealClock{})
	if ingressErr != nil {
		return nil, fmt.Errorf("notify ingress: %w", ingressErr)
	}
	tpOpts = append(tpOpts, transport.WithNotifyRaiser(notifyIngress), transport.WithNotifyFeed(notifyFeed))
```

Replace the existing `tpOpts = append(tpOpts, transport.WithNotifyRaiser(notifyPolicy))` line — the policy is now reached through ingress, never around it.

- [ ] **Step 4: Bind `OnChange` once the server exists**

After the `WSServer` is constructed, `notifyFeed.OnChange(func(rev uint64) { server.BroadcastFeedChanged(rev) })`.

- [ ] **Step 5: Run the tests and the ratchet**

```bash
go test ./internal/app/... ./internal/notify/... ./internal/transport/... -race
deadcode -tags gtk3 -whylive 'github.com/shady2k/nocx/internal/notify.(*Feed).Add' ./...
```

Expected: tests PASS; `deadcode` prints a path from `main`, **not** "reachable only through reflection". If it prints the latter, the feed is not wired and the task is not done — that is the `ContentDB.Add` shape and the whole reason this step exists.

- [ ] **Step 6: Commit**

```bash
git add internal/app/app.go internal/app/app_notify_feed_test.go
git commit -m "feat(app): the feed is reachable from main, not from its tests (nocx-p0xhg.6)"
```

---

### Task 7: The renderer's client and feed store

**Files:**

- Create: `frontend/src/notify/feed-client.ts`
- Create: `frontend/src/notify/feed-store.ts`
- Test: `frontend/src/notify/feed-store.test.ts`

**Interfaces:**

- Consumes: generated types from Task 5; `Dispatcher` from `frontend/src/dispatcher.ts` (`call`, `subscribe`).
- Produces:
  - `class NotifyFeedClient { read(): Promise<NotifyFeedRead>; markRead(): Promise<NotifyFeedMarkRead> }`
  - `function createFeedStore(client: NotifyFeedClient, dispatcher: Dispatcher): FeedStore`
  - `interface FeedStore { occurrences: () => NotifyFeedRead['occurrences']; unreadCount: () => number; dropped: () => NotifyFeedRead['dropped']; markRead: () => void; destroy: () => void }`

**Acceptance Criteria:**

- On creation the store fetches a snapshot and subscribes to `notify.feed.changed`.
- A `changed` whose revision is exactly `current + 1` is applied without a refetch **only if** the store can derive the new state; since it cannot (the notification carries no occurrence), it refetches. Assert the refetch happens exactly once per applied hint.
- A `changed` whose revision is **less than or equal to** the store's own is ignored — it is a late duplicate.
- A `changed` whose revision is more than one ahead triggers exactly one refetch, and a burst of such hints coalesces into one in-flight refetch rather than N.
- `markRead()` applies the revision from the method result directly; a `changed` for that same revision arriving afterwards is then a no-op.
- `destroy()` unsubscribes.

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/notify/feed-store.test.ts
import { describe, expect, it, vi } from 'vitest'
import { createFeedStore } from './feed-store'

function fakeDispatcher() {
  const handlers = new Map<string, Set<(p: unknown) => void>>()
  return {
    subscribe(method: string, h: (p: unknown) => void) {
      let s = handlers.get(method)
      if (!s) handlers.set(method, (s = new Set()))
      s.add(h)
      return () => s!.delete(h)
    },
    emit(method: string, params: unknown) {
      handlers.get(method)?.forEach((h) => h(params))
    },
  }
}

describe('feed store', () => {
  it('coalesces a burst of change hints into one refetch', async () => {
    const read = vi.fn().mockResolvedValue({
      revision: 1,
      unreadCount: 0,
      occurrences: [],
      dropped: { count: 0, oldest: '', newest: '' },
    })
    const d = fakeDispatcher()
    const store = createFeedStore({ read, markRead: vi.fn() } as never, d as never)
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(1))

    // Ten hints arriving while one refetch is in flight must not become ten
    // round trips: under a flood the hints are exactly what arrives in bulk.
    for (let i = 2; i <= 11; i++) d.emit('notify.feed.changed', { revision: i })
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(2))
    expect(read).toHaveBeenCalledTimes(2)
    store.destroy()
  })

  it('ignores a hint at or below its own revision', async () => {
    const read = vi.fn().mockResolvedValue({
      revision: 5,
      unreadCount: 0,
      occurrences: [],
      dropped: { count: 0, oldest: '', newest: '' },
    })
    const d = fakeDispatcher()
    const store = createFeedStore({ read, markRead: vi.fn() } as never, d as never)
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(1))

    // A late duplicate of a revision already applied. Refetching on it would
    // turn one dropped-and-resent hint into an endless refetch loop.
    d.emit('notify.feed.changed', { revision: 5 })
    d.emit('notify.feed.changed', { revision: 4 })
    await new Promise((r) => setTimeout(r, 0))
    expect(read).toHaveBeenCalledTimes(1)
    store.destroy()
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd frontend && npx vitest run src/notify/feed-store.test.ts`
Expected: FAIL — cannot resolve `./feed-store`.

- [ ] **Step 3: Write the client and the store**

```ts
// frontend/src/notify/feed-client.ts
// One method per wire call, every result a GENERATED type: the renderer
// declares nothing of its own, because a hand-written type can want a field
// the wire does not carry -- which is exactly how vault.status shipped one.
import type { Dispatcher } from '../dispatcher'
import type { NotifyFeedRead } from '../generated/notify.feed.read'
import type { NotifyFeedMarkRead } from '../generated/notify.feed.markRead'

export class NotifyFeedClient {
  constructor(private dispatcher: Dispatcher) {}

  read(): Promise<NotifyFeedRead> {
    return this.dispatcher.call<NotifyFeedRead>('notify.feed.read', {})
  }

  markRead(): Promise<NotifyFeedMarkRead> {
    return this.dispatcher.call<NotifyFeedMarkRead>('notify.feed.markRead', {})
  }
}
```

```ts
// frontend/src/notify/feed-store.ts
import { createSignal } from 'solid-js'
import type { Dispatcher } from '../dispatcher'
import type { NotifyFeedRead } from '../generated/notify.feed.read'
import type { NotifyFeedChanged } from '../generated/notify.feed.changed'
import type { NotifyFeedClient } from './feed-client'

export interface FeedStore {
  occurrences: () => NotifyFeedRead['occurrences']
  unreadCount: () => number
  dropped: () => NotifyFeedRead['dropped']
  markRead: () => void
  destroy: () => void
}

const EMPTY_DROPPED = { count: 0, oldest: '', newest: '' }

export function createFeedStore(client: NotifyFeedClient, dispatcher: Dispatcher): FeedStore {
  const [occurrences, setOccurrences] = createSignal<NotifyFeedRead['occurrences']>([])
  const [unreadCount, setUnreadCount] = createSignal(0)
  const [dropped, setDropped] = createSignal<NotifyFeedRead['dropped']>(EMPTY_DROPPED)

  let revision = -1
  // One in-flight refetch at a time. Under a flood the hints are exactly what
  // arrives in bulk, and one round trip per hint would turn a noisy pane into
  // a noisy socket.
  let inFlight: Promise<void> | null = null

  const apply = (snap: NotifyFeedRead) => {
    // A snapshot older than what we already hold is a reordered response, not
    // news. Applying it would move the feed backwards.
    if (snap.revision < revision) return
    revision = snap.revision
    setOccurrences(snap.occurrences)
    setUnreadCount(snap.unreadCount)
    setDropped(snap.dropped)
  }

  const refetch = (): Promise<void> => {
    if (inFlight) return inFlight
    inFlight = client
      .read()
      .then(apply)
      .catch(() => {
        // A failed read leaves the last snapshot on screen. The next hint
        // retries; a bell that blanks itself on a transient error is worse
        // than one that is briefly stale.
      })
      .finally(() => {
        inFlight = null
      })
    return inFlight
  }

  const unsubscribe = dispatcher.subscribe('notify.feed.changed', (params: unknown) => {
    const hint = params as NotifyFeedChanged
    // At or below our own revision is a late duplicate. Refetching on it would
    // turn one resent hint into an endless loop.
    if (typeof hint?.revision !== 'number' || hint.revision <= revision) return
    void refetch()
  })

  void refetch()

  return {
    occurrences,
    unreadCount,
    dropped,
    markRead: () => {
      // The METHOD RESULT is authoritative; the change notification that
      // follows is then a no-op by the rule above.
      void client
        .markRead()
        .then((r) => {
          if (r.revision > revision) {
            revision = r.revision
            setUnreadCount(0)
            setOccurrences((prev) => prev.map((o) => ({ ...o, read: true })))
          }
        })
        .catch(() => refetch())
    },
    destroy: unsubscribe,
  }
}
```

- [ ] **Step 4: Run and watch it pass**

Run: `cd frontend && npx vitest run src/notify/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/notify/
git commit -m "feat(frontend): the feed store reconciles on revision, and a burst costs one refetch (nocx-p0xhg.7)"
```

---

### Task 8: The bell, the panel, and click-to-tab

**Files:**

- Create: `frontend/src/ui/icons/BellIcon.tsx`
- Modify: `frontend/src/ui/icons/index.ts`
- Modify: `frontend/src/ui/README.md` (icon row)
- Create: `frontend/src/notify/notifications-panel.tsx`
- Create: `frontend/src/styles/components/notifications-panel.css`
- Modify: `frontend/src/main.tsx` (register the view; feed the unread count)
- Modify: `frontend/src/sidebar.tsx` (a view descriptor may carry a badge count)
- Test: `frontend/src/notify/notifications-panel.test.tsx`, `frontend/src/sidebar.test.tsx`

**Interfaces:**

- Consumes: `FeedStore` (Task 7); `CollectionView`, `RecordRow`, `Badge`, `EmptyState`, `IconButton` from `frontend/src/ui/`.
- Produces: `NOTIFICATIONS_VIEW: SidebarViewDescriptor`; `SidebarViewDescriptor` gains `badgeCount?: () => number`.

**Acceptance Criteria:**

- The bell is a view in the **top zone** of the activity bar, not an action: it opens the panel, which is what a view does (`sidebar.tsx` header).
- With zero unread the bell carries **no badge at all** — `nocx-708q.1` requires an entry point that is quiet where there is nothing to show, not a dead grey mark.
- Each row is a kit `RecordRow` inside a `CollectionView`: title, a `kind` badge toned from `level` (`danger`→`danger`, `warning`→`warning`, `success`→`success`, `info`→`neutral`), a meta line of host and time, and `count > 1` rendered as `×N`.
- The panel's header action is "Mark all read", an `IconButton`, disabled at zero unread.
- An empty feed renders the kit `EmptyState`, never bespoke markup.
- `dropped.count > 0` renders one row at the bottom naming the count and the span — outside the occurrence list, and it is never activatable.
- Activating a row resolves `(backendId, sessionId)` to a tab **in the renderer** and focuses it; a row whose session no longer exists is not activatable and says so in its meta line.
- No CSS in `notifications-panel.css` sets `background`, `border`, `color`, `font-*`, `padding` or `box-shadow` on a `ui-*` class. Placement only.
- **Visiting a tab does not mark its notifications read** (spec §6). They are different facts — output arrived, versus you saw what we told you — and conflating them is what decision #8 of the 2026-08-13 design did, which is why a centre could not be built on top of it. A test asserts the unread count is unchanged after focusing the tab a notification came from, and changes only on "Mark all read".

- [ ] **Step 1: Write the failing test for the quiet bell**

```tsx
// frontend/src/notify/notifications-panel.test.tsx
it('shows no badge when nothing is unread', () => {
  const { container } = render(() => <ActivityBarBell unread={() => 0} />)
  // nocx-708q.1: where there is nothing to show, the entry point is quiet
  // rather than dead. A zero badge is a dead mark.
  expect(container.querySelector('.ui-badge')).toBeNull()
})

it('shows the count when something is unread', () => {
  const { container } = render(() => <ActivityBarBell unread={() => 3} />)
  expect(container.querySelector('.ui-badge')?.textContent).toBe('3')
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd frontend && npx vitest run src/notify/notifications-panel.test.tsx`
Expected: FAIL — `ActivityBarBell` is not exported.

- [ ] **Step 3: Add `BellIcon` to the kit**

```tsx
// frontend/src/ui/icons/BellIcon.tsx
import type { Component } from 'solid-js'

/**
 * Bell — Lucide `bell` under ISC.
 *
 * Chosen over an envelope or a flag: an envelope says "messages addressed to
 * you", which these are not — they are things that happened. Uses currentColor
 * so it follows the container's text colour.
 */
const BellIcon: Component = () => (
  <svg
    xmlns="http://www.w3.org/2000/svg"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="M10.268 21a2 2 0 0 0 3.464 0" />
    <path d="M3.262 15.326A1 1 0 0 0 4 17h16a1 1 0 0 0 .74-1.673C19.41 13.956 18 12.499 18 8A6 6 0 0 0 6 8c0 4.499-1.411 5.956-2.738 7.326" />
  </svg>
)

export default BellIcon
```

Export it from `frontend/src/ui/icons/index.ts` and add its row to the icon table in `frontend/src/ui/README.md`.

- [ ] **Step 4: Build the panel and the bell**

```tsx
// frontend/src/notify/notifications-panel.tsx
import { For, Show } from 'solid-js'
import { EmptyState } from '../ui/empty-state'
import { RecordRow } from '../ui/record-row'
import type { BadgeTone } from '../ui'
import type { FeedStore } from './feed-store'
import type { NotifyFeedRead } from '../generated/notify.feed.read'

type Occurrence = NotifyFeedRead['occurrences'][number]

/** Level maps onto the kit's tone vocabulary. info is neutral, not "info":
 *  an ordinary completion is not an advisory, and toning every row blue is
 *  how a feed stops distinguishing anything. */
function toneOf(level: Occurrence['level']): BadgeTone {
  switch (level) {
    case 'danger':
      return 'danger'
    case 'warning':
      return 'warning'
    case 'success':
      return 'success'
    default:
      return 'neutral'
  }
}

export interface NotificationsPanelProps {
  store: FeedStore
  visible: () => boolean
  onActivate: (backendId: string, sessionId: string) => void
  canActivate: (backendId: string, sessionId: string) => boolean
}

export function NotificationsPanel(props: NotificationsPanelProps) {
  return (
    <div class="notifications-panel">
      <Show
        when={props.store.occurrences().length > 0}
        fallback={
          <EmptyState
            title="Nothing to catch up on"
            description="Notifications raised while you are elsewhere collect here."
          />
        }
      >
        {/* A plain role=list wrapping RecordRow -- the same shape
            notes-panel.tsx uses. NOT CollectionView: that is the searchable
            manager surface (it requires searchValue, onSearch, hasItems and
            an empty slot), and search over the feed is epic 2's. */}
        <div class="notifications-panel__list" role="list" aria-label="Notifications">
          <For each={props.store.occurrences()}>
            {(o) => {
              const live = () => props.canActivate(o.backendId, o.sessionId)
              return (
                <RecordRow
                  density="dense"
                  title={o.count > 1 ? `${o.title} ×${o.count}` : o.title}
                  kind={{ label: o.kind, tone: toneOf(o.level) }}
                  meta={live() ? `${o.host} · ${formatWhen(o.at)}` : `${o.host} · session closed`}
                  detail={o.body === '' ? undefined : o.body}
                  selected={!o.read}
                  onActivate={live() ? () => props.onActivate(o.backendId, o.sessionId) : undefined}
                  actions={<></>}
                />
              )
            }}
          </For>
        </div>
      </Show>

      <Show when={props.store.dropped().count > 0}>
        {/* Outside the list and never activatable: a soft degrade must be
            visible in the product, not only in a log. */}
        <div class="notifications-panel__dropped" data-testid="notifications-dropped">
          {props.store.dropped().count} notifications dropped between{' '}
          {formatWhen(props.store.dropped().oldest)} and {formatWhen(props.store.dropped().newest)}
        </div>
      </Show>
    </div>
  )
}
```

`formatWhen` is a local helper over `Intl.DateTimeFormat`; put it in this
module rather than in `ui/`, since it is this surface's phrasing and not a kit
concern.

```css
/* frontend/src/styles/components/notifications-panel.css
   Placement only. Not one rule here sets background, border, color, font-*,
   padding or box-shadow on a ui-* class -- a surface may place a kit
   component and may never repaint it. The dropped line is this surface's own
   element, so it may carry its own appearance. */
.notifications-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.notifications-panel__list {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
}

.notifications-panel__dropped {
  flex: 0 0 auto;
  padding: 8px 16px;
  font-family: var(--font-family-ui);
  font-size: var(--font-size-2xs);
  color: var(--color-text-dim);
  border-top: 1px solid var(--color-border);
}
```

Register the stylesheet wherever `styles/components/*.css` are imported, beside
`sidebar.css`.

- [ ] **Step 5: Let a view descriptor carry a count**

In `frontend/src/sidebar.tsx`, add `badgeCount?: () => number` to `SidebarViewDescriptor`, and render a `Badge` inside the view's `IconButton` when the accessor returns a number greater than zero. Add a `sidebar.test.tsx` case asserting no badge element exists at zero.

- [ ] **Step 6: Register the view in `main.tsx`**

```tsx
// Beside NOTES_VIEW in frontend/src/main.tsx.
//
// The store is created HERE, at the composition root, because two surfaces
// consume it: the panel body and the activity-bar badge. Creating it inside
// the view would leave the badge with nothing to read until the panel was
// first opened -- a bell that only starts counting once you look at it.
const feedStore = createFeedStore(new NotifyFeedClient(dispatcher), dispatcher)

const NOTIFICATIONS_VIEW: SidebarViewDescriptor = {
  id: 'notifications',
  title: 'Notifications',
  icon: BellIcon,
  // Quiet where there is nothing to show (nocx-708q.1): at zero the sidebar
  // renders no badge element at all, not a grey zero.
  badgeCount: () => feedStore.unreadCount(),
  actions: () => (
    <IconButton
      data-testid="notifications-mark-read"
      size="sm"
      ariaLabel="Mark all notifications read"
      title="Mark all read"
      disabled={feedStore.unreadCount() === 0}
      onClick={() => feedStore.markRead()}
    >
      <CheckCircleIcon />
    </IconButton>
  ),
  view: (props) => (
    <NotificationsPanel
      store={feedStore}
      visible={props.visible}
      // Resolution is the RENDERER's: it already owns session -> tab, and the
      // backend cannot do it at all (Attribution.Tab is a WebSocket connection
      // id -- nocx-wyp3p). Starting the click here is what keeps this epic off
      // nocx-jiwq.1, which only a BANNER click needs.
      onActivate={(backendId, sessionId) => {
        const tab = tm.findBySession(backendId, sessionId)
        if (tab) tm.focus(tab)
      }}
      canActivate={(backendId, sessionId) => tm.findBySession(backendId, sessionId) !== undefined}
    />
  ),
  order: 2,
}
```

`tm.findBySession` does not exist yet as a two-argument call. Add it to the tab
manager taking `(backendId, sessionId)` and returning the tab or `undefined`.
Today every tab's backend is `"local"`, so compare the first argument against
`commandnames.LocalRoute`'s value rather than ignoring it -- ignoring it is how
an argument silently stops meaning anything by the time the relay lands.

Register the view in the array below, keeping it sorted by `order`:

```tsx
const sidebarViews = [filesView, PORTS_VIEW, gitView, NOTES_VIEW, NOTIFICATIONS_VIEW].sort(
  (a, b) => a.order - b.order,
)
```

`NOTES_VIEW` already holds `order: 1`, so give `NOTIFICATIONS_VIEW` `order: 2`
and leave the existing assertion that Files is first untouched.

- [ ] **Step 7: Run the frontend gates**

```bash
cd frontend && npm run lint && npm run typecheck && npx vitest run && cd ..
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add frontend/src/ui/icons/BellIcon.tsx frontend/src/ui/icons/index.ts frontend/src/ui/README.md frontend/src/notify/ frontend/src/styles/components/notifications-panel.css frontend/src/main.tsx frontend/src/sidebar.tsx frontend/src/sidebar.test.tsx
git commit -m "feat(frontend): the bell is quiet until there is something to say (nocx-p0xhg.8)"
```

---

### Task 9: The acceptance check

**Files:**

- Create: `e2e/notification-centre.spec.ts`

**Interfaces:**

- Consumes: the whole epic.
- Produces: the check that closes `nocx-p0xhg`.

**Acceptance Criteria:**

- The spec drives the **UI**, not the store: it clicks the bell, reads the rendered rows, clicks "Mark all read".
- It waits on observable state — a row appearing, a badge disappearing — and never on a duration. A spec that needs a slow machine to pass is broken on a fast one too.
- It runs on the headless path (`cmd/devharness` + vite) so it works in `e2e/run-in-container.sh`.

- [ ] **Step 1: Write the spec**

```ts
// e2e/notification-centre.spec.ts
import { expect, test } from './fixtures'

test('a session that ends while you are elsewhere waits for you in the bell', async ({
  page,
  backend,
}) => {
  // Two tabs, so the one that ends is not the one in front.
  const watched = await backend.openLocalSession(page)
  await backend.openLocalSession(page)

  // Nothing has happened: the bell is quiet, not a grey zero (nocx-708q.1).
  await expect(page.getByTestId('activity-bell')).toBeVisible()
  await expect(page.getByTestId('activity-bell').locator('.ui-badge')).toHaveCount(0)

  await backend.endSession(watched, { cause: 'exited', status: 1 })

  // Wait on the badge, never on a duration.
  await expect(page.getByTestId('activity-bell').locator('.ui-badge')).toHaveText('1')

  await page.getByTestId('activity-bell').click()
  // Rows are targeted by the kit's identity class scoped to the panel -- the
  // established pattern (e2e/notes.spec.ts:19, e2e/snippets.spec.ts:80).
  // RecordRow takes no data-testid, and adding one to the kit for a test is
  // the tail wagging the dog.
  const rows = page.locator('.notifications-panel__list .ui-record-row__title')
  await expect(rows.first()).toContainText('ended')

  await rows.first().click()
  await expect(page.getByTestId('tab-active')).toHaveAttribute('data-session-id', watched)

  await page.getByTestId('notifications-mark-read').click()
  await expect(page.getByTestId('activity-bell').locator('.ui-badge')).toHaveCount(0)
  // The row stays: this is an inbox over a journal, not a queue that empties.
  await expect(rows).toHaveCount(1)
})
```

- [ ] **Step 2: Run it in the container**

```bash
PW_PROJECTS=chromium e2e/run-in-container.sh e2e/notification-centre.spec.ts
```

Expected: PASS. Remember the container's failure set is not CI's — confirm in CI before believing a layout-shaped red.

- [ ] **Step 3: Commit**

```bash
git add e2e/notification-centre.spec.ts
git commit -m "test(e2e): a session that ends while you are elsewhere waits for you in the bell (nocx-p0xhg.9)"
```

---

## Task dependency order

```
1 (feed) ──► 2 (collapse) ──► 3 (ingress) ──► 4 (session.ended)
                                   │                │
                                   └──► 5 (wire) ───┴──► 6 (composition root)
                                              │
                                              └──► 7 (client+store) ──► 8 (bell+panel) ──► 9 (e2e)
```

Task 4 and Task 5 may run in parallel once Task 3 lands; everything else is a chain.

## What this plan deliberately does not do

- No persistence, no `DocumentStore` document, no `localStorage`.
- No change to `Policy`'s position or to `Disposition`'s meaning — that is epic 3 (`nocx-3mniv`).
- No expandable groups — that is epic 2 (`nocx-ctl6q`), which is why Task 2 keeps each collapsed occurrence's identity even though nothing reads it yet.
- No touching of the 151 existing `showToast` sites.
- `DebounceKey` is not given `Level`. Only the feed's `CollapseKey` has it, and Task 2's comment says why.
