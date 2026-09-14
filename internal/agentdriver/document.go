package agentdriver

// A rule as a document: anchors bound from the frame's own chrome, then an
// ordered list of branches over the closed predicate set of predicate.go.
//
// # Why a document
//
// The classifier used to be Go, so the only way to fix an agent whose TUI
// changed was to ship a new nocx. Splitting it puts the PREDICATES in Go,
// where their bounds are enforced, and the COMPOSITION in a document, where a
// person can repair it. A person composes; a person cannot invent a predicate
// and cannot lift a bound one enforces.
//
// # Order is a safety property
//
// Branches are evaluated in document order and the first match wins. The
// dialog branches come before the free-text branch so a dialog can never be
// masked by an input box drawn beneath it. An evaluator that reordered for any
// reason — efficiency, tidiness, a map — would be this file's defect.
//
// # No memory
//
// Evaluation reads the frame it is given and nothing else, because Driver's
// contract says a rule that remembers is a rule that can be stuck.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/shady2k/nocx/internal/paneview"
)

// maxExtractorRows is the ENGINE's ceiling on how many rows a document may ask
// an extractor to read. It is the same bound region.maxRows already carries for
// the predicates, moved one level up: a document chooses a cap, and may not
// choose one large enough for the region to leave the chrome it is anchored in
// and reach the transcript. Sixteen is the claude panel's four with room for a
// screenful of children, and it is still far short of the distance from the
// mode line back up past the input box.
const maxExtractorRows = 16

// Document is one agent's rule.
type Document struct {
	Agent string `json:"agent"`
	// Anchors bind in order; a later one may refer to an earlier one.
	Anchors []AnchorSpec `json:"anchors"`
	// Branches are evaluated in order. First match wins.
	Branches []Branch `json:"branches"`
	// Extractors read VALUES off the same frame, beside the branches and
	// never inside them. Their yield cannot reach a branch, which is what
	// makes reading more off a screen unable to change what it is called.
	Extractors []Extractor `json:"extractors,omitempty"`
	// Default is the answer when no branch matched. It is a field rather
	// than a constant because a rule the engine does not understand should
	// be able to end in unknown, and free_text is the expensive direction.
	Default State `json:"default"`
	// InputBox is the rows of the agent's own input box, cursor included —
	// the region between its two rule rows (RegionSpec.To). Absent (or its
	// anchor unbound on a given frame) means the box's chrome could not be
	// found, which is the ordinary case while a dialog has replaced it.
	InputBox *RegionSpec `json:"inputBox,omitempty"`
	// MenuZone is the rows in which this agent can draw a menu — every row a
	// LATER menu moment might paint on, read at a free_text or working
	// moment that has no menu on screen yet. That is what makes a fixed
	// budget above the anchor wrong rather than merely tight (nocx-6q1uh.17):
	// the panel a dialog opens with is not a fixed distance from the anchor
	// it is measured from at the moment BEFORE it exists. Replayed off
	// claude-2.1.266-permission, a working moment at 46s has its cursor
	// parked at row 37 of a 40-row frame; the bash-permission dialog that
	// appears 3s later draws its panel header at row 13 and its question at
	// row 21, and the write-permission dialog, 72s after that SAME working
	// moment, draws its own panel from row 20 and its question at row 25 —
	// two different tops from one working moment's cursor, both far above
	// any budget worth calling a cap. So the zone
	// reads up from its anchor with RegionSpec.ToEdge instead of MaxRows: no
	// bound but the frame's own top, matching the honest answer design §6.3
	// allows when no anchor bounds it tighter. It still closes at the
	// frame's own last row (see menuZoneSpan), never at the anchor's own
	// count of rows in the other direction.
	MenuZone *RegionSpec `json:"menuZone,omitempty"`
	// MenuDisplacesInputBox is a declared, measured fact about this agent
	// (nocx-6q1uh.10): whenever a permission/modal menu is on screen, does
	// InputBox always come back empty (Last < First) relative to the
	// nearest PRECEDING free_text/working moment in the same capture — i.e.
	// does a menu always DISPLACE the input box, never merely coexist with
	// it at the same rows.
	//
	// Measured on every corpus pair replayed off testdata/captures that has
	// a real preceding free_text/working baseline in the same capture
	// (claude-2.1.266-permission, claude-lmstudio-permission, claude-modal,
	// claude-permission, claude-permission-60, and both model-menu
	// captures): InputBox went from bound (e.g. rows 35-38) to fully
	// unbound ({0,-1}) at every single menu moment, with zero
	// counterexamples — never merely shifted rows while staying bound. The
	// three onboarding dialogs with NO preceding baseline in their capture
	// (theme-picker, folder-trust, claude-trust) do not contradict this:
	// the box had never been drawn yet, so there was nothing to displace.
	// This is consistent with this file's own INPUT BOX FLOATS note above:
	// the box's top rule never binds inside a dialog, which is exactly why
	// it goes fully unbound rather than merely relocating.
	//
	// True here means callers MAY bind a state-changing step's precondition
	// to InputBox + cursor alone (design §8.2's paste and Enter, and the
	// step-4 "or working" submission check) instead of the wider MenuZone:
	// a menu appearing is caught because InputBox itself goes empty, so a
	// target minted from it stops matching the moment a menu is drawn.
	// Registry.MenuDisplacesInputBox answers false (fail closed, fall back
	// to MenuZone) for an agent that has no driver or never sets this.
	MenuDisplacesInputBox bool `json:"menuDisplacesInputBox,omitempty"`
}

