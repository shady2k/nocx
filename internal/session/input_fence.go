package session

import (
	"context"
	"errors"
	"sync"
)

var ErrInputFenced = errors.New("session input fenced")

// inputGate is owned by one session incarnation. active covers direct writes
// and queue entries from admission until their actual completion/discard.
type inputGate struct {
	mu      sync.Mutex
	active  int
	fencing bool
	closed  bool
	retired bool
	drained chan struct{}
}

func newInputGate() *inputGate { return &inputGate{} }

func (g *inputGate) admit() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fencing || g.closed {
		return false
	}
	g.active++
	return true
}

func (g *inputGate) finish() {
	g.mu.Lock()
	g.active--
	if g.active == 0 && g.fencing {
		close(g.drained)
	}
	g.mu.Unlock()
}

func (g *inputGate) allowed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.fencing && !g.closed
}

func (g *inputGate) fence(ctx context.Context, commit func() error, retire func()) error {
	return g.closeAdmission(ctx, commit, retire, true)
}

// seal closes an already-ended session's input without reclassifying its exit
// as replacement retirement.
func (g *inputGate) seal(ctx context.Context) error {
	return g.closeAdmission(ctx, nil, nil, false)
}

func (g *inputGate) closeAdmission(ctx context.Context, commit func() error, retire func(), retired bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	if g.closed || g.fencing {
		g.mu.Unlock()
		return ErrInputFenced
	}
	g.fencing = true
	g.drained = make(chan struct{})
	if g.active == 0 {
		close(g.drained)
	}
	drained := g.drained
	g.mu.Unlock()

	select {
	case <-drained:
	case <-ctx.Done():
		g.mu.Lock()
		g.fencing = false
		g.drained = nil
		g.mu.Unlock()
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		g.mu.Lock()
		g.fencing = false
		g.drained = nil
		g.mu.Unlock()
		return err
	}
	if commit != nil {
		if err := commit(); err != nil {
			g.mu.Lock()
			g.fencing = false
			g.drained = nil
			g.mu.Unlock()
			return err
		}
	}
	g.mu.Lock()
	g.closed = true
	g.retired = retired
	g.mu.Unlock()
	if retire != nil {
		retire()
	}
	g.mu.Lock()
	g.fencing = false
	g.drained = nil
	g.mu.Unlock()
	return nil
}
