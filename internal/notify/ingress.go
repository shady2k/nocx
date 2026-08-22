package notify

import (
	"context"
	"errors"
)

// Submitter is the delivery half ingress hands an event to. *Policy satisfies
// it; the narrow interface is what keeps ingress testable without a router.
type Submitter interface {
	Submit(ev Event) Disposition
}

// Ingress is the one entry point of the notification pipeline: it stamps the
// fields nocx owns, records the occurrence, and only then submits it for
// delivery. Membership and delivery become two decisions here, which is the
// whole inversion — before this, the policy sat in front of the router and a
// suppressed event was destroyed, so the events most worth seeing were
// exactly the ones nothing remembered.
//
// It is also the local interface a remote relay's notify service may later be
// the remote half of: the remote helper design's D17 forbids a helper service
// that has no local counterpart, so this type existing is the precondition,
// not a speculative hook.
type Ingress struct {
	feed  *Feed
	next  Submitter
	clock Clock
}

func NewIngress(feed *Feed, next Submitter, clock Clock) (*Ingress, error) {
	if feed == nil {
		return nil, errors.New("notify: ingress needs a feed")
	}
	if next == nil {
		return nil, errors.New("notify: ingress needs a submitter")
	}
	if clock == nil {
		return nil, errors.New("notify: ingress needs a clock")
	}
	return &Ingress{feed: feed, next: next, clock: clock}, nil
}

// Admit stamps, records, then submits — in that order. Recording first is
// deliberate: a delivery path that panics or blocks must not be able to lose
// the record, because the record is the only thing that survives the moment.
//
// A non-zero At is left alone. A relay replaying a batch it buffered while
// nothing was attached carries its own instants, and restamping them "now"
// would file yesterday's session end as having happened at reconnect.
func (i *Ingress) Admit(ctx context.Context, ev Event) (OccurrenceID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if ev.At.IsZero() {
		ev.At = i.clock.Now()
	}
	id := i.feed.Add(ev)
	i.next.Submit(ev)
	return id, nil
}

// Raise satisfies the transport's NotifyRaiser so ingress replaces the policy
// at that seam. The outcome stays empty for the same reason Policy.Raise's
// does: delivery is asynchronous past the debounce window, and a failure
// surfaces through the policy's result handler rather than at this caller.
func (i *Ingress) Raise(ctx context.Context, ev Event) Outcome {
	if _, err := i.Admit(ctx, ev); err != nil {
		return Outcome{Err: err}
	}
	return Outcome{}
}
