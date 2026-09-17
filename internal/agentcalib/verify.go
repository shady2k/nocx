package agentcalib

// TYPING IS REFUSED ON EVIDENCE AGAINST THE RULE, not on the absence of a
// calibration (nocx-9w0q0, superseding nocx-jse6x's "authority is earned by
// the labelled set").
//
// # Why the old position stopped holding
//
// Until nocx-qddv8 (commit a2545b0f), an agent this build could not identify
// fell through to free_text — "you may type" about whatever was on screen,
// answered by a driver that could no longer read it. Refusing to type without
// a calibration was, until then, the only thing standing between that
// fall-through and a keystroke landing in a tool-approval dialog whose first
// option is Yes: a rule nobody had checked was indistinguishable from a rule
// that could no longer read the screen at all. nocx-qddv8 closed the
// fall-through at its source — an unidentified frame now classifies to
// unknown, and free_text is a positive identification earned from the prompt
// anchor rather than a default — so the two cases this file used to conflate
// are now told apart before Verify is ever asked: "never calibrated" and
// "cannot be believed" stopped being the same risk.
//
// So the question this file answers changes from "has this rule earned belief"
// to "is there evidence this rule should not be believed". Absence of evidence
// is not evidence against: an agent nobody has sat down to calibrate — every
// fresh install, and a rule shipped in this build that has not yet been
// checked — is typed into exactly as any other rule is, because nothing has
// told nocx its rule is wrong for this agent. Calibrating remains how a person
// narrows that check to the states they actually produced; skipping it does
// not turn typing off.
//
// # What still refuses, and why those two causes are not like the others
//
// A DISAGREEMENT — the person calibrated, and the rule answered a labelled
// frame with something other than the state it was produced for — is real
// evidence the rule is wrong for this agent, and it refuses. This is the
// remedy working exactly as designed: a person who calibrates and finds a
// disagreement has found a genuine defect, not failed to satisfy a
// precondition.
//
// NO RULE IN THIS BUILD for the named agent refuses for a different reason:
// nothing can read that pane's screen at all, so there is no positive
// identification to type against — the frame gate in internal/agenttyping
// requires one (free_text, positively classified), and nothing here can
// manufacture it in a rule's absence.
//
// Everything else that keeps a replay from completing — the labelled set does
// not exist yet, is missing a required label, could not be read, or names a
// label this build does not ask for — is a fact about the SET, not about the
// rule, and each of those PERMITS, carrying the reason so a surface can still
// say what was not checked. A person reading "may type" for an uncalibrated
// agent must be told exactly that: not verified, not contradicted either.
//
// # It is a property of the RULE, not of who wrote it
//
// Nothing here knows whether a rule shipped in the binary or was written by
// the person at the keyboard. The claude rule is checked the same way and can
// be disproven the same way, which is exactly what should happen when an
// agent's update changes its chrome: a calibrated set stops classifying, the
// verdict flips to refuse, and nocx goes back to lighting a dot rather than
// typing on a rule just shown to be wrong.
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
	"context"
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
	// Reason explains the verdict, in the words the person reads. Empty ONLY
	// when the rule was replayed against a complete labelled set with no
	// disagreement — truly verified, nothing left to say. It is non-empty for
	// every refusal, and it is ALSO non-empty for a verdict that permits
	// without having verified: nothing here is evidence against the rule, but
	// a person reading "may type" still needs to know it was not checked.
	Reason string

	// mayType is unexported ON PURPOSE; see the file comment. It is written
	// in one statement in Verify and nowhere else, so the zero Verdict — and
	// every Verdict a caller builds itself — denies typing.
	mayType bool
}

// MayType reports whether nocx may currently type into a pane running this
// agent's rule. True unless something here amounts to evidence the rule
// should not be believed: a labelled disagreement, or no rule in this build
// at all. Everything else — never calibrated, an incomplete, unreadable or
// unreplayable set, a label this build does not map — is a fact about the
// evidence rather than about the rule, and permits. Reason, not this boolean,
// is what says whether the permit came from verification or from the absence
// of anything contradicting it; a surface must read both.
func (v Verdict) MayType() bool { return v.mayType }

