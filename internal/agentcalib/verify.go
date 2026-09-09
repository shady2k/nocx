package agentcalib

// TYPING AUTHORITY IS EARNED, and the currency is the labelled set
// (nocx-jse6x).
//
// # What this exists to prevent
//
// A mistimed keystroke does not merely fail to arrive. It ANSWERS whatever
// modal is on screen, and the modal a coding agent puts up is a tool approval
// whose first option is Yes — so the failure is not a lost keystroke, it is a
// tool call the person never saw being approved on their behalf. An earlier
// design proposed sending Escape first so a mistimed keystroke would be
// recoverable; it is not, for the same reason.
//
// So a rule may light an INDICATOR on nothing but its author's confidence — a
// wrong dot costs nothing and is corrected by looking — and it may GATE TYPING
// only once it has been replayed against every frame a person produced and
// labelled, and has answered each one with the state they were asked for.
//
// # It is a property of the RULE, not of who wrote it
//
// Nothing here knows whether a rule shipped in the binary or was written by
// the person at the keyboard. The claude rule earns its authority the same way
// and loses it the same way, which is exactly what should happen when an
// agent's update changes its chrome: the labelled set stops classifying, the
// verdict flips, and nocx goes back to lighting a dot.
//
// # The two vocabularies meet in ONE place
//
// A calibration is asked for in the words a person uses about their agent —
// idle, working, asks-you — and a driver answers in the closed set free_text,
// working, permission_choice, modal_choice, error, unknown, exited. Those are
// genuinely different vocabularies, and the correspondence between them is
// Step.Expect and nothing else: it is declared beside the question the person
// is asked, Expect below is the only reader of it, and this file never writes
// a second one. A mapping scattered across call sites is the defect this
// repository has paid for repeatedly — two derivations of one concept that
// agree everywhere anybody looked.
//
// A label the mapping cannot answer is a REFUSAL rather than a skip. Sets
// arrive from files a person can edit, and skipping an unmappable label would
// verify a rule against fewer states than the set claims to hold while
// reporting the total it claims.
//
// # Why the permission is a value only this file can make
//
// Verdict.mayType is unexported and is written in exactly one statement, at
// the end of verify. So every other way a caller can come by a Verdict — a
// struct literal, a zero value, a field it forgot to fill, a map lookup that
// missed — denies typing without anybody having remembered to check. The gate
// is structural in that specific sense: nocx-dkawo.1 cannot grant itself the
// authority it is supposed to ask for, because there is no exported way to
// write true into the field that carries it.
//
// What that does NOT do is make the ask compulsory, and it is worth saying so
// rather than letting the next reader assume it. A typing seam that never
// calls Verify at all is prevented by nothing here; making it impossible would
// mean this package handing out a capability the typing method takes as an
// argument, and that method does not exist yet — its signature belongs to the
// bead that writes it. What is settled here is that the answer it gets cannot
// be manufactured, and that every failure answers no.

import (
	"fmt"

	"github.com/shady2k/nocx/internal/agentdriver"
)

// Expect maps a calibration LABEL onto the driver State a frame carrying it
// must classify to. It reads the step list, which is where the correspondence
// is declared beside the question that produces the label; there is no second
// table.
//
// False for a label this build does not ask for — a set written by an older
// build, or edited by hand.
func Expect(l Label) (agentdriver.State, bool) {
	for _, s := range steps {
		if s.Label == l {
			return s.Expect, true
		}
	}
	return "", false
}

// Disagreement is one labelled frame the rule answered with something other
// than the state the person was asked to produce.
//
// It carries both sides because the point of showing it is repair: the person
// looks at the screen they made for that label and at what the rule said about
// it, and one of the two is wrong.
type Disagreement struct {
	// Label is the state the person was asked for.
	Label Label
	// Expected is the driver state that label must classify to.
	Expected agentdriver.State
	// Got is what the rule actually answered for that frame.
	Got agentdriver.State
}

