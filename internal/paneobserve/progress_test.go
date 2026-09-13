package paneobserve_test

// The THIRD FACET: whether a pane that is working has moved lately (nocx-tnx44).
//
// A hung agent reports "working" forever, because its spinner keeps spinning
// and a spinner is chrome. What tells the two apart is not the frame — the
// frame CHANGES, second by second, while nothing at all is produced — but a
// comparison between two readings of the one region a rule named as the agent's
// own output. That comparison needs a past and a clock, which is why it lives
// in this package rather than in the driver: `agentdriver.Driver.Classify`
// holds no state between frames on purpose, because a rule that remembers is a
// rule that can be stuck.
//
// Every frame here is a committed capture from
// internal/agentdriver/testdata/captures — real bytes off a real PTY, replayed
// through the same panegrid Store production uses, from byte zero. A fixture
// written beside the classifier encodes the author's model of the TUI,
// including the parts that are wrong, and then the code and the test agree and
// are wrong together (AGENTS.md, testing rule 4).
//
// No test here sleeps, and that is a requirement rather than a style: the
// threshold has TWO ends, and a test that waited for either would be measuring
// the machine instead of the rule.

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/paneview/paneviewtest"
)

// stallAfter is the threshold every test below states, and it is deliberately
// NOT paneobserve.DefaultStallAfter: what is under test is the interval, and a
// test that borrowed production's number would still pass if the constant moved
// to a minute or to an hour.
const stallAfter = 20 * time.Second

// clock is the watcher's own clock, and the only thing that moves time in these
// tests.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newClock() *clock { return &clock{now: time.Unix(1_700_000_000, 0)} }

// watcher returns a watcher over a real grid, with this test's clock and
// threshold. The grid is returned too: these tests read frames off it directly
// to prove the CASE they are about — that the chrome moved while the transcript
// did not — rather than asserting it in a comment.
func watcher(t *testing.T, c *clock) (*paneobserve.Watcher, *paneviewtest.Views, *recorder) {
	t.Helper()
	lg := log.NewSlogAdapter(nil)
	grid := paneviewtest.NewViews(lg)
	reg, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	rec := &recorder{}
	w := paneobserve.New(lg, grid.Store, reg, paneobserve.Config{
		Now:        c.Now,
		StallAfter: stallAfter,
	})
	w.SetEmitter(rec.emit)
	return w, grid, rec
}

// feeder replays a committed capture into a real grid the way a pane does: from
// byte zero, one mark at a time. Each call advances the pane to atMs and
// returns the frame it is showing there, so a test can compare two of them.
func feeder(t *testing.T, grid *paneviewtest.Views, pane, capture string) func(atMs int64) paneview.Frame {
	t.Helper()
	path := filepath.Join("..", "agentdriver", "testdata", "captures", capture+".jsonl")
	header, chunks, err := agentcapture.Read(path)
	if err != nil {
		t.Fatalf("read capture %s: %v", capture, err)
	}
	if err := grid.Watch(pane, header.Cols, header.Rows); err != nil {
		t.Fatalf("enrol %s: %v", pane, err)
	}
	t.Cleanup(func() { grid.Withdraw(pane) })
	fed := 0
	return func(atMs int64) paneview.Frame {
		through := agentcapture.ChunksThrough(chunks, atMs, fed)
		for _, c := range chunks[fed:through] {
			grid.Feed(pane, []byte(c.Data))
		}
		fed = through
		f, err := grid.Frame(pane)
		if err != nil {
			t.Fatalf("frame %s@%dms: %v", capture, atMs, err)
		}
		return f
	}
}

// screenText renders a frame whole, so a test can state that two frames DIFFER.
// That is the case this whole facet exists for, and asserting it is what keeps
// the tests below from passing against a rule that compared frames.
func screenText(f paneview.Frame) string {
	var b strings.Builder
	for y := range f.Rows {
		b.WriteString(f.Text(y))
		b.WriteByte('\n')
	}
	return b.String()
}

// assertNews asserts that a sweep produced exactly one observation, and returns
// it.
func assertNews(t *testing.T, rec *recorder, what string) paneobserve.Observation {
	t.Helper()
	got := rec.drain()
	if len(got) != 1 {
		t.Fatalf("%s: observations = %d, want 1: %+v", what, len(got), got)
	}
	return got[0]
}

