package notify

import "context"

// ToastPresenter is the in-app toast surface: the transient message drawn in
// nocx's own window, above whatever the user is looking at. It is the second
// attention surface of the design (§2.2) and the one that works on every host
// with a renderer attached, where the OS banner needs a desktop shell and an
// authorization the user may have refused.
//
// It is a PORT and nothing else: internal/notify declares it and something
// else satisfies it. The implementation is a control-plane push to the
// renderer, which owns the toast (frontend/src/ui/toast.tsx) — so the package
// stays Wails-free and transport-free, exactly as it does for AttentionHost.
type ToastPresenter interface {
	// Toast presents one transient message. The implementation reads only
	// the event's presentation fields and its level; where the event goes
	// was decided by the router before this port is reached.
	Toast(ctx context.Context, ev Event) error
}

// UnavailableToast is the ToastPresenter for a host with no toast surface at
// all. It reports ErrUnavailable — the same word the unavailable attention
// host uses, so the composition root's one exemption ("this channel does not
// exist on this host") recognises both without a second vocabulary.
type UnavailableToast struct{}

func (UnavailableToast) Toast(context.Context, Event) error { return ErrUnavailable }

// ToastSink bridges the router to the ToastPresenter port: Deliver presents
// the event, and every failure is a failed delivery the router records in the
// outcome. It never selects where an event goes (ADR-0029 §2.3) — the
// presenter is bound at construction, exactly as HostSink's host is.
//
// It is HostSink's sibling on purpose. A toast is not a special case in the
// renderer that the pipeline routes around; it is a sink like any other, and
// making it one is what keeps the router the only holder of "where" (D2).
type ToastSink struct {
	// Presenter is the bound toast surface. Bind UnavailableToast on hosts
	// with none: the sink then reports unavailable instead of stalling the
	// pipeline.
	Presenter ToastPresenter
}

func (s ToastSink) Deliver(ctx context.Context, d Delivery) error {
	return s.Presenter.Toast(ctx, d.Event)
}

// LeavesMachine is false: a toast is drawn in this window and goes nowhere.
func (ToastSink) LeavesMachine() bool { return false }
