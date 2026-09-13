package paneview

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// fakeSource is the seam as a test can drive it: a set of panes that have a
// frame, a set that cannot be read, and a count of how often the CHEAP question
// was asked — because "enrolment probes availability and does not read a frame"
// is a property, not an implementation detail.
type fakeSource struct {
	frames      map[string]Frame
	unavailable map[string]error
	readErr     error
	probes      int
	reads       int
}

func (f *fakeSource) Available(paneID string) error {
	f.probes++
	if err, ok := f.unavailable[paneID]; ok {
		return err
	}
	return nil
}

func (f *fakeSource) Screen(paneID string) (Frame, error) {
	f.reads++
	if f.readErr != nil {
		return Frame{}, f.readErr
	}
	fr, ok := f.frames[paneID]
	if !ok {
		return Frame{}, fmt.Errorf("fake: no frame for %s", paneID)
	}
	return fr, nil
}

func newFakeSource() *fakeSource {
	return &fakeSource{frames: map[string]Frame{}, unavailable: map[string]error{}}
}

func newStore(t *testing.T, src *fakeSource) *Store {
	t.Helper()
	return NewStore(log.NewSlogAdapter(nil), src)
}

// TestAWatchedPaneAnswersWhatItsRuntimeHolds is the ordinary case, and the
// paired success every refusal below needs.
func TestAWatchedPaneAnswersWhatItsRuntimeHolds(t *testing.T) {
	src := newFakeSource()
	src.frames["pane-1"] = Frame{Cols: 3, Rows: 1, CursorX: 1, Lines: [][]Cell{{{Text: "a", Width: 1}}}}
	store := newStore(t, src)

	if err := store.Enrol("pane-1"); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if !store.Watched("pane-1") || store.Count() != 1 {
		t.Fatalf("after enrol: watched=%v count=%d, want true and 1", store.Watched("pane-1"), store.Count())
	}
	f, err := store.Frame("pane-1")
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if f.Cols != 3 || f.CursorX != 1 || len(f.Lines) != 1 {
		t.Fatalf("frame = %+v, want the runtime's own frame", f)
	}
	if src.probes != 1 {
		t.Errorf("enrolment probed availability %d times, want exactly 1", src.probes)
	}
}

// TestFrameForAPaneNobodyWatchesIsRefused is the interval's teeth: the runtime
// can answer for a pane nobody watches (it holds every session's screen), and
// the store must not be a quieter way to read one.
func TestFrameForAPaneNobodyWatchesIsRefused(t *testing.T) {
	src := newFakeSource()
	src.frames["pane-1"] = Frame{Cols: 1, Rows: 1}
	store := newStore(t, src)

	if _, err := store.Frame("pane-1"); !errors.Is(err, ErrNotWatched) {
		t.Fatalf("frame for an unwatched pane = %v, want ErrNotWatched", err)
	}
	if src.reads != 0 {
		t.Errorf("the store read the source %d times for an unwatched pane, want 0", src.reads)
	}
	if err := store.Enrol("pane-1"); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if _, err := store.Frame("pane-1"); err != nil {
		t.Errorf("frame after the same pane was enrolled: %v", err)
	}
}

// TestWithdrawEndsTheInterval covers both ends: the read stops working, and
// withdrawing something already withdrawn is not an error.
func TestWithdrawEndsTheInterval(t *testing.T) {
	src := newFakeSource()
	src.frames["pane-1"] = Frame{Cols: 1, Rows: 1}
	store := newStore(t, src)
	if err := store.Enrol("pane-1"); err != nil {
		t.Fatalf("enrol: %v", err)
	}

	store.Withdraw("pane-1")
	if store.Watched("pane-1") || store.Count() != 0 {
		t.Fatalf("after withdraw: watched=%v count=%d, want false and 0", store.Watched("pane-1"), store.Count())
	}
	if _, err := store.Frame("pane-1"); !errors.Is(err, ErrNotWatched) {
		t.Errorf("frame after withdraw = %v, want ErrNotWatched", err)
	}

	store.Withdraw("pane-1")
	if store.Count() != 0 {
		t.Errorf("withdrawing twice changed the set: count=%d", store.Count())
	}
	// And the interval reopens: re-enrolling the same pane is the ordinary
	// case after an agent withdraws and starts again.
	if err := store.Enrol("pane-1"); err != nil {
		t.Fatalf("re-enrol: %v", err)
	}
	if _, err := store.Frame("pane-1"); err != nil {
		t.Errorf("frame after re-enrol: %v", err)
	}
}

