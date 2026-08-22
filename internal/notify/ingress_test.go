package notify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type recordingSubmitter struct {
	got  []Event
	disp Disposition
}

func (r *recordingSubmitter) Submit(ev Event) Disposition {
	r.got = append(r.got, ev)
	return r.disp
}

func TestIngressRecordsASuppressedEvent(t *testing.T) {
	clk := NewManualClock()
	// A ManualClock starts AT the zero instant, so a feed filed at clk.Now()
	// would be indistinguishable from one never stamped. Move it first, and
	// the assertion below has something to be false about.
	clk.Advance(time.Hour)
	feed, err := NewFeed(FeedLimits{MaxOccurrences: 10, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
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
	if !snap.Occurrences[0].Event.At.Equal(clk.Now()) {
		t.Fatalf("filed At %v, want the injected clock's %v", snap.Occurrences[0].Event.At, clk.Now())
	}
	// And the delivery path saw it too: recording must not replace delivering.
	if len(sub.got) != 1 {
		t.Fatalf("the submitter was invoked %d times, want 1", len(sub.got))
	}
}

// erroringSink is a sink whose delivery always fails: the banner has no host
// bound, or the host refused it.
type erroringSink struct {
	mu    sync.Mutex
	calls int
}

func (s *erroringSink) Deliver(ctx context.Context, d Delivery) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return errors.New("sink refused: no host bound")
}

func (s *erroringSink) LeavesMachine() bool { return false }

func (s *erroringSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// unfocused is a Focus that suppresses nothing, so the policy's focus rule
// never stands between the test and the sink it is trying to make fail.
type unfocused struct{}

func (unfocused) WindowFocused() bool    { return false }
func (unfocused) FocusedSession() string { return "" }

// TestIngressRecordsWhenTheSinkRefuses drives the REAL delivery stack —
// ingress → policy → router → sink — with a sink that always errors, and
// asserts the occurrence is still in the feed.
//
// If a refused delivery could remove the record, the one case the centre
// exists for — nothing reached you — would be the one case it forgets.
func TestIngressRecordsWhenTheSinkRefuses(t *testing.T) {
	clk := NewManualClock()
	clk.Advance(time.Hour)

	sink := &erroringSink{}
	router, err := NewRouter(Table{
		Key{Kind: KindProgramNotify, Trust: TrustProgramRequest}: {
			{Sink: sink, Destination: Destination{Target: "banner"}},
		},
	}, Limits{MaxInFlight: 1, MaxQueued: 4, MaxRetained: 1 << 20, DeliveryTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	var outcomes []Outcome
	policy, err := NewPolicy(context.Background(), router, 8*time.Second, unfocused{}, clk,
		WithResultHandler(func(o Outcome) { outcomes = append(outcomes, o) }))
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}

	feed, err := NewFeed(FeedLimits{MaxOccurrences: 10, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	in, err := NewIngress(feed, policy, clk)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}

	id, err := in.Admit(context.Background(), Event{
		SessionID: "s1", Title: "build finished", Kind: KindProgramNotify,
		Trust: TrustProgramRequest, Level: LevelInfo,
	})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}

	// The delivery genuinely happened and genuinely failed — otherwise this
	// test is only asserting that a no-op leaves the feed alone.
	if got := sink.count(); got != 1 {
		t.Fatalf("the sink was invoked %d times, want 1 — the refusal was never reached", got)
	}

	snap := feed.Snapshot()
	if len(snap.Occurrences) != 1 || snap.Occurrences[0].ID != id {
		t.Fatalf("a refused delivery lost its record: feed holds %d occurrences", len(snap.Occurrences))
	}
	if snap.UnreadCount != 1 {
		t.Fatalf("UnreadCount = %d, want 1", snap.UnreadCount)
	}
}

// TestIngressUnderAFullFeed: a full feed must not become a silent filter on
// delivery. Membership and delivery are two decisions and this is the whole
// point of the inversion.
func TestIngressUnderAFullFeed(t *testing.T) {
	clk := NewManualClock()
	clk.Advance(time.Hour)
	feed, err := NewFeed(FeedLimits{MaxOccurrences: 1, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	sub := &recordingSubmitter{disp: DispositionOpened}
	in, err := NewIngress(feed, sub, clk)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}

	// Distinct sessions: two arrivals that COLLAPSED would never reach the
	// eviction this test is about.
	first, err := in.Admit(context.Background(), Event{
		SessionID: "s1", Title: "first", Kind: KindProgramNotify, Trust: TrustProgramRequest, Level: LevelInfo,
	})
	if err != nil {
		t.Fatalf("first Admit: %v", err)
	}
	second, err := in.Admit(context.Background(), Event{
		SessionID: "s2", Title: "second", Kind: KindProgramNotify, Trust: TrustProgramRequest, Level: LevelInfo,
	})
	if err != nil {
		t.Fatalf("second Admit: %v", err)
	}

	if second == "" {
		t.Fatal("Admit into a full feed returned an empty id")
	}
	if second == first {
		t.Fatalf("both admissions returned the same id %q", second)
	}

	snap := feed.Snapshot()
	if len(snap.Occurrences) != 1 {
		t.Fatalf("feed holds %d occurrences, want 1 — MaxOccurrences did not bind", len(snap.Occurrences))
	}
	if snap.Occurrences[0].ID != second {
		t.Fatalf("the feed kept %q, want the newest %q", snap.Occurrences[0].ID, second)
	}
	if snap.Dropped.Count != 1 {
		t.Fatalf("Dropped.Count = %d, want 1 — the eviction was invisible", snap.Dropped.Count)
	}
	// The point: a full feed did not filter delivery.
	if len(sub.got) != 2 {
		t.Fatalf("the delivery path was invoked %d times, want 2 — a full feed silently filtered delivery", len(sub.got))
	}
	if sub.got[0].Title != "first" || sub.got[1].Title != "second" {
		t.Fatalf("the delivery path saw %q then %q, want first then second", sub.got[0].Title, sub.got[1].Title)
	}
}

// lockingSubmitter is safe to call from many goroutines at once, which the
// racing test needs and recordingSubmitter deliberately is not.
type lockingSubmitter struct {
	mu    sync.Mutex
	calls int
}

func (s *lockingSubmitter) Submit(ev Event) Disposition {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return DispositionOpened
}

func (s *lockingSubmitter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestMarkAllReadRacingAdmit: every mutation must bump the revision, or a
// renderer can miss a change and never learn it did. Run under -race.
func TestMarkAllReadRacingAdmit(t *testing.T) {
	const admits, marks = 64, 16

	clk := NewManualClock()
	clk.Advance(time.Hour)
	feed, err := NewFeed(FeedLimits{MaxOccurrences: 32, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	sub := &lockingSubmitter{}
	in, err := NewIngress(feed, sub, clk)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < admits; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct sessions: a collapse still bumps the revision, but
			// distinct keys make the expected count unambiguous.
			if _, err := in.Admit(context.Background(), Event{
				SessionID: fmt.Sprintf("s%d", i), Title: "ding",
				Kind: KindProgramNotify, Trust: TrustProgramRequest, Level: LevelInfo,
			}); err != nil {
				t.Errorf("Admit: %v", err)
			}
		}(i)
	}
	for i := 0; i < marks; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			feed.MarkAllRead()
		}()
	}
	wg.Wait()

	if got := feed.Snapshot().Revision; got < admits+marks {
		t.Fatalf("revision = %d after %d mutations, want at least %d — a mutation did not bump it",
			got, admits+marks, admits+marks)
	}
	if got := sub.count(); got != admits {
		t.Fatalf("the delivery path was invoked %d times, want %d", got, admits)
	}
}

// observingSubmitter reads the feed from INSIDE Submit, which is how the
// "recorded before delivered" half of the interval is observed: if the
// occurrence is visible there, it was filed before delivery began, so a
// delivery that panics or blocks cannot lose it.
type observingSubmitter struct {
	feed *Feed
	seen []FeedSnapshot
}

func (s *observingSubmitter) Submit(ev Event) Disposition {
	s.seen = append(s.seen, s.feed.Snapshot())
	return DispositionOpened
}

// TestOccurrenceIntervalHasBothEnds states the invariant as an interval, not a
// moment (AGENTS.md rule 3): an occurrence is present in Snapshot() from the
// return of Admit until the eviction that Dropped.Count records — and it is
// present at every point in between, not merely at the two ends.
func TestOccurrenceIntervalHasBothEnds(t *testing.T) {
	clk := NewManualClock()
	clk.Advance(time.Hour)
	feed, err := NewFeed(FeedLimits{MaxOccurrences: 2, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	sub := &observingSubmitter{feed: feed}
	in, err := NewIngress(feed, sub, clk)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}

	admit := func(session, title string) OccurrenceID {
		t.Helper()
		id, err := in.Admit(context.Background(), Event{
			SessionID: session, Title: title, Kind: KindProgramNotify,
			Trust: TrustProgramRequest, Level: LevelInfo,
		})
		if err != nil {
			t.Fatalf("Admit(%s): %v", title, err)
		}
		return id
	}
	held := func(id OccurrenceID) bool {
		t.Helper()
		for _, o := range feed.Snapshot().Occurrences {
			if o.ID == id {
				return true
			}
		}
		return false
	}

	// START: present at the return of Admit — and already present when
	// delivery was invoked, which is strictly earlier.
	first := admit("s1", "first")
	if len(sub.seen) != 1 {
		t.Fatalf("the submitter was invoked %d times, want 1", len(sub.seen))
	}
	var inFeedAtDelivery bool
	for _, o := range sub.seen[0].Occurrences {
		if o.ID == first {
			inFeedAtDelivery = true
		}
	}
	if !inFeedAtDelivery {
		t.Fatal("the occurrence was not in the feed when delivery began; a panicking delivery would lose it")
	}
	if !held(first) {
		t.Fatal("the occurrence is absent immediately after Admit returned")
	}
	if got := feed.Snapshot().Dropped.Count; got != 0 {
		t.Fatalf("Dropped.Count = %d before any eviction, want 0", got)
	}

	// MIDDLE: still there while the feed fills to its bound, and the interval
	// has not closed — asserted as "not before", which is what makes this an
	// interval rather than two unrelated moments.
	admit("s2", "second")
	if !held(first) {
		t.Fatal("the occurrence vanished before the feed was over its bound")
	}
	if got := feed.Snapshot().Dropped.Count; got != 0 {
		t.Fatalf("Dropped.Count = %d with the feed exactly at its bound, want 0", got)
	}

	// END: the eviction, and it is recorded in the same observation in which
	// the occurrence disappears.
	admit("s3", "third")
	snap := feed.Snapshot()
	if snap.Dropped.Count != 1 {
		t.Fatalf("Dropped.Count = %d after the evicting admission, want 1", snap.Dropped.Count)
	}
	if held(first) {
		t.Fatal("the occurrence survived the eviction its Dropped.Count recorded")
	}
}

// panickingSubmitter is the delivery path failing in the worst way it can.
type panickingSubmitter struct{ feed *Feed }

func (s *panickingSubmitter) Submit(ev Event) Disposition {
	panic("sink exploded")
}

// TestIngressKeepsTheRecordWhenDeliveryPanics is the paired failure case of
// "records before it submits": the record is the only thing that survives the
// moment, so it must already be filed when delivery blows up.
func TestIngressKeepsTheRecordWhenDeliveryPanics(t *testing.T) {
	clk := NewManualClock()
	clk.Advance(time.Hour)
	feed, err := NewFeed(FeedLimits{MaxOccurrences: 10, MaxRetainedBytes: 1 << 20, CollapseWindow: 30 * time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	in, err := NewIngress(feed, &panickingSubmitter{feed: feed}, clk)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}

	func() {
		defer func() {
			if rec := recover(); rec == nil {
				t.Error("the delivery panic did not propagate; this test then proves nothing")
			}
		}()
		//nolint:errcheck // the panic is the point; Admit never returns here.
		_, _ = in.Admit(context.Background(), Event{
			SessionID: "s1", Title: "boom", Kind: KindProgramNotify,
			Trust: TrustProgramRequest, Level: LevelInfo,
		})
	}()

	snap := feed.Snapshot()
	if len(snap.Occurrences) != 1 {
		t.Fatalf("feed holds %d occurrences after a panicking delivery, want 1", len(snap.Occurrences))
	}
	if snap.Occurrences[0].Event.Title != "boom" {
		t.Fatalf("feed holds %q, want the event whose delivery panicked", snap.Occurrences[0].Event.Title)
	}
}

// TestIngressRejectsAMissingDependency: each of NewIngress's three arguments
// is required, and a nil one is refused at construction rather than becoming
// a nil dereference at the first event.
func TestIngressRejectsAMissingDependency(t *testing.T) {
	clk := NewManualClock()
	feed, err := NewFeed(FeedLimits{MaxOccurrences: 4, MaxRetainedBytes: 1 << 20, CollapseWindow: time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	cases := []struct {
		name  string
		feed  *Feed
		next  Submitter
		clock Clock
	}{
		{"no feed", nil, &recordingSubmitter{}, clk},
		{"no submitter", feed, nil, clk},
		{"no clock", feed, &recordingSubmitter{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, err := NewIngress(tc.feed, tc.next, tc.clock)
			if err == nil {
				t.Fatal("NewIngress returned no error")
			}
			if in != nil {
				t.Fatal("NewIngress returned an ingress alongside its error")
			}
		})
	}
}

// TestAdmitHonoursACancelledContext: a cancelled caller neither files a
// record nor delivers one.
func TestAdmitHonoursACancelledContext(t *testing.T) {
	clk := NewManualClock()
	clk.Advance(time.Hour)
	feed, err := NewFeed(FeedLimits{MaxOccurrences: 4, MaxRetainedBytes: 1 << 20, CollapseWindow: time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	sub := &recordingSubmitter{disp: DispositionOpened}
	in, err := NewIngress(feed, sub, clk)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := in.Admit(ctx, Event{SessionID: "s1", Title: "late", Kind: KindProgramNotify, Trust: TrustProgramRequest, Level: LevelInfo}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Admit returned %v, want context.Canceled", err)
	}
	if got := len(feed.Snapshot().Occurrences); got != 0 {
		t.Fatalf("feed holds %d occurrences after a cancelled Admit, want 0", got)
	}
	if len(sub.got) != 0 {
		t.Fatalf("the delivery path was invoked %d times after a cancelled Admit, want 0", len(sub.got))
	}

	// And Raise carries the same refusal to the transport seam.
	if out := in.Raise(ctx, Event{SessionID: "s1", Title: "late", Kind: KindProgramNotify, Trust: TrustProgramRequest, Level: LevelInfo}); !errors.Is(out.Err, context.Canceled) {
		t.Fatalf("Raise returned Err %v, want context.Canceled", out.Err)
	}
}

// TestIngressLeavesANonZeroAtAlone: a relay replaying a batch it buffered
// carries its own instants, and restamping them "now" would file yesterday's
// session end as having happened at reconnect.
func TestIngressLeavesANonZeroAtAlone(t *testing.T) {
	clk := NewManualClock()
	clk.Advance(24 * time.Hour)
	feed, err := NewFeed(FeedLimits{MaxOccurrences: 4, MaxRetainedBytes: 1 << 20, CollapseWindow: time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	in, err := NewIngress(feed, &recordingSubmitter{disp: DispositionOpened}, clk)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}

	carried := clk.Now().Add(-6 * time.Hour)
	if _, err := in.Admit(context.Background(), Event{
		SessionID: "s1", Title: "replayed", Kind: KindSessionEnded, Trust: TrustAttested, Level: LevelInfo,
		At: carried,
	}); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	got := feed.Snapshot().Occurrences[0].Event.At
	if !got.Equal(carried) {
		t.Fatalf("filed At %v, want the carried %v — ingress restamped a replayed instant", got, carried)
	}
}

// TestIngressCarriesAttributionBackendToTheFeed: the field Task 2 added is
// carried through unchanged and reaches the feed, which is what lets the feed
// tell two backends' sessions apart.
func TestIngressCarriesAttributionBackendToTheFeed(t *testing.T) {
	clk := NewManualClock()
	clk.Advance(time.Hour)
	feed, err := NewFeed(FeedLimits{MaxOccurrences: 4, MaxRetainedBytes: 1 << 20, CollapseWindow: time.Second}, clk)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	sub := &recordingSubmitter{disp: DispositionOpened}
	in, err := NewIngress(feed, sub, clk)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}

	ev := Event{SessionID: "s1", Title: "done", Kind: KindProgramNotify, Trust: TrustProgramRequest, Level: LevelInfo}
	ev.Attribution = Attribution{Backend: "relay-7", Tab: "t1", Host: "h1", Session: "s1"}
	if _, err := in.Admit(context.Background(), ev); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	if got := feed.Snapshot().Occurrences[0].Event.Attribution; got != ev.Attribution {
		t.Fatalf("the feed holds attribution %+v, want %+v", got, ev.Attribution)
	}
	if got := sub.got[0].Attribution; got != ev.Attribution {
		t.Fatalf("the delivery path saw attribution %+v, want %+v", got, ev.Attribution)
	}
}
