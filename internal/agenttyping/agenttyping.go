// Package agenttyping is the typing primitive: nocx puts bytes into a pane it
// is watching, and only when that pane's own rule says it may (nocx-dkawo.1,
// D14 of the orchestration mechanism design).
//
// # What this exists to refuse
//
// A mistimed keystroke does not merely fail to arrive. It ANSWERS whatever
// modal is on screen, and the modal a coding agent puts up is a tool approval
// whose first option is Yes — so the failure is not a lost keystroke, it is a
// tool call the person never saw being approved on their behalf. An earlier
// draft proposed sending Escape first so a mistimed keystroke would be
// recoverable; that is strictly worse than refusing, because Escape ANSWERS the
// modal too. There is no mitigation here, only refusal.
//
// So: type only on a positively identified free_text. A permission menu, a
// user-opened menu, a spinner, the TUI's own error, an unknown and a pane nocx
// is not watching all receive NOTHING AT ALL, and the refusal is recorded with
// the state that refused. unknown is not a gap to be optimistic about: every
// consumer of internal/agentdriver treats it as busy, precisely so that the
// failure direction is a refusal.
//
// # The two gates are the permit's constructor, not two ifs in a row
//
// A caller of this package never reaches the pane's input queue. What reaches
// it is a permit, and grant is the only thing in this package that builds one:
//
//   - the pane's current state is free_text, POSITIVELY IDENTIFIED from a frame
//     read inside grant — never remembered from an earlier one, never supplied
//     by the caller;
//   - the agent's rule has EARNED typing authority (agentcalib.Verdict.MayType,
//     a value whose backing field is unexported in that package and written in
//     exactly one statement, so nothing here can manufacture it).
//
// Every early return in grant returns a refusal and a ZERO permit, and a zero
// permit's `by` is nil, so put writes nowhere. The pane and the agent live
// INSIDE the permit — the pattern agenttools.Runner uses for a session grant —
// so a holder of one cannot name another pane, and the agent is never a
// parameter: it comes from the enrolment act, exactly as a calibration's label
// comes from the step rather than from the surface.
//
// # THE INTERVAL, AND BOTH ITS ENDS
//
// It opens at the frame read whose classification is free_text and closes when
// the pane's input queue has accepted that segment's bytes. Between those two
// moments the screen can move, and a decision that is stale by the time it is
// acted on is exactly the mistimed keystroke — so the decision is RE-TAKEN from
// a frame read inside put, immediately before the write, with nothing between
// the two but the queue call itself. The bytes are built before the read, so
// the window holds no work at all.
//
// The permit's own grant is therefore not what authorises a write; it is what
// authorises an ATTEMPT. Each segment gets its own interval and its own frame,
// which is why the submit key — the one byte that can answer a modal — is
// gated on a frame read microseconds before it rather than on the frame that
// admitted the text.
//
// What BOUNDS the residual window is a property of the state itself, and it is
// worth stating because it is the reason this is shippable. A pane classified
// free_text has no turn in flight: that is what the classification means, and
// internal/agentdriver's rule sends a turn in flight, a background child, a
// menu, a dialog and any chrome it has not seen to other states. An agent with
// no turn in flight emits nothing, so in that window there is no producer that
// could raise a modal. The one producer that remains is the PERSON at that
// pane's keyboard, and their keystroke racing ours cannot be excluded by any
// local mechanism — their bytes and ours reach one pty from two goroutines.
// That residual is named rather than closed, and its cost is bounded: text
// interleaved in an input box, never an answer to a dialog, because a dialog
// cannot appear without a turn.
//
// # The submission is two writes, and never one
//
// The text is framed as a bracketed paste and the submit key is pressed
// SEPARATELY, so it cannot be swallowed as paste content — and so the byte that
// submits is gated on its own frame. nelix measured the paste framing at 0.0s
// against 2.2s for raw echo on 61.5 KB, so this is also the only form that
// scales.
//
// The framing assumes the receiving program enabled bracketed paste, which this
// package cannot ask a Frame about. It is not a hole: the only panes reachable
// here are panes whose agent's rule has been verified against a labelled set —
// a full-screen TUI — and if one of them did not enable it, the marker bytes
// land in an input box as literal text rather than as an answer to anything,
// because the gate has already established there is nothing on screen to
// answer.
//
// # Delivery is UNACKNOWLEDGED
//
// Accepted is not delivered. The queue taking a job says the bytes are in line
// for the pty, and seeing our text echo in the input region would be evidence
// it was typed, never that it was acted on. Nothing here closes a fact; only
// the participant's own subsequent call does.
//
// # Local only
//
// The remote helper types on the far side (D14) and that is not this package.
// What is written here goes to a session's own input queue, in this process.
package agenttyping

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/agentcalib"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
)

