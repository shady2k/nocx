package notify_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/notify"
)

// fakeFocus is a scriptable Focus for policy tests.
type fakeFocus struct {
	focused bool
	session string
}

func (f *fakeFocus) WindowFocused() bool    { return f.focused }
func (f *fakeFocus) FocusedSession() string { return f.session }

// unlockSink holds an invocation open until release closes, then returns
// immediately — it never waits for the delivery deadline, so a test frees
// the in-flight slot when its assertion is observable instead of sleeping
// out the timeout (AGENTS.md: a test may not depend on timing). It is the
// channel-release half of gateSink (which exists for the deadline-observing
// tests and deliberately blocks past the deadline).
type unlockSink struct {
	called  chan struct{}
	release chan struct{}
}

func (s *unlockSink) Deliver(ctx context.Context, d notify.Delivery) error {
	select {
	case s.called <- struct{}{}:
	default:
	}
	<-s.release
	return nil
}

func (s *unlockSink) LeavesMachine() bool { return false }

// policyTest wires a router, one recording sink, a manual clock and a
// scriptable focus into a Policy. Tests drive windows with advanceWindow;
// nothing sleeps — the manual clock fires deliveries synchronously.
type policyTest struct {
	t      *testing.T
	router *notify.Router
	sink   *recordingSink
	clock  *notify.ManualClock
	focus  *fakeFocus
	policy *notify.Policy
	window time.Duration
}

func newPolicyTest(t *testing.T, opts ...notify.PolicyOption) *policyTest {
	t.Helper()
	sink := &recordingSink{notified: make(chan struct{}, 64)}
	table := notify.Table{
		{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: sink, Destination: notify.Destination{Target: "toast"}},
		},
		{Kind: kindB, Trust: notify.TrustProgramRequest}: {
			{Sink: sink, Destination: notify.Destination{Target: "toast"}},
		},
	}
	router, err := notify.NewRouter(table, testLimits())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	clock := notify.NewManualClock()
	focus := &fakeFocus{}
	window := 8 * time.Second // the design's window, termic's number (§6.2)
	policy, err := notify.NewPolicy(context.Background(), router, window, focus, clock, opts...)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	return &policyTest{t: t, router: router, sink: sink, clock: clock, focus: focus, policy: policy, window: window}
}

// advanceWindow closes every open debounce window, firing its deliveries
// synchronously.
func (pt *policyTest) advanceWindow() {
	pt.clock.Advance(pt.window)
}

// mk returns an event for session and kind carrying body, with the
// attribution the backend would stamp for that session.
func (pt *policyTest) mk(session string, kind notify.Kind, body string) notify.Event {
	ev := eventFor(kind, notify.TrustProgramRequest)
	ev.SessionID = session
	ev.Body = body
	ev.Attribution = notify.Attribution{Tab: "tab-" + session, Host: "host", Session: session}
	return ev
}

// ---------------------------------------------------------------------------
// Suppression (design §6.1)

// TestPolicy_Suppression_FocusedPaneSuppressed: nothing is delivered about
// the tab the user is looking at in a focused window; the same event with
// the window unfocused is delivered; and an event about a DIFFERENT tab in
// a focused window is delivered.
func TestPolicy_Suppression_FocusedPaneSuppressed(t *testing.T) {
	t.Run("focused tab suppressed", func(t *testing.T) {
		pt := newPolicyTest(t)
		pt.focus.focused = true
		pt.focus.session = "s1"
		if d := pt.policy.Submit(pt.mk("s1", kindA, "done")); d != notify.DispositionSuppressed {
			t.Fatalf("Submit = %v, want suppressed", d)
		}
		pt.advanceWindow()
		if got := pt.sink.count(); got != 0 {
			t.Fatalf("%d deliveries for the focused tab, want 0", got)
		}
	})

	t.Run("same event, window unfocused, delivered", func(t *testing.T) {
		pt := newPolicyTest(t)
		pt.focus.focused = false
		pt.focus.session = "s1"
		if d := pt.policy.Submit(pt.mk("s1", kindA, "done")); d != notify.DispositionOpened {
			t.Fatalf("Submit = %v, want opened", d)
		}
		pt.advanceWindow()
		if got := pt.sink.count(); got != 1 {
			t.Fatalf("%d deliveries, want 1", got)
		}
	})

	t.Run("other tab delivered while focused", func(t *testing.T) {
		pt := newPolicyTest(t)
		pt.focus.focused = true
		pt.focus.session = "s1"
		if d := pt.policy.Submit(pt.mk("s2", kindA, "done")); d != notify.DispositionOpened {
			t.Fatalf("Submit = %v, want opened", d)
		}
		pt.advanceWindow()
		if got := pt.sink.count(); got != 1 {
			t.Fatalf("%d deliveries for the other tab, want 1", got)
		}
	})
}

// TestPolicy_Suppression_RecheckedAtDelivery: focus landing on the tab while
// its window is open suppresses the window's closing summary — suppression
// applies to the delivery itself, not only to submit, so nothing is delivered
// about the tab the user is looking at even when the window opened before the
// focus landed.
//
// The leading delivery is NOT retracted, and must not be: at the moment it
// went out the user was looking elsewhere, so telling them was right. Only
// what the window still owes is suppressed.
func TestPolicy_Suppression_RecheckedAtDelivery(t *testing.T) {
	pt := newPolicyTest(t)
	pt.focus.focused = false
	if d := pt.policy.Submit(pt.mk("s1", kindA, "done")); d != notify.DispositionOpened {
		t.Fatalf("Submit = %v, want opened", d)
	}
	if d := pt.policy.Submit(pt.mk("s1", kindA, "and again")); d != notify.DispositionCoalesced {
		t.Fatalf("second Submit = %v, want coalesced", d)
	}
	if got := pt.sink.count(); got != 1 {
		t.Fatalf("%d deliveries on the leading edge, want 1", got)
	}

	// The user looks at the tab before the window closes.
	pt.focus.focused = true
	pt.focus.session = "s1"
	pt.advanceWindow()
	if got := pt.sink.count(); got != 1 {
		t.Fatalf("%d deliveries after focus landed, want 1 — the summary must be suppressed", got)
	}
}

// TestPolicy_SuppressedEvents_NotCounted: events dropped by the focus rule
// never enter a window, so they do not inflate the count of a later one.
func TestPolicy_SuppressedEvents_NotCounted(t *testing.T) {
	pt := newPolicyTest(t)
	pt.focus.focused = true
	pt.focus.session = "s1"
	for i := range 3 {
		if d := pt.policy.Submit(pt.mk("s1", kindA, "spam")); d != notify.DispositionSuppressed {
			t.Fatalf("submit %d = %v, want suppressed", i, d)
		}
	}
	pt.focus.focused = false
	if d := pt.policy.Submit(pt.mk("s1", kindA, "real")); d != notify.DispositionOpened {
		t.Fatalf("Submit = %v, want opened", d)
	}
	pt.advanceWindow()
	got := pt.sink.received()
	if len(got) != 1 {
		t.Fatalf("%d deliveries, want 1", len(got))
	}
	if got[0].Event.Body != "real" {
		t.Fatalf("delivered body = %q, want the single real event — suppressed events were not counted", got[0].Event.Body)
	}
}