// AnchorSpec binds a name to a row of the frame. A binding that fails is not
// an error: the anchor is ABSENT, and a predicate may ask about that.
type AnchorSpec struct {
	Name string `json:"name"`
	// Kind is "searchUp", "offset", "firstNonBlankBelow" or "cursor".
	//
	// "cursor" binds at Frame.CursorY unconditionally — NOT only when
	// CursorVisible, unlike the plan this package started from: every real
	// capture in testdata/captures reads CursorVisible=false at every menu
	// AND every free-text moment (Claude Code parks DECTCEM off and draws its
	// own indicator), and every existing predicate that reads the cursor
	// (cursorOn, cursorOpensItsRow, numberedOptionAfterCursor and the rest of
	// predicate.go) already treats CursorX/CursorY as trustworthy regardless
	// of visibility. Gating this anchor on CursorVisible would make it never
	// bind for the one agent this package drives.
	//
	// validate refuses a "cursor" anchor named by a Pred — the deliberate
	// decision at holds's belowCursorContains case stands, generalised from a
	// predicate to an anchor: only an Extractor or a document-level region
	// (InputBox, MenuZone) may name it.
	Kind string `json:"kind"`
	// From names the anchor this one is computed from. Empty means the
	// frame's own bottom edge, which only searchUp uses.
	From string `json:"from,omitempty"`
	// FromOffset shifts the starting row before the search begins.
	FromOffset int `json:"fromOffset,omitempty"`
	// Floor is the lowest row a searchUp may reach, inclusive.
	Floor int `json:"floor,omitempty"`
	// Offset is the delta for kind "offset".
	Offset int `json:"offset,omitempty"`
	// RuleGlyph, when set, makes searchUp look for a full-width rule of it.
	RuleGlyph string `json:"ruleGlyph,omitempty"`
	// MinRow rejects a binding whose row is below it. Zero means no floor.
	MinRow int `json:"minRow,omitempty"`
	// RequireCell, when set, rejects the binding unless the bound row
	// carries this text at RequireCol exactly.
	RequireCell string `json:"requireCell,omitempty"`
	RequireCol  int    `json:"requireCol,omitempty"`
	// RequireBound names anchors that must have bound for this one to bind.
	// It exists because a compound piece of chrome binds or fails WHOLE: an
	// input box whose prompt marker is missing is not an input box, and
	// every anchor derived from it must go absent together. Without this a
	// later anchor computed from an earlier step of the same structure would
	// survive its own structure's rejection.
	RequireBound []string `json:"requireBound,omitempty"`
}

// Branch is one rule of the ordered list. A branch is either a conjunction of
// predicates answering one state, or a three-valued Below switch.
type Branch struct {
	State State  `json:"state,omitempty"`
	When  []Pred `json:"when,omitempty"`
	Below *Below `json:"below,omitempty"`
}

// Below is the three-valued predicate, and the three cases are why it is not
// in When. All rows recognised answers AllMatched; a counterexample answers
// Counterexample; nothing drawn there at all falls through to the next branch.
// Collapsing the middle into the last turns a refusal into free_text.
type Below struct {
	Anchor         string   `json:"anchor"`
	Glyphs         []string `json:"glyphs"`
	AllMatched     State    `json:"allMatched"`
	Counterexample State    `json:"counterexample"`
}