// MaxText bounds one submission, in bytes. A wake carries a sentence and a
// dispatch carries a brief; 64 KiB is well past both and well under the point
// where one job could starve a session's bounded write queue.
const MaxText = 64 << 10

// The submission's bytes, spelled once. bracketed paste around the text so a
// newline inside it is content rather than a submission, and the submit key on
// its own so it cannot be swallowed as paste content.
const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
	submitKey  = "\r"
)

// Screens is the seam onto a pane's live grid (AD-8). One method, because
// typing may READ a screen and may not enrol, withdraw, resize or classify one.
type Screens interface {
	Frame(paneID string) (panegrid.Frame, error)
}

// Rules classifies one frame under one agent's rule. The concrete registry
// satisfies it; the seam exists so this package depends on the abstraction and
// not on the registry's other powers.
type Rules interface {
	Classify(agent string, f panegrid.Frame) agentdriver.State
}

// Authority answers what an agent's rule has EARNED against the frames a person
// labelled for it (nocx-jse6x).
//
// The answer is a value this package cannot build: agentcalib.Verdict's backing
// field is unexported and written in one statement inside Verify, so every
// other way a Verdict can come about — a struct literal, a zero value, a failed
// lookup — denies typing without anybody having remembered to check.
type Authority interface {
	Verify(agent string) agentcalib.Verdict
}

// Enrolment answers which agent a pane was enrolled under.
//
// It is here so that the agent is never a parameter of a typing call. A caller
// that could name the agent could name one whose rule verifies while the pane
// runs something else, and the frame would then be read by a rule that was
// never about it. The agent comes from the enrolment act, which is also what
// opened the grid the frame is read from.
type Enrolment interface {
	// AgentOn names the agent, and false for a pane nocx is not watching.
	// False is the ordinary answer: almost no pane is enrolled.
	AgentOn(paneID string) (string, bool)
}

// Input is the pane's own input queue — the same one every keystroke goes to,
// so a submission is subject to everything a keystroke is subject to: the
// bootstrap window's quarantine, the queue's bound, this session and no other.
type Input interface {
	// Accept queues b as this pane's input and reports whether the queue took
	// it. ACCEPTED IS NOT DELIVERED: true means the queue has the job, and the
	// write to the pty happens later and can still fail.
	Accept(paneID string, b []byte) bool
}

// Outcome is the closed set of things that can happen to a submission. Three
// values, because "the text landed and the submit key did not" is a real state
// of the world and folding it into either neighbour would lie about it.
type Outcome string

const (
	// OutcomeSubmitted means both segments were accepted: the text is in the
	// pane's input queue and so is the key that submits it.
	OutcomeSubmitted Outcome = "submitted"
	// OutcomeTyped means the text was accepted and no submit key was sent —
	// either because none was asked for, or because the screen stopped being
	// free_text between the two. The person sees the text in the input region
	// and can send or clear it; that is the recoverable half of the only
	// partial state this has.
	OutcomeTyped Outcome = "typed"
	// OutcomeRefused means nothing at all was written.
	OutcomeRefused Outcome = "refused"
)

// Result is what happened, and it has the same shape whether nocx typed or
// refused. A refusal is an ANSWER rather than an absence: a control that
// silently does nothing is indistinguishable from a broken one, and a refusal
// nobody can read is how this degrades into typing blindly.
type Result struct {
	// PaneID is the pane the attempt was about.
	PaneID string
	// Agent is who the enrolment act said is running there. Empty for a pane
	// nocx is not watching, which is the one case where there is nobody to
	// name.
	Agent string
	// Outcome is the closed-set answer.
	Outcome Outcome
	// State is the state that DECIDED. free_text when bytes were written;
	// otherwise the state that refused them. StateUnknown when nothing could
	// read the screen at all — no rule with authority, no grid, no enrolment —
	// which is the same value every other consumer treats as busy.
	State agentdriver.State
	// Reason is why, in the words a person reads. Empty exactly when the whole
	// submission was accepted.
	Reason string
}