// assertSilent asserts that a sweep produced nothing.
func assertSilent(t *testing.T, rec *recorder, what string) {
	t.Helper()
	if got := rec.drain(); len(got) != 0 {
		t.Fatalf("%s: %+v was reported as news, and it is not", what, got)
	}
}

// ACCEPTANCE ONE, and the case frame equality gets wrong: an agent whose
// spinner is LIVE and whose transcript is FROZEN reads stalled once the
// threshold has passed.
//
// The capture is claude-working, and it is the only committed one that shows
// this cleanly from the first second: the turn is in flight ("* Misting… (2s)"),
// the spinner's own timer ticks, and the pane has printed nothing since the
// user's own prompt. claude-error and claude-2.1.266-api-refused show a live
// spinner over a frozen transcript too, but both are classified `error` —
// failing visibly, with retry chrome of its own — and progress answers for the
// WORKING states only.
//
// ACCEPTANCE SIX is the other half of the same interval: before the threshold
// elapses, a frozen transcript is still moving.
func TestALiveSpinnerOverAFrozenTranscriptReadsStalled(t *testing.T) {
	c := newClock()
	w, grid, rec := watcher(t, c)
	feed := feeder(t, grid, "p1", "claude-working")
	w.Watch("p1", "claude")

	first := feed(15000)
	w.Touch("p1")
	w.Sweep()
	got := assertNews(t, rec, "the first reading of a working pane")
	if got.State != agentdriver.StateWorking || got.Progress != paneobserve.ProgressMoving {
		t.Fatalf("first reading = %s/%s, want working/moving", got.State, got.Progress)
	}

	// Two seconds later the spinner has ticked and the transcript has not.
	second := feed(17000)
	if screenText(first) == screenText(second) {
		t.Fatal("the two frames are identical, so this frame cannot be the case frame equality gets wrong")
	}

	// THE LOW END. The transcript has not moved, but the threshold has not
	// passed either, so this is still a pane that is thinking.
	w.Touch("p1")
	w.Sweep()
	assertSilent(t, rec, "a frozen transcript younger than the threshold")

	// THE HIGH END, one second past it.
	c.advance(stallAfter + time.Second)
	w.Touch("p1")
	w.Sweep()
	got = assertNews(t, rec, "a frozen transcript past the threshold")
	if got.State != agentdriver.StateWorking {
		t.Errorf("stalled pane's state = %q, want %q — progress must never displace the state", got.State, agentdriver.StateWorking)
	}
	if got.Progress != paneobserve.ProgressStalled {
		t.Errorf("progress = %q, want %q", got.Progress, paneobserve.ProgressStalled)
	}
}

// ACCEPTANCE FIVE: a pane whose state is unchanged and whose progress changed
// is news. The sweep above is that assertion — the emission there carries the
// verdict it already had.
//
// This is the same fact from the other side: a pane that sends NOTHING AT ALL
// is still swept. The dirty flag comes from the session's read path, so a
// suspended or wedged agent would otherwise never be looked at again — and the
// one pane whose stall is the whole point of the facet would be the one pane
// nobody could report. No Touch below is the assertion.
func TestAWorkingPaneThatHasGoneCompletelySilentIsStillReported(t *testing.T) {
	c := newClock()
	w, grid, rec := watcher(t, c)
	feed := feeder(t, grid, "p1", "claude-working")
	w.Watch("p1", "claude")

	feed(15000)
	w.Touch("p1")
	w.Sweep()
	assertNews(t, rec, "the first reading")

	c.advance(stallAfter + time.Second)
	w.Sweep()
	got := assertNews(t, rec, "a silent working pane past the threshold")
	if got.State != agentdriver.StateWorking || got.Progress != paneobserve.ProgressStalled {
		t.Fatalf("silent pane = %s/%s, want working/stalled", got.State, got.Progress)
	}

	// And it says so ONCE. The pane is already reported as stalled, so it
	// stops qualifying for the deadline sweep — which is what keeps this from
	// being a poll of every pane per tick.
	w.Sweep()
	assertSilent(t, rec, "a pane already reported stalled")
}

