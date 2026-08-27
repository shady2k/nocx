package notify_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/notify"
)

// TestToastHolder_UnboundReportsUnavailable: the zero holder is the state a
// host lives in until something binds a renderer-facing surface. It must
// report unavailable, so a delivery there is visibly failed rather than
// silently dropped.
func TestToastHolder_UnboundReportsUnavailable(t *testing.T) {
	var h notify.ToastHolder
	if err := h.Toast(context.Background(), notify.Event{}); !errors.Is(err, notify.ErrUnavailable) {
		t.Errorf("Toast on unbound holder = %v, want ErrUnavailable", err)
	}
}

// TestToastHolder_DelegatesOnceBound: what arrives late is the
// IMPLEMENTATION, never the destination — the route was decided when the
// table was built.
func TestToastHolder_DelegatesOnceBound(t *testing.T) {
	var h notify.ToastHolder
	p := &recordingToast{}
	h.Set(p)

	if err := h.Toast(context.Background(), notify.Event{Title: "deploy"}); err != nil {
		t.Fatalf("Toast: %v", err)
	}
	seen := p.seen()
	if len(seen) != 1 || seen[0].Title != "deploy" {
		t.Fatalf("the bound presenter saw %+v, want one event titled deploy", seen)
	}
}

// A bound presenter's failure reaches the caller unchanged: the holder adds
// no policy of its own.
func TestToastHolder_ReportsTheBoundPresentersFailure(t *testing.T) {
	var h notify.ToastHolder
	want := errors.New("toasttest: refused")
	h.Set(&recordingToast{err: want})

	if err := h.Toast(context.Background(), notify.Event{}); !errors.Is(err, want) {
		t.Errorf("Toast = %v, want the bound presenter's error", err)
	}
}

// Set happens during startup while raises can already be arriving from a
// reattached session, so the holder is used concurrently by construction.
func TestToastHolder_IsSafeUnderConcurrentSetAndToast(t *testing.T) {
	var h notify.ToastHolder
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); h.Set(&recordingToast{}) }()
		go func() { defer wg.Done(); _ = h.Toast(context.Background(), notify.Event{}) }()
	}
	wg.Wait()
}
