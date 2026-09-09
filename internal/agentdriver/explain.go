package agentdriver

// What the rule SEES on a frame, for the person who has to write or repair it.
//
// # Why this exists at all
//
// The configuration design (2026-08-27, §5) is explicit that the emitting view
// is not optional: "a rule the user must write blind is a dead rule." Half of
// that view is the screen, which panegrid already answers. This file is the
// other half, and it is the half that is easy to forget — a view that shows
// only the screen is a screen, and the person already has one of those.
//
// # It is a RECORDING of the walk, never a second walk
//
// Everything here comes out of the same decide() the product acts on, with a
// recorder threaded through it that is nil on every production path. That is
// deliberate and it is the whole design: a separate explainer would agree with
// the classifier on every frame anybody tried and disagree on the one nobody
// did, which is the shape AGENTS.md names — "two evaluations of one question is
// how the two come to disagree".
//
// Recording cannot move an answer, and the reason is structural rather than
// careful: every branch is decided before the recorder is told about it, the
// recorder returns only what the predicate walk already returned, and nothing
// read back out of it reaches decide().
//
// # It decides nothing, which is why it is not a third power
//
// The AD-6 amendment grants an enrolled pane's grid exactly two powers —
// whether nocx may write into the pane, and what its indicator shows — and
// says the list is exhaustive. Nothing in this file decides either, and
// nothing decides anything else: an Explanation is a read-out handed to the
// person operating the pane, of a screen they own and are already looking at.
// It creates no enrolment, changes no state, and reaches no network
// destination. The power it would need to be a third one is a power to DECIDE,
// and it has none.

import (
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/panegrid"
)

// Explanation is one rule's whole reading of one frame.
//
// It carries the verdict twice over — as State, and as the branch that
// produced it — because those are two different questions and a person
// repairing a rule needs both. "unknown" alone sends them nowhere; "unknown,
// from branch 2, whose second predicate did not hold" sends them to a line.
type Explanation struct {
	// Agent is the agent the frame was read as, verbatim from the enrolment.
	Agent string
	// HasRule distinguishes the two ways an answer can be unknown: this
	// agent's rule could not read the screen, or nocx has no rule for this
	// agent at all. Only the second is permanent, and a view that folded them
	// together would tell a person to repair a rule that does not exist.
	HasRule bool
	// State is exactly what Classify returns for the same frame.
	State State
	// Default is the answer the document falls to when no branch matched.
	Default State
	// Matched indexes Branches, and is -1 when the default answered.
	Matched int
	// Rows is one reading per row of the frame, in row order. It is a
	// reading of the FRAME rather than of the rule, so it is present even
	// when there is no rule: the screen is legible either way, and it is
	// what a person is comparing the rule against.
	Rows []RowReading
	// Anchors are in document order, so a reader sees a derived anchor
	// beneath the one it is computed from.
	Anchors []AnchorReading
	// Branches are in document order, which is the order they were
	// evaluated in — and that order is a safety property of the document,
	// not a presentation choice.
	Branches []BranchReading
	// Extractors is the value-reading half of the rule: for each one, the
	// rows it was PERMITTED to read and the rows it actually read. Both
	// halves, because the bound is the engine's and an extractor that read
	// nothing is a different repair depending on which of the two was wrong.
	Extractors []ExtractorReading
}

// ExtractorReading is one extractor's part in the reading.
type ExtractorReading struct {
	Name   string
	Anchor string
	// Region is the row span the engine allowed, nil when the anchor did
	// not bind — in which case the extractor never ran at all.
	Region *RowSpan
	// Rows is the yield, exactly as Observe reports it in Extra.Rows: one
	// map per matched row, and a capture group that did not participate
	// contributes no key.
	Rows []map[string]string
}