// ---------------------------------------------------------------------------
// Debounce and coalescing (design §6.2)

// TestPolicy_Debounce_OneNotificationPerWindow: a tight loop of events is one
// notification at the leading edge plus one summary per window naming what was
// held back — never one per iteration, and never silence for the length of the
// window. A second burst after the window closes opens a second window, and
// time passing with no events delivers nothing further (a stale timer cannot
// double-deliver).
func TestPolicy_Debounce_OneNotificationPerWindow(t *testing.T) {
	pt := newPolicyTest(t)

	// A "loop printing OSC 9" burst: five events, same session and kind.
	for i := range 5 {
		want := notify.DispositionOpened
		if i > 0 {
			want = notify.DispositionCoalesced
		}
		if d := pt.policy.Submit(pt.mk("s1", kindA, fmt.Sprintf("iteration %d", i))); d != want {
			t.Fatalf("submit %d = %v, want %v", i, d, want)
		}
	}
	// The leading edge: the first event is out already, carrying its own
	// body — the burst does not make the user wait for the window.
	leading := pt.sink.received()
	if len(leading) != 1 {
		t.Fatalf("%d deliveries before the window closed, want 1 (the leading edge)", len(leading))
	}
	if body := leading[0].Event.Body; body != "iteration 0" {
		t.Fatalf("leading body = %q, want the first event's own body verbatim", body)
	}

	pt.advanceWindow()
	got := pt.sink.received()
	if len(got) != 2 {
		t.Fatalf("%d deliveries for one window, want 2 (leading + summary)", len(got))
	}
	summary := got[1].Event
	if summary.Title != "4 more notifications" {
		t.Fatalf("summary title = %q, want %q naming the suppressed count", summary.Title, "4 more notifications")
	}
	if summary.Body != "title — iteration 4" {
		t.Fatalf("summary body = %q, want the newest held-back event's content in full (title and body)", summary.Body)
	}
	if summary.Attribution != (notify.Attribution{Tab: "tab-s1", Host: "host", Session: "s1"}) {
		t.Fatalf("summary attribution = %+v, want the keyed session's", summary.Attribution)
	}

	// A second burst after the window closed: a second window, so a second
	// leading delivery and a second summary — never one per iteration.
	pt.policy.Submit(pt.mk("s1", kindA, "again"))
	pt.policy.Submit(pt.mk("s1", kindA, "again"))
	pt.advanceWindow()
	if got := pt.sink.count(); got != 4 {
		t.Fatalf("%d deliveries after two windows, want 4", got)
	}

	// Time passing with no events delivers nothing further.
	pt.advanceWindow()
	if got := pt.sink.count(); got != 4 {
		t.Fatalf("%d deliveries after an empty window, want 4", got)
	}
}

// TestPolicy_Debounce_WindowIsAnInterval: the debounce window is an
// interval with both ends, not a mute button. Inside the window a repeat is
// coalesced (nothing is delivered yet); the moment the deadline passes the
// pending notification is delivered exactly once; a repeat after the close
// opens a fresh window. The manual clock drives the window half-way and
// then to the deadline — no test sleeps a window (AGENTS.md: a test may
// not depend on timing).
func TestPolicy_Debounce_WindowIsAnInterval(t *testing.T) {
	pt := newPolicyTest(t)

	if d := pt.policy.Submit(pt.mk("s1", kindA, "first")); d != notify.DispositionOpened {
		t.Fatalf("first Submit = %v, want opened", d)
	}

	// Half the window: still inside it, a repeat is held back and nothing
	// further is delivered — the leading edge already went out at submit.
	pt.clock.Advance(pt.window / 2)
	if d := pt.policy.Submit(pt.mk("s1", kindA, "second")); d != notify.DispositionCoalesced {
		t.Fatalf("repeat inside the window = %v, want coalesced", d)
	}
	if got := pt.sink.count(); got != 1 {
		t.Fatalf("%d deliveries inside the window, want 1 (the leading edge only)", got)
	}

	// The rest of the window: the deadline passes and the summary for the
	// one held-back event is delivered, exactly once.
	pt.clock.Advance(pt.window / 2)
	got := pt.sink.received()
	if len(got) != 2 {
		t.Fatalf("%d deliveries at window close, want 2", len(got))
	}
	if summary := got[1].Event; summary.Title != "1 more notification" || summary.Body != "title — second" {
		t.Fatalf("summary = %q / %q, want the count in the title and the held-back content in the body", summary.Title, summary.Body)
	}

	// After the window: the next repeat opens a fresh window and is
	// delivered at once — a debounce is an interval, not a mute.
	if d := pt.policy.Submit(pt.mk("s1", kindA, "third")); d != notify.DispositionOpened {
		t.Fatalf("Submit after the window closed = %v, want opened", d)
	}
	if got := pt.sink.count(); got != 3 {
		t.Fatalf("%d deliveries after the fresh window opened, want 3", got)
	}
	pt.advanceWindow()
	if got := pt.sink.count(); got != 3 {
		t.Fatalf("%d deliveries after an empty window closed, want 3", got)
	}
}