// Typist is the one thing in nocx that writes into an agent's pane.
type Typist struct {
	log      log.Logger
	screens  Screens
	rules    Rules
	calib    Authority
	enrolled Enrolment
	input    Input
}

// New wires the typing seam at the composition root.
func New(lg log.Logger, screens Screens, rules Rules, calib Authority, enrolled Enrolment, input Input) *Typist {
	return &Typist{log: lg, screens: screens, rules: rules, calib: calib, enrolled: enrolled, input: input}
}

// Submit puts text into a pane's input and then presses the submit key, as two
// separate writes each gated on its own frame.
func (t *Typist) Submit(paneID, text string) Result { return t.put(paneID, text, true) }

// Type puts text into a pane's input and presses nothing. It is the same
// primitive with the second segment left off: the text appears in the input
// region and answers nothing, which is what a person confirming that a rule
// they just calibrated actually works needs, and what a caller composing a
// submission a human will send needs.
func (t *Typist) Type(paneID, text string) Result { return t.put(paneID, text, false) }

func (t *Typist) put(paneID, text string, submit bool) Result {
	body, err := bodyOf(text)
	if err != nil {
		// Before the grant, because a submission nocx cannot send is not a
		// question about the screen. There is no agent to name yet either.
		return *t.refuse(paneID, "", agentdriver.StateUnknown, err.Error())
	}
	p, refusal := t.grant(paneID)
	if refusal != nil {
		return *refusal
	}
	if r := p.write([]byte(pasteStart + body + pasteEnd)); r != nil {
		return *r
	}
	typed := Result{
		PaneID: p.pane, Agent: p.agent,
		Outcome: OutcomeTyped, State: agentdriver.StateFreeText,
	}
	if !submit {
		t.log.Info("nocx typed into a pane", "pane_id", p.pane, "agent", p.agent, "bytes", len(body))
		return typed
	}
	if r := p.write([]byte(submitKey)); r != nil {
		// The one partial state this has, and it is named rather than folded
		// into either neighbour: the text is in the input region and nothing
		// answered anything. Better than the alternative by exactly the margin
		// this package exists for — the alternative is a submit key landing on
		// a screen that is no longer free text.
		typed.Reason = r.Reason
		typed.State = r.State
		return typed
	}
	t.log.Info("nocx submitted into a pane", "pane_id", p.pane, "agent", p.agent, "bytes", len(body))
	return Result{
		PaneID: p.pane, Agent: p.agent,
		Outcome: OutcomeSubmitted, State: agentdriver.StateFreeText,
	}
}

// permit is the authority to put bytes into ONE pane.
//
// grant is the only thing that builds a non-zero one, and it does so in a
// single statement after BOTH gates have answered. So a permit a later edit
// builds by hand carries no Typist, and write refuses on that — there is no
// boolean to set and no pane to point it at, because the pane is inside the
// permit and the only door to the input queue is `by`.
type permit struct {
	by    *Typist
	pane  string
	agent string
}

// grant asks both gates and mints the permit, or answers why not.
//
// The order is deliberate. Authority first, because classifying with a rule
// that has not earned the right to be believed produces a state nobody should
// act on — so an unverified rule refuses at StateUnknown, which is the honest
// answer (without a verified rule nocx does not know what the pane is) and is
// what every other consumer already treats as busy.
func (t *Typist) grant(paneID string) (permit, *Result) {
	agent, watched := t.enrolled.AgentOn(paneID)
	if !watched {
		return permit{}, t.refuse(paneID, "", agentdriver.StateUnknown,
			"nocx is not watching that pane, so there is no rule to ask about it")
	}
	if v := t.calib.Verify(agent); !v.MayType() {
		reason := v.Reason
		if reason == "" {
			reason = fmt.Sprintf("%s's rule has not earned the right to be typed against", agent)
		}
		return permit{}, t.refuse(paneID, agent, agentdriver.StateUnknown, reason)
	}
	if _, refusal := t.look(paneID, agent); refusal != nil {
		return permit{}, refusal
	}
	// Both answers are in. This is the only statement in this package that
	// builds a permit with a Typist in it.
	return permit{by: t, pane: paneID, agent: agent}, nil
}