// RegionSpec is WHERE something looks: an anchor, a direction from it, and the
// cap on how far it may go. It is one type because a predicate and an
// extractor ask the same question of the frame and must not be able to answer
// it differently — the boolean half and the reading half share a bound or the
// bound is decoration.
//
// The fields are promoted into the JSON of whatever embeds it, so a document
// spells a region the same way wherever one appears.
type RegionSpec struct {
	Anchor      string `json:"anchor,omitempty"`
	Up          bool   `json:"up,omitempty"`
	MaxRows     int    `json:"maxRows,omitempty"`
	Col0Only    bool   `json:"col0Only,omitempty"`
	StopAtBlank bool   `json:"stopAtBlank,omitempty"`
	// SkipStatusGlyphs names the glyphs a pane's STATUS ROWS open with, so
	// that a region reading up from the input box's own chrome steps over
	// the status stack instead of ending on it. The row that carries one of
	// them in its first cell and sits highest in the leading run of non-blank
	// rows is the stack's last row; nothing at or below it is read. A
	// leading run with none of them is not a stack at all, and the region
	// reads it — which is what keeps a transcript that fills the pane, with
	// no chrome between it and the box, from being discarded.
	SkipStatusGlyphs []string `json:"skipStatusGlyphs,omitempty"`
	// ToEdge says the region's bound is the FRAME's own edge in its
	// direction, instead of a row count. The engine refuses it for a region
	// that reads DOWN (see validate), which is the direction the row cap
	// exists to bound.
	ToEdge bool `json:"toEdge,omitempty"`
	// To names a SECOND anchor that closes this region, inclusive of both
	// ends, instead of a row count or the frame's edge. It exists for
	// Document.InputBox alone: the input box is closed by its own second rule
	// row (claude's "bottomRule"), and that row's position cannot be a row
	// count because the box grows with a multi-line paste. validate refuses
	// it anywhere but a document-level region (a predicate or an extractor
	// still has only the engine's two bounds).
	To string `json:"to,omitempty"`
	// FromCol renders every row this region visits starting at a COLUMN
	// rather than column 0. The only value validate accepts is "cursor",
	// meaning the frame's own CursorX — the one column an option list is
	// reliably aligned to, since the SAME agent draws it at column 1 in an
	// inline dialog and at column 3 inside an overlay panel (measured off
	// claude's bash-permission and 2.1.266-model captures). It exists for the
	// menu extractors alone; validate refuses it on a predicate.
	FromCol string `json:"fromCol,omitempty"`
}

func (r RegionSpec) at(f paneview.Frame, row int) region {
	colFrom := 0
	if r.FromCol == "cursor" {
		colFrom = f.CursorX
	}
	return region{
		anchor:           row,
		up:               r.Up,
		maxRows:          r.MaxRows,
		col0Only:         r.Col0Only,
		stopAtBlank:      r.StopAtBlank,
		skipStatusGlyphs: r.SkipStatusGlyphs,
		toEdge:           r.ToEdge,
		colFrom:          colFrom,
	}
}

// Pred is one predicate invocation. Kind selects which; the rest are its
// arguments, and an argument a kind does not use is ignored. Every predicate
// that names a position names it through RegionSpec.Anchor, region or not.
type Pred struct {
	Kind   string `json:"kind"`
	Glyph  string `json:"glyph,omitempty"`
	Text   string `json:"text,omitempty"`
	Suffix string `json:"suffix,omitempty"`
	// Glyphs is regionAny's "opens with" bound: a set rather than one glyph,
	// because a status row's own grammar is one of several dingbats (nocx
	// reads six off testdata/captures) and the document has no alternation —
	// a set is how "a person composes, and cannot invent a predicate" grows
	// to cover an OR without a branch per member. Checked against the row's
	// own opening content (opensWithGlyph), never against presence anywhere
	// in it — that distinction is the whole reason this field exists rather
	// than one more use of Text.
	Glyphs []string `json:"glyphs,omitempty"`

	RegionSpec
}

