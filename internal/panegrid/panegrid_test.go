package panegrid_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
)

func newStore(t *testing.T) *panegrid.Store {
	t.Helper()
	return panegrid.New(log.NewSlogAdapter(nil))
}

// The ordinary case. Every "returns an error when…" below is paired with this
// one, per AGENTS.md: a suite that only proves the refusals cannot report that
// the feature never works.
func TestAnEnrolledPaneShowsWhatWasWrittenToIt(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 40, 5); err != nil {
		t.Fatalf("enrol on an ordinary store: %v", err)
	}
	s.Feed("p1", []byte("hello"))
	f, err := s.Frame("p1")
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if got := strings.TrimRight(f.Text(0), " "); got != "hello" {
		t.Errorf("row 0 = %q, want %q", got, "hello")
	}
	if f.Cols != 40 || f.Rows != 5 {
		t.Errorf("size = %dx%d, want 40x5", f.Cols, f.Rows)
	}
	if f.CursorX != 5 || f.CursorY != 0 {
		t.Errorf("cursor = %d,%d want 5,0", f.CursorX, f.CursorY)
	}
}

// The geometry that decided the library (nocx-szb40.1). A double-width
// character takes two columns; the second is a continuation with Width 0.
// Both permitted powers are positional, so this is the property that matters
// more than the text.
func TestADoubleWidthCharacterOccupiesTwoColumns(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 20, 2); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	s.Feed("p1", []byte("[こ]"))
	f, err := s.Frame("p1")
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if f.Lines[0][0].Text != "[" {
		t.Fatalf("col 0 = %q, want %q", f.Lines[0][0].Text, "[")
	}
	if got := f.Lines[0][1]; got.Text != "こ" || got.Width != 2 {
		t.Errorf("col 1 = %q/w%d, want こ/w2", got.Text, got.Width)
	}
	if got := f.Lines[0][2]; got.Width != 0 {
		t.Errorf("col 2 width = %d, want 0 (continuation of the wide cell)", got.Width)
	}
	// And the closing bracket is at column 3, not column 2 — which is the
	// whole point: a chrome anchor is addressed by column.
	if got := f.Lines[0][3]; got.Text != "]" {
		t.Errorf("col 3 = %q, want %q — the wide character did not consume two columns",
			got.Text, "]")
	}
	// Text() skips continuations, so content readers see it once.
	if got := strings.TrimRight(f.Text(0), " "); got != "[こ]" {
		t.Errorf("Text(0) = %q, want %q", got, "[こ]")
	}
}

// The interval has both ends, and this asserts the closing one: after
// Withdraw the pane is gone, not merely stale.
func TestWithdrawEndsTheIntervalAndTheGridIsGone(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 10, 2); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	s.Feed("p1", []byte("x"))
	s.Withdraw("p1")
	if s.Enrolled("p1") {
		t.Error("pane still enrolled after Withdraw")
	}
	if _, err := s.Frame("p1"); !errors.Is(err, panegrid.ErrNotEnrolled) {
		t.Errorf("Frame after Withdraw = %v, want ErrNotEnrolled", err)
	}
	if s.Count() != 0 {
		t.Errorf("Count = %d after Withdraw, want 0", s.Count())
	}
	// Idempotent: a caller racing session teardown should not have to care
	// who won.
	s.Withdraw("p1")
	s.Withdraw("never-existed")
}

// Bytes for an unenrolled pane cost nothing and are not buffered for later.
// This is the hot path of every session in the product.
func TestBytesForAnUnenrolledPaneAreDroppedNotBuffered(t *testing.T) {
	s := newStore(t)
	s.Feed("p1", []byte("written before anybody asked"))
	if s.Enrolled("p1") {
		t.Fatal("Feed enrolled a pane; enrolment must be an act, never a side effect")
	}
	if err := s.Enrol("p1", 20, 2); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	f, err := s.Frame("p1")
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if got := strings.TrimSpace(f.Text(0)); got != "" {
		t.Errorf("row 0 = %q after enrolling; pre-enrolment bytes must not be replayed", got)
	}
}

func TestEnrollingTwiceIsRefusedRatherThanDiscardingTheGrid(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 10, 2); err != nil {
		t.Fatalf("first enrol: %v", err)
	}
	s.Feed("p1", []byte("abc"))
	if err := s.Enrol("p1", 10, 2); !errors.Is(err, panegrid.ErrAlreadyEnrolled) {
		t.Fatalf("second enrol = %v, want ErrAlreadyEnrolled", err)
	}
	// The point of refusing: the first grid is intact, so the byte-zero
	// guarantee still holds.
	f, _ := s.Frame("p1")
	if got := strings.TrimRight(f.Text(0), " "); got != "abc" {
		t.Errorf("row 0 = %q, want %q — the refused enrol damaged the grid", got, "abc")
	}
}

func TestEnrolRefusesNonsenseAndStillWorksAfterwards(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("", 10, 2); err == nil {
		t.Error("empty pane id was accepted")
	}
	if err := s.Enrol("p1", 0, 2); err == nil {
		t.Error("zero columns was accepted")
	}
	if err := s.Enrol("p1", 10, -1); err == nil {
		t.Error("negative rows was accepted")
	}
	if s.Count() != 0 {
		t.Fatalf("Count = %d after three refusals, want 0", s.Count())
	}
	// Paired success: the store is not poisoned by having refused.
	if err := s.Enrol("p1", 10, 2); err != nil {
		t.Errorf("enrol after refusals: %v", err)
	}
}

