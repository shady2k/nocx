package hostconn

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/monoclock"
)

// AttemptRunner applies the composition root's operation lifetime to each dial.
type AttemptRunner func(context.Context, func(context.Context) error) error

// WithAttempt installs the operation boundary for every attempt, including retries.
func WithAttempt(run AttemptRunner) Option { return func(m *Manager) { m.attempt = run } }

// ErrNoReconnect means the offered state has changed before the action claimed it.
var ErrNoReconnect = errors.New("hostconn: reconnect is no longer offered")

// Retain holds the logical connection for a pane, independently of a transport's
// channel leases. A shell dying must not erase the pane that needs a retry.
func (s *Supervisor) Retain(policy string) func() {
	s.mu.Lock()
	s.retained++
	s.reconnectPolicy = policy
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.retained--
			var keep *Lease
			if s.retained == 0 {
				if s.retryCancel != nil {
					s.retryCancel()
				}
				keep, s.keep = s.keep, nil
				if s.live == nil {
					s.advance(Event{Kind: Closed, Cause: CauseReleased})
				}
			}
			s.mu.Unlock()
			if keep != nil {
				keep.Release()
			}
		})
	}
}

// RequestReconnect claims the supervisor's current offer and closes the socket
// before returning. The dial is registered here, but runs asynchronously. A
// recovered probe cannot be destroyed by a press for an older observation.
func (s *Supervisor) RequestReconnect(expectedSeq uint64) error {
	s.mu.Lock()
	if expectedSeq != 0 && expectedSeq != s.seq {
		s.mu.Unlock()
		return ErrNoReconnect
	}
	switch s.state.Kind {
	case Stalled, Reconnecting, Failed, Ended, WaitingPerson:
	default:
		s.mu.Unlock()
		return ErrNoReconnect
	}
	if s.planContext == nil || s.retained == 0 {
		s.mu.Unlock()
		return ErrNoReconnect
	}
	if s.retryCancel != nil {
		s.mu.Unlock()
		return nil
	}
	l := s.live
	if l != nil && !s.fenceLocked(l) {
		s.mu.Unlock()
		return nil
	}
	if l != nil {
		s.dropRidersLocked(l)
		s.notifyWaiters(l, ErrLinkLost)
	}
	s.startRetryLocked(true)
	s.mu.Unlock()
	if l != nil {
		s.finishFenced(l, Event{Kind: Lost, Cause: CauseLostLink})
	}
	return nil
}

func (s *Supervisor) scheduleRetryLocked() {
	if s.state.Kind != Reconnecting || (s.state.Cause != CauseLostLink && s.state.Cause != CauseSilence) {
		return
	}
	if s.retained == 0 {
		return
	}
	if s.reconnectPolicy != "auto" {
		s.advance(Event{Kind: Closed, Cause: s.state.Cause})
		return
	}
	s.startRetryLocked(false)
}

func (s *Supervisor) startRetryLocked(immediate bool) {
	if s.retryCancel != nil || s.retained == 0 || s.planContext == nil {
		return
	}
	ctx, cancel := context.WithCancel(s.planContext)
	s.retryCancel = cancel
	go s.retry(ctx, immediate)
}

func retryDelay(attempt int) time.Duration {
	delay := 500 * time.Millisecond
	for i := 1; i < attempt && delay < 30*time.Second; i++ {
		delay *= 2
	}
	spread := int64(delay) / 5
	//nolint:gosec // jitter spreads retries; it does not protect a secret
	delay += time.Duration(rand.Int64N(2*spread+1) - spread)
	if delay > 15*time.Second {
		delay = 15 * time.Second
	}
	return delay
}

func (s *Supervisor) retry(ctx context.Context, immediate bool) {
	defer func() {
		s.mu.Lock()
		s.retryCancel = nil
		s.retryScheduled = false
		s.scheduleRetryLocked()
		s.mu.Unlock()
	}()
	for {
		s.mu.Lock()
		if ctx.Err() != nil || s.retained == 0 {
			s.mu.Unlock()
			return
		}
		state, plan := s.state, s.plan
		if !immediate && (state.Kind != Reconnecting || s.reconnectPolicy != "auto") {
			s.mu.Unlock()
			return
		}
		var due <-chan monoclock.Nanos
		if !immediate {
			due = s.clock.After(retryDelay(state.Attempt))
			s.retryScheduled = true
		}
		s.mu.Unlock()
		if due != nil {
			select {
			case <-due:
			case <-ctx.Done():
				return
			}
		}
		s.mu.Lock()
		s.retryScheduled = false
		s.mu.Unlock()
		lease, err := s.Acquire(ctx, plan)
		if err == nil {
			s.mu.Lock()
			previous := s.keep
			if s.retained > 0 && ctx.Err() == nil {
				s.keep = lease
				lease = nil
			}
			s.mu.Unlock()
			if previous != nil {
				previous.Release()
			}
			if lease != nil {
				lease.Release()
			}
			return
		}
		immediate = false
	}
}