// Extractor is the value-reading half of the grammar: a REGION, which is where
// the engine permits reading, plus a PATTERN, which is what the document reads
// out of a row there.
//
// The split is the safety property. A document may say what a row looks like
// and may not say how far to look, because the far half is what an agent's
// printed output can reach. The pattern is RE2 (Go's regexp), so a
// user-authored pattern — untrusted input — cannot backtrack catastrophically,
// and it is applied to the row rendered WHOLE and right-trimmed, which is the
// same rendering the region hands a predicate.
//
// Fields are NAMED CAPTURE GROUPS and nothing else. A pattern that names no
// group reads nothing, and a group that did not participate contributes no
// field rather than an empty one.
type Extractor struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`

	RegionSpec
}

// compiledExtractor is an Extractor with its pattern already compiled. A
// Classify that compiled a regexp per frame would be paying for the grammar on
// the sweep's hot path, and the compile is also where a bad pattern is caught —
// which belongs to process start, not to the first frame that finds it silent.
type compiledExtractor struct {
	spec Extractor
	re   *regexp.Regexp
}

// documentDriver is an Observer whose answer is an evaluation of a Document.
type documentDriver struct {
	doc        Document
	extractors []compiledExtractor
}

// newDocumentDriver validates and compiles a document once. Everything it
// refuses is a wiring mistake — a rule that could never answer, or an
// extractor that could never read — and a wiring mistake belongs to process
// start, exactly as NewRegistry's three refusals do.
func newDocumentDriver(doc Document) (documentDriver, error) {
	if err := doc.validate(); err != nil {
		return documentDriver{}, err
	}
	ex, err := compileExtractors(doc.Extractors)
	if err != nil {
		return documentDriver{}, err
	}
	return documentDriver{doc: doc, extractors: ex}, nil
}

func compileExtractors(specs []Extractor) ([]compiledExtractor, error) {
	out := make([]compiledExtractor, 0, len(specs))
	for _, e := range specs {
		re, err := regexp.Compile(e.Pattern)
		if err != nil {
			return nil, fmt.Errorf("agentdriver: extractor %q has a pattern that does not compile: %w", e.Name, err)
		}
		named := false
		for _, n := range re.SubexpNames() {
			if n != "" {
				named = true
				break
			}
		}
		if !named {
			return nil, fmt.Errorf("agentdriver: extractor %q names no capture group, so it could only ever read nothing", e.Name)
		}
		out = append(out, compiledExtractor{spec: e, re: re})
	}
	return out, nil
}

func (d documentDriver) Agent() string { return d.doc.Agent }

// MenuDisplacesInputBox answers Document.MenuDisplacesInputBox for this
// agent's rule — the declared, measured fact Registry.MenuDisplacesInputBox
// projects (nocx-6q1uh.10).
func (d documentDriver) MenuDisplacesInputBox() bool { return d.doc.MenuDisplacesInputBox }

// bound is the anchor table for one frame. Absent names are simply missing,
// which is what makes "did this bind" askable.
type bound map[string]int

// Classify is the SCALAR PROJECTION of Observe. It is written as one rather
// than as a second evaluation so that there is one place the answer comes
// from; two evaluations of one question is how the two come to disagree.
func (d documentDriver) Classify(f paneview.Frame) State {
	return d.Observe(f).State
}

// Observe evaluates the document against one frame: the branches for the
// state, then the extractors for whatever else the rule can read.
//
// The order is not an implementation detail. The state is decided BEFORE any
// extractor runs and from a value no extractor can see, which is what makes
// "extras never decide the state" a property of the shape rather than a
// promise about the branches somebody wrote.
func (d documentDriver) Observe(f paneview.Frame) Observation {
	// The degenerate frame is the engine's, not a branch: a document cannot
	// express "there is no grid to read", and should not have to.
	if f.Rows <= 0 || f.Cols <= 0 || len(f.Lines) == 0 {
		return Observation{State: StateUnknown, InputBox: noRowSpan, MenuZone: noRowSpan}
	}
	anchors := d.bindAnchors(f)
	obs := Observation{State: d.decide(f, anchors, nil), Extras: d.extract(f, anchors)}
	obs.InputBox = betweenAnchors(f, anchors, d.doc.InputBox)
	obs.MenuZone = menuZoneSpan(f, anchors, d.doc.MenuZone)
	return obs
}

// noRowSpan is the empty RowSpan: Last below First, per RowSpan's own contract
// (agentdriver.go), so a caller checking emptiness never has to know the zero
// value {0,0} would otherwise read as "row zero alone".
var noRowSpan = RowSpan{Last: -1}

// betweenAnchors computes Document.InputBox: the span from one bound anchor
// through a SECOND named anchor (RegionSpec.To), inclusive of both. It is not
// region.eachRow's walk — that walk excludes its own anchor row and is capped
// by a row count or the frame's edge, neither of which fits a box whose height
// is a multi-line paste — so this reads the two rows the earlier binding pass
// already found and reports the span directly.
func betweenAnchors(f paneview.Frame, anchors bound, spec *RegionSpec) RowSpan {
	if spec == nil {
		return noRowSpan
	}
	first, ok := anchors[spec.Anchor]
	if !ok {
		return noRowSpan
	}
	last, ok := anchors[spec.To]
	if !ok {
		return noRowSpan
	}
	if first > last {
		first, last = last, first
	}
	if last >= f.Rows {
		last = f.Rows - 1
	}
	if first < 0 {
		first = 0
	}
	return RowSpan{First: first, Last: last}
}

// menuZoneSpan computes Document.MenuZone: from the anchor upward through the
// frame's own LAST row — never the anchor's own count of rows in the other
// direction. Upward reach is either a fixed MaxRows budget above the anchor,
// or (RegionSpec.ToEdge) unbounded up to the frame's own top row.
//
// The direction is fixed rather than configurable because no single anchor
// closes a menu's chrome the same way twice: an inline dialog (claude's
// bash-permission, write-permission, folder-trust, the theme picker) closes
// with the same "─" rule the input box's own bottom border uses, found as
// "bottomRule"; an overlay panel (2.1.266-model's /model picker) draws no "─"
// anywhere on its frame and opens instead with a "▔" rule, found as
// "overlayTop". Anchoring on the CURSOR is what both share — every menu
// moment this package classifies as permission_choice or modal_choice binds
// it, by the same predicates that decide the state — and its end is always
// the frame's own bottom: nothing below an input box or a menu is the
// agent's own untrusted output, so walking toward it is the safe direction
// here exactly as walking toward the top is region.eachRow's for an
// extractor.
//
// ToEdge exists because MaxRows answered a question this zone does not ask.
// A budget of rows above the anchor bounds how far a REPAINT already on
// screen may be read from — right for an extractor, which only ever reads
// chrome that is already there. The menu zone is read at a moment with NO
// menu on screen, to decide where a menu would be safe to have appeared by
// the time Enter lands (design §6.3), and nocx-6q1uh.17 measured that this
// panel's own top is not a fixed distance from the cursor: claude's
// bash-permission dialog opens its panel 24 rows above a working moment's
// cursor, write-permission opens its own panel 17 rows above that SAME
// cursor, later in the same capture. A cap sized for one starves the other,
// and the corpus gives no anchor that binds tighter at every free_text and
// working moment, so ToEdge reads all the way to the frame's own top row —
// the honest fallback design §6.3 names when nothing bounds it closer.
func menuZoneSpan(f paneview.Frame, anchors bound, spec *RegionSpec) RowSpan {
	if spec == nil {
		return noRowSpan
	}
	row, ok := anchors[spec.Anchor]
	if !ok {
		return noRowSpan
	}
	first := 0
	if !spec.ToEdge {
		first = row - spec.MaxRows + 1
		if first < 0 {
			first = 0
		}
	}
	last := f.Rows - 1
	if last < first {
		return noRowSpan
	}
	return RowSpan{First: first, Last: last}
}

// decide is the ordered branch walk. It reads predicates and anchors, and
// nothing an extractor produced.
//
// tr is the RECORDER, and it is nil on every path in the product. It exists so
// that the emitting view (nocx-02uci) reports the walk the product actually
// took rather than a second walk written beside it — two evaluations of one
// question is how the two come to disagree on the frame nobody tried. Nothing
// it records is read back here: recording cannot change an answer, because
// every branch below is decided before the recorder is told about it.
func (d documentDriver) decide(f paneview.Frame, anchors bound, tr *trace) State {
	for i, b := range d.doc.Branches {
		if b.Below != nil {
			_, bound := anchors[b.Below.Anchor]
			verdict := belowAnchorOpensOnlyWith(f, anchors[b.Below.Anchor], b.Below.Glyphs)
			state := State("")
			if bound {
				switch verdict {
				case belowAllMatched:
					state = b.Below.AllMatched
				case belowCounterexample:
					state = b.Below.Counterexample
				}
			}
			tr.below(i, b, bound, verdict, state)
			if state != "" {
				return state
			}
			continue
		}
		if tr.conjunction(i, b, f, anchors) {
			return b.State
		}
	}
	tr.fellThrough()
	return d.doc.Default
}

// extract runs every extractor whose anchor bound, in document order. An
// extractor that matched no row contributes NOTHING — not an empty entry —
// because a reader must be able to tell "the panel is not on screen" from "the
// panel is on screen and says nothing", and only the first of those is true
// here.
func (d documentDriver) extract(f paneview.Frame, anchors bound) []Extra {
	var out []Extra
	for _, e := range d.extractors {
		row, ok := anchors[e.spec.Anchor]
		if !ok {
			continue
		}
		rows := e.spec.at(f, row).capture(f, e.re)
		if len(rows) == 0 {
			continue
		}
		out = append(out, Extra{Name: e.spec.Name, Rows: rows})
	}
	return out
}

func allHold(f paneview.Frame, anchors bound, preds []Pred) bool {
	for _, p := range preds {
		if !holds(f, anchors, p) {
			return false
		}
	}
	return true
}

// holds evaluates one predicate. A predicate naming an anchor that did not
// bind answers false, EXCEPT aboveAnchorIfBound, whose whole purpose is the
// other answer.
func holds(f paneview.Frame, anchors bound, p Pred) bool {
	switch p.Kind {
	case "cursorOn":
		return cursorOn(f, p.Glyph)
	case "cursorOpensItsRow":
		col, ok := firstNonBlankCol(f, f.CursorY)
		return ok && col == f.CursorX
	case "numberedOptionAfterCursor":
		return numberedOption(rowTextFrom(f, f.CursorY, f.CursorX+1))
	case "anchorUnbound":
		_, ok := anchors[p.Anchor]
		return !ok
	case "anchorBound":
		_, ok := anchors[p.Anchor]
		return ok
	case "cursorAboveAnchorIfBound":
		row, ok := anchors[p.Anchor]
		if !ok {
			return true
		}
		return f.CursorY < row
	case "nearestNonBlankAboveCursorContains":
		text, ok := nearestNonBlankAbove(f, f.CursorY)
		return ok && strings.Contains(text, p.Text)
	case "belowCursorContains":
		// A region read DOWN from the cursor's own row, capped by the
		// document's maxRows and by nothing it can widen (validate refuses a
		// missing or oversized cap). It is a predicate and not an anchor on
		// purpose: the cursor exists on every screen, and an anchor that binds
		// on every screen would tell a person reading the emitting view that
		// chrome was found where none was. What it answers is narrower —
		// whether chrome is drawn within a bounded distance BENEATH the row
		// the cursor chose — which is how a menu that numbers none of its
		// options is identified (nocx-f545a.2). Direction is fixed; Up and
		// Anchor are ignored.
		return region{anchor: f.CursorY, maxRows: p.MaxRows, col0Only: p.Col0Only, stopAtBlank: p.StopAtBlank}.
			anyRow(f, func(text string) bool { return strings.Contains(text, p.Text) })
	case "rowOpensWith":
		row, ok := anchors[p.Anchor]
		if !ok {
			return false
		}
		return rowOpensWith(f, row, p.Glyph)
	case "rowContains":
		row, ok := anchors[p.Anchor]
		if !ok {
			return false
		}
		return rowContains(f, row, p.Text)
	case "regionAny":
		row, ok := anchors[p.Anchor]
		if !ok {
			return false
		}
		return p.at(f, row).anyRow(f, func(text string) bool {
			if p.Text != "" && !strings.Contains(text, p.Text) {
				return false
			}
			if p.Suffix != "" && !strings.HasSuffix(text, p.Suffix) {
				return false
			}
			if len(p.Glyphs) > 0 && !opensWithGlyph(text, p.Glyphs) {
				return false
			}
			return true
		})
	}
	return false
}

func (d documentDriver) bindAnchors(f paneview.Frame) bound {
	anchors := make(bound, len(d.doc.Anchors))
	for _, a := range d.doc.Anchors {
		row, ok := bindOne(f, anchors, a)
		if !ok {
			continue
		}
		anchors[a.Name] = row
	}
	return anchors
}

func bindOne(f paneview.Frame, anchors bound, a AnchorSpec) (int, bool) {
	for _, need := range a.RequireBound {
		if _, ok := anchors[need]; !ok {
			return 0, false
		}
	}
	start := f.Rows - 1
	if a.From != "" {
		row, ok := anchors[a.From]
		if !ok {
			return 0, false
		}
		start = row
	}
	switch a.Kind {
	case "searchUp":
		found := -1
		for y := start + a.FromOffset; y >= a.Floor; y-- {
			if fullWidthRule(f, y, a.RuleGlyph) {
				found = y
				break
			}
		}
		if found < 0 {
			return 0, false
		}
		return guard(f, found, a)
	case "offset":
		return guard(f, start+a.Offset, a)
	case "firstNonBlankBelow":
		row, ok := firstNonBlankRowBelow(f, start)
		if !ok {
			return 0, false
		}
		return guard(f, row, a)
	case "cursor":
		return guard(f, f.CursorY, a)
	}
	return 0, false
}

func guard(f paneview.Frame, row int, a AnchorSpec) (int, bool) {
	if row < 0 || row >= f.Rows {
		return 0, false
	}
	if row < a.MinRow {
		return 0, false
	}
	if a.RequireCell != "" && !cellAtCol(f, row, a.RequireCol, a.RequireCell) {
		return 0, false
	}
	return row, true
}

// validate refuses a document that could never answer, at construction. These
// are wiring mistakes and belong to process start, like NewRegistry's.
func (d Document) validate() error {
	if d.Agent == "" {
		return fmt.Errorf("agentdriver: document names no agent, so nothing could ever look it up")
	}
	if !d.Default.Valid() {
		return fmt.Errorf("agentdriver: document default %q is not a state", d.Default)
	}
	seen := make(map[string]bool, len(d.Anchors))
	cursorAnchors := make(map[string]bool, len(d.Anchors))
	for _, a := range d.Anchors {
		if a.Name == "" {
			return fmt.Errorf("agentdriver: an anchor has no name")
		}
		if a.From != "" && !seen[a.From] {
			return fmt.Errorf("agentdriver: anchor %q is computed from %q, which is not bound before it", a.Name, a.From)
		}
		for _, need := range a.RequireBound {
			if !seen[need] {
				return fmt.Errorf("agentdriver: anchor %q requires %q, which is not bound before it", a.Name, need)
			}
		}
		seen[a.Name] = true
		if a.Kind == "cursor" {
			cursorAnchors[a.Name] = true
		}
	}
	for i, b := range d.Branches {
		if b.Below != nil {
			if !seen[b.Below.Anchor] {
				return fmt.Errorf("agentdriver: branch %d reads below %q, which no anchor binds", i, b.Below.Anchor)
			}
			if cursorAnchors[b.Below.Anchor] {
				return fmt.Errorf("agentdriver: branch %d reads below %q, a cursor anchor; only an extractor or a document-level region may name one", i, b.Below.Anchor)
			}
			if !b.Below.AllMatched.Valid() || !b.Below.Counterexample.Valid() {
				return fmt.Errorf("agentdriver: branch %d names a state that does not exist", i)
			}
			continue
		}
		if !b.State.Valid() {
			return fmt.Errorf("agentdriver: branch %d answers %q, which is not a state", i, b.State)
		}
		for _, p := range b.When {
			if p.Anchor != "" && !seen[p.Anchor] {
				return fmt.Errorf("agentdriver: branch %d names anchor %q, which no anchor binds", i, p.Anchor)
			}
			if p.Anchor != "" && cursorAnchors[p.Anchor] {
				return fmt.Errorf("agentdriver: branch %d names anchor %q, a cursor anchor; only an extractor or a document-level region may name one", i, p.Anchor)
			}
			if p.ToEdge && !p.Up {
				return fmt.Errorf("agentdriver: branch %d reads to the frame edge without reading up; an unbounded region may only walk AWAY from the chrome it is anchored in", i)
			}
			if len(p.SkipStatusGlyphs) > 0 && !p.Up {
				return fmt.Errorf("agentdriver: branch %d steps over a status stack without reading up; a status stack is only ever between an anchor and the agent's output ABOVE it", i)
			}
			if p.To != "" {
				return fmt.Errorf("agentdriver: branch %d closes a region at a second anchor %q; that reach is for the input box alone", i, p.To)
			}
			if p.FromCol != "" {
				return fmt.Errorf("agentdriver: branch %d reads from column %q; a predicate has no use for it and it is refused rather than ignored", i, p.FromCol)
			}
			if p.Kind == "belowCursorContains" && (p.MaxRows <= 0 || p.MaxRows > maxExtractorRows) {
				return fmt.Errorf("agentdriver: branch %d reads below the cursor with a cap of %d rows; the engine requires 1 to %d, because an uncapped region is how a forged row gets read",
					i, p.MaxRows, maxExtractorRows)
			}
		}
	}
	for _, e := range d.Extractors {
		if e.Name == "" {
			return fmt.Errorf("agentdriver: an extractor has no name, so nothing could ever read its yield")
		}
		if !seen[e.Anchor] {
			return fmt.Errorf("agentdriver: extractor %q reads from %q, which no anchor binds", e.Name, e.Anchor)
		}
		if len(e.SkipStatusGlyphs) > 0 && !e.Up {
			return fmt.Errorf("agentdriver: extractor %q steps over a status stack without reading up; a status stack is only ever between an anchor and the agent's output ABOVE it", e.Name)
		}
		if e.To != "" {
			return fmt.Errorf("agentdriver: extractor %q closes a region at a second anchor %q; that reach is for the input box alone", e.Name, e.To)
		}
		if e.FromCol != "" && e.FromCol != "cursor" {
			return fmt.Errorf("agentdriver: extractor %q reads from column %q; the engine only knows \"cursor\"", e.Name, e.FromCol)
		}
		if e.ToEdge {
			// The row cap below is the engine's bound on how far a region
			// anchored in CHROME may reach, and it exists because the
			// agent's printed output is what lies past it. A region that
			// reads UP from the input box's own chrome is asking for that
			// output, so its bound is the frame's top edge — the same bound
			// belowAnchorOpensOnlyWith has, and the engine's for the same
			// reason: there is nothing above the first row to reach. Reading
			// DOWN to the edge is refused outright, because the chrome below
			// an anchor is exactly what the cap is for.
			if !e.Up {
				return fmt.Errorf("agentdriver: extractor %q reads to the frame edge without reading up; an unbounded region may only walk AWAY from the chrome it is anchored in", e.Name)
			}
			if e.MaxRows != 0 {
				return fmt.Errorf("agentdriver: extractor %q names both a cap of %d rows and the frame edge; a region has one bound, and two is how they come to disagree", e.Name, e.MaxRows)
			}
			continue
		}
		if e.MaxRows <= 0 {
			return fmt.Errorf("agentdriver: extractor %q declares no row cap, and an uncapped region is how a forged row gets read", e.Name)
		}
		if e.MaxRows > maxExtractorRows {
			return fmt.Errorf("agentdriver: extractor %q asks for %d rows; the engine allows %d", e.Name, e.MaxRows, maxExtractorRows)
		}
	}
	if d.InputBox != nil {
		if err := validateInputBox(*d.InputBox, seen); err != nil {
			return err
		}
	}
	if d.MenuZone != nil {
		if err := validateMenuZone(*d.MenuZone, seen); err != nil {
			return err
		}
	}
	return nil
}

// validateInputBox requires exactly the two anchors a between-anchors span
// needs and nothing a row-count or edge-bounded region would also accept —
// two shapes sharing a JSON type is not a licence to accept either shape
// wherever one of them appears.
func validateInputBox(r RegionSpec, seen map[string]bool) error {
	if r.Anchor == "" || !seen[r.Anchor] {
		return fmt.Errorf("agentdriver: inputBox names anchor %q, which no anchor binds", r.Anchor)
	}
	if r.To == "" || !seen[r.To] {
		return fmt.Errorf("agentdriver: inputBox closes at %q, which no anchor binds", r.To)
	}
	if r.Up || r.ToEdge || r.MaxRows != 0 || r.FromCol != "" {
		return fmt.Errorf("agentdriver: inputBox names a row count, an edge or a column origin beside its closing anchor %q; a between-anchors span has one bound", r.To)
	}
	return nil
}

// validateMenuZone requires the fixed shape menuZoneSpan interprets — an
// anchor read upward from, either by a row budget or (ToEdge) with no bound
// but the frame's own top — and refuses every field neither shape uses, for
// the same reason validateInputBox does.
//
// MaxRows and ToEdge are mutually exclusive, as they are for an extractor
// (see the Extractors loop above): a region has one upward bound, and two is
// how they come to disagree. Unlike an extractor, MenuZone's ToEdge needs no
// companion Up check beyond the one below — an upward-only field is already
// meaningless without it — because there is no downward MenuZone shape to
// confuse it with; the zone's closing edge is always the frame's own last
// row, fixed by menuZoneSpan itself rather than by anything a document names.
func validateMenuZone(r RegionSpec, seen map[string]bool) error {
	if r.Anchor == "" || !seen[r.Anchor] {
		return fmt.Errorf("agentdriver: menuZone names anchor %q, which no anchor binds", r.Anchor)
	}
	if !r.Up {
		return fmt.Errorf("agentdriver: menuZone does not read up from %q; its reach is read ABOVE the anchor, the frame's own last row closes it either way", r.Anchor)
	}
	if r.ToEdge {
		if r.MaxRows != 0 {
			return fmt.Errorf("agentdriver: menuZone names both a cap of %d rows and the frame edge above %q; a region has one bound, and two is how they come to disagree", r.MaxRows, r.Anchor)
		}
	} else if r.MaxRows <= 0 || r.MaxRows > maxExtractorRows {
		return fmt.Errorf("agentdriver: menuZone asks for %d rows above %q; the engine requires 1 to %d, or toEdge for no bound but the frame's own top", r.MaxRows, r.Anchor, maxExtractorRows)
	}
	if r.To != "" || r.FromCol != "" {
		return fmt.Errorf("agentdriver: menuZone names a closing anchor or a column origin beside its upward reach; the zone always closes at the frame's own last row")
	}
	return nil
}