// TestPolicy_Coalescing_PayloadIndependent: neither the debounce key nor the
// coalescing count reads Title or Body (design §6.2, acceptance criterion
// 6). Bodies that look like counts, bodies of wildly different sizes, and
// invalid-encoding bodies must not change the key or the count; a different
// session or kind is a different window, never a merge.
func TestPolicy_Coalescing_PayloadIndependent(t *testing.T) {
	pt := newPolicyTest(t)

	// Bodies that look like counts plus hostile payloads: the count is the
	// EVENT count, 3, not anything parsed from the bodies.
	bodies := []string{"1", strings.Repeat("x", 1<<20), "\xff\xfe"}
	for i, body := range bodies {
		want := notify.DispositionOpened
		if i > 0 {
			want = notify.DispositionCoalesced
		}
		if d := pt.policy.Submit(pt.mk("s1", kindA, body)); d != want {
			t.Fatalf("submit %q = %v, want %v", body, d, want)
		}
	}
	pt.advanceWindow()
	got := pt.sink.received()
	if len(got) != 2 {
		t.Fatalf("%d deliveries, want 2 (leading + one summary)", len(got))
	}
	summary := got[1].Event
	if summary.Title != "2 more notifications" {
		t.Fatalf("summary title = %q, want %q (count from events, not content)", summary.Title, "2 more notifications")
	}
	if summary.Body != "title — "+bodies[len(bodies)-1] {
		t.Fatalf("summary body = %q, want the newest held-back content (title and body), count still from events", summary.Body)
	}

	// The key: same {session, kind} regardless of title/body. A fresh
	// session is a fresh window — never merged into another's.
	if d := pt.policy.Submit(pt.mk("s2", kindA, "also 3")); d != notify.DispositionOpened {
		t.Fatalf("different session = %v, want opened (never merged)", d)
	}
	// A fresh kind for the same session is a fresh window too.
	if d := pt.policy.Submit(pt.mk("s1", kindB, "other kind")); d != notify.DispositionOpened {
		t.Fatalf("different kind = %v, want opened (never merged)", d)
	}
	// Each opened its own window and delivered its own leading edge; neither
	// held anything back, so their windows close silently.
	if got := pt.sink.count(); got != 4 {
		t.Fatalf("%d deliveries after two fresh windows opened, want 4", got)
	}
	pt.advanceWindow()
	if got := pt.sink.count(); got != 4 {
		t.Fatalf("%d deliveries after those windows closed, want 4", got)
	}
}

// TestPolicy_Coalescing_SummaryCarriesTheNewestMessage: a window that held
// several events back closes with one summary that names the count in its
// TITLE and carries the newest held-back event's content in its BODY — both
// its title and its body when both exist, never half of it (nocx-jiwq.5). A
// bodyless message falls back to its title, so a banner never says only the
// count.
func TestPolicy_Coalescing_SummaryCarriesTheNewestMessage(t *testing.T) {
	pt := newPolicyTest(t)
	// Per-event titles so the summary's content can be told apart from the
	// leading delivery's, and a bodyless event to exercise the fallback.
	evs := []notify.Event{
		pt.mk("s1", kindA, "compiled in 4.2s"),
		pt.mk("s1", kindA, "3 tests failed in api_test.go"),
		pt.mk("s1", kindA, "deploy exited 1"),
	}
	evs[0].Title = "build finished"
	evs[1].Title = "tests failed"
	evs[2].Title = "deploy crashed"
	for i, ev := range evs {
		want := notify.DispositionOpened
		if i > 0 {
			want = notify.DispositionCoalesced
		}
		if d := pt.policy.Submit(ev); d != want {
			t.Fatalf("submit %d = %v, want %v", i, d, want)
		}
	}
	pt.advanceWindow()
	got := pt.sink.received()
	if len(got) != 2 {
		t.Fatalf("%d deliveries, want 2 (leading + summary)", len(got))
	}
	// The leading edge went out with the FIRST event's own content, unaltered.
	leading := got[0].Event
	if leading.Title != "build finished" || leading.Body != "compiled in 4.2s" {
		t.Fatalf("leading edge altered: title %q body %q", leading.Title, leading.Body)
	}
	// The summary names the count in the title and carries the NEWEST
	// held-back event's content in the body — its title AND its body joined,
	// the case this test exists for — keeping the keyed session's attribution.
	summary := got[1].Event
	if summary.Title != "2 more notifications" {
		t.Fatalf("summary title = %q, want %q", summary.Title, "2 more notifications")
	}
	if summary.Body != "deploy crashed — deploy exited 1" {
		t.Fatalf("summary body = %q, want the newest held-back title and body both", summary.Body)
	}
	if summary.SessionID != "s1" || summary.Attribution.Session != "s1" {
		t.Fatalf("summary lost its session attribution: %+v", summary.Attribution)
	}

	// A bodyless held-back message still says something: the count in the
	// title and the newest message even when it lived only in a title.
	pt2 := newPolicyTest(t)
	if d := pt2.policy.Submit(pt2.mk("s1", kindA, "warm start")); d != notify.DispositionOpened {
		t.Fatalf("Submit = %v, want opened", d)
	}
	bare := pt2.mk("s1", kindA, "")
	bare.Title = "remote build done"
	if d := pt2.policy.Submit(bare); d != notify.DispositionCoalesced {
		t.Fatalf("bodyless Submit = %v, want coalesced", d)
	}
	pt2.advanceWindow()
	got2 := pt2.sink.received()
	if len(got2) != 2 {
		t.Fatalf("%d deliveries, want 2", len(got2))
	}
	if s := got2[1].Event; s.Title != "1 more notification" || s.Body != "remote build done" {
		t.Fatalf("bodyless summary = %q / %q, want the count title and the title as the message", s.Title, s.Body)
	}
}

// TestPolicy_Debounce_TwoSessionsTwoNotifications: two sessions emitting the
// same kind produce two notifications with their own attribution, never one
// merged (acceptance criterion 2).
func TestPolicy_Debounce_TwoSessionsTwoNotifications(t *testing.T) {
	pt := newPolicyTest(t)

	submits := []struct {
		session string
		want    notify.Disposition
	}{
		{"s1", notify.DispositionOpened},
		{"s2", notify.DispositionOpened},
		{"s1", notify.DispositionCoalesced},
		{"s2", notify.DispositionCoalesced},
	}
	for i, s := range submits {
		if d := pt.policy.Submit(pt.mk(s.session, kindA, "spam")); d != s.want {
			t.Fatalf("submit %d (session %s) = %v, want %v", i, s.session, d, s.want)
		}
	}
	pt.advanceWindow()

	got := pt.sink.received()
	if len(got) != 4 {
		t.Fatalf("%d deliveries, want 4 — a leading edge and a summary per session, never merged", len(got))
	}
	// Each session's own window: its leading delivery, then its own summary.
	// Nothing merges across sessions and no summary borrows another's count.
	summaries := map[string]notify.Delivery{}
	for _, d := range got {
		// The summary carries the count in its TITLE and the held-back body
		// ("spam" in both sessions), so the count title is what identifies it.
		if d.Event.Title == "1 more notification" {
			summaries[d.Event.SessionID] = d
		}
	}
	for _, session := range []string{"s1", "s2"} {
		d, ok := summaries[session]
		if !ok {
			t.Fatalf("no window summary for session %s", session)
		}
		if d.Event.Attribution.Session != session {
			t.Fatalf("session %s: summary attribution = %q, want its own", session, d.Event.Attribution.Session)
		}
	}
}

