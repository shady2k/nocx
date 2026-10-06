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
	retired bool
	drained chan struct{}
}

func newInputGate() *inputGate { return &inputGate{} }

func (g *inputGate) admit() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fencing || g.retired {
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
	return !g.fencing && !g.retired
}

func (g *inputGate) fence(ctx context.Context, commit func() error, retire func()) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	if g.retired || g.fencing {
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
	if err := commit(); err != nil {
		g.mu.Lock()
		g.fencing = false
		g.drained = nil
		g.mu.Unlock()
		return err
	}
	g.mu.Lock()
	g.retired = true
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