// Verdict is the whole answer to "may this agent's rule be typed against", and
// the counts a surface needs to state the consequence rather than merely the
// outcome.
type Verdict struct {
	// Agent is who the verdict is about.
	Agent string
	// Labelled is how many labelled frames the set holds. A declined state
	// contributes none, because nothing was captured for it.
	Labelled int
	// Agreed is how many of those the rule answered with their label's state.
	Agreed int
	// Disagreements is one entry per labelled frame that classified to
	// something else, in the order the person was asked for them.
	Disagreements []Disagreement
	// Reason says why an unverified verdict is unverified, in the words the
	// person reads. Empty exactly when the rule verified.
	Reason string

	// mayType is unexported ON PURPOSE; see the file comment. It is written
	// in one statement in verify and nowhere else, so the zero Verdict — and
	// every Verdict a caller builds itself — denies typing.
	mayType bool
}

// MayType reports whether this rule has earned the right to be typed against.
// False is the answer for everything that is not a rule replayed against a
// complete labelled set with no disagreement in it, including every error.
func (v Verdict) MayType() bool { return v.mayType }

// Verify replays the agent's labelled set against its rule and answers what
// that rule may now do.
//
// It returns no error, and that is deliberate: every failure here has the same
// consequence — no typing — and a caller that had to distinguish an unreadable
// capture from a missing one could get the distinction wrong in the direction
// that types. The reason travels inside the verdict instead, where the surface
// that shows the consequence also shows the cause.
//
// Nothing is cached, and the measurement is why: a six-label set at 120x40
// verifies in 2.3ms, replay and all, so the settings page's half-second poll
// spends half a percent of one core on it. A cache would have to be
// invalidated by a set changing under it, and a stale "may type" is the one
// answer this file exists to prevent.
func (c *Calibrations) Verify(agent string) Verdict {
	v := Verdict{Agent: agent}
	if err := validAgent(agent); err != nil {
		v.Reason = err.Error()
		return v
	}
	set, found, err := c.store.Load(agent)
	switch {
	case err != nil:
		v.Reason = fmt.Sprintf("the labelled set could not be read: %v", err)
		return v
	case !found:
		v.Reason = fmt.Sprintf(
			"%s has never been calibrated, so there is nothing to check its rule against", agent)
		return v
	case !set.Complete():
		v.Reason = fmt.Sprintf(
			"%s's labelled set is missing a state a rule must classify, so it cannot verify one", agent)
		return v
	}
	frames, err := set.Frames(c.log)
	if err != nil {
		v.Reason = fmt.Sprintf("%s's labelled set could not be replayed: %v", agent, err)
		return v
	}
	v.Labelled = len(frames)
	// AFTER the replay, so a surface can still say how many labelled states
	// an agent has even when nothing in this build can read its screen — that
	// is the state a person is in while a rule is being written for a new
	// agent, and "6 labelled states and no rule yet" is the useful sentence.
	if _, has := c.rules.For(agent); !has {
		v.Reason = fmt.Sprintf(
			"nothing in this build knows how to read %s's screen, so there is no rule to verify", agent)
		return v
	}
	for _, lf := range frames {
		want, mapped := Expect(lf.Label)
		if !mapped {
			// Refused whole rather than skipped: see the file comment.
			v.Agreed, v.Disagreements = 0, nil
			v.Reason = fmt.Sprintf(
				"%s's labelled set names %q, which this build does not ask for, "+
					"so there is no state that frame could be checked against", agent, lf.Label)
			return v
		}
		got := c.rules.Classify(agent, lf.Frame)
		if got == want {
			v.Agreed++
			continue
		}
		v.Disagreements = append(v.Disagreements, Disagreement{Label: lf.Label, Expected: want, Got: got})
	}
	if len(v.Disagreements) > 0 {
		v.Reason = fmt.Sprintf(
			"%s's rule answered %d of the %d labelled states with something other than the state "+
				"they were produced for", agent, len(v.Disagreements), v.Labelled)
		return v
	}
	if v.Labelled == 0 {
		// Unreachable while Complete requires three labels with frames behind
		// them, and stated anyway: a rule verified against nothing has
		// verified nothing, and an invariant held only by a caller's good
		// behaviour is held by nobody.
		v.Reason = fmt.Sprintf("%s's labelled set holds no frame to check a rule against", agent)
		return v
	}
	v.mayType = true
	return v
}