// TestPolicy_SingleEvent_DeliveredImmediately: one event is delivered at
// once, with its own content, and the debounce window does not delay it. The
// window exists to suppress what FOLLOWS, not to hold the first one back —
// a build that finishes announces itself now, not a window later.
//
// The clock never advances in this test, which is the whole point: every
// other test in this file advances it before asserting, and that is how a
// uniformly late notification stayed invisible (nocx-jiwq.4).
func TestPolicy_SingleEvent_DeliveredImmediately(t *testing.T) {
	pt := newPolicyTest(t)
	if d := pt.policy.Submit(pt.mk("s1", kindA, "build finished")); d != notify.DispositionOpened {
		t.Fatalf("Submit = %v, want opened", d)
	}
	got := pt.sink.received()
	if len(got) != 1 {
		t.Fatalf("%d deliveries with the clock untouched, want 1", len(got))
	}
	if d := got[0].Event; d.Title != "title" || d.Body != "build finished" {
		t.Fatalf("single event altered: title %q body %q", d.Title, d.Body)
	}
}

// TestPolicy_LoneEvent_NoSummaryAtWindowClose: the window of a lone event
// closes with nothing suppressed, so it delivers nothing. Without this the
// leading-edge delivery would be followed by a redundant "1 notification"
// and every notification would arrive twice.
func TestPolicy_LoneEvent_NoSummaryAtWindowClose(t *testing.T) {
	pt := newPolicyTest(t)
	pt.policy.Submit(pt.mk("s1", kindA, "build finished"))
	if got := pt.sink.count(); got != 1 {
		t.Fatalf("%d deliveries before the window closed, want 1", got)
	}
	pt.advanceWindow()
	if got := pt.sink.count(); got != 1 {
		t.Fatalf("%d deliveries after the window closed, want 1 — nothing was suppressed", got)
	}
}

// ---------------------------------------------------------------------------
// Delivery outcomes are observable (design §6.4)

// TestPolicy_ResultHandler_SeesRefusedDelivery: when a window-close
// delivery is refused by admission, the result handler observes the refusal
// — a failed delivery is visible, not silently dropped.
func TestPolicy_ResultHandler_SeesRefusedDelivery(t *testing.T) {
	// Saturate the router: one in-flight slot, zero queue places, a gated
	// sink holding the slot.
	gated := &unlockSink{called: make(chan struct{}, 1), release: make(chan struct{})}
	router, err := notify.NewRouter(notify.Table{
		{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: &recordingSink{}, Destination: notify.Destination{Target: "toast"}},
		},
		{Kind: kindB, Trust: notify.TrustProgramRequest}: {
			{Sink: gated, Destination: notify.Destination{Target: "gate"}},
		},
	}, notify.Limits{MaxInFlight: 1, MaxQueued: 0, MaxRetained: 0, DeliveryTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	results := make(chan notify.Outcome, 1)
	clock := notify.NewManualClock()
	policy, err := notify.NewPolicy(context.Background(), router, 8*time.Second,
		&fakeFocus{}, clock,
		notify.WithResultHandler(func(out notify.Outcome) { results <- out }))
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		router.Raise(context.Background(), event(kindB))
	}()
	<-gated.called // the slot is held

	// The debounce window closes; the delivery is refused on the queue
	// bound and the handler observes the refusal — a failed delivery is
	// visible, not silently dropped.
	policy.Submit(event(kindA))
	clock.Advance(8 * time.Second)
	select {
	case out := <-results:
		var refused *notify.RefusedError
		if !errors.As(out.Err, &refused) {
			t.Fatalf("handler outcome Err = %v, want a refusal", out.Err)
		}
		// The refused delivery still carries its resolved set.
		if len(out.Resolved) != 1 {
			t.Fatalf("refused outcome resolved %d routes, want 1", len(out.Resolved))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("result handler was not called for the refused delivery")
	}

	close(gated.release)
	<-done
}

// TestPolicy_ResultHandler_SeesSinkError: a sink that fails at window-close
// delivery reaches the handler as a failed delivery — visible, not dropped —
// and the policy survives: the next window still delivers cleanly (the
// paired positive assertion: one failed window never wedges the policy).
func TestPolicy_ResultHandler_SeesSinkError(t *testing.T) {
	failing := &errSink{err: errPolicySink}
	clean := &recordingSink{notified: make(chan struct{}, 64)}
	router, err := notify.NewRouter(notify.Table{
		{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: failing, Destination: notify.Destination{Target: "failing"}},
		},
		{Kind: kindB, Trust: notify.TrustProgramRequest}: {
			{Sink: clean, Destination: notify.Destination{Target: "clean"}},
		},
	}, testLimits())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	results := make(chan notify.Outcome, 2)
	clock := notify.NewManualClock()
	policy, err := notify.NewPolicy(context.Background(), router, 8*time.Second,
		&fakeFocus{}, clock,
		notify.WithResultHandler(func(out notify.Outcome) { results <- out }))
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}

	// The failing window: the sink's rejection is a failed delivery the
	// handler observes — admission admitted it (Err nil), the failure is
	// in Results, and the resolved set is intact.
	policy.Submit(event(kindA))
	clock.Advance(8 * time.Second)
	out := <-results
	if out.Err != nil {
		t.Fatalf("sink failure reported as an admission failure: %v", out.Err)
	}
	if len(out.Resolved) != 1 || len(out.Results) != 1 {
		t.Fatalf("outcome routes: resolved %d, results %d; want 1 and 1", len(out.Resolved), len(out.Results))
	}
	if !errors.Is(out.Results[0].Err, errPolicySink) {
		t.Fatalf("handler outcome result err = %v, want the sink's error", out.Results[0].Err)
	}

	// And on an ordinary machine it succeeds: the next window delivers
	// cleanly.
	policy.Submit(event(kindB))
	clock.Advance(8 * time.Second)
	out = <-results
	if out.Err != nil || len(out.Results) != 1 || out.Results[0].Err != nil {
		t.Fatalf("window after a sink failure: %+v", out.Results)
	}
	if got := clean.count(); got != 1 {
		t.Fatalf("clean sink delivered %d, want 1", got)
	}
}

// TestPolicy_ResultHandler_SeesPanickingSink: a sink that panics at
// window-close delivery reaches the handler as a failed delivery, and the
// policy is neither taken down nor wedged: the next window still delivers
// cleanly.
func TestPolicy_ResultHandler_SeesPanickingSink(t *testing.T) {
	clean := &recordingSink{notified: make(chan struct{}, 64)}
	router, err := notify.NewRouter(notify.Table{
		{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: panickingSink{}, Destination: notify.Destination{Target: "boom"}},
		},
		{Kind: kindB, Trust: notify.TrustProgramRequest}: {
			{Sink: clean, Destination: notify.Destination{Target: "clean"}},
		},
	}, testLimits())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	results := make(chan notify.Outcome, 2)
	clock := notify.NewManualClock()
	policy, err := notify.NewPolicy(context.Background(), router, 8*time.Second,
		&fakeFocus{}, clock,
		notify.WithResultHandler(func(out notify.Outcome) { results <- out }))
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}

	policy.Submit(event(kindA))
	clock.Advance(8 * time.Second)
	out := <-results
	if out.Err != nil {
		t.Fatalf("panic reported as an admission failure: %v", out.Err)
	}
	if len(out.Results) != 1 || out.Results[0].Err == nil {
		t.Fatalf("panicking route produced no recorded failure: %+v", out.Results)
	}
	if !strings.Contains(out.Results[0].Err.Error(), "panicked") {
		t.Fatalf("result err %v does not name the panic", out.Results[0].Err)
	}

	// The policy survived the panic: the next window delivers cleanly.
	policy.Submit(event(kindB))
	clock.Advance(8 * time.Second)
	out = <-results
	if out.Err != nil || len(out.Results) != 1 || out.Results[0].Err != nil {
		t.Fatalf("window after a panicking sink: %+v", out.Results)
	}
	if got := clean.count(); got != 1 {
		t.Fatalf("clean sink delivered %d, want 1", got)
	}
}