func TestEnrollingTwiceIsRefusedAndLeavesOneWatch(t *testing.T) {
	src := newFakeSource()
	src.frames["pane-1"] = Frame{Cols: 1, Rows: 1}
	store := newStore(t, src)
	if err := store.Enrol("pane-1"); err != nil {
		t.Fatalf("first enrol: %v", err)
	}
	if err := store.Enrol("pane-1"); !errors.Is(err, ErrAlreadyWatched) {
		t.Fatalf("second enrol = %v, want ErrAlreadyWatched", err)
	}
	if store.Count() != 1 {
		t.Errorf("count after a refused second enrol = %d, want 1", store.Count())
	}
	// One withdrawal closes it, which is what "one watch" means.
	store.Withdraw("pane-1")
	if store.Watched("pane-1") {
		t.Error("the pane is still watched after one withdrawal: the second enrol opened a second interval")
	}
}

func TestTheBoundHoldsAndFreesOnWithdraw(t *testing.T) {
	src := newFakeSource()
	store := newStore(t, src)
	for i := range MaxWatched {
		pane := fmt.Sprintf("pane-%d", i)
		if err := store.Enrol(pane); err != nil {
			t.Fatalf("enrol %s: %v", pane, err)
		}
	}
	if err := store.Enrol("pane-over"); !errors.Is(err, ErrTooManyWatched) {
		t.Fatalf("enrol past the bound = %v, want ErrTooManyWatched", err)
	}
	if store.Count() != MaxWatched {
		t.Errorf("count at the bound = %d, want %d", store.Count(), MaxWatched)
	}
	store.Withdraw("pane-0")
	if err := store.Enrol("pane-over"); err != nil {
		t.Errorf("enrol after a withdrawal freed a slot: %v", err)
	}
}

// TestAPaneWhoseRuntimeCannotBeReadIsRefusedBeforeItIsWatched is the shape that
// keeps a refusal from becoming a silent empty pane: the order is probe, then
// watch, and a pane with no readable runtime never enters the set.
func TestAPaneWhoseRuntimeCannotBeReadIsRefusedBeforeItIsWatched(t *testing.T) {
	src := newFakeSource()
	refusal := errors.New("no helper holds this pane")
	src.unavailable["pane-1"] = refusal
	store := newStore(t, src)

	err := store.Enrol("pane-1")
	if !errors.Is(err, refusal) {
		t.Fatalf("enrol of an unreadable pane = %v, want the source's own refusal", err)
	}
	if store.Watched("pane-1") || store.Count() != 0 {
		t.Fatalf("a refused pane was watched anyway: watched=%v count=%d", store.Watched("pane-1"), store.Count())
	}
	if src.reads != 0 {
		t.Errorf("enrolment read a frame %d times, want 0: availability is the cheap question", src.reads)
	}
}

func TestEnrolRefusesAnEmptyPaneAndStillWorksAfterwards(t *testing.T) {
	src := newFakeSource()
	store := newStore(t, src)
	if err := store.Enrol(""); err == nil {
		t.Fatal("an empty pane id was enrolled")
	}
	if err := store.Enrol("pane-1"); err != nil {
		t.Fatalf("enrol after a refused one: %v", err)
	}
}

// TestFrameAndEnrolAreSafeConcurrently is the store's own lock discipline: the
// observer sweeps read while an enrolment or a withdrawal lands.
func TestFrameAndEnrolAreSafeConcurrently(t *testing.T) {
	src := newFakeSource()
	src.frames["pane-1"] = Frame{Cols: 1, Rows: 1}
	store := newStore(t, src)
	if err := store.Enrol("pane-1"); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			if _, err := store.Frame("pane-1"); err != nil && !strings.Contains(err.Error(), "not watched") {
				t.Errorf("frame during churn: %v", err)
				return
			}
		}
	}()
	for range 200 {
		store.Withdraw("pane-1")
		if err := store.Enrol("pane-1"); err != nil {
			t.Errorf("enrol during churn: %v", err)
			break
		}
	}
	<-done
}
