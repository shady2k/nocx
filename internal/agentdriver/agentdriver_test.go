package agentdriver_test

import (
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/panegrid"
)

// The ordinary case: an agent we ship a driver for is driven by it.
func TestTheRegistryAnswersForAnAgentItKnows(t *testing.T) {
	r, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	d, ok := r.For("claude")
	if !ok {
		t.Fatal("the registry does not know claude, which is the one driver we ship")
	}
	if d.Agent() != "claude" {
		t.Errorf("For(claude) returned the %q driver", d.Agent())
	}
}

// And fails closed for one it does not. Not a default driver, not a guess: no
// driver, and the caller has to answer unknown — which every caller treats as
// busy, so an unrecognised agent is never typed into.
func TestTheRegistryFailsClosedForAnAgentItDoesNotKnow(t *testing.T) {
	r, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if d, ok := r.For("codex"); ok {
		t.Fatalf("the registry invented a %q driver for an agent nothing was written for", d.Agent())
	}
	if got := r.Classify("codex", panegrid.Frame{}); got != agentdriver.StateUnknown {
		t.Errorf("Classify for an unregistered agent = %q, want %q", got, agentdriver.StateUnknown)
	}
}

// A PARTIAL driver is refused at the composition root rather than at the first
// frame. A driver that cannot name its agent cannot be looked up, and one
// registered twice means two answers to one question — both are wiring
// mistakes, and a wiring mistake belongs to the start of the process.
type namelessDriver struct{}

func (namelessDriver) Agent() string                             { return "" }
func (namelessDriver) Classify(panegrid.Frame) agentdriver.State { return agentdriver.StateUnknown }

func TestADriverThatCannotNameItsAgentIsRefused(t *testing.T) {
	if _, err := agentdriver.NewRegistry(namelessDriver{}); err == nil {
		t.Fatal("a driver with no agent name was accepted")
	}
}

func TestTwoDriversForOneAgentAreRefused(t *testing.T) {
	if _, err := agentdriver.NewRegistry(agentdriver.Claude(), agentdriver.Claude()); err == nil {
		t.Fatal("two drivers were registered for one agent")
	}
}

func TestANilDriverIsRefused(t *testing.T) {
	if _, err := agentdriver.NewRegistry(nil); err == nil {
		t.Fatal("a nil driver was accepted")
	}
}

// The closed set is closed: Valid() is what every caller crossing a boundary
// asks, and it is the reason an unknown string off a wire cannot become a
// state nobody wrote a branch for.
func TestTheClosedSetIsClosed(t *testing.T) {
	for _, s := range agentdriver.States() {
		if !s.Valid() {
			t.Errorf("%q is listed in the set and reports itself invalid", s)
		}
	}
	if agentdriver.State("busy").Valid() {
		t.Error("a state nobody declared reports itself valid")
	}
	if agentdriver.State("").Valid() {
		t.Error("the empty state reports itself valid")
	}
}
