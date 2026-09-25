package app

// A pane's lane lives in the environment-entry registry exactly as long as
// the pane does (nocx-2v80t.3.32). Every hosted pane registers its lane; a
// registry that never forgot one grew for the life of the process, and so
// did the emitter's per-lane depth.

import (
	"context"
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/waittest"
)

// retained is how many lanes the registry holds.
func (r *environmentEntryRegistry) retained() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byLane)
}

// fixedStacks answers every lane's stack at the depth the test set for it.
type fixedStacks map[lifecycle.LaneID]int

func (f fixedStacks) State(lane lifecycle.LaneID) (lifecycle.LaneSnapshot, error) {
	stack := make([]lifecycle.DomainID, f[lane])
	for i := range stack {
		stack[i] = lifecycle.DomainID(fmt.Sprintf("dom-%s-%d", lane, i))
	}
	return lifecycle.LaneSnapshot{Lane: lane, Stack: stack}, nil
}

// Opens five panes, lets each integrate and enter a child, closes them, and
// then lets a late fact arrive for every closed lane — the domain-closed a
// kernel publishes after the pane is gone. Nothing may be left behind.
func TestPanesThatOpenAndCloseLeaveNothingInTheEntryRegistry(t *testing.T) {
	registry := newEnvironmentEntryRegistry()
	stacks := fixedStacks{}
	emitter := newEnvironmentEntryEmitter(noopEmitter{}, stacks, registry)

	var lanes []lifecycle.LaneID
	for i := range 5 {
		lane := lifecycle.LaneID(fmt.Sprintf("lane-%d", i))
		lanes = append(lanes, lane)
		ctx, closePane := context.WithCancel(context.Background())
		obs := &fakeEnvEntryObserver{}
		registry.register(ctx, lane, obs)

		stacks[lane] = 1
		emitter.checkGrowth(lane)
		stacks[lane] = 2
		emitter.checkGrowth(lane)
		if obs.n != 1 {
			t.Fatalf("pane %d: its live lane fired %d entries, want 1", i, obs.n)
		}

		closePane()
		stacks[lane] = 1
		emitter.checkGrowth(lane) // the late fact, after the pane is gone
	}

	// A pane's end is its context's, and the registration is dropped when
	// that context reports done — waited for as a state, never a duration.
	waittest.WaitFor(t, "every closed pane's lane to leave the registry", func() bool {
		return registry.retained() == 0
	})
	// And a fact arriving after the drop does not bring a lane back.
	for _, lane := range lanes {
		emitter.checkGrowth(lane)
	}
	if n := registry.retained(); n != 0 {
		t.Fatalf("a late fact put %d lanes back into the registry, want 0", n)
	}
}

// Paired: a lane stays registered — and still raises its entries — for as
// long as its pane lives, and a pane that re-registers its lane under a new
// lifetime (a re-adoption) is not unregistered by the old lifetime's end.
func TestALiveLaneStaysRegisteredAndARegistrationOutlivesTheOneItReplaced(t *testing.T) {
	registry := newEnvironmentEntryRegistry()
	lane := lifecycle.LaneID("lane-live")

	oldCtx, endOld := context.WithCancel(context.Background())
	registry.register(oldCtx, lane, &fakeEnvEntryObserver{})
	newCtx, endNew := context.WithCancel(context.Background())
	defer endNew()
	current := &fakeEnvEntryObserver{}
	registry.register(newCtx, lane, current)

	endOld()
	o, ok := registry.lookup(lane)
	if !ok || o != current {
		t.Fatalf("after the replaced lifetime ended the lane reads %v, %v; want the current registration", o, ok)
	}
	if n := registry.retained(); n != 1 {
		t.Fatalf("the registry holds %d lanes, want the one live pane's", n)
	}
}
