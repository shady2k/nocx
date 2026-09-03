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

// Observation is a driver's whole answer about ONE frame: the scalar state,
// and whatever else the rule was able to read off the same screen.
//
// # Why the answer stopped being a scalar
//
// It answered one question well and three requirements have each hit the same
// wall. The claude task panel carries, per child, a name, a task, an elapsed
// time and a token flow, and the whole panel reduced to the single word
// "working". Progress needs a measurement beside the state, not instead of it.
// And a declared checkpoint and a measured one must be shown side by side and
// never merged, which is two values where there was one.
//
// # Extras are OPTIONAL, and that is a safety property rather than a courtesy
//
// A rule that extracts nothing observes exactly the scalar it observed before
// extraction existed, with an empty Extras. So no agent regresses when the
// shape grows, and nothing is invented for a screen that did not say it: a
// missing field is ABSENT, never zero and never empty-string.
//
// # Extras never decide the state
//
// The branches of a rule document read predicates; extractors run beside that
// evaluation and their yield is not visible to it. A rule that reads MORE off
// a screen can therefore never answer that screen differently — which is what
// keeps the closed set closed and keeps the typing decision where it was.
type Observation struct {
	// State is the closed-set answer, and it is exactly what Classify
	// returns for the same frame.
	State State
	// Extras is one entry per extractor that matched at least one row, in
	// document order. An extractor that matched nothing is absent rather
	// than present and empty, because absent and empty are different claims
	// and only one of them is true.
	Extras []Extra
}

// Extra is one extractor's yield: the name the document gave it, and one map
// per row it matched, from a capture group's name to the text it captured. A
// group that did not participate in the match contributes no key.
type Extra struct {
	Name string
	Rows []map[string]string
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

// Observer is the richer half of Driver: a driver whose rule can read VALUES
// off the screen as well as name its state.
//
// It is a separate interface rather than a third method on Driver because
// answering the scalar is the whole of what a driver must do — a driver that
// only classifies is an observer that extracts nothing, and Registry.Observe
// says so by lifting its answer. Nothing is required to implement this, and a
// caller that only wants the state never looks for it.
type Observer interface {
	Driver
	// Observe answers the whole observation for one frame. Its State is the
	// same value Classify returns, and its Extras are whatever the rule
	// could extract; the same no-memory contract applies.
	Observe(f panegrid.Frame) Observation
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

// Observe is the failing-closed form of For: an agent with no driver answers
// StateUnknown with no extras, rather than an error the caller has to remember
// to handle. A driver that is not an Observer answers the same way it always
// did, lifted — which is the whole of what "extras are optional" means at this
// seam.
func (r *Registry) Observe(agent string, f panegrid.Frame) Observation {
	d, ok := r.For(agent)
	if !ok {
		return Observation{State: StateUnknown}
	}
	if o, ok := d.(Observer); ok {
		return o.Observe(f)
	}
	return Observation{State: d.Classify(f)}
}

// Classify is the scalar projection of Observe, and it is written as one so
// there is a single evaluation behind both. Two paths answering one question
// is how the two come to disagree on the frame nobody tried.
func (r *Registry) Classify(agent string, f panegrid.Frame) State {
	return r.Observe(agent, f).State
}

// SubagentsExtra is the name a rule document gives the extractor that reads an
// agent's CHILD rows off its own chrome. It is a constant here rather than a
// string at each reader because the document's vocabulary is this package's
// contract: a caller that misspells it silently observes a pane with no
// children, which is indistinguishable from a pane that has none.
const SubagentsExtra = "subagents"

// Subagent is one child agent the pane's agent has spawned, as the screen
// names it.
//
// # Two fields, and the omissions are the decision
//
// The claude panel draws four things per row — a name, the task, an elapsed
// time and a token flow — and only the first two survive this projection. The
// reason is the emit seam downstream (internal/paneobserve): observations are
// pushed on CHANGE, the elapsed time and the token count move on every single
// frame, and there is no third answer. Carrying them and keying on them emits
// per repaint, which is what "only changes travel" exists to prevent; carrying
// them and NOT keying on them ships a clock that stops at whatever it read
// when the row set last moved, which is worse than no clock because it looks
// live. So what crosses is what is STABLE for the life of a row, and the
// measurement stays in Extras for a caller that is looking at one frame.
//
// There is no running/finished flag for the same kind of reason, measured
// rather than chosen: over claude-subagent's whole window no glyph on a child
// row ever changes. A child that finishes LEAVES the panel, and its presence
// is the whole of what the screen says about it.
type Subagent struct {
	// Name is the child's name — "Explore", not the pane's agent. Never
	// empty: a row the screen did not name is not a child anybody could be
	// shown, and it is dropped rather than rendered blank.
	Name string
	// Task is what it was given to do, and it is OPTIONAL. A panel row
	// carries one only once the task has been drawn; absent means the screen
	// did not say, which is a different claim from an empty task.
	Task string
}

// Subagents projects the observation's extras onto the children the screen
// named, in the order the panel drew them.
//
// It is here rather than at each reader because Extras is a generic map keyed
// by a document's own vocabulary, and turning that vocabulary into meaning is
// this package's job (AD-8). A watcher that reached into Extras itself would
// be a second owner of what "subagents" means, and the two would disagree the
// first time a rule was repaired.
//
// Nil when the rule extracted nothing — which is every agent with no such
// extractor, and every frame of an agent that has spawned nothing.
func (o Observation) Subagents() []Subagent {
	var out []Subagent
	for _, e := range o.Extras {
		if e.Name != SubagentsExtra {
			continue
		}
		for _, row := range e.Rows {
			name := row["name"]
			if name == "" {
				continue
			}
			out = append(out, Subagent{Name: name, Task: row["task"]})
		}
	}
	return out
}