// RowReading is what the frame arithmetic sees on one row, before any rule
// looks at it: whether it is a full-width rule, and where it opens.
//
// Those two are not decoration. The input box is FOUND by its two full-width
// rules, and "opens with" is what separates a chrome marker from the same
// glyph wrapped into a transcript line — so they are the two facts a person
// comparing a screen against a rule reads off the screen first.
type RowReading struct {
	// RuleGlyph is the repeated cell when the row is nothing but one cell
	// edge to edge, and empty otherwise.
	RuleGlyph string
	// OpensAt is the first non-blank column, and -1 for a blank row. Absent
	// rather than zero, because column zero is a real column and a blank row
	// drawn as opening there looks like chrome.
	OpensAt int
}

// AnchorReading is where one anchor bound, or that it did not.
type AnchorReading struct {
	Name string
	Kind string
	// From is the anchor this one was computed from, empty for one computed
	// from the frame's own bottom edge.
	From string
	// Bound is false when the chrome this anchor names was not on the
	// screen. Row is meaningful only when it is true.
	Bound bool
	Row   int
}

// BranchReading is one branch's part in the walk.
type BranchReading struct {
	// State is what this branch answered — for a Below branch, the value its
	// verdict produced, and empty when it produced none.
	State State
	// Reached is false for every branch after the one that matched. Order is
	// a safety property, so a person must be able to see that a later branch
	// was never asked rather than "fix" one that could not have run.
	Reached bool
	Matched bool
	// Predicates is the conjunction, in document order. Empty for a Below
	// branch, which carries Below instead.
	Predicates []PredicateReading
	Below      *BelowReading
}

// PredicateReading is one predicate's answer, and where the branch stopped.
type PredicateReading struct {
	Kind string
	// Anchor is the anchor the predicate names, empty for one that reads the
	// cursor.
	Anchor string
	// Detail renders the predicate's own arguments, so a person reads what
	// was looked for and not only whether it was found.
	Detail string
	// Evaluated is false for every predicate after the one that failed. A
	// conjunction short-circuits, and reporting the rest as "false" would
	// send a person to the wrong line.
	Evaluated bool
	Held      bool
	// Region is the row span the predicate was permitted to search, when it
	// searches one and its anchor bound. Inclusive at both ends.
	Region *RowSpan
}

// BelowReading is the three-valued branch's answer, kept three-valued: folding
// "nothing is drawn down there" into "something unrecognised is" is exactly
// the collapse that turns a refusal into free_text.
type BelowReading struct {
	Anchor string
	Glyphs []string
	// AnchorBound is false when the anchor was absent, in which case the
	// verdict below decided nothing at all.
	AnchorBound bool
	// Verdict is "nothing", "allMatched" or "counterexample".
	Verdict string
}

// RowSpan is an inclusive row range of the frame.
type RowSpan struct{ From, To int }

// Explainer is a driver whose rule can report its own reading. It is a
// separate interface for the same reason Observer is: nothing is required to
// implement it, and a caller that only wants the verdict never looks for it.
type Explainer interface {
	Driver
	Explain(f panegrid.Frame) Explanation
}

// Explain is the failing-open form for a READ-OUT, which is the opposite
// disposition from Observe's and is right for the same reason Observe's is
// wrong here: refusing to say anything about an agent with no rule leaves the
// person who has to WRITE that rule looking at nothing. The frame is reported
// either way and HasRule says which case they are in.
func (r *Registry) Explain(agent string, f panegrid.Frame) Explanation {
	d, ok := r.For(agent)
	if !ok {
		return Explanation{Agent: agent, State: StateUnknown, Default: StateUnknown, Matched: -1, Rows: readRows(f)}
	}
	e, ok := d.(Explainer)
	if !ok {
		// A driver that is not an Explainer answers the verdict it always
		// did, with no reading beside it. Not an error and not a lie: there
		// is a rule, and this driver cannot show its working.
		return Explanation{
			Agent: agent, HasRule: true, State: d.Classify(f),
			Default: StateUnknown, Matched: -1, Rows: readRows(f),
		}
	}
	ex := e.Explain(f)
	ex.Agent = agent
	return ex
}