// TestPolicy_ResultHandler_SeesDeadlineExpiredSink: a sink that blocks past
// its delivery deadline is a failed delivery the handler observes, and the
// policy is neither taken down nor wedged: the next window still delivers
// cleanly. The wait is for the observable outcome on the results channel,
// never a duration; the 100ms deadline is the router's own, the way
// notify_test.go's deadline tests set it.
func TestPolicy_ResultHandler_SeesDeadlineExpiredSink(t *testing.T) {
	clean := &recordingSink{notified: make(chan struct{}, 64)}
	router, err := notify.NewRouter(notify.Table{
		{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: deadlineSink{}, Destination: notify.Destination{Target: "wedge"}},
		},
		{Kind: kindB, Trust: notify.TrustProgramRequest}: {
			{Sink: clean, Destination: notify.Destination{Target: "clean"}},
		},
	}, notify.Limits{MaxInFlight: 8, MaxQueued: 8, MaxRetained: 1 << 20, DeliveryTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	results := make(chan notify.Outcome, 2)
	clock := notify.NewManualClock()
	policy, err := notify.NewPolicy(context.Background(), router, 8*time.Second,
		&fakeFocus{}, clock,
		notify.WithResultHandler(func(out notify.Outcome) { results <- out }))
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}

	// The wedged window: the sink ignores cancellation until its deadline
	// fires, so the delivery fails with DeadlineExceeded — a failed
	// delivery the handler observes.
	policy.Submit(event(kindA))
	clock.Advance(8 * time.Second)
	out := <-results
	if out.Err != nil {
		t.Fatalf("deadline expiry reported as an admission failure: %v", out.Err)
	}
	if len(out.Results) != 1 || out.Results[0].Err == nil {
		t.Fatalf("wedged route produced no recorded failure: %+v", out.Results)
	}
	if !errors.Is(out.Results[0].Err, context.DeadlineExceeded) {
		t.Fatalf("wedged route err = %v, want DeadlineExceeded", out.Results[0].Err)
	}

	// And on an ordinary machine it succeeds: the next window delivers
	// cleanly — one wedged sink never wedges the policy.
	policy.Submit(event(kindB))
	clock.Advance(8 * time.Second)
	out = <-results
	if out.Err != nil || len(out.Results) != 1 || out.Results[0].Err != nil {
		t.Fatalf("window after a wedged sink: %+v", out.Results)
	}
	if got := clean.count(); got != 1 {
		t.Fatalf("clean sink delivered %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// The ADR-0029 differential property test

// payloadValues is a hand-rolled set of schema-valid presentation values
// covering what resolution must not care about: normal text, the empty
// string, Unicode, bidi controls, the injection vectors of design §4.2
// (CR, LF, NUL, quotes, URL metacharacters), sizes far beyond any sink's
// limit, and byte sequences that are not valid UTF-8 — valid in a Go
// string, rejected by a validating sink, and exactly the "invalid-encoding"
// case the ADR's generator must not exclude (ADR-0029 §4.3).
func payloadValues() []string {
	return []string{
		"",
		"spam",
		"build finished",
		"Ваш банк: подтвердите вход",
		"🚀",
		"naïve — naïve",
		"\n",
		"\r\n",
		"\x00",
		"\x07",
		"\x1b",
		"\t",
		"\u202e\u202c",
		"\u202a",
		"\"",
		"\\",
		`{"a":1}`,
		"%2F",
		"?",
		"#",
		"http://example.com/",
		"a\xff",
		"\xfe\xff",
		"abc\xc3(",
		"\x80\x80",
		"end\xed\xa0\x80",
		strings.Repeat("x", 1<<20), // oversized: far beyond any sink limit
		strings.Repeat("Б", 1<<20), // oversized, multibyte
	}
}

// randomPayload draws a hostile payload the way a program would write one:
// arbitrary bytes (including invalid UTF-8), control characters, and sizes
// from empty to far beyond any sink limit.
func randomPayload(rng *rand.Rand) string {
	alphabets := [][]byte{
		[]byte("abcdefghijklmnopqrstuvwxyz ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"),
		[]byte("\n\r\x00\x07\x1b\t\"\\{}%?#&;'`"),
		[]byte("Ваш банк: подтвердите вход 🚀 —"),
		{0x80, 0xff, 0xfe, 0xc3, 0x28, 0xed, 0xa0, 0x80},
	}
	alphabet := alphabets[rng.IntN(len(alphabets))]
	var n int
	switch rng.IntN(3) {
	case 0:
		n = rng.IntN(64)
	case 1:
		n = rng.IntN(1 << 16)
	default:
		n = rng.IntN(1 << 20)
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rng.IntN(len(alphabet))]
	}
	return string(b)
}

// payload is one (title, body) presentation.
type payload struct {
	title, body string
}

// sameResolution compares two resolved route sets by the ADR-0029 contract:
// the same sinks (by identity), the same target identifiers, the same
// credentials, the same destination and the same method. The destination
// value carries target, credentials and method; DeepEqual covers all of
// them, so a destination that gains a field is compared automatically.
func sameResolution(a, b []notify.Route) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Sink != b[i].Sink {
			return false
		}
		if !reflect.DeepEqual(a[i].Destination, b[i].Destination) {
			return false
		}
	}
	return true
}

// sameOutcome compares two raises by the ADR-0029 contract: the same
// resolved route set in the same order and, for every delivery, the same
// sink identity, the same destination and a clean result. It compares what
// actually came out of the pipeline — a divergence in routing, ordering or
// per-route results fails it — and it requires both raises to have
// delivered: a refused pair of raises would only prove the refusal was
// payload-independent, which is not the property.
func sameOutcome(a, b notify.Outcome) bool {
	if a.Err != nil || b.Err != nil {
		return false
	}
	if !sameResolution(a.Resolved, b.Resolved) {
		return false
	}
	if len(a.Results) != len(b.Results) {
		return false
	}
	for i := range a.Results {
		if a.Results[i].Route.Sink != b.Results[i].Route.Sink {
			return false
		}
		if !reflect.DeepEqual(a.Results[i].Route.Destination, b.Results[i].Route.Destination) {
			return false
		}
		if a.Results[i].Err != nil || b.Results[i].Err != nil {
			return false
		}
	}
	return true
}