// look reads the pane's screen NOW and answers whether nocx may write into it.
//
// One derivation of "is this pane inviting input", read by the grant and by
// every write. A second one would agree everywhere anybody looked and disagree
// on the frame nobody tried, which is the shape this repository has paid for.
func (t *Typist) look(paneID, agent string) (agentdriver.State, *Result) {
	f, err := t.screens.Frame(paneID)
	if err != nil {
		// The ordinary race: the session ended and the grid was withdrawn. A
		// pane with no screen is not a pane with an idle screen.
		return agentdriver.StateUnknown, t.refuse(paneID, agent, agentdriver.StateUnknown,
			"nocx has no live screen for that pane, so it cannot see what typing would answer")
	}
	state := t.rules.Classify(agent, f)
	if state != agentdriver.StateFreeText {
		return state, t.refuse(paneID, agent, state,
			fmt.Sprintf("that pane is %s, and nocx types only into a pane that is waiting for input", said(state)))
	}
	return state, nil
}

// write re-takes the decision from a frame read HERE and writes b only if that
// frame still says free_text. Nothing stands between the read and the queue
// call: b was built before the permit was asked for.
func (p permit) write(b []byte) *Result {
	if p.by == nil {
		// The zero permit. Unreachable while grant is the only constructor,
		// and stated anyway: an invariant held only by a caller's good
		// behaviour is held by nobody.
		return &Result{
			PaneID: p.pane, Outcome: OutcomeRefused, State: agentdriver.StateUnknown,
			Reason: "nothing granted this write, so nothing was written",
		}
	}
	if _, refusal := p.by.look(p.pane, p.agent); refusal != nil {
		return refusal
	}
	if !p.by.input.Accept(p.pane, b) {
		return p.by.refuse(p.pane, p.agent, agentdriver.StateFreeText,
			"that pane is not accepting input at the moment, so nothing was written")
	}
	return nil
}

// refuse records the refusal WITH ITS REASON and returns it. Both, always: the
// log is what a person reads after the fact and the result is what the caller
// shows them now, and a soft degrade visible only in a log is how a feature
// that does not exist survives a release.
func (t *Typist) refuse(paneID, agent string, state agentdriver.State, reason string) *Result {
	t.log.Warn("nocx refused to type into a pane",
		"pane_id", paneID, "agent", agent, "state", string(state), "reason", reason)
	return &Result{PaneID: paneID, Agent: agent, Outcome: OutcomeRefused, State: state, Reason: reason}
}

// said names a state in the words a person reads. The closed set is the
// driver's; this is the only place it is turned into prose, so a state added
// there arrives here as its own name rather than as a second vocabulary that
// disagrees.
func said(s agentdriver.State) string {
	switch s {
	case agentdriver.StatePermissionChoice:
		return "asking you to approve something"
	case agentdriver.StateModalChoice:
		return "showing a menu"
	case agentdriver.StateWorking:
		return "working"
	case agentdriver.StateError:
		return "showing an error of its own"
	case agentdriver.StateExited:
		return "no longer running"
	default:
		return "in a state nocx cannot read"
	}
}

// bodyOf is what may be typed, and it REFUSES rather than repairs.
//
// A control character is not a character: inside a bracketed paste, an escape
// ends the paste early and everything after it is keystrokes. Stripping one
// would send a submission nobody wrote, so the whole thing is refused and the
// caller is told which byte did it. Carriage returns are the exception the
// other way: a text with \r\n line endings is ordinary, and \r is the submit
// key, so the endings are normalised to \n — a line break inside a bracketed
// paste is content.
func bodyOf(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("that text is not valid UTF-8, so nocx cannot type it")
	}
	if len(text) > MaxText {
		return "", fmt.Errorf("that text is %d bytes and nocx types at most %d in one submission",
			len(text), MaxText)
	}
	body := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("there is nothing to type")
	}
	for _, r := range body {
		if r == '\n' || r == '\t' {
			continue
		}
		// C0, DEL, and C1 — the last of which is the one worth naming: a
		// terminal decoding UTF-8 reads U+009B as CSI, so an escape sequence
		// can be spelled in two bytes that are not the byte 0x1b and would
		// pass a check that only looked below 0x20.
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return "", fmt.Errorf(
				"that text carries a control character (%#U), which inside a paste stops being text and starts being keys",
				r)
		}
	}
	return body, nil
}
