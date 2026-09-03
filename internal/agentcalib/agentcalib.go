// Package agentcalib is the guided calibration: nocx asks a person to drive
// their agent into a NAMED state, and labels the frame with the state it
// asked for.
//
// # Why the person produces the evidence
//
// Verifying a rule against a recording it happened to be pointed at proves
// nothing — a rule that never matches looks exactly like a rule that works.
// So the evidence is produced on demand and labelled at the moment it is
// produced. The labelled set is then the artifact the rest of the design
// consumes: verification replays a rule against it and decides whether the
// rule may gate typing at all, and a proposal is derived from the differences
// between the labelled frames (the configuration design, 2026-08-27 §5).
//
// # A LABEL IS BOUND TO THE STEP, and that is structural rather than careful
//
// Nothing here takes a label from a caller. The walk holds an ordered list of
// steps, exactly one of which is PENDING; the only verbs are capture and skip,
// and the label they write is read out of the pending step. There is no
// signature in this package with a Label parameter, so there is nothing for a
// surface to point at an arbitrary frame with — which is the first thing the
// bead says this design is falsified by.
//
// The frame is bound the same way. It is never passed in: it is read from the
// pane's own live grid at the instant the person says "now", through the
// Screens seam, for the pane the walk was begun on. So a label sits on the
// screen the person had in front of them when they answered the question they
// were being asked.
//
// # A COMPLETED CALIBRATION CANNOT LACK A REQUIRED LABEL
//
// idle, working and asks-you are the three states a person can produce on
// demand, and skipping one is refused rather than recorded. The other three
// are optional: uncalibrated they fall to unknown, which every consumer treats
// as busy, which is a refusal rather than a wrong answer. And completeness is
// DERIVED from the labels a set holds (see Set.Complete) rather than stored,
// so the guarantee survives a file a person edited.
//
// exited is not here at all. It is a fact about the process, and reading it
// off a screen would mean believing an agent that printed the word.
package agentcalib

import (
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
)

// Label names a screen a person can be asked to produce. It is the design's
// vocabulary rather than the driver's, because it is what the question is
// asked in; Step.Expect carries the driver State the same screen must
// classify to.
type Label string

const (
	// LabelIdle is the agent waiting for input with nothing running.
	LabelIdle Label = "idle"
	// LabelWorking is a turn in flight.
	LabelWorking Label = "working"
	// LabelAsksYou is the agent's own question — a tool approval — waiting
	// on a human. Answering it answers the AGENT, which is why it is
	// separate from a menu the person opened.
	LabelAsksYou Label = "asks-you"
	// LabelWaitingOnChild is work happening in a background agent while the
	// main input box looks live. Optional: it is a refinement of working and
	// classifies to the same state.
	LabelWaitingOnChild Label = "waiting-on-child"
	// LabelError is the TUI's OWN error drawn as chrome. Optional, because
	// a person cannot always produce one on demand.
	LabelError Label = "error"
	// LabelMenuOpen is a menu the PERSON opened, such as /model.
	LabelMenuOpen Label = "menu-open"
)

// Step is one thing the person is asked to do, and it is the only place a
// label comes from.
type Step struct {
	// Label is what the frame captured for this step is labelled with.
	Label Label
	// Required says whether the calibration can complete without it. The
	// three required ones are the three a person can produce on demand.
	Required bool
	// Ask is the instruction, in the second person, as the person reads it.
	Ask string
	// Expect is the driver State this screen must classify to. It is here
	// rather than at the verifier because the two vocabularies must be
	// mapped in exactly one place; a second mapping would disagree the first
	// time a state was added.
	Expect agentdriver.State
}

// steps is the closed, ordered list. The required three come first
// deliberately: a walk that is abandoned part way is most likely to have
// produced the labels a rule cannot be verified without.
var steps = []Step{
	{
		Label: LabelIdle, Required: true, Expect: agentdriver.StateFreeText,
		Ask: "Leave the agent waiting for input, with nothing typed and nothing running.",
	},
	{
		Label: LabelWorking, Required: true, Expect: agentdriver.StateWorking,
		Ask: "Give the agent something to do, and capture while it is working.",
	},
	{
		Label: LabelAsksYou, Required: true, Expect: agentdriver.StatePermissionChoice,
		Ask: "Let the agent ask you to approve a tool, and leave its question on screen.",
	},
	{
		Label: LabelWaitingOnChild, Expect: agentdriver.StateWorking,
		Ask: "Optional: let the agent start a background agent, and capture while that child is running.",
	},
	{
		Label: LabelError, Expect: agentdriver.StateError,
		Ask: "Optional: capture the agent's own error chrome — the API unreachable, overloaded, or out of quota.",
	},
	{
		Label: LabelMenuOpen, Expect: agentdriver.StateModalChoice,
		Ask: "Optional: open one of the agent's own menus yourself, such as /model, and leave it open.",
	},
}

// Steps is the closed list, in the order the walk asks for them.
func Steps() []Step { return append([]Step(nil), steps...) }

