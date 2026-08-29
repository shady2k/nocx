package wailsadapter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/notify/wailsadapter"
)

func sinkLimits() notify.Limits {
	return notify.Limits{
		MaxInFlight:     8,
		MaxQueued:       8,
		MaxRetained:     1 << 20,
		DeliveryTimeout: time.Second,
	}
}

// TestHostSinkUnavailableHostRecordsFailedDelivery: on a host where the port
// is unavailable (devharness, a web host), the sink reports unavailable and
// the router records a failed delivery — the pipeline does not stall and the
// event is not silently dropped.
func TestHostSinkUnavailableHostRecordsFailedDelivery(t *testing.T) {
	r, err := notify.NewRouter(notify.Table{
		{Kind: notify.KindProgramNotify, Trust: notify.TrustProgramRequest}: {
			{Sink: notify.HostSink{Host: notify.UnavailableHost{}}},
		},
	}, sinkLimits())
	if err != nil {
		t.Fatal(err)
	}

	out := r.Raise(context.Background(), event("s1", "body"))

	if len(out.Resolved) != 1 {
		t.Fatalf("Resolved %d routes, want 1", len(out.Resolved))
	}
	if len(out.Results) != 1 {
		t.Fatalf("Results %d, want 1", len(out.Results))
	}
	if !errors.Is(out.Results[0].Err, notify.ErrUnavailable) {
		t.Errorf("delivery error: got %v want ErrUnavailable", out.Results[0].Err)
	}
	if out.Err != nil {
		t.Errorf("Outcome.Err = %v, want nil (the delivery was attempted, not refused)", out.Err)
	}
}

// TestHostSinkNotRequestedRecordsFailedDelivery: authorization never
// requested is a failed delivery through the sink — sending anyway would be
// a silent no-op, since macOS drops notifications from unauthorized apps.
func TestHostSinkNotRequestedRecordsFailedDelivery(t *testing.T) {
	_, host := newHarness(t)
	r, err := notify.NewRouter(notify.Table{
		{Kind: notify.KindProgramNotify, Trust: notify.TrustProgramRequest}: {
			{Sink: notify.HostSink{Host: host}},
		},
	}, sinkLimits())
	if err != nil {
		t.Fatal(err)
	}

	out := r.Raise(context.Background(), event("s1", "body"))

	if len(out.Results) != 1 {
		t.Fatalf("Results %d, want 1", len(out.Results))
	}
	if !errors.Is(out.Results[0].Err, wailsadapter.ErrNotRequested) {
		t.Errorf("delivery error: got %v want ErrNotRequested", out.Results[0].Err)
	}
}

// TestHostSinkDeniedRecordsFailedDelivery: a denied host produces a visible
// failed delivery through the sink — macOS is suppressing display, and the
// product must see that, never a silent drop.
func TestHostSinkDeniedRecordsFailedDelivery(t *testing.T) {
	_, host := newHarness(t, func(h *harness) { h.requestGranted = false })
	if _, err := host.RequestAuthorization(context.Background()); !errors.Is(err, wailsadapter.ErrDenied) {
		t.Fatalf("RequestAuthorization: %v, want ErrDenied", err)
	}
	r, err := notify.NewRouter(notify.Table{
		{Kind: notify.KindProgramNotify, Trust: notify.TrustProgramRequest}: {
			{Sink: notify.HostSink{Host: host}},
		},
	}, sinkLimits())
	if err != nil {
		t.Fatal(err)
	}

	out := r.Raise(context.Background(), event("s1", "body"))

	if len(out.Results) != 1 {
		t.Fatalf("Results %d, want 1", len(out.Results))
	}
	if !errors.Is(out.Results[0].Err, wailsadapter.ErrDenied) {
		t.Errorf("delivery error: got %v want ErrDenied", out.Results[0].Err)
	}
}

// TestHostSinkDeliversThroughHost: the available+granted path end to end —
// the sink passes the event's presentation fields to the bound host, the
// banner reaches the runtime with the session id in the click payload, and
// the delivery succeeds.
func TestHostSinkDeliversThroughHost(t *testing.T) {
	h, host := newHarness(t)
	grant(t, host)
	r, err := notify.NewRouter(notify.Table{
		{Kind: notify.KindProgramNotify, Trust: notify.TrustProgramRequest}: {
			{Sink: notify.HostSink{Host: host}},
		},
	}, sinkLimits())
	if err != nil {
		t.Fatal(err)
	}

	ev := event("s1", "body")
	out := r.Raise(context.Background(), ev)
	if len(out.Results) != 1 || out.Results[0].Err != nil {
		t.Fatalf("Results: %+v, want one successful delivery", out.Results)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(h.sent))
	}
	opts := h.sent[0]
	if opts.Title != ev.Title || opts.Body != ev.Body {
		t.Errorf("banner fields: got title=%q body=%q want title=%q body=%q", opts.Title, opts.Body, ev.Title, ev.Body)
	}
	if got := opts.Data["sessionId"]; got != "s1" {
		t.Errorf("click payload sessionId: got %v want s1", got)
	}
}

// TestHostSinkSendFailureRecordsFailedDelivery: when the underlying runtime
// call fails, the failure is a failed delivery through the sink — the
// pipeline never swallows a failed send.
func TestHostSinkSendFailureRecordsFailedDelivery(t *testing.T) {
	sendErr := errors.New("darwin bridge refused")
	_, host := newHarness(t, func(h *harness) { h.sendErr = sendErr })
	grant(t, host)
	r, err := notify.NewRouter(notify.Table{
		{Kind: notify.KindProgramNotify, Trust: notify.TrustProgramRequest}: {
			{Sink: notify.HostSink{Host: host}},
		},
	}, sinkLimits())
	if err != nil {
		t.Fatal(err)
	}

	out := r.Raise(context.Background(), event("s1", "body"))

	if len(out.Results) != 1 {
		t.Fatalf("Results %d, want 1", len(out.Results))
	}
	if !errors.Is(out.Results[0].Err, sendErr) {
		t.Errorf("delivery error: got %v want %v", out.Results[0].Err, sendErr)
	}
}

// TestHostSinkLeavesMachineFalse: a banner leaves the machine nowhere, so the
// heuristic trust bound (ADR-0047 §3) never blocks it.
func TestHostSinkLeavesMachineFalse(t *testing.T) {
	if (notify.HostSink{}).LeavesMachine() {
		t.Error("HostSink.LeavesMachine() = true, want false")
	}
}
