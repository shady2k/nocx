package notify

import (
	"context"
	"errors"
)

// ErrUnavailable is what an attention surface reports when it does not exist
// on this host at all: the unavailable AttentionHost adapter on a build with
// no desktop banner (cmd/devharness, a web host), and the unavailable toast
// presenter on a backend no renderer is attached to. Reported rather than
// panicked on or silently succeeded, so a soft degrade is visible.
//
// ONE sentinel for the two surfaces on purpose (AD-8). The composition root's
// single exemption from the failure feed is exactly this word — a channel
// that does not exist on this host is not a channel that lost a message — and
// a second spelling would mean that exemption had to know how many kinds of
// absence there are.
var ErrUnavailable = errors.New("notify: attention surface unavailable on this host")

// AttentionHost is the host-context-bound attention surface: the OS banner,
// the dock badge and the attention bounce (spec §2.2). runtime.SendNotification,
// badge and bounce are host-context-bound, so they are reached only through
// this port — the desktop shell binds the Wails adapter (a separate task),
// and hosts without one bind UnavailableHost. Without this seam the "one
// core" of AD-2 would be welded to the AD-3 shell.
type AttentionHost interface {
	// Banner presents one notification banner. The adapter reads only the
	// event's presentation fields and attribution.
	Banner(ctx context.Context, ev Event) error

	// Badge sets the dock badge count; 0 clears it.
	Badge(ctx context.Context, count int) error

	// Bounce requests the attention bounce.
	Bounce(ctx context.Context) error
}

// UnavailableHost is the AttentionHost adapter for hosts with no desktop
// attention surface. Every method reports ErrUnavailable.
type UnavailableHost struct{}

func (UnavailableHost) Banner(context.Context, Event) error { return ErrUnavailable }

func (UnavailableHost) Badge(context.Context, int) error { return ErrUnavailable }

func (UnavailableHost) Bounce(context.Context) error { return ErrUnavailable }