// Answer is what a person can do about the step they are being asked.
type Answer string

const (
	// AnswerCapture labels the pane's current screen with the pending step.
	AnswerCapture Answer = "capture"
	// AnswerSkip leaves an optional state uncalibrated, and is refused for
	// a required one.
	AnswerSkip Answer = "skip"
	// AnswerRedo re-asks the step just answered.
	//
	// It exists so that the missing label picker costs nothing: a person who
	// captured at the wrong moment would otherwise have to walk the whole
	// calibration again, and the cheap repair for that — letting them point
	// a label at a frame they already have — is exactly what this design is
	// falsified by. Re-ASKING the step keeps both bindings: the label still
	// comes from the step, and the frame is still read live when they answer.
	AnswerRedo Answer = "redo"
)

// Screens is the seam onto the pane's live grid (AD-8). One method, because
// calibration may read a frame and may not enrol, withdraw, classify or type.
type Screens interface {
	Frame(paneID string) (panegrid.Frame, error)
}

// Calibrations holds the walks in flight and writes the sets they produce.
//
// A walk lives in memory for its duration and is written to disk only when it
// COMPLETES. That is not laziness about persistence: a partial walk saved over
// a good set would destroy, on the first abandoned retry, the calibration a
// rule was verified against yesterday.
type Calibrations struct {
	log     log.Logger
	screens Screens
	store   Store

	mu    sync.Mutex
	walks map[string]*walk
}

// New wires the calibration seam at the composition root.
func New(lg log.Logger, screens Screens, store Store) *Calibrations {
	return &Calibrations{log: lg, screens: screens, store: store, walks: map[string]*walk{}}
}

// walk is one calibration in progress: the pane it is being driven on, the
// answers so far, and the capture they have painted.
type walk struct {
	agent  string
	pane   string
	header agentcapture.Header
	at     int
	given  []Record
	chunks []agentcapture.Chunk
}

// Status is everything a surface needs to draw the calibration for one agent:
// the closed step list, the walk in progress if there is one, and what is
// already on disk.
type Status struct {
	Agent  string
	Steps  []Step
	Walk   *WalkStatus
	Stored *StoredStatus
}

// WalkStatus is a calibration in progress.
type WalkStatus struct {
	// Pane is the pane the person is driving. A walk is bound to it at
	// Begin, so a later answer cannot be taken from another pane.
	Pane string
	// Pending is the step being asked for. Never negative: a walk with every
	// step answered has been written out and is no longer in progress.
	Pending int
	// Given is one record per step answered, in order.
	Given []Record
}

// StoredStatus is the set on disk, as a surface must state it: the design
// requires the verification verdict to be shown with its consequence, and
// "verified against 3 of 3" is not sayable without knowing which three.
type StoredStatus struct {
	Complete bool
	Labels   []Record
}

// Begin starts a calibration for the agent running in a pane, discarding any
// walk already in progress there.
//
// The pane must have a live grid: a calibration on a pane nocx is not watching
// could label nothing, and finding that out at the first capture would be
// finding it out after the person had driven their agent somewhere.
func (c *Calibrations) Begin(pane, agent string) (Status, error) {
	if pane == "" {
		return Status{}, fmt.Errorf("agentcalib: a calibration needs a pane to be driven on")
	}
	if err := validAgent(agent); err != nil {
		return Status{}, err
	}
	f, err := c.screens.Frame(pane)
	if err != nil {
		return Status{}, fmt.Errorf("agentcalib: pane %s has no live screen to calibrate against: %w", pane, err)
	}
	c.mu.Lock()
	c.walks[pane] = &walk{
		agent: agent, pane: pane,
		header: agentcapture.Header{
			Agent: agent, Argv: []string{agent},
			Cols: f.Cols, Rows: f.Rows,
		},
	}
	c.mu.Unlock()
	c.log.Info("calibration begun", "pane_id", pane, "agent", agent, "cols", f.Cols, "rows", f.Rows)
	return c.Status(pane, agent)
}

// Abandon drops a walk in progress. Nothing is written, so the set that was
// already on disk is exactly as it was.
func (c *Calibrations) Abandon(pane string) {
	c.mu.Lock()
	_, had := c.walks[pane]
	delete(c.walks, pane)
	c.mu.Unlock()
	if had {
		c.log.Info("calibration abandoned", "pane_id", pane)
	}
}