func TestTheEnrolmentBoundHoldsAndFreesOnWithdraw(t *testing.T) {
	s := newStore(t)
	for i := 0; i < panegrid.MaxEnrolled; i++ {
		if err := s.Enrol(paneName(i), 10, 2); err != nil {
			t.Fatalf("enrol %d: %v", i, err)
		}
	}
	if err := s.Enrol("one-too-many", 10, 2); !errors.Is(err, panegrid.ErrTooManyEnrolled) {
		t.Fatalf("enrol past the bound = %v, want ErrTooManyEnrolled", err)
	}
	s.Withdraw(paneName(0))
	if err := s.Enrol("one-too-many", 10, 2); err != nil {
		t.Errorf("enrol after a withdraw freed a slot: %v", err)
	}
}

func paneName(i int) string { return "pane-" + string(rune('a'+i%26)) + string(rune('0'+i/26)) }

// Feed runs on the session pump goroutine and Frame on a handler's. A data
// race here would be a race in every session, so it is asserted rather than
// assumed. Run the suite with -race for this to mean anything.
func TestFeedAndFrameAreSafeToCallConcurrently(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 40, 10); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				s.Feed("p1", []byte("line of output\r\n"))
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if _, err := s.Frame("p1"); err != nil {
					t.Errorf("frame during concurrent feed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// A TUI sends diffs, not repaints — the reason the grid has to be fed
// continuously rather than reconstructed from a tail. Feeding the same stream
// in two pieces must land in the same place as feeding it whole.
func TestAStreamSplitAcrossFeedsLandsWhereAWholeOneDoes(t *testing.T) {
	whole := newStore(t)
	split := newStore(t)
	if err := whole.Enrol("p", 20, 3); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if err := split.Enrol("p", 20, 3); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	// An escape sequence deliberately cut in half across the two writes.
	stream := "ab\x1b[2;3Hcd"
	whole.Feed("p", []byte(stream))
	split.Feed("p", []byte(stream[:5]))
	split.Feed("p", []byte(stream[5:]))
	fw, _ := whole.Frame("p")
	fs, _ := split.Frame("p")
	for y := 0; y < fw.Rows; y++ {
		if fw.Text(y) != fs.Text(y) {
			t.Errorf("row %d differs: whole %q, split %q", y, fw.Text(y), fs.Text(y))
		}
	}
	if fw.CursorX != fs.CursorX || fw.CursorY != fs.CursorY {
		t.Errorf("cursor differs: whole %d,%d split %d,%d",
			fw.CursorX, fw.CursorY, fs.CursorX, fs.CursorY)
	}
}

// The alternate screen is an observation and decides nothing on its own
// (ADR-0024 decision 1). It is reported because the driver needs it; the
// assertion here is only that it is reported honestly.
func TestAlternateScreenIsReportedAndIsOnlyAnObservation(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p", 20, 3); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	f, _ := s.Frame("p")
	if f.AltScreen {
		t.Error("a fresh grid reports the alternate screen")
	}
	s.Feed("p", []byte("\x1b[?1049h"))
	f, _ = s.Frame("p")
	if !f.AltScreen {
		t.Error("entering the alternate screen was not reported")
	}
	s.Feed("p", []byte("\x1b[?1049l"))
	f, _ = s.Frame("p")
	if f.AltScreen {
		t.Error("leaving the alternate screen was not reported")
	}
}

// The interval outlives a resize, so the grid has to survive one. Every anchor
// a driver reads is POSITIONAL — the input box is bounded by full-width rules,
// the spinner sits directly above the token counter — so a grid left at the
// size it was enrolled at does not answer about a stale screen, it answers
// about a screen that never existed: the pane repaints at the new width while
// the emulator keeps wrapping at the old one.
func TestAResizedPaneAnswersAtTheNewSize(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 20, 4); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if err := s.Resize("p1", 40, 6); err != nil {
		t.Fatalf("resize: %v", err)
	}
	s.Feed("p1", []byte("wider than twenty columns"))
	f, err := s.Frame("p1")
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if f.Cols != 40 || f.Rows != 6 {
		t.Fatalf("size = %dx%d, want 40x6", f.Cols, f.Rows)
	}
	// At 20 columns this would have wrapped onto a second row; the point of
	// the resize is that it does not.
	if got := strings.TrimRight(f.Text(0), " "); got != "wider than twenty columns" {
		t.Errorf("row 0 = %q, want the whole line unwrapped", got)
	}
	if got := strings.TrimRight(f.Text(1), " "); got != "" {
		t.Errorf("row 1 = %q, want it empty", got)
	}
}

// A resize for a pane with no grid is the ordinary case, not a failure: most
// panes never have one and every one of them is resized.
func TestResizingAPaneWithNoGridIsNotAnError(t *testing.T) {
	s := newStore(t)
	if err := s.Resize("nobody", 40, 6); !errors.Is(err, panegrid.ErrNotEnrolled) {
		t.Errorf("resize of an unenrolled pane = %v, want ErrNotEnrolled", err)
	}
}

func TestResizeRefusesASizeThatIsNotASize(t *testing.T) {
	s := newStore(t)
	if err := s.Enrol("p1", 20, 4); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if err := s.Resize("p1", 0, 6); err == nil {
		t.Error("resize to 0 columns was accepted")
	}
	if err := s.Resize("p1", 40, 0); err == nil {
		t.Error("resize to 0 rows was accepted")
	}
	// And the refusal left the grid usable at the size it had.
	f, err := s.Frame("p1")
	if err != nil {
		t.Fatalf("frame after a refused resize: %v", err)
	}
	if f.Cols != 20 || f.Rows != 4 {
		t.Errorf("size after a refused resize = %dx%d, want 20x4", f.Cols, f.Rows)
	}
}
