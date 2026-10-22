package capability

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/uistate"
)

// The window half of the UI-state service (ADR-0098): the recorded
// geometry is read once at bootstrap for the shell to restore, and every
// live sample the shell relays is folded by the store's own rule. What is
// asserted here is that the SERVICE carries both halves to the store and
// nowhere else — the folding itself is uistate's own table.

func newUIStateOpForTest(t *testing.T) (UIStateOperation, *uistate.Store) {
	t.Helper()
	store := uistate.New(storage.NewDocumentStore(t.TempDir()), nil)
	t.Cleanup(func() { _ = store.Close() })
	gate := func(name string) control.Admission {
		return Gate(name, 1, 8, time.Second)
	}
	return NewUIStateOperation(gate(GateConfig), gate("lane"), store), store
}

func TestUIStateWindow_RecordsAndReadsThroughOperation(t *testing.T) {
	op, store := newUIStateOpForTest(t)
	saved := uistate.Window{Width: 1280, Height: 800, X: 40, Y: 60, Displays: "1:1920x1080p"}

	live := uistate.Window{
		Width: 1920, Height: 1080, X: 0, Y: 0,
		Maximised: true, Displays: "1:1920x1080p",
	}
	var got uistate.Window
	err := op.Run(context.Background(), func(ctx context.Context, svc UIStateService) error {
		svc.ObserveWindow(ctx, live)
		got = svc.Window(ctx)
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// With no prior normal geometry there is nothing to protect, so the
	// maximised size stands (uistate.Observe's own rule); the second test
	// covers the carry-forward. What this test pins is that the SERVICE
	// answered exactly what the store now holds — one owner, no second
	// copy shaped by the caller.
	if !got.Maximised {
		t.Errorf("Maximised = %v, want true", got.Maximised)
	}
	if store.Window() != got {
		t.Errorf("store holds %+v, operation answered %+v", store.Window(), got)
	}
	_ = saved
}

func TestUIStateWindow_FoldsAgainstRecordedNormalGeometry(t *testing.T) {
	op, store := newUIStateOpForTest(t)
	store.SetWindow(uistate.Window{Width: 1280, Height: 800, X: 40, Y: 60, Displays: "1:1920x1080p"})

	live := uistate.Window{
		Width: 1920, Height: 1080, Maximised: true, Displays: "1:1920x1080p",
	}
	err := op.Run(context.Background(), func(ctx context.Context, svc UIStateService) error {
		svc.ObserveWindow(ctx, live)
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := uistate.Window{Width: 1280, Height: 800, X: 40, Y: 60, Maximised: true, Displays: "1:1920x1080p"}
	if got := store.Window(); got != want {
		t.Errorf("folded = %+v, want %+v", got, want)
	}
}

func TestUIStateWindow_EscapedHandleIsInert(t *testing.T) {
	op, store := newUIStateOpForTest(t)
	store.SetWindow(uistate.Window{Width: 1280, Height: 800, Displays: "1:1920x1080p"})

	var escaped UIStateService
	if err := op.Run(context.Background(), func(ctx context.Context, svc UIStateService) error {
		escaped = svc
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := escaped.Window(context.Background()); got != (uistate.Window{}) {
		t.Errorf("Window on dead handle = %+v, want zero", got)
	}
	escaped.ObserveWindow(context.Background(), uistate.Window{Width: 640, Height: 480})
	if got := store.Window(); got.Width != 1280 {
		t.Errorf("ObserveWindow on dead handle changed the store: %+v", got)
	}
}
