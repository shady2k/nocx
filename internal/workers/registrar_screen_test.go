package workers

// Reading a held participant's pane (nocx-f545a.6).
//
// The authority is Close's, asked in Close's order, because it is the same
// question about the same participant: is it this session's, and does the
// delegation still permit the act. The act is EffectObserve.

import (
	"context"
	"errors"
	"testing"
)

type fakeScreener struct {
	asked  []ParticipantID
	screen PaneScreen
	err    error
}

func (f *fakeScreener) ReadScreen(_ context.Context, p Participant) (PaneScreen, error) {
	f.asked = append(f.asked, p.ID)
	return f.screen, f.err
}

func registeredForScreen(t *testing.T) (*harness, *fakeScreener, Registration) {
	t.Helper()
	h := newHarness(t)
	sc := &fakeScreener{screen: PaneScreen{Readable: true, State: "permission_choice", Rows: []string{"❯ No, exit"}}}
	h.reg.screener = sc
	reg, err := h.reg.Register(context.Background(), RegisterRequest{
		Group: testGroup, CoordinatorSession: coordSession,
		Role: RoleWorker, Task: "read AGENTS.md and report", Command: "claude",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return h, sc, reg
}

func TestTheHoldingSessionReadsItsWorkersPane(t *testing.T) {
	h, sc, reg := registeredForScreen(t)
	got, err := h.reg.Screen(context.Background(), coordSession, reg.ID)
	if err != nil {
		t.Fatalf("Screen: %v", err)
	}
	if !got.Readable || got.State != "permission_choice" || len(got.Rows) != 1 {
		t.Fatalf("screen = %+v, want what the screener read", got)
	}
	if len(sc.asked) != 1 || sc.asked[0] != reg.ID {
		t.Fatalf("screener asked for %v, want exactly %q", sc.asked, reg.ID)
	}
}

// Another session's worker is refused with the ownership sentinel, and the
// pane is never read — the refusal comes before the seam, not after it.
func TestAnotherSessionCannotReadAWorkersPane(t *testing.T) {
	h, sc, reg := registeredForScreen(t)
	_, err := h.reg.Screen(context.Background(), "sess-somebody-else", reg.ID)
	if !errors.Is(err, ErrNotHeld) {
		t.Fatalf("err = %v, want ErrNotHeld", err)
	}
	if len(sc.asked) != 0 {
		t.Fatalf("a refused read still reached the pane: %v", sc.asked)
	}
}

// A worker that has ended has no pane to read. That is an answer: not
// readable, no error, and the screener is not asked.
func TestAnEndedWorkerHasNoScreenAndThatIsNotAnError(t *testing.T) {
	h, sc, reg := registeredForScreen(t)
	if _, err := h.reg.Exited(context.Background(), reg.ID, reg.Liveness, Exit{}); err != nil {
		t.Fatalf("Exited: %v", err)
	}
	got, err := h.reg.Screen(context.Background(), coordSession, reg.ID)
	if err != nil {
		t.Fatalf("Screen of an ended worker: %v", err)
	}
	if got.Readable || len(got.Rows) != 0 {
		t.Fatalf("screen = %+v, want no reading", got)
	}
	if len(sc.asked) != 0 {
		t.Fatalf("an ended worker's pane was read: %v", sc.asked)
	}
}

// Unwired is a refusal that says so, never an empty screen a coordinator would
// read as a pane with nothing on it.
func TestAnUnwiredScreenerRefusesRatherThanShowingNothing(t *testing.T) {
	h, _, reg := registeredForScreen(t)
	h.reg.screener = nil
	if _, err := h.reg.Screen(context.Background(), coordSession, reg.ID); err == nil {
		t.Fatal("an unwired screener answered a screen")
	}
}

// And a failing read is the read's error, unchanged.
func TestAScreenerFailureIsTheCallersError(t *testing.T) {
	h, sc, reg := registeredForScreen(t)
	sc.err = errors.New("injected: the grid could not be read")
	if _, err := h.reg.Screen(context.Background(), coordSession, reg.ID); !errors.Is(err, sc.err) {
		t.Fatalf("err = %v, want the screener's own error", err)
	}
}