// propertyRouter is the table the differential test resolves through: local
// and network rows, several kinds, and one kind (kindD) with no row at all,
// so default-deny — the empty resolution — is part of the compared space.
func propertyRouter(t *testing.T) *notify.Router {
	t.Helper()
	toast := &recordingSink{}
	banner := &recordingSink{}
	push := &recordingSink{leaves: true}
	table := notify.Table{
		{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: toast, Destination: notify.Destination{Target: "toast"}},
			{Sink: banner, Destination: notify.Destination{Target: "banner"}},
		},
		{Kind: kindA, Trust: notify.TrustAttested}: {
			{Sink: toast, Destination: notify.Destination{Target: "toast"}},
			{Sink: push, Destination: notify.Destination{Target: "push"}},
		},
		{Kind: kindB, Trust: notify.TrustAttested}: {
			{Sink: push, Destination: notify.Destination{Target: "push-b"}},
		},
		{Kind: notify.KindPaneWorkFinished, Trust: notify.TrustHeuristic}: {
			{Sink: toast, Destination: notify.Destination{Target: "toast"}},
		},
	}
	router, err := notify.NewRouter(table, testLimits())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return router
}

// TestNoninterference_ResolutionIndependentOfPresentation is the executable
// form of ADR-0029's differential rule (§2.2, §4.3): for any two
// schema-valid payloads differing only in title or body, the pipeline
// produces the same sinks, the same target identifiers, the same
// credentials, the same destination and the same order. It is exercised
// through Router.Raise, the level that takes the whole event: the router's
// Resolve cannot see title or body, so at that level noninterference is
// guaranteed by the signature and a test there can only be a tautology.
// Raising both events compares what actually came out — the resolved route
// set and, for every delivery, the sink identity, the destination and the
// order (acceptance criterion 5). The subscription route has no
// whole-event entry point (Resolve maps only kind and trust), so its
// noninterference is type-guaranteed and is not part of this test. It
// ranges over every schema-valid input — not only payloads a sink would
// accept — because restricting the generator would exclude exactly the
// oversized and invalid-encoding cases that could diverge. Resolution is
// compared before sink-level validation (the rejection half is
// TestSinkRejection_ResolvedSetUnchanged).
func TestNoninterference_ResolutionIndependentOfPresentation(t *testing.T) {
	router := propertyRouter(t)
	rng := rand.New(rand.NewPCG(1, 2)) // #nosec G404 — deterministic seed so a divergence is reproducible; not security-critical

	// Every corpus presentation paired with every other, plus random
	// hostile pairs.
	corpus := payloadValues()
	var presentations []payload
	for _, v := range corpus {
		presentations = append(presentations,
			payload{title: v, body: ""},
			payload{title: "", body: v},
			payload{title: v, body: v},
		)
	}
	var pairs [][2]payload
	for _, a := range presentations {
		for _, b := range presentations {
			pairs = append(pairs, [2]payload{a, b})
		}
	}
	for range 2000 {
		pairs = append(pairs, [2]payload{
			{title: randomPayload(rng), body: randomPayload(rng)},
			{title: randomPayload(rng), body: randomPayload(rng)},
		})
	}

	kinds := []notify.Kind{kindA, kindB, notify.KindPaneWorkFinished, kindD}
	trusts := []notify.Trust{notify.TrustAttested, notify.TrustProgramRequest, notify.TrustHeuristic}

	checked := 0
	for _, pair := range pairs {
		for _, kind := range kinds {
			for _, trust := range trusts {
				evA := eventFor(kind, trust)
				evA.SessionID = "s-p"
				evA.Title, evA.Body = pair[0].title, pair[0].body
				evB := eventFor(kind, trust)
				evB.SessionID = "s-p"
				evB.Title, evB.Body = pair[1].title, pair[1].body
				outA := router.Raise(context.Background(), evA)
				outB := router.Raise(context.Background(), evB)
				if !sameOutcome(outA, outB) {
					t.Fatalf("resolution diverged: kind=%q trust=%q\n  a=(%q, %q)\n  b=(%q, %q)\n  outA=%+v\n  outB=%+v",
						kind, trust, pair[0].title, pair[0].body, pair[1].title, pair[1].body, outA, outB)
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("property test compared nothing")
	}
}

// TestPolicy_CoalescedDelivery_ResolvesSameAsSource: the policy writes the
// title of a window's closing summary to name the count and puts the newest
// held-back message in its body — that rewrite must not change route
// resolution, which is the invariant the policy itself could otherwise break
// (ADR-0029 §2.2). The delivered event must also keep the keyed session's
// attribution.
func TestPolicy_CoalescedDelivery_ResolvesSameAsSource(t *testing.T) {
	// Capture the outcome of the policy's own delivery — the real path,
	// not a second Resolve call. The summary is the last one delivered, so
	// this holds its outcome once the window has closed.
	var deliveredOut notify.Outcome
	pt := newPolicyTest(t, notify.WithResultHandler(func(out notify.Outcome) { deliveredOut = out }))

	source := []string{"", strings.Repeat("x", 1<<20), "\xff\xfe", "спам"}
	for _, body := range source {
		pt.policy.Submit(pt.mk("s1", kindA, body))
	}
	pt.advanceWindow()
	got := pt.sink.received()
	if len(got) != 2 {
		t.Fatalf("%d deliveries, want 2 (leading + summary)", len(got))
	}
	delivered := got[1].Event
	if delivered.Title != "3 more notifications" {
		t.Fatalf("summary title = %q, want %q naming the suppressed count", delivered.Title, "3 more notifications")
	}
	if delivered.Body != "title — "+source[len(source)-1] {
		t.Fatalf("summary body = %q, want the newest held-back content (title and body)", delivered.Body)
	}

	// The rewrite must not change resolution: raise each source event
	// through the real path and compare its outcome to the policy's own
	// delivery outcome — identical sinks, destinations and order.
	for _, body := range source {
		orig := pt.mk("s1", kindA, body)
		out := pt.router.Raise(context.Background(), orig)
		if !sameOutcome(deliveredOut, out) {
			t.Fatalf("coalesced event resolved differently from source (body %q): delivered %+v, source %+v", body, deliveredOut, out)
		}
	}
	if delivered.SessionID != "s1" || delivered.Attribution.Session != "s1" {
		t.Fatalf("coalesced event lost its session attribution: %+v", delivered.Attribution)
	}
}

// ---------------------------------------------------------------------------
// Resolution precedes sink validation (ADR-0029 §2.2)

// TestSinkRejection_ResolvedSetUnchanged: route resolution completes before
// sink-level validation. A sink that rejects a payload — oversized, invalid
// encoding — records an attempted delivery that failed and never removes
// itself from the resolved set (acceptance criterion 5).
func TestSinkRejection_ResolvedSetUnchanged(t *testing.T) {
	rejector := &sizeRejectingSink{}
	router, err := notify.NewRouter(notify.Table{
		{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: rejector, Destination: notify.Destination{Target: "rejector"}},
		},
	}, testLimits())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	// The oversized payload is rejected by the sink…
	big := event(kindA)
	big.Body = strings.Repeat("x", 1<<20)
	resolved := router.Resolve(big.Kind, big.Trust, notify.RouteRaise)
	if len(resolved) != 1 {
		t.Fatalf("Resolve returned %d routes, want 1", len(resolved))
	}
	out := router.Raise(context.Background(), big)
	if out.Err != nil {
		t.Fatalf("Raise: %v", out.Err)
	}
	// …as a recorded failed delivery: the error is in Results…
	if len(out.Results) != 1 || out.Results[0].Err == nil {
		t.Fatalf("rejection not recorded as a failed delivery: %+v", out.Results)
	}
	// …and the resolved set is unchanged — the sink was never removed.
	if !sameResolution(resolved, out.Resolved) {
		t.Fatal("rejection changed the resolved set")
	}

	// The same resolution for an acceptable payload still names the same
	// sink: one rejection never removes a sink from the table.
	small := event(kindA)
	small.Body = "ok"
	if !sameResolution(resolved, router.Resolve(small.Kind, small.Trust, notify.RouteRaise)) {
		t.Fatal("resolution for an acceptable payload diverged after a rejection")
	}
	out2 := router.Raise(context.Background(), small)
	if out2.Err != nil || len(out2.Results) != 1 || out2.Results[0].Err != nil {
		t.Fatalf("acceptable payload did not deliver cleanly: %+v", out2.Results)
	}
	if rejector.rejectedCount() != 1 || rejector.acceptedCount() != 1 {
		t.Fatalf("sink rejected %d, accepted %d; want 1 and 1", rejector.rejectedCount(), rejector.acceptedCount())
	}
}

// errPolicySink is the failure an errSink reports.
var errPolicySink = errors.New("notify_test: sink failed")

// errSink fails every delivery with a fixed error, like a real sink
// rejecting a payload (ADR-0029 §2.3).
type errSink struct{ err error }

func (s *errSink) Deliver(ctx context.Context, d notify.Delivery) error { return s.err }

func (s *errSink) LeavesMachine() bool { return false }

// sizeRejectingSink validates like a real sink (ADR-0029 §2.3): oversized
// payloads fail visibly; everything else delivers.
type sizeRejectingSink struct {
	mu       sync.Mutex
	accepted int
	rejected int
}

const sizeRejectingSinkMax = 1 << 10

var errPayloadTooLarge = errors.New("notify_test: payload too large")

func (s *sizeRejectingSink) Deliver(ctx context.Context, d notify.Delivery) error {
	if len(d.Event.Title)+len(d.Event.Body) > sizeRejectingSinkMax {
		s.mu.Lock()
		s.rejected++
		s.mu.Unlock()
		return errPayloadTooLarge
	}
	s.mu.Lock()
	s.accepted++
	s.mu.Unlock()
	return nil
}

func (s *sizeRejectingSink) LeavesMachine() bool { return false }

func (s *sizeRejectingSink) rejectedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rejected
}

func (s *sizeRejectingSink) acceptedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

// deadlineSink blocks past its delivery deadline, like a real sink that
// cannot honor cancellation promptly; it observes the context but only
// returns when the deadline fires (ADR-0029 §2.2's "a deadline is not proof
// a goroutine stopped writing").
type deadlineSink struct{}

func (deadlineSink) Deliver(ctx context.Context, d notify.Delivery) error {
	<-ctx.Done()
	return ctx.Err()
}

func (deadlineSink) LeavesMachine() bool { return false }

// TestPolicy_ResultHandler_NamesTheEventThatFailed: the outcome the handler
// observes names the event whose delivery failed, with the ORIGINAL
// attribution — for the leading-edge delivery and for the window-closing
// summary alike (nocx-r6pxp, D3).
//
// The summary is the case that matters. It is delivered when the window
// closes, which is a whole debounce window after notify.raise answered {},
// so there is no caller left to fail to and no request context to read the
// session off. If the outcome did not carry the event, a failure there could
// not be filed beside the notification it is about.
func TestPolicy_ResultHandler_NamesTheEventThatFailed(t *testing.T) {
	failing := &errSink{err: errPolicySink}
	router, err := notify.NewRouter(notify.Table{
		{Kind: kindA, Trust: notify.TrustProgramRequest}: {
			{Sink: failing, Destination: notify.Destination{Target: "banner"}},
		},
	}, testLimits())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	results := make(chan notify.Outcome, 4)
	clock := notify.NewManualClock()
	policy, err := notify.NewPolicy(context.Background(), router, 8*time.Second,
		&fakeFocus{}, clock,
		notify.WithResultHandler(func(out notify.Outcome) { results <- out }))
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}

	attribution := notify.Attribution{Backend: "local", Tab: "7", Host: "build-01", Session: "s1"}
	first := event(kindA)
	first.Title = "build finished"
	first.Attribution = attribution
	second := event(kindA)
	second.Title = "tests failed"
	second.Attribution = attribution

	policy.Submit(first)  // the leading edge: delivered at once
	policy.Submit(second) // held back inside the window
	clock.Advance(8 * time.Second)

	leading := <-results
	if !errors.Is(leading.Results[0].Err, errPolicySink) {
		t.Fatalf("leading delivery err = %v, want the sink's error", leading.Results[0].Err)
	}
	if leading.Event.Title != "build finished" || leading.Event.Attribution != attribution {
		t.Fatalf("leading outcome names %+v, want the submitted event", leading.Event)
	}

	summary := <-results
	if !errors.Is(summary.Results[0].Err, errPolicySink) {
		t.Fatalf("summary delivery err = %v, want the sink's error", summary.Results[0].Err)
	}
	if summary.Event.Attribution != attribution || summary.Event.SessionID != first.SessionID {
		t.Fatalf("summary outcome attribution %+v/%q, want the original %+v/%q",
			summary.Event.Attribution, summary.Event.SessionID, attribution, first.SessionID)
	}
	// It is the summary that failed, not the leading event again: the row a
	// handler writes must say which delivery was lost.
	if !strings.Contains(summary.Event.Title, "1 more notification") {
		t.Fatalf("summary outcome title = %q, want the window's summary", summary.Event.Title)
	}
}