// Explain evaluates the document and records the walk.
func (d documentDriver) Explain(f panegrid.Frame) Explanation {
	ex := Explanation{
		Agent: d.doc.Agent, HasRule: true,
		Default: d.doc.Default, Matched: -1, Rows: readRows(f),
	}
	// The degenerate frame is the engine's answer, exactly as it is in
	// Observe, and it is reported as such: no anchors bound, no branch ran.
	if f.Rows <= 0 || f.Cols <= 0 || len(f.Lines) == 0 {
		ex.State = StateUnknown
		return ex
	}
	anchors := d.bindAnchors(f)
	ex.Anchors = d.readAnchors(anchors)
	tr := &trace{frame: f, anchors: anchors, branches: make([]BranchReading, len(d.doc.Branches)), matched: -1}
	ex.State = d.decide(f, anchors, tr)
	ex.Branches = tr.branches
	ex.Matched = tr.matched
	ex.Extractors = d.readExtractors(f, anchors)
	return ex
}

func (d documentDriver) readAnchors(anchors bound) []AnchorReading {
	out := make([]AnchorReading, 0, len(d.doc.Anchors))
	for _, a := range d.doc.Anchors {
		row, ok := anchors[a.Name]
		out = append(out, AnchorReading{Name: a.Name, Kind: a.Kind, From: a.From, Bound: ok, Row: row})
	}
	return out
}

// readRows is the frame arithmetic reported for its own sake. A row is a
// full-width rule under exactly the predicate fullWidthRule applies — every
// column carrying the same cell — read here without a glyph to look for,
// because the point of the view is to show what the row IS rather than to
// confirm what somebody guessed.
func readRows(f panegrid.Frame) []RowReading {
	if f.Rows <= 0 {
		return nil
	}
	out := make([]RowReading, f.Rows)
	for y := 0; y < f.Rows; y++ {
		r := RowReading{OpensAt: -1}
		if col, ok := firstNonBlankCol(f, y); ok {
			r.OpensAt = col
			if y < len(f.Lines) && len(f.Lines[y]) >= f.Cols {
				if glyph := f.Lines[y][0].Text; strings.TrimSpace(glyph) != "" && fullWidthRule(f, y, glyph) {
					r.RuleGlyph = glyph
				}
			}
		}
		out[y] = r
	}
	return out
}

// trace is the recorder threaded through decide. Every method is nil-safe,
// because nil is what the product passes and the walk must be identical.
type trace struct {
	frame    panegrid.Frame
	anchors  bound
	branches []BranchReading
	matched  int
}

// conjunction evaluates one branch's predicates and records each answer. It
// returns exactly what allHold returns, and on a nil recorder it IS allHold —
// so the product's walk has no recorder-shaped branch in it.
func (t *trace) conjunction(i int, b Branch, f panegrid.Frame, anchors bound) bool {
	if t == nil {
		return allHold(f, anchors, b.When)
	}
	rec := BranchReading{State: b.State, Reached: true, Predicates: make([]PredicateReading, len(b.When))}
	held := true
	for j, p := range b.When {
		rec.Predicates[j] = PredicateReading{
			Kind: p.Kind, Anchor: p.Anchor, Detail: predDetail(p), Region: predRegion(f, anchors, p),
		}
		if !held {
			// Short-circuited: this predicate was never asked, and saying
			// it did not hold would point at the wrong line.
			continue
		}
		rec.Predicates[j].Evaluated = true
		rec.Predicates[j].Held = holds(f, anchors, p)
		held = rec.Predicates[j].Held
	}
	rec.Matched = held
	t.branches[i] = rec
	if held {
		t.matched = i
	}
	return held
}

// below records the three-valued branch. The verdict and the boundness are
// recorded separately because they are separate facts: a verdict computed
// against an absent anchor decided nothing, and a view that showed only the
// verdict would look like it had.
func (t *trace) below(i int, b Branch, anchorBound bool, verdict belowVerdict, produced State) {
	if t == nil {
		return
	}
	rec := BranchReading{
		State: produced, Reached: true, Matched: produced != "",
		Below: &BelowReading{
			Anchor: b.Below.Anchor, Glyphs: b.Below.Glyphs,
			AnchorBound: anchorBound, Verdict: verdictName(verdict),
		},
	}
	t.branches[i] = rec
	if rec.Matched {
		t.matched = i
	}
}

