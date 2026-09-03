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

	"github.com/shady2k/nocx/internal/panegrid"
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
}

// AnchorSpec binds a name to a row of the frame. A binding that fails is not
// an error: the anchor is ABSENT, and a predicate may ask about that.
type AnchorSpec struct {
	Name string `json:"name"`
	// Kind is "searchUp", "offset" or "firstNonBlankBelow".
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
}

func (r RegionSpec) at(row int) region {
	return region{anchor: row, up: r.Up, maxRows: r.MaxRows, col0Only: r.Col0Only, stopAtBlank: r.StopAtBlank}
}

// Pred is one predicate invocation. Kind selects which; the rest are its
// arguments, and an argument a kind does not use is ignored. Every predicate
// that names a position names it through RegionSpec.Anchor, region or not.
type Pred struct {
	Kind   string `json:"kind"`
	Glyph  string `json:"glyph,omitempty"`
	Text   string `json:"text,omitempty"`
	Suffix string `json:"suffix,omitempty"`

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

// bound is the anchor table for one frame. Absent names are simply missing,
// which is what makes "did this bind" askable.
type bound map[string]int

// Classify is the SCALAR PROJECTION of Observe. It is written as one rather
// than as a second evaluation so that there is one place the answer comes
// from; two evaluations of one question is how the two come to disagree.
func (d documentDriver) Classify(f panegrid.Frame) State {
	return d.Observe(f).State
}

// Observe evaluates the document against one frame: the branches for the
// state, then the extractors for whatever else the rule can read.
//
// The order is not an implementation detail. The state is decided BEFORE any
// extractor runs and from a value no extractor can see, which is what makes
// "extras never decide the state" a property of the shape rather than a
// promise about the branches somebody wrote.
func (d documentDriver) Observe(f panegrid.Frame) Observation {
	// The degenerate frame is the engine's, not a branch: a document cannot
	// express "there is no grid to read", and should not have to.
	if f.Rows <= 0 || f.Cols <= 0 || len(f.Lines) == 0 {
		return Observation{State: StateUnknown}
	}
	anchors := d.bindAnchors(f)
	return Observation{State: d.decide(f, anchors, nil), Extras: d.extract(f, anchors)}
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
func (d documentDriver) decide(f panegrid.Frame, anchors bound, tr *trace) State {
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
func (d documentDriver) extract(f panegrid.Frame, anchors bound) []Extra {
	var out []Extra
	for _, e := range d.extractors {
		row, ok := anchors[e.spec.Anchor]
		if !ok {
			continue
		}
		rows := e.spec.at(row).capture(f, e.re)
		if len(rows) == 0 {
			continue
		}
		out = append(out, Extra{Name: e.spec.Name, Rows: rows})
	}
	return out
}

func allHold(f panegrid.Frame, anchors bound, preds []Pred) bool {
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
func holds(f panegrid.Frame, anchors bound, p Pred) bool {
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
	case "cursorAboveAnchorIfBound":
		row, ok := anchors[p.Anchor]
		if !ok {
			return true
		}
		return f.CursorY < row
	case "nearestNonBlankAboveCursorContains":
		text, ok := nearestNonBlankAbove(f, f.CursorY)
		return ok && strings.Contains(text, p.Text)
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
		return p.at(row).anyRow(f, func(text string) bool {
			if p.Text != "" && !strings.Contains(text, p.Text) {
				return false
			}
			if p.Suffix != "" && !strings.HasSuffix(text, p.Suffix) {
				return false
			}
			return true
		})
	}
	return false
}

func (d documentDriver) bindAnchors(f panegrid.Frame) bound {
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

func bindOne(f panegrid.Frame, anchors bound, a AnchorSpec) (int, bool) {
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
	}
	return 0, false
}

func guard(f panegrid.Frame, row int, a AnchorSpec) (int, bool) {
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
	}
	for i, b := range d.Branches {
		if b.Below != nil {
			if !seen[b.Below.Anchor] {
				return fmt.Errorf("agentdriver: branch %d reads below %q, which no anchor binds", i, b.Below.Anchor)
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
		}
	}
	for _, e := range d.Extractors {
		if e.Name == "" {
			return fmt.Errorf("agentdriver: an extractor has no name, so nothing could ever read its yield")
		}
		if !seen[e.Anchor] {
			return fmt.Errorf("agentdriver: extractor %q reads from %q, which no anchor binds", e.Name, e.Anchor)
		}
		if e.MaxRows <= 0 {
			return fmt.Errorf("agentdriver: extractor %q declares no row cap, and an uncapped region is how a forged row gets read", e.Name)
		}
		if e.MaxRows > maxExtractorRows {
			return fmt.Errorf("agentdriver: extractor %q asks for %d rows; the engine allows %d", e.Name, e.MaxRows, maxExtractorRows)
		}
	}
	return nil
}