// ---------------------------------------------------------------------------
// The live debounce window (nocx-3mniv task 3)

// liveWindow is a window the user can change while the policy runs, which is
// what the composition root's settings read is: a source the policy calls,
// whose answer moves underneath it. Guarded, because the policy calls it from
// whichever goroutine submitted.
type liveWindow struct {
	mu sync.Mutex
	d  time.Duration
}

func newLiveWindow(d time.Duration) *liveWindow { return &liveWindow{d: d} }

func (w *liveWindow) get() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.d
}

func (w *liveWindow) set(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.d = d
}

// TestPolicy_Window_TheSourceGovernsFromTheFirstWindow: a policy given a
// source uses the source's answer, not the duration it was constructed with,
// for the very first window it opens. Without this the setting would take
// effect only after some unnamed warm-up, which is the same defect as not
// being live at all.
func TestPolicy_Window_TheSourceGovernsFromTheFirstWindow(t *testing.T) {
	live := newLiveWindow(3 * time.Second) // the constructed window is 8s
	pt := newPolicyTest(t, notify.WithWindowSource(live.get))

	if d := pt.policy.Submit(pt.mk("s1", kindA, "one")); d != notify.DispositionOpened {
		t.Fatalf("first Submit = %v, want opened", d)
	}
	if d := pt.policy.Submit(pt.mk("s1", kindA, "two")); d != notify.DispositionCoalesced {
		t.Fatalf("second Submit = %v, want coalesced", d)
	}

	pt.clock.Advance(3 * time.Second)
	if got := pt.sink.count(); got != 2 {
		t.Fatalf("%d deliveries three seconds in, want 2 — the leading edge and the "+
			"summary of a window the source sized at 3s, not the 8s the policy was built with", got)
	}
}