// fellThrough is called when no branch matched. Nothing to record: every
// branch was reached, and the zero BranchReading for each already says so
// once conjunction and below have filled them in.
func (t *trace) fellThrough() {}

func verdictName(v belowVerdict) string {
	switch v {
	case belowAllMatched:
		return "allMatched"
	case belowCounterexample:
		return "counterexample"
	default:
		return "nothing"
	}
}

// predDetail renders a predicate's arguments for a person. Only the fields the
// kind actually reads, because a view that printed every field of Pred would
// show a document arguments its predicate ignores and invite a repair to one
// of them.
func predDetail(p Pred) string {
	var parts []string
	switch p.Kind {
	case "cursorOn", "rowOpensWith":
		parts = append(parts, fmt.Sprintf("glyph=%q", p.Glyph))
	case "rowContains", "nearestNonBlankAboveCursorContains":
		parts = append(parts, fmt.Sprintf("text=%q", p.Text))
	case "regionAny":
		if p.Text != "" {
			parts = append(parts, fmt.Sprintf("text=%q", p.Text))
		}
		if p.Suffix != "" {
			parts = append(parts, fmt.Sprintf("suffix=%q", p.Suffix))
		}
		parts = append(parts, regionDetail(p.RegionSpec))
	}
	return strings.Join(parts, " ")
}

func regionDetail(r RegionSpec) string {
	parts := []string{"up=false"}
	if r.Up {
		parts[0] = "up=true"
	}
	parts = append(parts, fmt.Sprintf("maxRows=%d", r.MaxRows))
	if r.Col0Only {
		parts = append(parts, "col0Only")
	}
	if r.StopAtBlank {
		parts = append(parts, "stopAtBlank")
	}
	return strings.Join(parts, " ")
}

// predRegion is the row span a region-taking predicate was allowed to search,
// clamped to the frame. Nil for a predicate that searches no region and for
// one whose anchor did not bind — in both cases there is no span, and drawing
// one anyway would put a highlight on rows nothing looked at.
func predRegion(f panegrid.Frame, anchors bound, p Pred) *RowSpan {
	if p.Kind != "regionAny" || p.MaxRows <= 0 {
		return nil
	}
	row, ok := anchors[p.Anchor]
	if !ok {
		return nil
	}
	return spanOf(f, p.RegionSpec, row)
}

// readExtractors reports each extractor twice over: the span it was permitted
// to read, and what it read there.
//
// The yield comes from extract(), the same call Observe makes, so the view
// cannot show a person a reading the product did not take — the identity that
// makes the branch walk trustworthy applied to the other half of the grammar.
func (d documentDriver) readExtractors(f panegrid.Frame, anchors bound) []ExtractorReading {
	if len(d.extractors) == 0 {
		return nil
	}
	yield := make(map[string][]map[string]string, len(d.extractors))
	for _, e := range d.extract(f, anchors) {
		yield[e.Name] = e.Rows
	}
	out := make([]ExtractorReading, 0, len(d.extractors))
	for _, e := range d.extractors {
		r := ExtractorReading{Name: e.spec.Name, Anchor: e.spec.Anchor, Rows: yield[e.spec.Name]}
		if row, ok := anchors[e.spec.Anchor]; ok {
			r.Region = spanOf(f, e.spec.RegionSpec, row)
		}
		out = append(out, r)
	}
	return out
}

// spanOf clamps a region to the frame. The region walks maxRows rows away
// from its anchor, starting one row off it, and the frame's own edge is the
// other bound — the same two bounds region.eachRow enforces, read rather than
// re-decided.
func spanOf(f panegrid.Frame, r RegionSpec, anchor int) *RowSpan {
	first, last := anchor+1, anchor+r.MaxRows
	if r.Up {
		first, last = anchor-r.MaxRows, anchor-1
	}
	if first < 0 {
		first = 0
	}
	if last > f.Rows-1 {
		last = f.Rows - 1
	}
	if first > last {
		return nil
	}
	return &RowSpan{From: first, To: last}
}
