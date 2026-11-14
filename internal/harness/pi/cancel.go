package pi

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shady2k/nocx/internal/harness"
)

// Give pi time to abort tools and persist their results before closing stdin.
const abortGrace = 2 * time.Second

const (
	turnActive uint32 = iota
	turnCancelled
	turnTerminal
	turnCancelledTerminal
)

type cancelState struct{ status atomic.Uint32 }

func (s *cancelState) cancelled() bool {
	status := s.status.Load()
	return status == turnCancelled || status == turnCancelledTerminal
}

// terminal and Cancel compete for the same transition, before publishing an event.
func (s *cancelState) terminal() (cancelled, first bool) {
	for {
		status := s.status.Load()
		switch status {
		case turnTerminal, turnCancelledTerminal:
			return status == turnCancelledTerminal, false
		case turnActive:
			if s.status.CompareAndSwap(status, turnTerminal) {
				return false, true
			}
		case turnCancelled:
			if s.status.CompareAndSwap(status, turnCancelledTerminal) {
				return true, true
			}
		}
	}
}

type turn struct {
	*harness.Process
	state      *cancelState
	done       chan struct{}
	cancelOnce sync.Once
	writeMu    sync.Mutex
	cleanupErr error
}

func newTurn(ctx context.Context, process *harness.Process, cleanup func() error, state *cancelState) *turn {
	t := &turn{Process: process, state: state, done: make(chan struct{})}
	go func() {
		_, _ = process.Wait(context.Background())
		t.cleanupErr = cleanup()
		close(t.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = t.Cancel(context.Background())
		case <-t.done:
		}
	}()
	return t
}

// Cancel first asks pi to settle its session. Killing immediately would leave
// persisted assistant tool calls without their corresponding tool results.
func (t *turn) Cancel(ctx context.Context) error {
	select {
	case <-t.done:
		return nil
	default:
	}
	t.cancelOnce.Do(func() {
		if !t.state.status.CompareAndSwap(turnActive, turnCancelled) {
			return
		}
		go func() {
			timer := time.NewTimer(abortGrace)
			defer timer.Stop()
			select {
			case <-t.done:
			case <-timer.C:
				_ = t.Process.Cancel(context.Background())
			}
		}()
		go func() {
			if err := t.writeCommand(map[string]string{"type": "abort"}); err != nil {
				_ = t.Process.Cancel(context.Background())
			}
		}()
	})
	select {
	case <-t.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *turn) Wait(ctx context.Context) (harness.Outcome, error) {
	select {
	case <-t.done:
		outcome, err := t.Process.Wait(context.Background())
		return cancellationResult(t.state.cancelled(), outcome, errors.Join(err, t.cleanupErr))
	case <-ctx.Done():
		return harness.Outcome{}, ctx.Err()
	}
}

// write never leaves Run waiting on an agent that stopped reading its pipe.
// The writer is released when the cancellation grace timer kills that agent.
func (t *turn) write(ctx context.Context, value any) error {
	result := make(chan error, 1)
	go func() { result <- t.writeCommand(value) }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-t.done:
		_, err := t.Wait(context.Background())
		if err == nil {
			err = errors.New("pi exited before accepting a command")
		}
		return err
	}
}

func (t *turn) writeCommand(value any) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return json.NewEncoder(t.Process.Stdin()).Encode(value)
}

func cancellationResult(cancelled bool, outcome harness.Outcome, err error) (harness.Outcome, error) {
	if !cancelled {
		return outcome, err
	}
	if failure, ok := err.(*harness.TurnError); ok && failure.Reason == "cancelled" {
		err = nil
	}
	return harness.Outcome{}, errors.Join(harness.ErrCancelled, err)
}

func (t *turn) startPrompt(text string) {
	go func() {
		if err := t.write(context.Background(), map[string]string{"type": "prompt", "message": text}); err != nil {
			_ = t.Process.Cancel(context.Background())
		}
	}()
}