// Answer applies one answer to the PENDING step.
//
// step is the step the surface believes it is showing, and it must be the
// pending one. It is a staleness guard and never a selector: the label written
// comes from the walk's own pending step either way, so a surface that redrew
// late is refused rather than answered into the wrong label.
func (c *Calibrations) Answer(pane string, step int, answer Answer) (Status, error) {
	c.mu.Lock()
	w, ok := c.walks[pane]
	c.mu.Unlock()
	if !ok {
		return Status{}, fmt.Errorf("agentcalib: no calibration is in progress on pane %s", pane)
	}
	if step != w.at {
		return Status{}, fmt.Errorf(
			"agentcalib: this calibration is asking for step %d (%s), and the answer names step %d",
			w.at, steps[w.at].Label, step)
	}
	switch answer {
	case AnswerRedo:
		if err := c.redo(w); err != nil {
			return Status{}, err
		}
	case AnswerSkip:
		if err := c.skip(w); err != nil {
			return Status{}, err
		}
	case AnswerCapture:
		if err := c.capture(w); err != nil {
			return Status{}, err
		}
	default:
		return Status{}, fmt.Errorf("agentcalib: %q is not an answer (choose capture, skip or redo)", answer)
	}
	if w.at >= len(steps) {
		if err := c.finish(w); err != nil {
			return Status{}, err
		}
	}
	return c.Status(pane, w.agent)
}

// capture reads the pane's screen NOW and labels it with the pending step.
func (c *Calibrations) capture(w *walk) error {
	f, err := c.screens.Frame(w.pane)
	if err != nil {
		return fmt.Errorf("agentcalib: pane %s has no screen to capture: %w", w.pane, err)
	}
	if f.Cols != w.header.Cols || f.Rows != w.header.Rows {
		return fmt.Errorf(
			"agentcalib: the pane was resized from %dx%d to %dx%d during this calibration; "+
				"a labelled set holds one geometry, so start it again at the size you mean to run at",
			w.header.Cols, w.header.Rows, f.Cols, f.Rows)
	}
	// The mark is the ORDINAL of this capture, not a wall clock. A capture's
	// marks only have to be non-decreasing and to separate the moments, and a
	// clock would add both a dependency a test may not have and a way for two
	// stamps to land in one millisecond — which would make the replay of one
	// label answer with another label's screen.
	mark := int64(len(w.chunks))
	data := string(agentcapture.Paint(f))
	w.chunks = append(w.chunks, agentcapture.Chunk{
		AtMs:   mark,
		Offset: agentcapture.EndOffset(w.chunks, len(w.chunks)),
		Data:   data,
	})
	w.given = append(w.given, Record{Label: steps[w.at].Label, AtMs: &mark})
	w.at++
	return nil
}

// skip leaves an optional state uncalibrated, and refuses a required one.
func (c *Calibrations) skip(w *walk) error {
	s := steps[w.at]
	if s.Required {
		return fmt.Errorf(
			"agentcalib: %s cannot be skipped — a rule that has not classified it may not gate typing, "+
				"so a calibration without it verifies nothing; abandon this one instead if you cannot produce it",
			s.Label)
	}
	w.given = append(w.given, Record{Label: s.Label, Skipped: true})
	w.at++
	return nil
}

// redo re-opens the step just answered, dropping its record and the frame it
// captured.
func (c *Calibrations) redo(w *walk) error {
	if w.at == 0 {
		return fmt.Errorf("agentcalib: nothing has been answered yet, so there is nothing to do again")
	}
	last := w.given[len(w.given)-1]
	w.given = w.given[:len(w.given)-1]
	if !last.Skipped {
		w.chunks = w.chunks[:len(w.chunks)-1]
	}
	w.at--
	return nil
}

// finish writes the set and ends the walk. Every step has been answered, so
// the required three are present unless one was skipped, and skipping one is
// refused above.
func (c *Calibrations) finish(w *walk) error {
	set := Set{Agent: w.agent, Header: w.header, Chunks: w.chunks, Labels: w.given}
	if !set.Complete() {
		// Unreachable while skip refuses a required step, and stated anyway:
		// this is the invariant the bead is falsified by, and an invariant
		// held only by a caller's good behaviour is held by nobody.
		return fmt.Errorf("agentcalib: refusing to store a calibration that lacks a required label")
	}
	if err := c.store.Save(set); err != nil {
		return fmt.Errorf("agentcalib: store the labelled set: %w", err)
	}
	c.mu.Lock()
	delete(c.walks, w.pane)
	c.mu.Unlock()
	c.log.Info("calibration stored", "pane_id", w.pane, "agent", w.agent, "labels", len(w.given))
	return nil
}

// Status answers the walk in progress on a pane, if any, and what is on disk
// for the agent. Both, in one answer, because a surface draws them together
// and a second round trip would let them disagree.
func (c *Calibrations) Status(pane, agent string) (Status, error) {
	if err := validAgent(agent); err != nil {
		return Status{}, err
	}
	out := Status{Agent: agent, Steps: Steps()}
	c.mu.Lock()
	w, ok := c.walks[pane]
	if ok {
		out.Walk = &WalkStatus{Pane: w.pane, Pending: w.at, Given: append([]Record(nil), w.given...)}
	}
	c.mu.Unlock()
	set, found, err := c.store.Load(agent)
	if err != nil {
		return Status{}, fmt.Errorf("agentcalib: read the stored set: %w", err)
	}
	if found {
		out.Stored = &StoredStatus{Complete: set.Complete(), Labels: set.Labels}
	}
	return out, nil
}
