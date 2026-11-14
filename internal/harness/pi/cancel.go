package pi

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shady2k/nocx/internal/harness"
)

// Give pi time to abort tools and persist their results before closing stdin.
const abortGrace = 2 * time.Second

type cancelState struct{ requested atomic.Bool }

type turn struct {
	*harness.Process
	state      *cancelState
	done       chan struct{}
	cancelOnce sync.Once
}

func newTurn(ctx context.Context, process *harness.Process, cleanup func(), state *cancelState) *turn {
	t := &turn{Process: process, state: state, done: make(chan struct{})}
	go func() {
		_, _ = process.Wait(context.Background())
		cleanup()
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
		t.state.requested.Store(true)
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
			if err := json.NewEncoder(t.Process.Stdin()).Encode(map[string]string{"type": "abort"}); err != nil {
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
		if t.state.requested.Load() {
			return harness.Outcome{}, harness.ErrCancelled
		}
		return t.Process.Wait(context.Background())
	case <-ctx.Done():
		return harness.Outcome{}, ctx.Err()
	}
}
