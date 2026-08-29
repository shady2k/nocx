package notify_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/notify"
)

// recordingToast is a bound toast surface that records what reached it, so a
// test can tell "the sink delegated" from "the sink swallowed".
type recordingToast struct {
	mu     sync.Mutex
	events []notify.Event
	err    error
}

func (p *recordingToast) Toast(_ context.Context, ev notify.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
	return p.err
}

func (p *recordingToast) seen() []notify.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]notify.Event(nil), p.events...)
}

// TestToastSink_HandsTheEventToItsPort: the whole of the sink. It validates
// nothing, decides nothing and selects nothing — the destination arrived
// resolved (ADR-0047 §2.3) and the port is bound at construction.
func TestToastSink_HandsTheEventToItsPort(t *testing.T) {
	p := &recordingToast{}
	sink := notify.ToastSink{Presenter: p}

	ev := notify.Event{SessionID: "s1", Title: "build finished", Body: "42 targets"}
	if err := sink.Deliver(context.Background(), notify.Delivery{
		Event:       ev,
		Destination: notify.Destination{Target: "toast"},
	}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	seen := p.seen()
	if len(seen) != 1 {
		t.Fatalf("the port saw %d events, want 1", len(seen))
	}
	if seen[0].Title != ev.Title || seen[0].Body != ev.Body || seen[0].SessionID != ev.SessionID {
		t.Errorf("the port saw %+v, want the event verbatim %+v", seen[0], ev)
	}
}

// A port failure is a FAILED DELIVERY the router records, never a swallow.
func TestToastSink_ReportsThePortsFailure(t *testing.T) {
	want := errors.New("toasttest: the renderer refused it")
	sink := notify.ToastSink{Presenter: &recordingToast{err: want}}

	if err := sink.Deliver(context.Background(), notify.Delivery{}); !errors.Is(err, want) {
		t.Errorf("Deliver = %v, want the port's error", err)
	}
}

// LeavesMachine is false: a toast is drawn in this window and goes nowhere.
// The router reads it to enforce the heuristic trust bound, and an undeclared
// network sink is a fail-open — so this is asserted rather than assumed.
func TestToastSink_DoesNotLeaveTheMachine(t *testing.T) {
	if (notify.ToastSink{}).LeavesMachine() {
		t.Error("ToastSink.LeavesMachine() = true, want false")
	}
}

// UnavailableToast is what an unbound holder answers with, and it is the same
// vocabulary the unavailable attention host uses: one word for "this host has
// no such surface", so the composition root's exemption can recognise both.
func TestUnavailableToast_ReportsUnavailable(t *testing.T) {
	if err := (notify.UnavailableToast{}).Toast(context.Background(), notify.Event{}); !errors.Is(err, notify.ErrUnavailable) {
		t.Errorf("UnavailableToast.Toast = %v, want ErrUnavailable", err)
	}
}