// Verify replays the agent's labelled set against its rule, so far as one
// exists and can be replayed, and answers whether anything found there
// contradicts the rule.
//
// It returns no error, and that is deliberate: a caller that had to
// distinguish an unreadable capture from a missing one could get the
// distinction wrong in the direction that refuses typing nocx has no reason
// to refuse. The reason travels inside the verdict instead, where the surface
// that shows the consequence also shows the cause — for a refusal and for a
// permit alike.
//
// Nothing is cached, and the measurement is why: a six-label set at 120x40
// verifies in 2.3ms, replay and all, so the settings page's half-second poll
// spends half a percent of one core on it. A cache would have to be
// invalidated by a set changing under it, and a stale verdict is the one
// answer this file exists to prevent.
//
// mayType is written in exactly one statement below, guarded by refused —
// every path that finds evidence against the rule returns early with refused
// true and mayType left at its zero value; every other path only records why,
// and falls through to it. See the file comment for what belongs on which
// side.
func (c *Calibrations) Verify(ctx context.Context, agent string) Verdict {
	v := Verdict{Agent: agent}
	if err := validAgent(agent); err != nil {
		v.Reason = err.Error()
		return v
	}
	refused := c.evaluate(ctx, agent, &v)
	if !refused {
		v.mayType = true
	}
	return v
}

// evaluate fills in v's Labelled, Agreed, Disagreements and Reason, and
// reports whether what it found is evidence against the rule. Only a name
// that already passed validAgent reaches this.
func (c *Calibrations) evaluate(ctx context.Context, agent string, v *Verdict) (refused bool) {
	set, found, err := c.store.Load(agent)
	switch {
	case err != nil:
		// A fact about the file, not about the rule.
		v.Reason = fmt.Sprintf("the labelled set could not be read: %v", err)
		return false
	case !found:
		// Absence of evidence is not evidence against.
		v.Reason = fmt.Sprintf(
			"%s has never been calibrated, so there is nothing to check its rule against", agent)
		return false
	case !set.Complete():
		v.Reason = fmt.Sprintf(
			"%s's labelled set is missing a state a rule must classify, so it cannot verify one", agent)
		return false
	}
	frames, err := set.Frames(ctx, c.replay)
	if err != nil {
		v.Reason = fmt.Sprintf("%s's labelled set could not be replayed: %v", agent, err)
		return false
	}
	v.Labelled = len(frames)
	// AFTER the replay, so a surface can still say how many labelled states
	// an agent has even when nothing in this build can read its screen — that
	// is the state a person is in while a rule is being written for a new
	// agent, and "6 labelled states and no rule yet" is the useful sentence.
	if _, has := c.rules.For(agent); !has {
		// No positive identification is possible at all: refuse.
		v.Reason = fmt.Sprintf(
			"nothing in this build knows how to read %s's screen, so there is no rule to verify", agent)
		return true
	}
	for _, lf := range frames {
		want, mapped := Expect(lf.Label)
		if !mapped {
			// A fact about the SET — it names a label this build does not
			// ask for — so it permits; see the file comment. Refused whole
			// rather than skipped: skipping would verify against fewer
			// states than the set claims to hold.
			v.Agreed, v.Disagreements = 0, nil
			v.Reason = fmt.Sprintf(
				"%s's labelled set names %q, which this build does not ask for, "+
					"so there is no state that frame could be checked against", agent, lf.Label)
			return false
		}
		got := c.rules.Classify(agent, lf.Frame)
		if got == want {
			v.Agreed++
			continue
		}
		v.Disagreements = append(v.Disagreements, Disagreement{Label: lf.Label, Expected: want, Got: got})
	}
	if len(v.Disagreements) > 0 {
		// Real evidence the rule is wrong for this agent: refuse.
		v.Reason = fmt.Sprintf(
			"%s's rule answered %d of the %d labelled states with something other than the state "+
				"they were produced for", agent, len(v.Disagreements), v.Labelled)
		return true
	}
	if v.Labelled == 0 {
		// Unreachable while Complete requires three labels with frames behind
		// them, and stated anyway: a rule checked against nothing has learned
		// nothing either way, and an invariant held only by a caller's good
		// behaviour is held by nobody.
		v.Reason = fmt.Sprintf("%s's labelled set holds no frame to check a rule against", agent)
		return false
	}
	return false
}