// ACCEPTANCE TWO, and the recovery direction of the same interval: a GROWING
// transcript reads moving however still the chrome is.
//
// The capture is claude-2.1.266-turn at 79s and 80s, a streaming reply with NO
// spinner at all — the status stack is erased outright once Claude starts
// printing, so there is no chrome to move and the transcript is the only thing
// that changes. The first half freezes the pane deliberately (well past the
// threshold, with nothing fed), so the second half proves growth beats the
// deadline rather than merely coinciding with it.
func TestAGrowingTranscriptReadsMovingHoweverStillTheChromeIs(t *testing.T) {
	c := newClock()
	w, grid, rec := watcher(t, c)
	feed := feeder(t, grid, "p1", "claude-2.1.266-turn")
	w.Watch("p1", "claude")

	feed(79000)
	w.Touch("p1")
	w.Sweep()
	got := assertNews(t, rec, "the first reading of a streaming turn")
	if got.State != agentdriver.StateWorking || got.Progress != paneobserve.ProgressMoving {
		t.Fatalf("first reading = %s/%s, want working/moving", got.State, got.Progress)
	}

	// Nothing more arrives for a while. This pane is now genuinely stalled —
	// its turn is live and its transcript has stood still past the threshold.
	c.advance(stallAfter + time.Second)
	w.Touch("p1")
	w.Sweep()
	got = assertNews(t, rec, "a streaming turn that has gone quiet")
	if got.Progress != paneobserve.ProgressStalled {
		t.Fatalf("quiet streaming turn = %q, want %q", got.Progress, paneobserve.ProgressStalled)
	}

	// And here the reply continues: the transcript grows, the chrome does not
	// move at all, and the pane is moving again.
	feed(80000)
	w.Touch("p1")
	w.Sweep()
	got = assertNews(t, rec, "a transcript that grew after the threshold")
	if got.State != agentdriver.StateWorking {
		t.Errorf("state = %q, want %q", got.State, agentdriver.StateWorking)
	}
	if got.Progress != paneobserve.ProgressMoving {
		t.Errorf("progress = %q, want %q — a growing transcript is not a stalled pane", got.Progress, paneobserve.ProgressMoving)
	}
}

// ACCEPTANCE THREE: progress is NOT a value of the screen facet.
//
// The bead is falsified if progress is expressed as a value of
// agentdriver.State, so this asserts the shape directly, and then the
// consequence a caller actually depends on: a listing of the panes whose state
// is a working state still contains the stalled one. Two panes, one stalled and
// one moving, both working — and both in the listing.
func TestStalledIsNotAStateAndAWorkingListingStillContainsThePane(t *testing.T) {
	c := newClock()
	w, grid, rec := watcher(t, c)
	frozen := feeder(t, grid, "frozen", "claude-working")
	growing := feeder(t, grid, "growing", "claude-2.1.266-turn")
	w.Watch("frozen", "claude")
	w.Watch("growing", "claude")

	frozen(15000)
	growing(79000)
	w.Touch("frozen")
	w.Touch("growing")
	w.Sweep()
	rec.drain()

	// The frozen pane's deadline passes; the growing pane's transcript does not.
	c.advance(stallAfter + time.Second)
	growing(80000)
	w.Touch("frozen")
	w.Touch("growing")
	w.Sweep()

	// The scalar set has no member that means "stalled", and a stalled reading
	// is not a state a caller could have been handed.
	if agentdriver.State(paneobserve.ProgressStalled).Valid() {
		t.Fatalf("%q is a value of agentdriver.State — progress has been folded into the screen facet", paneobserve.ProgressStalled)
	}
	for _, s := range agentdriver.States() {
		if s == agentdriver.State(paneobserve.ProgressStalled) {
			t.Fatalf("agentdriver.States() lists %q", paneobserve.ProgressStalled)
		}
	}

	// The listing a coordinator asks for: panes whose STATE is working.
	working := map[string]paneobserve.Progress{}
	for _, p := range w.Watching() {
		o, ok := w.Snapshot(p.PaneID)
		if !ok || !o.State.Working() {
			continue
		}
		working[p.PaneID] = o.Progress
	}
	if _, ok := working["frozen"]; !ok {
		t.Fatalf("a list of working panes lost the stalled one: %+v", working)
	}
	if _, ok := working["growing"]; !ok {
		t.Fatalf("a list of working panes lost the moving one: %+v", working)
	}
	if working["frozen"] != paneobserve.ProgressStalled {
		t.Errorf("frozen pane's progress = %q, want %q", working["frozen"], paneobserve.ProgressStalled)
	}
	if working["growing"] != paneobserve.ProgressMoving {
		t.Errorf("growing pane's progress = %q, want %q", working["growing"], paneobserve.ProgressMoving)
	}
}