// TestPolicy_Window_AnOpenWindowKeepsTheLengthItOpenedWith is the chosen
// interval, asserted: a window's length is fixed from the moment it opens
// until the moment it closes, so shortening the setting mid-burst does not
// retime the window already running.
//
// It fails under the rejected alternative, and it fails TWICE, once through
// each of the two things a window decides. A policy that retimed this window
// to two seconds would have closed it at the three-second mark and delivered
// its summary there — the count assertion. And it would answer the submit at
// that mark with DispositionOpened, because the event would now be outside
// the window rather than inside it — the disposition assertion, which needs
// no timer at all and so holds against a retiming built any way round.
func TestPolicy_Window_AnOpenWindowKeepsTheLengthItOpenedWith(t *testing.T) {
	live := newLiveWindow(8 * time.Second)
	pt := newPolicyTest(t, notify.WithWindowSource(live.get))

	pt.policy.Submit(pt.mk("s1", kindA, "one"))
	if d := pt.policy.Submit(pt.mk("s1", kindA, "two")); d != notify.DispositionCoalesced {
		t.Fatalf("second Submit = %v, want coalesced", d)
	}

	// The user shortens the window while the burst is still running.
	live.set(2 * time.Second)

	// Three seconds in: past where a retimed window would have closed, and
	// well inside the one that is actually open.
	pt.clock.Advance(3 * time.Second)
	if got := pt.sink.count(); got != 1 {
		t.Fatalf("%d deliveries three seconds after the window was shortened, want 1 — "+
			"an open window keeps the eight seconds it opened with; a retimed one "+
			"would have closed at two and delivered its summary here", got)
	}
	if d := pt.policy.Submit(pt.mk("s1", kindA, "three")); d != notify.DispositionCoalesced {
		t.Fatalf("Submit three seconds into a window that opened at eight = %v, want "+
			"coalesced — a retimed window would call this one opened, which is the "+
			"disposition of an event decided by a value read after it arrived", d)
	}

	pt.clock.Advance(5 * time.Second) // eight in total: the length it opened with
	if got := pt.sink.count(); got != 2 {
		t.Fatalf("%d deliveries at the eight-second mark, want 2 — the window that "+
			"opened at eight seconds must still close at eight", got)
	}
}

// TestPolicy_Window_TheNextWindowUsesTheNewValue is the other end of the same
// interval: a change governs every window opened after it. Together with the
// test above, the interval is stated with both ends and both are asserted.
func TestPolicy_Window_TheNextWindowUsesTheNewValue(t *testing.T) {
	live := newLiveWindow(8 * time.Second)
	pt := newPolicyTest(t, notify.WithWindowSource(live.get))

	pt.policy.Submit(pt.mk("s1", kindA, "one"))
	pt.policy.Submit(pt.mk("s1", kindA, "two"))
	live.set(2 * time.Second)
	pt.clock.Advance(8 * time.Second) // the first window closes at its own length
	if got := pt.sink.count(); got != 2 {
		t.Fatalf("%d deliveries after the first window closed, want 2", got)
	}

	// The next window is opened after the change, so it is two seconds long.
	if d := pt.policy.Submit(pt.mk("s1", kindA, "three")); d != notify.DispositionOpened {
		t.Fatalf("Submit after the window closed = %v, want opened", d)
	}
	if d := pt.policy.Submit(pt.mk("s1", kindA, "four")); d != notify.DispositionCoalesced {
		t.Fatalf("Submit inside the new window = %v, want coalesced", d)
	}
	pt.clock.Advance(2 * time.Second)
	if got := pt.sink.count(); got != 4 {
		t.Fatalf("%d deliveries two seconds into the window that opened AFTER the "+
			"change, want 4 — the new value governs every window opened after it", got)
	}
}

// TestPolicy_Window_ANonPositiveSourceFallsBackToTheConstructedWindow: an
// unreadable setting must not silently disable the debounce. A zero window is
// not "no debouncing", it is one notification per event — the flood the policy
// exists to prevent — so the policy falls back to the window it was built
// with rather than honouring an answer it cannot use.
func TestPolicy_Window_ANonPositiveSourceFallsBackToTheConstructedWindow(t *testing.T) {
	live := newLiveWindow(0)
	pt := newPolicyTest(t, notify.WithWindowSource(live.get))

	pt.policy.Submit(pt.mk("s1", kindA, "one"))
	if d := pt.policy.Submit(pt.mk("s1", kindA, "two")); d != notify.DispositionCoalesced {
		t.Fatalf("Submit with a zero-answering source = %v, want coalesced — a zero "+
			"window would have opened a second window and delivered at once", d)
	}
	if got := pt.sink.count(); got != 1 {
		t.Fatalf("%d deliveries before any time passed, want 1", got)
	}
	pt.clock.Advance(8 * time.Second) // the constructed window
	if got := pt.sink.count(); got != 2 {
		t.Fatalf("%d deliveries at the constructed window's length, want 2", got)
	}
}
