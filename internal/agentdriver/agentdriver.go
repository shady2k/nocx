// Package agentdriver answers ONE question about an enrolled pane: what is on
// its screen inviting, and may nocx act on it.
//
// # The closed set, and why it is closed
//
// A driver returns one of five values and nothing else. There is no "probably
// idle", no confidence and no free-form string, because both callers of this
// package are making a decision with a bad failure mode: nocx typing into a
// pane (nocx-dkawo.1) and the indicator telling a person what a worker is
// doing (nocx-szb40.4). A value nobody wrote a branch for would land in
// whichever branch was written last.
//
// Anything the driver cannot positively identify is StateUnknown, and every
// caller treats unknown as busy. That is the whole safety argument: refusal
// rather than mitigation. An earlier draft of the typing design proposed
// sending Escape first so a mistimed keystroke would be recoverable; it is
// not, because a mistimed keystroke does not merely fail to arrive — it
// answers whatever modal is on screen, which can approve a tool call the user
// never saw.
//
// StateExited is in the set and no driver may return it. A crashed or exited
// agent is a fact about the PROCESS, and reading it off the screen would mean
// believing an agent that printed the word.
//
// # Anchored in chrome, never in the agent's output
//
// Every anchor a driver uses is a POSITION in the terminal's own furniture —
// the rules that bound the input box, the row the mode line occupies, the
// cell the cursor is parked in. None of it is text the agent chose to print,
// which is what makes the classification injection-safe. This repository has
// already paid for the other approach once: a completion sentinel that matched
// itself at 7-13 seconds because the agent printed the brief it had just read.
//
// # One implementation per agent, and the registry fails closed
//
// Per AD-8 the driver is an interface with one implementation per agent. An
// agent nothing was written for has no driver, and a pane running it answers
// StateUnknown for its whole life — it is never typed into and its indicator
// never claims to know. Registration refuses a driver that cannot name its
// agent and a second driver for an agent that already has one, because both
// are wiring mistakes and a wiring mistake belongs to process start.
package agentdriver

import (
	"fmt"

	"github.com/shady2k/nocx/internal/panegrid"
)

// State is what a pane's screen is inviting. The set is closed; see the
// package comment for why.
type State string

const (
	// StateFreeText means an input box is on screen and waiting. It is the
	// ONLY state nocx may type into.
	StateFreeText State = "free_text"
	// StatePermissionChoice means the agent has raised a tool-approval
	// dialog and is waiting on a human. Answering it answers the agent.
	StatePermissionChoice State = "permission_choice"
	// StateModalChoice means a menu is up that the agent did not raise — a
	// user-opened one such as /model. Also waiting on a human, and answering
	// it does not answer the agent.
	StateModalChoice State = "modal_choice"
	// StateWorking means work is in progress in this pane: a turn in flight,
	// or a background agent the main turn may be blocked on. No input is
	// being invited even when an input box is visible.
	StateWorking State = "working"
	// StateError is the TUI's OWN error, drawn as chrome: the API is
	// unreachable, overloaded, or the quota is gone. It earns its own value
	// because the two neighbours are both wrong. StateUnknown would fold it
	// into busy, and busy tells the person "it is running, leave it alone"
	// when the truth is "come here, this will not resolve itself". And it is
	// not a choice awaiting an answer, so it is not StatePermissionChoice
	// either: there is nothing to answer.
	//
	// An error the agent PRINTED into its transcript and then went back to
	// waiting is StateFreeText, deliberately. On the screen it is
	// indistinguishable from idle because it IS idle — the agent finished,
	// badly, and is waiting for you. Telling those apart needs meaning, and
	// no driver reads meaning.
	StateError State = "error"
	// StateUnknown means the driver could not positively identify the state.
	// Every caller treats it as busy.
	StateUnknown State = "unknown"
	// StateExited means the process is gone. It is supplied by whoever owns
	// the session, never read off the screen, and Classify never returns it.
	StateExited State = "exited"
)

// States lists the closed set, in the order the package comment introduces it.
func States() []State {
	return []State{
		StateFreeText, StatePermissionChoice, StateModalChoice,
		StateWorking, StateError, StateUnknown, StateExited,
	}
}

// Valid reports whether s is in the closed set. What crosses a boundary is a
// string, and this is where a string stops being one.
func (s State) Valid() bool {
	for _, k := range States() {
		if s == k {
			return true
		}
	}
	return false
}

// Driver classifies one agent's screen. One implementation per agent (AD-8).
type Driver interface {
	// Agent names the agent this driver drives, as the enrolment act names
	// it. It is the registry key.
	Agent() string
	// Classify answers from the closed set, and never returns StateExited.
	// It reads only the frame it is given: a driver holds no state between
	// frames, because a rule that remembers is a rule that can be stuck.
	Classify(f panegrid.Frame) State
}

// Registry maps an agent name to its driver, and fails closed.
type Registry struct {
	byAgent map[string]Driver
}

// NewRegistry validates the wiring once, at the composition root.
func NewRegistry(drivers ...Driver) (*Registry, error) {
	r := &Registry{byAgent: make(map[string]Driver, len(drivers))}
	for i, d := range drivers {
		if d == nil {
			return nil, fmt.Errorf("agentdriver: driver %d is nil", i)
		}
		name := d.Agent()
		if name == "" {
			return nil, fmt.Errorf("agentdriver: driver %d names no agent, so nothing could ever look it up", i)
		}
		if _, dup := r.byAgent[name]; dup {
			return nil, fmt.Errorf("agentdriver: two drivers registered for %q, which is two answers to one question", name)
		}
		r.byAgent[name] = d
	}
	return r, nil
}

// For returns the driver for an agent, and false when there is none. False is
// a normal answer: most agents have no driver, and that is what keeps nocx out
// of their panes.
func (r *Registry) For(agent string) (Driver, bool) {
	if r == nil {
		return nil, false
	}
	d, ok := r.byAgent[agent]
	return d, ok
}

// Classify is the failing-closed form of For: an agent with no driver answers
// StateUnknown rather than an error the caller has to remember to handle.
func (r *Registry) Classify(agent string, f panegrid.Frame) State {
	d, ok := r.For(agent)
	if !ok {
		return StateUnknown
	}
	return d.Classify(f)
}
