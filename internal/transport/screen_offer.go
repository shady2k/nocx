package transport

import (
	"context"
)

// resetScreenOffer closes the offer interval owned by the prior subscriber.
// The state retains only revisions and one byte count, never a frame payload.
func (rx *sessionRx) resetScreenOffer(wconn *wsConn) {
	rx.screenMu.Lock()
	if rx.screenOfferCancel != nil {
		rx.screenOfferCancel()
	}
	rx.screenOfferConn = wconn
	rx.screenOfferCtx = nil
	rx.screenOfferCancel = nil
	rx.screenOfferedRevision = 0
	rx.screenOfferTarget = 0
	rx.screenOfferFailure = 0
	rx.screenOfferFrameBytes = 0
	rx.screenOfferRunning = false
	rx.screenOfferWake = make(chan struct{})
	if wconn != nil {
		// Owner: the current sessionRx subscriber's screen-offer worker.
		// Closing event: resetScreenOffer when that subscriber is replaced or cleared.
		rx.screenOfferCtx, rx.screenOfferCancel = context.WithCancel(context.Background())
	}
	rx.screenMu.Unlock()
}

func (rx *sessionRx) signalScreenOfferLocked() {
	if rx.screenOfferWake == nil {
		rx.screenOfferWake = make(chan struct{})
		return
	}
	// This is a broadcast, not a one-consumer notification: both the offer
	// worker and the ring pump may be waiting for the same accepted revision.
	close(rx.screenOfferWake)
	rx.screenOfferWake = make(chan struct{})
}

// noteScreenFrame records an accepted carrier offer or the size of a dropped
// frame that a targeted resend must retry after outbound capacity changes.
func (rx *sessionRx) noteScreenFrame(wconn *wsConn, revision uint64, frameBytes int, offered bool) {
	rx.screenMu.Lock()
	defer rx.screenMu.Unlock()
	if rx.screenOfferConn != wconn {
		return
	}
	if offered {
		if revision > rx.screenOfferedRevision {
			rx.screenOfferedRevision = revision
		}
		if revision >= rx.screenOfferFailure {
			rx.screenOfferFailure = 0
		}
	} else if revision >= rx.screenOfferTarget {
		rx.screenOfferFrameBytes = frameBytes
	}
	rx.signalScreenOfferLocked()
}

// requestScreenOffer coalesces all waiting prompt boundaries to their largest
// required revision. A single worker per session owns resend/backpressure.
func (rx *sessionRx) requestScreenOffer(wconn *wsConn, revision uint64) (ctx context.Context, start bool, settled bool, failed bool) {
	rx.screenMu.Lock()
	defer rx.screenMu.Unlock()
	if rx.screenOfferConn != wconn || rx.screenOfferCtx == nil {
		return nil, false, false, true
	}
	if rx.screenOfferedRevision >= revision {
		return rx.screenOfferCtx, false, true, false
	}
	if rx.screenOfferFailure >= revision {
		return rx.screenOfferCtx, false, false, true
	}
	if revision > rx.screenOfferTarget {
		rx.screenOfferTarget = revision
		rx.screenOfferFrameBytes = 0
	}
	if !rx.screenOfferRunning {
		rx.screenOfferRunning = true
		start = true
	}
	rx.signalScreenOfferLocked()
	return rx.screenOfferCtx, start, false, false
}

func (rx *sessionRx) screenOfferSnapshot(wconn *wsConn) (ctx context.Context, target, offered, failed uint64, frameBytes int, wake <-chan struct{}, ok bool) {
	rx.screenMu.Lock()
	defer rx.screenMu.Unlock()
	if rx.screenOfferConn != wconn || rx.screenOfferCtx == nil {
		return nil, 0, 0, 0, 0, nil, false
	}
	return rx.screenOfferCtx, rx.screenOfferTarget, rx.screenOfferedRevision, rx.screenOfferFailure,
		rx.screenOfferFrameBytes, rx.screenOfferWake, true
}

// rejectScreenOffer settles all currently coalesced targets when the bounded
// server submission cannot admit a worker. No worker exists to call finish.
func (rx *sessionRx) rejectScreenOffer(wconn *wsConn, revision uint64) {
	rx.screenMu.Lock()
	defer rx.screenMu.Unlock()
	if rx.screenOfferConn != wconn {
		return
	}
	if revision > rx.screenOfferTarget {
		rx.screenOfferTarget = revision
	}
	if rx.screenOfferTarget > rx.screenOfferFailure {
		rx.screenOfferFailure = rx.screenOfferTarget
	}
	rx.screenOfferRunning = false
	rx.signalScreenOfferLocked()
}

func (rx *sessionRx) failScreenOffer(wconn *wsConn, revision uint64) {
	rx.screenMu.Lock()
	defer rx.screenMu.Unlock()
	if rx.screenOfferConn != wconn {
		return
	}
	if revision > rx.screenOfferFailure {
		rx.screenOfferFailure = revision
	}
	rx.signalScreenOfferLocked()
}

// finishScreenOffer atomically closes the worker only if all currently
// requested revisions are offered or failed. A concurrent newer request either
// keeps this worker alive or observes it stopped and starts one replacement.
func (rx *sessionRx) finishScreenOffer(wconn *wsConn) bool {
	rx.screenMu.Lock()
	defer rx.screenMu.Unlock()
	if rx.screenOfferConn != wconn {
		rx.screenOfferRunning = false
		return true
	}
	if rx.screenOfferTarget > rx.screenOfferedRevision && rx.screenOfferTarget > rx.screenOfferFailure {
		return false
	}
	rx.screenOfferRunning = false
	return true
}

func (rx *sessionRx) waitScreenOffer(ctx context.Context, wconn *wsConn, revision uint64) error {
	for {
		offerCtx, _, offered, failed, _, wake, ok := rx.screenOfferSnapshot(wconn)
		if !ok {
			return context.Canceled
		}
		if offered >= revision || failed >= revision {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-offerCtx.Done():
			return offerCtx.Err()
		case <-wake:
		}
	}
}

func (rx *sessionRx) dueEffectWait(wconn *wsConn, offset uint64) (screenRevision uint64, notificationBytes int) {
	rx.deliveryMu.Lock()
	defer rx.deliveryMu.Unlock()
	if rx.deliveryConn != wconn || len(rx.pendingEffects) == 0 {
		return 0, 0
	}
	effect := rx.pendingEffects[0]
	if effect.conn != wconn || (effect.waitForBytes && effect.offset > offset) {
		return 0, 0
	}
	if effect.screenRevision > 0 {
		_, _, offered, failed, _, _, ok := rx.screenOfferSnapshot(wconn)
		if ok && failed < effect.screenRevision && offered < effect.screenRevision {
			return effect.screenRevision, len(notificationPayload(effect.method, effect.params))
		}
	}
	return 0, len(notificationPayload(effect.method, effect.params))
}