// A pane whose rule reads no transcript is never called stalled, and it is not
// a degraded reading: "nothing measured this" and "this stopped" are different
// claims, and only the second may be reported. The pane here is working — a
// live turn — and the frame simply has no scrollback the rule can name.
func TestAPaneWithNoMeasurableTranscriptIsNeverCalledStalled(t *testing.T) {
	c := newClock()
	w, grid, rec := watcher(t, c)
	feed := feeder(t, grid, "p1", "claude-2.1.266-turn")
	w.Watch("p1", "claude")

	// 47500ms is a turn before its elapsed timer, on a pane whose transcript is
	// the user's own prompt.
	feed(47500)
	w.Touch("p1")
	w.Sweep()
	got := assertNews(t, rec, "the first reading")
	if got.State != agentdriver.StateWorking {
		t.Fatalf("state = %q, want %q", got.State, agentdriver.StateWorking)
	}

	// An agent nothing was written for answers unknown for its whole life, and
	// unknown is not a working state: the facet has nothing to say about it.
	w.Watch("p2", "no-such-agent")
	if err := grid.Watch("p2", 40, 14); err != nil {
		t.Fatalf("enrol p2: %v", err)
	}
	t.Cleanup(func() { grid.Withdraw("p2") })
	w.Touch("p2")
	w.Sweep()
	other := assertNews(t, rec, "an agent with no driver")
	if other.State != agentdriver.StateUnknown || other.Progress != paneobserve.ProgressMoving {
		t.Fatalf("unknown pane = %s/%s, want unknown/moving", other.State, other.Progress)
	}

	c.advance(10 * stallAfter)
	w.Touch("p2")
	w.Sweep()
	// Exactly one pane is news here, and it is the one with a measurable
	// transcript: the pane the driver cannot read stays silent on the same
	// clock — and the working pane, whose transcript stands still, stalls.
	// That contrast is what makes the silence a fact about the reading rather
	// than about the deadline sweep being inert.
	got = assertNews(t, rec, "the deadline sweep")
	if got.PaneID != "p1" || got.Progress != paneobserve.ProgressStalled {
		t.Fatalf("deadline sweep reported %+v, want the measurable working pane as stalled", got)
	}
}

// Unwatch forgets the progress with everything else. A pane watched again is a
// new incarnation, and a stall measured against a previous incarnation's
// transcript is a fact about a pane nobody is looking at.
func TestUnwatchForgetsTheTranscriptAndItsClock(t *testing.T) {
	c := newClock()
	w, grid, rec := watcher(t, c)
	feed := feeder(t, grid, "p1", "claude-working")
	w.Watch("p1", "claude")
	feed(15000)
	w.Touch("p1")
	w.Sweep()
	rec.drain()

	c.advance(stallAfter + time.Second)
	w.Unwatch("p1")
	w.Watch("p1", "claude")
	w.Touch("p1")
	w.Sweep()
	got := assertNews(t, rec, "a pane watched again")
	if got.Progress != paneobserve.ProgressMoving {
		t.Fatalf("re-watched pane = %q, want %q: its first reading has no past to compare against", got.Progress, paneobserve.ProgressMoving)
	}
}

// An agent that exited has no transcript, so nothing about it can be stalled —
// and the last progress a client is told must not outlive the process that
// produced it.
func TestAnExitedPaneIsNeverStalled(t *testing.T) {
	c := newClock()
	w, grid, rec := watcher(t, c)
	feed := feeder(t, grid, "p1", "claude-working")
	w.Watch("p1", "claude")
	feed(15000)
	w.Touch("p1")
	w.Sweep()
	rec.drain()

	c.advance(stallAfter + time.Second)
	w.Exited("p1")
	got := assertNews(t, rec, "the pane's agent exiting")
	if got.State != agentdriver.StateExited || got.Progress != paneobserve.ProgressMoving {
		t.Fatalf("exited pane = %s/%s, want exited/moving", got.State, got.Progress)
	}
	if o, ok := w.Snapshot("p1"); !ok || o.Progress != paneobserve.ProgressMoving {
		t.Fatalf("retained observation = %+v, want moving", o)
	}
}
