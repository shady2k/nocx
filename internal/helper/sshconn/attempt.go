//go:build nocx_local_ssh

package sshconn

import (
	"context"
	"sync"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/operation/catalog"
)

func runAttempt(ctx context.Context, dial func(context.Context) error) error {
	return runAttemptOnClock(ctx, hostconn.RealClock{}, dial)
}

// Each phase is independently bounded. Waiting for a person is not silence
// from a host; its reverse request owns that wait's operation.
func runAttemptOnClock(ctx context.Context, clock hostconn.Clock, dial func(context.Context) error) error {
	attemptCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)
	var mu sync.Mutex
	var scope *catalog.Scope
	var timer <-chan monoclock.Nanos
	var stage hostconn.Stage
	waiting := false
	changed := make(chan struct{}, 1)
	done := make(chan struct{})
	renew := func() {
		if scope != nil {
			scope.Complete()
		}
		scope = catalog.BeginInternal(ctx, catalog.OpInternalSshAttempt)
		timer = clock.After(scope.Declaration().Max.Duration())
	}
	renew()
	report := func(event hostconn.Event) {
		mu.Lock()
		switch event.Kind {
		case hostconn.StageReached:
			if event.Stage == stage || waiting {
				mu.Unlock()
				return
			}
			stage = event.Stage
			renew()
		case hostconn.NeedsPerson:
			if waiting {
				mu.Unlock()
				return
			}
			waiting = true
			scope.Complete()
			scope = nil
			timer = nil
		case hostconn.PersonAnswered:
			if !waiting {
				mu.Unlock()
				return
			}
			waiting = false
			renew()
		default:
			mu.Unlock()
			return
		}
		mu.Unlock()
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	go func() {
		for {
			mu.Lock()
			expired := timer
			mu.Unlock()
			select {
			case <-expired:
				mu.Lock()
				current := timer == expired
				if current {
					cancel(context.DeadlineExceeded)
				}
				mu.Unlock()
				if current {
					return
				}
			case <-changed:
			case <-done:
				return
			case <-attemptCtx.Done():
				return
			}
		}
	}()
	err := dial(hostconn.WithAttemptReporter(attemptCtx, report))
	close(done)
	mu.Lock()
	if scope != nil {
		scope.Complete()
	}
	mu.Unlock()
	if cause := context.Cause(attemptCtx); cause != nil {
		return cause
	}
	return err
}
